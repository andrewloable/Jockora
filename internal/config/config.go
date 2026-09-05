// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package config holds Jockora's runtime configuration. Flags and environment
// variables only: no config file parser, because flags plus env is the smallest
// surface that works identically in a container and as a bare binary.
package config

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

const envPrefix = "JOCKORA_"

// Config is the whole of Jockora's runtime configuration.
type Config struct {
	LibraryPath string // music library root; read-only, Jockora never writes here
	DBPath      string // SQLite file holding dossiers, stations and state
	SegmentDir  string // where HLS segments and the playlist are written
	ListenAddr  string // host:port for the HTTP server; loopback by default
	LLMBaseURL  string // llama-server base URL, native /completion API
	TTSAddr     string // Kokoro sidecar base URL

	SampleRate        int // canonical bus sample rate
	Channels          int // canonical bus channel count
	SegmentSeconds    int // HLS target segment duration
	ListSize          int // HLS playlist window, in segments
	BreakEveryNTracks int // schedule a DJ break after this many tracks

	CrossfadeSeconds float64 // default crossfade length at a track boundary

	// Args holds the positional arguments left after flag parsing, so a command
	// can read them without parsing os.Args a second time.
	Args []string
}

// Load reads configuration from the command line, then fills in any flag the
// operator did not set from the matching environment variable. Precedence is
// flag, then env, then default.
func Load() (*Config, error) { return LoadWith(nil) }

// LoadWith is Load, with a chance to register additional flags first.
//
// It exists because there is exactly one flag set: a command that registered
// its own flags on flag.CommandLine would have them rejected here as unknown,
// which is a failure that only appears when the binary is actually run.
func LoadWith(register func(*flag.FlagSet)) (*Config, error) {
	var c Config

	fs := flag.NewFlagSet("jockora", flag.ContinueOnError)
	if register != nil {
		register(fs)
	}
	fs.StringVar(&c.LibraryPath, "library-path", "", "music library root (read-only)")
	fs.StringVar(&c.DBPath, "db-path", "jockora.db", "SQLite database file")
	fs.StringVar(&c.SegmentDir, "segment-dir", "segments", "directory for HLS segments")
	// Loopback, never 0.0.0.0: v0.1 ships no authentication at all, and
	// self-hosters port-forward services without realising.
	fs.StringVar(&c.ListenAddr, "listen-addr", "127.0.0.1:8080", "HTTP listen address")
	// 8081, not llama-server's own default of 8080, because Jockora already
	// listens there. Start llama-server with --port 8081 or set JOCKORA_LLM_URL.
	fs.StringVar(&c.LLMBaseURL, "llm-url", "http://127.0.0.1:8081", "llama-server base URL")
	fs.StringVar(&c.TTSAddr, "tts-url", "http://127.0.0.1:8090", "TTS sidecar base URL")

	fs.IntVar(&c.SampleRate, "sample-rate", 48000, "canonical bus sample rate")
	fs.IntVar(&c.Channels, "channels", 2, "canonical bus channel count")
	fs.IntVar(&c.SegmentSeconds, "segment-seconds", 4, "HLS segment duration")
	fs.IntVar(&c.ListSize, "list-size", 10, "HLS playlist window, in segments")
	fs.IntVar(&c.BreakEveryNTracks, "break-every-n-tracks", 4, "tracks between DJ breaks")

	fs.Float64Var(&c.CrossfadeSeconds, "crossfade-seconds", 2.0, "crossfade length at a track boundary")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}

	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	var err error
	fs.VisitAll(func(f *flag.Flag) {
		if err != nil || explicit[f.Name] {
			return
		}
		v := os.Getenv(envKey(f.Name))
		if v == "" {
			return
		}
		// Set, not a manual assignment, so ints and floats are parsed and
		// rejected the same way the flag would have been.
		if setErr := fs.Set(f.Name, v); setErr != nil {
			err = fmt.Errorf("%s: %w", envKey(f.Name), setErr)
		}
	})
	if err != nil {
		return nil, err
	}

	c.Args = fs.Args()
	return &c, nil
}

// envKey maps a flag name to its environment variable: listen-addr becomes
// JOCKORA_LISTEN_ADDR.
func envKey(flagName string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
}
