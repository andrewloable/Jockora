// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package app assembles the spine into a running program: decoders feeding a
// ring, one mixer goroutine writing to a supervised ffmpeg, and an HTTP server
// handing the result to listeners.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/decode"
	"github.com/andrewloable/jockora/internal/encode"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/sched"
	"github.com/andrewloable/jockora/internal/server"
)

// Options are the run-specific choices the config file does not cover.
type Options struct {
	Tracks     []string // music files, played in order and then looped
	BreakPath  string   // a WAV to splice, optional
	BreakAtSec int      // when it should be heard, in seconds from the start
	Log        *slog.Logger
}

// App is one running station.
type App struct {
	cfg  *config.Config
	opts Options
	log  *slog.Logger

	ring    *mix.Ring
	queue   *sched.Queue
	srv     *server.Server
	sup     *encode.Supervisor
	metrics mix.Metrics

	// stallUntil pauses the feeder, for the fault injection GATE 2 requires.
	// Guarded because the signal handler and the feeder are different goroutines.
	stallMu    sync.Mutex
	stallUntil time.Time
}

// ringSeconds is how much decoded audio is buffered ahead of the mixer. Enough
// to ride out a slow disk or a decoder starting up, small enough that a track
// change is not delayed by a wall of buffered audio.
const ringSeconds = 10

// New validates everything that can be validated before anything starts, then
// opens the listener and spawns ffmpeg.
//
// Failures that would only appear at airtime are checked here instead: a missing
// break file, no tracks, a non-loopback listen address.
func New(cfg *config.Config, opts Options) (*App, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if len(opts.Tracks) == 0 {
		return nil, errors.New("app: no tracks to play")
	}
	for _, p := range opts.Tracks {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("app: track %s: %w", p, err)
		}
	}

	a := &App{
		cfg:   cfg,
		opts:  opts,
		log:   log,
		ring:  mix.NewRing(ringSeconds * mix.SampleRate),
		queue: &sched.Queue{},
	}

	if opts.BreakPath != "" {
		// Read it now rather than at airtime. A break that cannot be read is a
		// dropped break, and finding that out mid-stream is much harder to
		// diagnose than refusing to start.
		if _, err := mix.ReadWAV(opts.BreakPath); err != nil {
			return nil, fmt.Errorf("app: break audio: %w", err)
		}
		at := int64(opts.BreakAtSec) * mix.SampleRate
		if err := a.queue.Enqueue(sched.Entry{
			AfterSample: at,
			Action:      sched.ActionSpliceAudio,
			Path:        opts.BreakPath,
			Placement:   mix.PlacementBetween.String(),
		}); err != nil {
			return nil, fmt.Errorf("app: scheduling break: %w", err)
		}
	}

	srv, err := server.New(server.Config{
		ListenAddr: cfg.ListenAddr,
		SegmentDir: cfg.SegmentDir,
	}, log)
	if err != nil {
		return nil, err
	}
	a.srv = srv

	// The encoder comes up before the mixer, so the mixer never writes into a
	// pipe that does not exist yet.
	sup, err := encode.StartSupervisor(context.Background(), encode.Config{
		SegmentDir:     cfg.SegmentDir,
		SegmentSeconds: cfg.SegmentSeconds,
		ListSize:       cfg.ListSize,
		SampleRate:     cfg.SampleRate,
		Channels:       cfg.Channels,
	}, log)
	if err != nil {
		return nil, err
	}
	a.sup = sup

	return a, nil
}

// Addr is the address the server is listening on.
func (a *App) Addr() string { return a.srv.Addr() }

// PendingBreaks reports how many scheduled items have not aired yet.
func (a *App) PendingBreaks() int { return a.queue.Pending() }

// Metrics reports the run so far: inter-write gaps, ring occupancy, write count.
func (a *App) Metrics() mix.MetricsSnapshot { return a.metrics.Snapshot() }

// StallFeeder pauses decoding for d, leaving the mixer to ride it out.
//
// This is fault injection, not a feature. It stalls the DECODER on purpose so
// the safety net can be seen firing: the ring drains, silence-fill covers it,
// UnderrunCount rises, and the stream keeps playing. A stall that ends the
// stream is a failure. Nothing stalls the mixer itself.
func (a *App) StallFeeder(d time.Duration) {
	a.stallMu.Lock()
	defer a.stallMu.Unlock()
	a.stallUntil = time.Now().Add(d)
	a.log.Warn("FAULT INJECTION: stalling the decoder", "for", d)
}

// feederStall reports how much longer the feeder should stay paused.
func (a *App) feederStall() time.Duration {
	a.stallMu.Lock()
	defer a.stallMu.Unlock()
	return time.Until(a.stallUntil)
}

// Run streams until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	defer a.sup.Stop()

	go func() {
		if err := a.srv.Run(ctx); err != nil && ctx.Err() == nil {
			a.log.Error("http server stopped", "err", err)
		}
	}()

	go a.feedTracks(ctx)

	a.log.Info("on air",
		"listen", "http://"+a.Addr(),
		"segments", a.cfg.SegmentDir,
		"tracks", len(a.opts.Tracks))

	// One mixer goroutine, owning the bus clock, sole writer to the encoder.
	m := &mix.Mixer{
		Ring:    a.ring,
		Queue:   a.queue,
		Chain:   mix.NewChain(),
		Pacer:   mix.NewPacer(clock.Real{}),
		W:       a.sup.Writer(),
		Log:     a.log,
		Metrics: &a.metrics,
	}
	err := m.Run(ctx)

	// The four numbers GATE 2 asserts on, logged at shutdown so a long
	// unattended run needs no extra tooling to be judged.
	s := a.metrics.Snapshot()
	a.log.Info("off air",
		"frames", m.SamplePos(),
		"audio", mix.FramesToDuration(int(m.SamplePos())).Round(time.Second),
		"p99_inter_write_gap", s.P99Gap.Round(time.Millisecond),
		"max_inter_write_gap", s.MaxGap.Round(time.Millisecond),
		"min_ring_occupancy", s.MinOccupancy.Round(time.Millisecond),
		"final_drift", m.Pacer.Drift().Round(time.Millisecond),
		"underruns", a.ring.UnderrunCount(),
		"encoder_restarts", a.sup.Restarts())

	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// feedTracks decodes the track list into the ring, looping forever. A track that
// will not decode is logged and skipped: one bad file must not end the stream.
func (a *App) feedTracks(ctx context.Context) {
	blocks := make(chan []mix.Frame, 8)

	for ctx.Err() == nil {
		played := 0
		for _, path := range a.opts.Tracks {
			if ctx.Err() != nil {
				return
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				for b := range blocks {
					// Honour an injected stall here, between decode and the
					// ring, so the decoder is what pauses and the mixer is left
					// to ride it out on silence-fill.
					for d := a.feederStall(); d > 0; d = a.feederStall() {
						select {
						case <-ctx.Done():
							return
						case <-time.After(min(d, 100*time.Millisecond)):
						}
					}
					if err := feedRing(ctx, a.ring, b); err != nil {
						return
					}
				}
			}()

			err := decode.Decode(ctx, path, blocks)
			// The consumer must finish before the next track reuses the channel.
			for len(blocks) > 0 && ctx.Err() == nil {
				time.Sleep(time.Millisecond)
			}
			close(blocks)
			<-done
			blocks = make(chan []mix.Frame, 8)

			if err != nil {
				if ctx.Err() != nil {
					return
				}
				a.log.Warn("skipping track", "path", path, "err", err)
				continue
			}
			played++
		}
		if played == 0 {
			a.log.Error("no track in the list could be decoded; the stream will be silence")
			return
		}
	}
}

// feedRing writes every frame into the ring, retrying while it is full.
//
// Ring.Write returns short when there is no space, by design. Treating a short
// write as complete would silently drop audio, which is the kind of bug that
// only shows up as an occasional glitch nobody can reproduce.
func feedRing(ctx context.Context, ring *mix.Ring, frames []mix.Frame) error {
	for len(frames) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := ring.Write(frames)
		frames = frames[n:]
		if n == 0 {
			// Full. The mixer drains at real-time speed, so a short wait is
			// exactly the right amount of patience.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	return nil
}

// encoderPID exposes the running ffmpeg so tests can prove it was reaped.
func (a *App) encoderPID() int { return a.sup.PID() }
