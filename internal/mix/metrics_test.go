// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/sched"
)

func TestMetricsRecordsGapsAndOccupancy(t *testing.T) {
	var m Metrics

	m.recordWrite(80 * time.Millisecond)
	m.recordWrite(90 * time.Millisecond)
	m.recordWrite(300 * time.Millisecond)
	m.recordOccupancy(5 * time.Second)
	m.recordOccupancy(2500 * time.Millisecond)
	m.recordOccupancy(9 * time.Second)

	s := m.Snapshot()
	if s.Writes != 3 {
		t.Errorf("Writes = %d, want 3", s.Writes)
	}
	if s.MaxGap != 300*time.Millisecond {
		t.Errorf("MaxGap = %v, want 300ms", s.MaxGap)
	}
	if s.MinOccupancy != 2500*time.Millisecond {
		t.Errorf("MinOccupancy = %v, want 2.5s", s.MinOccupancy)
	}
}

// TestMetricsP99IsTheTail pins the percentile's semantics in both directions.
// GATE 2 asks for p99 rather than max precisely so that a handful of outliers
// does not fail a healthy run, and asks for it rather than a mean so that a
// sustained problem cannot be averaged away.
func TestMetricsP99IsTheTail(t *testing.T) {
	fill := func(fast, slow int) MetricsSnapshot {
		var m Metrics
		for i := 0; i < fast; i++ {
			m.recordWrite(80 * time.Millisecond)
		}
		for i := 0; i < slow; i++ {
			m.recordWrite(2 * time.Second)
		}
		return m.Snapshot()
	}

	// 2% slow: the tail is real and p99 must show it.
	if s := fill(980, 20); s.P99Gap != 2*time.Second {
		t.Errorf("with 2%% slow writes P99Gap = %v, want 2s: a sustained problem was averaged away", s.P99Gap)
	}

	// Exactly 1% slow: nearest-rank p99 tolerates it by design, and max is where
	// that outlier still shows up.
	s := fill(990, 10)
	if s.P99Gap != 80*time.Millisecond {
		t.Errorf("with 1%% slow writes P99Gap = %v, want 80ms: p99 is meant to tolerate that much", s.P99Gap)
	}
	if s.MaxGap != 2*time.Second {
		t.Errorf("MaxGap = %v, want 2s: the outlier must still be visible somewhere", s.MaxGap)
	}
}

func TestMetricsEmptySnapshotIsSafe(t *testing.T) {
	var m Metrics
	s := m.Snapshot()

	if s.Writes != 0 || s.P99Gap != 0 || s.MaxGap != 0 {
		t.Errorf("empty snapshot = %+v, want zeros", s)
	}
	if s.MinOccupancy != 0 {
		t.Errorf("MinOccupancy = %v, want 0 when nothing was recorded", s.MinOccupancy)
	}
}

// TestMetricsIsOptional: a nil Metrics must change nothing about the mixer.
func TestMetricsIsOptional(t *testing.T) {
	m := newMetricsMixer(t, nil, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	m.stopAfterFrames(cancel, 2*SampleRate)

	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run with nil Metrics: %v", err)
	}
	if m.SamplePos() == 0 {
		t.Error("the mixer produced nothing")
	}
}

// TestMixerRecordsMetricsThroughTheFakeClock proves the numbers come from the
// pacer's clock rather than time.Now(): with a fake clock the gaps are exactly
// the block duration, deterministically.
func TestMixerRecordsMetricsThroughTheFakeClock(t *testing.T) {
	var metrics Metrics
	const block = 4800

	m := newMetricsMixer(t, &metrics, block)
	m.Ring.Write(sine(4*SampleRate, 440, 0.3))

	ctx, cancel := context.WithCancel(context.Background())
	m.stopAfterFrames(cancel, 2*SampleRate)
	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run: %v", err)
	}

	s := metrics.Snapshot()
	want := FramesToDuration(block)
	if s.Writes < 10 {
		t.Fatalf("only %d writes recorded", s.Writes)
	}
	if s.MaxGap != want {
		t.Errorf("MaxGap = %v, want exactly the block duration %v: the metric is not using the pacer's clock",
			s.MaxGap, want)
	}
	if s.P99Gap != want {
		t.Errorf("P99Gap = %v, want %v", s.P99Gap, want)
	}
}

// TestMixerRecordsAStall is fault injection 1 in miniature: time jumps while the
// mixer is working, and the metric must show it rather than average it away.
func TestMixerRecordsAStall(t *testing.T) {
	var metrics Metrics
	const block = 4800

	m := newMetricsMixer(t, &metrics, block)
	m.Ring.Write(sine(4*SampleRate, 440, 0.3))

	fake := m.Pacer.Clock().(*clock.Fake)
	ctx, cancel := context.WithCancel(context.Background())

	stalled := false
	m.onBlock = func(pos int64) {
		if !stalled && pos >= int64(block)*5 {
			stalled = true
			fake.Advance(8 * time.Second) // the decoder stall GATE 2 injects
		}
		if pos >= 2*SampleRate {
			cancel()
		}
	}

	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run: %v", err)
	}

	s := metrics.Snapshot()
	if s.MaxGap < 8*time.Second {
		t.Errorf("MaxGap = %v, want at least the 8s stall: the metric smoothed over the fault", s.MaxGap)
	}
	// And the stream did not stop.
	if m.SamplePos() < 2*SampleRate {
		t.Errorf("the mixer produced %d frames and stopped; a stall must not end the stream", m.SamplePos())
	}
}

// TestMixerRecordsMinimumOccupancy: GATE 2 asserts the ring never drops below
// two seconds, so the minimum has to be tracked, not the average.
func TestMixerRecordsMinimumOccupancy(t *testing.T) {
	var metrics Metrics

	m := newMetricsMixer(t, &metrics, 4800)
	m.Ring.Write(sine(3*SampleRate, 440, 0.3)) // three seconds, then it drains

	ctx, cancel := context.WithCancel(context.Background())
	m.stopAfterFrames(cancel, 4*SampleRate)
	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run: %v", err)
	}

	s := metrics.Snapshot()
	if s.MinOccupancy != 0 {
		t.Errorf("MinOccupancy = %v, want 0: the ring was drained dry and the minimum must show it", s.MinOccupancy)
	}
	if m.Ring.UnderrunCount() == 0 {
		t.Error("the ring drained dry without counting an underrun")
	}
}

// --- helpers ---

func newMetricsMixer(t *testing.T, metrics *Metrics, block int) *Mixer {
	t.Helper()
	return &Mixer{
		Ring:        NewRing(10 * SampleRate),
		Queue:       &sched.Queue{},
		Chain:       NewChain(),
		Pacer:       NewPacer(clock.NewFake(time.Unix(0, 0))),
		W:           &bytes.Buffer{},
		Log:         slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		BlockFrames: block,
		Metrics:     metrics,
	}
}

func (m *Mixer) stopAfterFrames(cancel context.CancelFunc, frames int64) {
	m.onBlock = func(pos int64) {
		if pos >= frames {
			cancel()
		}
	}
}

// TestMixerPrebuffersBeforeGoingOnAir: without this the stream opens on
// silence-fill and the minimum ring occupancy over any run is always zero,
// which makes GATE 2's occupancy criterion unpassable no matter how healthy the
// run actually was.
func TestMixerPrebuffersBeforeGoingOnAir(t *testing.T) {
	var metrics Metrics
	m := newMetricsMixer(t, &metrics, 4800)
	m.Ring.Write(sine(6*SampleRate, 440, 0.3)) // six seconds waiting

	ctx, cancel := context.WithCancel(context.Background())
	m.stopAfterFrames(cancel, 2*SampleRate)
	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run: %v", err)
	}

	if got := metrics.Snapshot().MinOccupancy; got < 2*time.Second {
		t.Errorf("MinOccupancy = %v, want at least the 2s prebuffer", got)
	}
	if m.Ring.UnderrunCount() != 0 {
		t.Errorf("%d underruns with six seconds buffered: the mixer started before the ring filled",
			m.Ring.UnderrunCount())
	}
}

// TestMixerPrebufferIsBounded: a library where nothing decodes must still
// produce a stream rather than hanging silently before the first byte.
func TestMixerPrebufferIsBounded(t *testing.T) {
	m := newMetricsMixer(t, nil, 4800)
	// Nothing is ever written to the ring.

	ctx, cancel := context.WithCancel(context.Background())
	m.stopAfterFrames(cancel, SampleRate)

	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run never went on air with an empty ring: the prebuffer wait is unbounded")
	}
	if m.SamplePos() == 0 {
		t.Error("the mixer produced nothing at all")
	}
}

func TestMixerPrebufferCanBeDisabled(t *testing.T) {
	m := newMetricsMixer(t, nil, 4800)
	m.PrebufferFrames = -1

	ctx, cancel := context.WithCancel(context.Background())
	m.stopAfterFrames(cancel, SampleRate)
	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run: %v", err)
	}
	if m.SamplePos() < SampleRate {
		t.Error("the mixer did not run with the prebuffer disabled")
	}
}

// TestMixerLogsUnderrunsAsTheyHappen: silence-fill is this system's signature
// failure -- nothing sounds broken. Reporting it only in the shutdown summary
// makes a decoder falling behind invisible for the whole run, which is exactly
// the silent degradation the observability contract exists to surface.
func TestMixerLogsUnderrunsAsTheyHappen(t *testing.T) {
	var logs bytes.Buffer
	m := newMetricsMixer(t, nil, 4800)
	m.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	m.PrebufferFrames = -1 // go on air immediately, with a dry ring

	ctx, cancel := context.WithCancel(context.Background())
	m.stopAfterFrames(cancel, SampleRate)
	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run: %v", err)
	}

	out := logs.String()
	if !strings.Contains(out, `"event":"ring_underrun"`) {
		t.Fatalf("a dry ring produced no ring_underrun record:\n%s", out)
	}
	if !strings.Contains(out, "frames_filled") {
		t.Error("the record does not say how many real frames were available")
	}

	// Logged once per episode, not once per block: a sustained stall would
	// otherwise emit a dozen lines a second and drown everything else.
	if n := strings.Count(out, `"event":"ring_underrun"`); n > 3 {
		t.Errorf("%d underrun records for one continuous stall; it should log the episode, not every block", n)
	}
}

// TestMixerDoesNotLogUnderrunsWhenHealthy keeps the log quiet on a good run, or
// the record stops meaning anything.
func TestMixerDoesNotLogUnderrunsWhenHealthy(t *testing.T) {
	var logs bytes.Buffer
	m := newMetricsMixer(t, nil, 4800)
	m.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	m.Ring.Write(sine(4*SampleRate, 440, 0.3))

	ctx, cancel := context.WithCancel(context.Background())
	m.stopAfterFrames(cancel, 2*SampleRate)
	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "ring_underrun") {
		t.Errorf("a healthy run logged an underrun:\n%s", logs.String())
	}
}
