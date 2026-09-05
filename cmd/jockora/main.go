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
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jockora:", err)
		os.Exit(1)
	}
}

func run() error {
	// Run-specific flags live here rather than in config, which describes the
	// server rather than one spike run.
	var breakPath string
	var breakAt int
	cfg, err := config.LoadWith(func(fs *flag.FlagSet) {
		fs.StringVar(&breakPath, "break", "", "WAV to splice into the stream (48kHz 16-bit)")
		fs.IntVar(&breakAt, "break-at", 30, "when the break should be heard, seconds from start")
	})
	if err != nil {
		return err
	}

	tracks := cfg.Args
	if len(tracks) == 0 {
		return fmt.Errorf("usage: jockora [flags] <track> [track...]\n" +
			"       jockora -break voice.wav -break-at 30 a.mp3 b.mp3")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	a, err := app.New(cfg, app.Options{
		Tracks:     tracks,
		BreakPath:  breakPath,
		BreakAtSec: breakAt,
		Log:        log,
	})
	if err != nil {
		return err
	}

	// SIGINT must reach the encoder so ffmpeg flushes its final segment. An
	// orphaned ffmpeg keeps the segment directory and the next start fails.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// SIGUSR1 stalls the decoder for eight seconds. This is GATE 2's first fault
	// injection: `kill -USR1 <pid>` should make the ring drain, silence-fill
	// cover it and the stream carry on. It is a test hook, not a feature.
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for range usr1 {
			a.StallFeeder(8 * time.Second)
		}
	}()

	return a.Run(ctx)
}
