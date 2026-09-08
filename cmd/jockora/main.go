// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Command jockora is the Jockora server: a music library, a set of AI DJs and
// one HLS stream out.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/andrewloable/jockora/internal/app"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/doctor"
	"github.com/andrewloable/jockora/internal/errs"
	"github.com/andrewloable/jockora/internal/library"
	"golang.org/x/term"
)

// Version is the release this binary reports. Semver, so an operator can tell
// two builds apart in a bug report.
// Version is a VAR, not a const, so a release build can stamp the tag it was
// built from with -ldflags "-X main.Version=v1.2.3". -X cannot write to a
// const, so the linker flag would have been silently ignored and every release
// binary would have reported the same hardcoded string.
var Version = "0.1.0"

// subcommands is the WHOLE surface, named once and deliberately.
//
// Naming it here rather than letting it accrue is the point of this list: a
// tool whose commands arrived one at a time feels arbitrary to use, and every
// one of them is a promise that has to keep working.
var subcommands = []string{"serve", "scan", "enrich", "admin", "doctor", "version"}

// usageExit is what an unusable command line exits with. Two, not one, so a
// script can tell "you asked for something that does not exist" from "the thing
// you asked for failed".
const usageExit = 2

func main() {
	os.Exit(dispatch(context.Background(), os.Args[1:], os.Stderr))
}

// dispatch runs one subcommand and returns its exit code.
//
// It takes its arguments and its output rather than reaching for os.Args and
// os.Stderr, which is the difference between ASSERTING that an unknown
// subcommand exits 2 and hoping it does.
func dispatch(ctx context.Context, args []string, out io.Writer) int {
	if len(args) == 0 {
		printUsage(out, "")
		return usageExit
	}

	// The subcommand comes first. A flag before it is a mistake worth naming:
	// silently accepting `jockora -db-path x serve` would teach an ordering
	// that stops working the moment two subcommands want the same flag name.
	name := args[0]
	if strings.HasPrefix(name, "-") {
		printUsage(out, name)
		return usageExit
	}
	if !slices.Contains(subcommands, name) {
		printUsage(out, name)
		return usageExit
	}

	if name == "version" {
		fmt.Fprintf(out, "jockora %s\n", Version)
		return 0
	}

	// admin parses its own flags: its arguments are a verb and a name, which
	// the server's flag set knows nothing about.
	if name == "admin" {
		cfg, err := config.LoadArgs(nil, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "jockora:", err)
			return 1
		}
		if err := runAdmin(ctx, args[1:], cfg.DBPath, promptPassword(out), out); err != nil {
			fmt.Fprintln(os.Stderr, "jockora:", err)
			return 1
		}
		return 0
	}

	if err := runSubcommand(ctx, name, args[1:], out); err != nil {
		fmt.Fprintln(os.Stderr, "jockora:", err)
		return 1
	}
	return 0
}

// promptPassword reads a password from the terminal WITHOUT ECHO, falling back
// to a plain line when stdin is a pipe.
//
// Not a flag and not an environment variable: a password in a flag is a
// password in the shell history and in every ps listing on the machine while
// the command runs.
//
// Lives here rather than beside runAdmin because it needs a real terminal, and
// a function that can only be exercised under a pseudo-terminal does not belong
// in the file that has to be tested without one.
func promptPassword(out io.Writer) passwordFunc {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return stdinPassword(os.Stdin)
	}
	return func() (string, error) {
		fmt.Fprint(out, "password: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("reading the password: %w", err)
		}
		return string(b), nil
	}
}

func printUsage(out io.Writer, unknown string) {
	if unknown != "" {
		fmt.Fprintf(out, "jockora: unknown subcommand %q\n\n", unknown)
	}
	fmt.Fprint(out, `usage: jockora <command> [flags]

  serve     stream the station
  scan      read the music library into the database
  enrich    build dossiers for scanned tracks
  admin     create the first operator account
  doctor    check that everything this needs is present
  version   print the version

Every flag has a JOCKORA_-prefixed environment equivalent.
Run `+"`jockora <command> -h`"+` for a command's flags.
`)
}

// newHandler picks the log format.
//
// JSON when stderr is not a terminal, text when it is. That is not a style
// preference: a container's logs are collected and parsed by something else --
// this host runs Seq for exactly that -- and text records lose their structure
// on the way in. A human at a terminal wants the readable form. Neither should
// have to remember a flag, so the default is inferred and the flag exists to
// override it.
func newHandler(w *os.File, format string) slog.Handler {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	switch format {
	case "json":
		return slog.NewJSONHandler(w, opts)
	case "text":
		return slog.NewTextHandler(w, opts)
	default:
		if fi, err := w.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return slog.NewTextHandler(w, opts)
		}
		return slog.NewJSONHandler(w, opts)
	}
}

// expandTracks turns any directory argument into the audio files beneath it,
// leaving plain file arguments untouched.
func expandTracks(args []string) ([]string, error) {
	var out []string
	for _, arg := range args {
		fi, err := os.Stat(arg)
		if err != nil {
			return nil, fmt.Errorf("track %s: %w", arg, err)
		}
		if !fi.IsDir() {
			out = append(out, arg)
			continue
		}
		err = filepath.WalkDir(arg, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // an unreadable subdirectory must not end the walk
			}
			if !d.IsDir() && library.IsAudio(p) {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scanning %s: %w", arg, err)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no audio files found in %v", args)
	}
	sort.Strings(out)
	return out, nil
}

// ringSecondsForStall mirrors the app's ring depth, so a SIGUSR2 stall is
// guaranteed to outlast the buffer rather than being absorbed by it.
const ringSecondsForStall = 10 * time.Second

func runSubcommand(ctx context.Context, name string, args []string, out io.Writer) error {
	// Run-specific flags live here rather than in config, which describes the
	// server rather than one spike run.
	var breakPath string
	var breakAt, breakEvery, sample int
	cfg, err := config.LoadArgs(args, func(fs *flag.FlagSet) {
		fs.StringVar(&breakPath, "break", "", "WAV to splice into the stream (48kHz 16-bit)")
		fs.IntVar(&breakAt, "break-at", 30, "when the first break should be heard, seconds from start")
		fs.IntVar(&breakEvery, "break-every", 0, "repeat the break every N seconds (0 = once)")
		fs.IntVar(&sample, "sample", 0, "with `enrich`, measure synced-lyric coverage over N sampled tracks")
	})
	if err != nil {
		return err
	}

	log := slog.New(newHandler(os.Stderr, cfg.LogFormat))

	doctorCfg := doctor.Config{
		LibraryPath: cfg.LibraryPath,
		SegmentDir:  cfg.SegmentDir,
		DBPath:      cfg.DBPath,
		LLMBaseURL:  cfg.LLMBaseURL,
		// The dialect too, or the doctor probes llama.cpp's native /completion
		// against whatever is configured and calls a working hosted endpoint
		// a 404. LLMModelPath doubles as the model NAME for a hosted host.
		LLMAPI:    cfg.LLMAPI,
		LLMModel:  cfg.LLMModelPath,
		LLMKey:    cfg.LLMAPIKey,
		TTSAddr:   cfg.TTSAddr,
		TTSPython: cfg.TTSPython,
		TTSScript: cfg.TTSScript,
		// Required only when a DJ is actually asked for. The spike path calls
		// no model and speaks no TTS, and demanding them there would refuse to
		// start the one mode that needs nothing -- which is the mode GATE 2 and
		// room test A run against.
		// NOT HARD ANY MORE. A model that has gone away is now fixable from the
		// operator console -- provider, key and model are all on a page there --
		// and refusing to start locks the operator out of the very screen that
		// fixes it. That happened: a provider withdrew a free tier overnight and
		// the station would not come up until somebody edited a compose file over
		// ssh. It is still reported, loudly, as a failed check.
		RequireLLM: false,
		RequireTTS: cfg.LibraryPath != "" && cfg.PersonaPath != "",
	}

	tracks := cfg.Args

	switch name {
	case "doctor":
		// Reports and exits, so an operator can diagnose without starting a
		// stream.
		checks := doctor.Run(ctx, doctorCfg)
		fmt.Fprint(out, doctor.Report(checks))
		if failed := doctor.Failed(checks); len(failed) > 0 {
			return fmt.Errorf("%d hard check(s) failed", len(failed))
		}
		return nil

	case "scan":
		root := cfg.LibraryPath
		if len(tracks) == 1 {
			root = tracks[0]
		}
		if root == "" {
			return fmt.Errorf("scan needs a library: jockora scan -library-path PATH")
		}
		return runScan(ctx, cfg.DBPath, root, log)

	case "enrich":
		// Coverage measurement lives under enrich rather than as a sixth
		// subcommand: the surface is deliberately five, and measuring
		// synced-lyric coverage is enrichment work that stops early.
		if sample > 0 {
			root := cfg.LibraryPath
			if len(tracks) == 1 {
				root = tracks[0]
			}
			if root == "" {
				return fmt.Errorf("enrich -sample needs a library: jockora enrich -sample 200 -library-path PATH")
			}
			return runCoverage(ctx, cfg.DBPath, root, sample, log)
		}
		return fmt.Errorf("full enrichment is not wired to the CLI yet (Jockora-q4q); " +
			"use -sample N to measure synced-lyric coverage")
	}

	// PREFLIGHT FIRST, before anything is opened, scanned or spawned.
	//
	// It used to run after the library and the DJ were built, which meant a
	// failed check exited having already scanned a library and started a Python
	// process -- work thrown away, and a sidecar left to be cleaned up by a
	// deferred close that had not been registered yet.
	if checks := doctor.Run(ctx, doctorCfg); len(doctor.Failed(checks)) > 0 {
		// Fail loudly at startup rather than quietly at airtime. Wrapped as
		// ErrToolMissing so a caller can tell "you have not installed ffmpeg"
		// from "your configuration is wrong" -- the first is the single most
		// likely first-run problem this program has.
		fmt.Fprint(out, doctor.Report(checks))
		return fmt.Errorf("%w: preflight failed; fix the checks above or run: jockora doctor",
			errs.ErrToolMissing)
	}

	// serve, in one of two modes.
	//
	// A library, if one is configured: scan it, select from the database, run
	// the DJ and enrich in the background. Otherwise the spike path -- a list
	// of files, no database, no model, no sidecar -- which is what GATE 2 and
	// room test A run against and which must keep working.
	opts := app.Options{
		BreakPath:     breakPath,
		BreakAtSec:    breakAt,
		BreakEverySec: breakEvery,
		Log:           log,
	}

	// EITHER source puts the station in library mode. Gating on LibraryPath
	// alone sent a Subsonic-only configuration down the spike path, where it
	// would have demanded a list of files and ignored the server entirely.
	if cfg.LibraryPath != "" || cfg.SubsonicURL != "" {
		lib, err := buildLibrary(ctx, cfg, log)
		if err != nil {
			return err
		}
		defer lib.Store.Close() //nolint:errcheck // closing at shutdown

		opts.Library = lib
		opts.Personas = loadRoster(cfg.PersonaPath, log)
		// ONE client for the DJ and the enricher, swappable from the console.
		llm := buildLLM(ctx, cfg, lib, log)
		opts.LLM = llm
		opts.Enricher = buildEnricher(llm, lib, log)
		newBreaks, pipeline, writer, sidecar := buildBreaks(ctx, cfg, lib, log, llm)
		// The FACTORY is what a station-based deployment uses; the pair is the
		// spike path's, and the default a station inherits before it has one.
		opts.NewBreaks = newBreaks
		opts.Breaks, opts.Writer = pipeline, writer
		if sidecar != nil {
			defer sidecar.Close() //nolint:errcheck // closing at shutdown
		}
		// AFTER buildBreaks, because a MANAGED sidecar picks its own port and
		// only knows it once started. Reading cfg.TTSAddr instead measured
		// loudness and silently never measured tempo, which is exactly the
		// half-wired state this task was filed about.
		opts.Analyser = buildAnalyser(cfg, lib, sidecar, log)
		// Same address the analyser uses, for the console's voice list and
		// voice preview.
		opts.TTSAddr = ttsAddr(cfg, sidecar)
	} else {
		if len(tracks) == 0 {
			return fmt.Errorf("serve needs something to play:\n" +
				"       jockora serve -library-path /music\n" +
				"       jockora serve -subsonic-url https://navidrome.example -subsonic-user you\n" +
				"       jockora serve <track|directory>...")
		}
		// A directory argument expands to the audio files beneath it.
		expanded, err := expandTracks(tracks)
		if err != nil {
			return err
		}
		opts.Tracks = expanded
		log.Info("file list", "tracks", len(expanded))
	}

	// The spike runs against explicit track files rather than a scanned library,
	// so the library-path check does not apply to it. It equally does not apply
	// to a remote library, where there is no local path for the doctor to stat.
	if cfg.LibraryPath == "" {
		doctorCfg.LibraryPath = ""
	}

	a, err := app.New(cfg, opts)
	if err != nil {
		return err
	}

	// SIGINT must reach the encoder so ffmpeg flushes its final segment. An
	// orphaned ffmpeg keeps the segment directory and the next start fails.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Decoder-stall fault injection. Test hooks, not features.
	//
	// SIGUSR1 stalls 8 seconds, which is what GATE 2 specifies. On the default
	// 10-second ring that stall is ABSORBED: the buffer covers it, UnderrunCount
	// stays at zero and silence-fill never fires. That is the system working,
	// but it does not demonstrate the safety net.
	//
	// SIGUSR2 stalls longer than the ring can hold, so the ring genuinely runs
	// dry, silence-fill fires and UnderrunCount rises. The gate asks for the net
	// to be proven to fire, not merely proven never to be needed, so both are
	// available and both should be exercised.
	stalls := make(chan os.Signal, 2)
	signal.Notify(stalls, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range stalls {
			if sig == syscall.SIGUSR2 {
				a.StallFeeder(ringSecondsForStall + 5*time.Second)
				continue
			}
			a.StallFeeder(8 * time.Second)
		}
	}()

	return a.Run(ctx)
}
