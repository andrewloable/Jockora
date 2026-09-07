// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/encode"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/sched"
	"github.com/andrewloable/jockora/internal/store"
)

// ErrRuntimeRunning refuses a second Start on a station already on air.
var ErrRuntimeRunning = errors.New("station: this station is already running")

// RingSeconds is how much decoded audio is buffered ahead of the mixer. Enough
// to ride out a slow disk or a decoder starting up, small enough that a track
// change is not delayed by a wall of buffered audio.
const RingSeconds = 10

// Encoder is the part of encode.Supervisor a runtime uses.
//
// An interface only so a test can prove the encoder was actually killed. The
// real one is *encode.Supervisor and nothing here knows the difference.
type Encoder interface {
	Writer() io.Writer
	Stop() error
	Restarts() int
	Degraded() string
	PID() int
}

// Deps is everything one station's pipeline needs from outside.
type Deps struct {
	Log   *slog.Logger
	Clock clock.Clock

	SampleRate, Channels, SegmentSeconds, ListSize int

	// Sel draws the tracks. Writer is told which tracks surround each break.
	Sel      *Selector
	BreakOut *BreakWriter

	// Feed keeps the ring full for as long as its context lives. Supplied by
	// the caller because WHAT to play is the application's business, while
	// keeping the bus fed and the encoder alive is the runtime's.
	Feed func(ctx context.Context, ring *mix.Ring, queue *sched.Queue)

	// StartEncoder spawns the encoder. Defaults to encode.StartSupervisor.
	StartEncoder func(context.Context, encode.Config, *slog.Logger) (Encoder, error)
}

// RuntimeStatus is one station's pipeline, from outside.
type RuntimeStatus struct {
	StationID       int64   `json:"station_id"`
	Running         bool    `json:"running"`
	RingOccupancyS  float64 `json:"ring_occupancy_s"`
	PendingBreaks   int     `json:"pending_breaks"`
	EncoderRestarts int     `json:"encoder_restarts"`
	Underruns       uint64  `json:"underruns"`
}

// Runtime is ONE station's pipeline: its ring, its queue, its encoder and the
// goroutines that drive them.
//
// Each station owns all of it. Sharing a ring or a queue would make one
// station's stall another station's silence, and the whole point of a per
// station runtime is that they are separate timelines.
type Runtime struct {
	stationID  int64
	segmentDir string
	deps       Deps

	mu      sync.Mutex
	running bool
	// restarted marks a runtime that has been stopped at least once, so the
	// next Start knows to build a fresh ring and queue rather than resume a
	// spent one.
	restarted bool
	cancel    context.CancelFunc
	mixDone   chan struct{}
	feedDone  chan struct{}
	mixErr    error

	ring    *mix.Ring
	queue   *sched.Queue
	enc     Encoder
	mixer   *mix.Mixer
	metrics mix.Metrics
}

// NewRuntime builds a stopped station.
//
// The ring and the queue exist from here rather than from Start, because a
// caller has to be able to schedule into the queue before the station goes on
// air -- which is exactly what the repeating break window does.
func NewRuntime(d Deps, stationID int64, segmentDir string) *Runtime {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.StartEncoder == nil {
		d.StartEncoder = func(ctx context.Context, cfg encode.Config, log *slog.Logger) (Encoder, error) {
			return encode.StartSupervisor(ctx, cfg, log)
		}
	}
	return &Runtime{
		stationID:  stationID,
		segmentDir: segmentDir,
		deps:       d,
		ring:       mix.NewRing(RingSeconds * mix.SampleRate),
		queue:      &sched.Queue{},
	}
}

// NewRuntimeForStation builds a station's pipeline over its own playlist.
//
// The playlist is read FRESH each time a station comes up, so a rescan or an
// operator's edit between one listener leaving and the next arriving is already
// in effect when it does.
func NewRuntimeForStation(ctx context.Context, s *store.Store, d Deps,
	stationID int64, segmentDir string, state store.SelectorState, resume bool) (*Runtime, error) {
	pool, err := PoolForStation(ctx, s, stationID)
	if err != nil {
		return nil, err
	}

	// A fresh station draws its own seed, so two coming up at the same moment
	// do not play the same order.
	d.Sel = NewSelector(pool, time.Now().UnixNano())
	if resume {
		// The playlist may have changed while the station was off air, so the
		// stored cursor cannot always be honoured exactly; NewSelectorAt
		// reshuffles rather than resuming into nothing.
		d.Sel = NewSelectorAt(pool, state.Seed, state.Cursor, nil)
	}
	return NewRuntime(d, stationID, segmentDir), nil
}

// Ring is the decoded-audio buffer this station feeds.
func (r *Runtime) Ring() *mix.Ring {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ring
}

// Queue holds the breaks waiting to air on this station.
func (r *Runtime) Queue() *sched.Queue {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queue
}

// Selector draws this station's tracks.
func (r *Runtime) Selector() *Selector { return r.deps.Sel }

// Writer is told which tracks surround each break.
func (r *Runtime) Writer() *BreakWriter { return r.deps.BreakOut }

// StationID names the station this pipeline belongs to.
func (r *Runtime) StationID() int64 { return r.stationID }

// SegmentDir is where this station's HLS output lands. One per station, or two
// stations would overwrite each other's segments.
func (r *Runtime) SegmentDir() string { return r.segmentDir }

// Encoder is the running ffmpeg, or nil before the first Start.
func (r *Runtime) Encoder() Encoder {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enc
}

// Metrics is the mixer's timing record.
func (r *Runtime) Metrics() *mix.Metrics { return &r.metrics }

// Start puts the station on air.
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return ErrRuntimeRunning
	}
	// A restart gets a FRESH ring and queue. Resuming the old ones would open
	// with whatever audio was buffered when the last listener left, and with
	// breaks scheduled against a sample position that no longer exists.
	if r.restarted {
		r.ring = mix.NewRing(RingSeconds * mix.SampleRate)
		r.queue = &sched.Queue{}
		r.metrics = mix.Metrics{}
	}
	ring, queue := r.ring, r.queue
	r.mu.Unlock()

	// Its own directory, created here: two stations writing into one would
	// overwrite each other's segments and each other's playlist.
	if err := os.MkdirAll(r.segmentDir, 0o755); err != nil {
		return fmt.Errorf("station %d: making the segment directory: %w", r.stationID, err)
	}

	// The encoder comes up BEFORE the mixer, so the mixer never writes into a
	// pipe that does not exist yet.
	enc, err := r.deps.StartEncoder(ctx, encode.Config{
		SegmentDir:     r.segmentDir,
		SegmentSeconds: r.deps.SegmentSeconds,
		ListSize:       r.deps.ListSize,
		SampleRate:     r.deps.SampleRate,
		Channels:       r.deps.Channels,
		// CONTINUE the numbering across a restart. Starting again at seg0
		// overwrites the segments the previous run wrote, and those are served
		// with a one-year immutable cache -- so a listener who returns later
		// gets yesterday's audio out of their own cache and no request ever
		// reaches the server to correct it.
		StartNumber: nextSegmentNumber(r.segmentDir),
	}, r.deps.Log)
	if err != nil {
		return fmt.Errorf("station %d: starting the encoder: %w", r.stationID, err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	m := &mix.Mixer{
		Ring:    ring,
		Queue:   queue,
		Chain:   mix.NewChain(),
		Pacer:   mix.NewPacer(r.deps.Clock),
		W:       enc.Writer(),
		Log:     r.deps.Log,
		Metrics: &r.metrics,
	}
	mixDone, feedDone := make(chan struct{}), make(chan struct{})

	r.mu.Lock()
	r.enc, r.mixer, r.cancel = enc, m, cancel
	r.mixDone, r.feedDone, r.mixErr = mixDone, feedDone, nil
	r.running, r.restarted = true, true
	r.mu.Unlock()

	go func() {
		defer close(feedDone)
		if r.deps.Feed != nil {
			r.deps.Feed(runCtx, ring, queue)
		}
	}()
	go func() {
		defer close(mixDone)
		err := m.Run(runCtx)
		r.mu.Lock()
		r.mixErr = err
		r.mu.Unlock()
	}()
	return nil
}

// Wait blocks until the station goes off air and reports why.
//
// A cancelled context is a normal stop, not a failure: it is what the last
// listener leaving looks like.
func (r *Runtime) Wait() error {
	r.mu.Lock()
	done := r.mixDone
	r.mu.Unlock()
	if done == nil {
		return nil
	}
	<-done

	r.mu.Lock()
	defer r.mu.Unlock()
	if errors.Is(r.mixErr, context.Canceled) {
		return nil
	}
	return r.mixErr
}

// Stop takes the station off air and reaps its encoder.
//
// IDEMPOTENT: the manager may stop a station that has already stopped itself,
// and a second Stop that panicked would take the whole server with it.
func (r *Runtime) Stop() error {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return nil
	}
	cancel, mixDone, feedDone, enc := r.cancel, r.mixDone, r.feedDone, r.enc
	r.running = false
	r.mu.Unlock()

	cancel()
	// Both goroutines, not just the mixer. A feeder still decoding into a ring
	// nobody drains is a station that stopped on paper and is still burning a
	// core.
	<-mixDone
	<-feedDone
	return enc.Stop()
}

// Status reports what an operator needs to see about one station.
func (r *Runtime) Status() RuntimeStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	st := RuntimeStatus{StationID: r.stationID, Running: r.running}
	if r.ring != nil {
		st.RingOccupancyS = r.ring.Occupancy().Seconds()
		st.Underruns = r.ring.UnderrunCount()
	}
	if r.queue != nil {
		st.PendingBreaks = r.queue.Pending()
	}
	if r.enc != nil {
		st.EncoderRestarts = r.enc.Restarts()
	}
	return st
}

// SamplePos is how far the mixer has got, in frames. Zero before the first
// Start, which is what a station that has never played has produced.
func (r *Runtime) SamplePos() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mixer == nil {
		return 0
	}
	return r.mixer.SamplePos()
}

// Drift is how far the bus clock has slipped from real time.
func (r *Runtime) Drift() (d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mixer == nil {
		return 0
	}
	return r.mixer.Pacer.Drift()
}

// segmentFile matches the encoder's own output names.
var segmentFile = regexp.MustCompile(`^seg(\d+)\.ts$`)

// nextSegmentNumber is one past the highest segment already on disk, so a
// restarted station never reuses a URL it has already served.
func nextSegmentNumber(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Nothing there yet, or nothing readable: starting at zero is right
		// for the first case and harmless for the second, which Start is about
		// to fail on anyway.
		return 0
	}
	highest := -1
	for _, e := range entries {
		m := segmentFile.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return highest + 1
}
