// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Command jockora is the Jockora server: a music library, a set of AI DJs and
// one HLS stream out.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/andrewloable/jockora/internal/app"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/doctor"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jockora:", err)
		os.Exit(1)
	}
}

// ringSecondsForStall mirrors the app's ring depth, so a SIGUSR2 stall is
// guaranteed to outlast the buffer rather than being absorbed by it.
const ringSecondsForStall = 10 * time.Second

func run() error {
	// Run-specific flags live here rather than in config, which describes the
	// server rather than one spike run.
	var breakPath string
	var breakAt, breakEvery int
	cfg, err := config.LoadWith(func(fs *flag.FlagSet) {
		fs.StringVar(&breakPath, "break", "", "WAV to splice into the stream (48kHz 16-bit)")
		fs.IntVar(&breakAt, "break-at", 30, "when the first break should be heard, seconds from start")
		fs.IntVar(&breakEvery, "break-every", 0, "repeat the break every N seconds (0 = once)")
	})
	if err != nil {
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	doctorCfg := doctor.Config{
		LibraryPath: cfg.LibraryPath,
		SegmentDir:  cfg.SegmentDir,
		DBPath:      cfg.DBPath,
		LLMBaseURL:  cfg.LLMBaseURL,
		TTSAddr:     cfg.TTSAddr,
		// The spike calls no model and speaks no TTS: it plays tracks and a
		// pre-rendered WAV. These become required when the DJ brain is wired.
		RequireLLM: false,
		RequireTTS: false,
	}

	tracks := cfg.Args

	// `jockora doctor` reports and exits, so an operator can diagnose without
	// starting a stream.
	if len(tracks) == 1 && tracks[0] == "doctor" {
		checks := doctor.Run(context.Background(), doctorCfg)
		fmt.Print(doctor.Report(checks))
		if failed := doctor.Failed(checks); len(failed) > 0 {
			return fmt.Errorf("%d hard check(s) failed", len(failed))
		}
		return nil
	}

	if len(tracks) == 0 {
		return fmt.Errorf("usage: jockora [flags] <track> [track...]\n" +
			"       jockora -break voice.wav -break-at 30 a.mp3 b.mp3\n" +
			"       jockora doctor")
	}

	// The spike runs against explicit track files rather than a scanned library,
	// so the library check does not apply to it.
	doctorCfg.LibraryPath = ""
	if checks := doctor.Run(context.Background(), doctorCfg); len(doctor.Failed(checks)) > 0 {
		// Fail loudly at startup rather than quietly at airtime.
		fmt.Print(doctor.Report(checks))
		return fmt.Errorf("preflight failed; fix the checks above or run: jockora doctor")
	}

	a, err := app.New(cfg, app.Options{
		Tracks:        tracks,
		BreakPath:     breakPath,
		BreakAtSec:    breakAt,
		BreakEverySec: breakEvery,
		Log:           log,
	})
	if err != nil {
		return err
	}

	// SIGINT must reach the encoder so ffmpeg flushes its final segment. An
	// orphaned ffmpeg keeps the segment directory and the next start fails.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
