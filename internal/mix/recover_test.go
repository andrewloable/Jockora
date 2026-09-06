// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/sched"
)

// panicChain panics on the blocks a test names and otherwise behaves.
type panicChain struct {
	inner   *Chain
	calls   atomic.Int64
	panicOn func(n int64) bool
}

func (p *panicChain) LatencyFrames() int { return p.inner.LatencyFrames() }

func (p *panicChain) Process(out, musicA, musicB, speech []Frame, st State) {
	if n := p.calls.Add(1); p.panicOn != nil && p.panicOn(n) {
		panic("chain exploded on block " + string(rune('0'+n%10)))
	}
	p.inner.Process(out, musicA, musicB, speech, st)
}

func recoverMixer(t *testing.T, ch *panicChain, log *slog.Logger) (*Mixer, *clock.Fake, *bytes.Buffer) {
	t.Helper()
	fake := clock.NewFake(time.Unix(1_700_000_000, 0))
	out := &bytes.Buffer{}
	ring := NewRing(10 * SampleRate)
	// Filled so Read never underruns and the test measures only the chain.
	ring.Write(make([]Frame, 10*SampleRate))

	m := &Mixer{
		Ring:        ring,
		Chain:       ch,
		Queue:       &sched.Queue{},
		Pacer:       NewPacer(fake),
		W:           out,
		Log:         log,
		BlockFrames: 4800,
	}
	return m, fake, out
}

// TestMixerRecoversFromPanic. The mixer is the one goroutine whose death is
// total silence and the only one with nothing above it to restart it.
func TestMixerRecoversFromPanic(t *testing.T) {
	var logbuf bytes.Buffer
	ch := &panicChain{inner: NewChain(), panicOn: func(n int64) bool { return n == 3 }}
	m, fake, out := recoverMixer(t, ch, slog.New(slog.NewTextHandler(&logbuf, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	// Let it run well past the exploding block.
	waitForBlocks(t, ch, 8, fake)
	cancel()
	<-done

	if out.Len() == 0 {
		t.Fatal("the mixer wrote nothing")
	}
	if !strings.Contains(logbuf.String(), "mixer panicked") {
		t.Errorf("the panic was swallowed silently:\n%s", logbuf.String())
	}
	if ch.calls.Load() < 6 {
		t.Errorf("the chain ran %d times; the mixer stopped at the panic instead of continuing", ch.calls.Load())
	}
}

// TestMixerRecoverPanicIsLoggedWithStack. A recovered panic with no stack is a
// bug that has been made invisible rather than survivable.
func TestMixerRecoverPanicIsLoggedWithStack(t *testing.T) {
	var logbuf bytes.Buffer
	ch := &panicChain{inner: NewChain(), panicOn: func(n int64) bool { return n == 2 }}
	m, fake, _ := recoverMixer(t, ch, slog.New(slog.NewTextHandler(&logbuf, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	waitForBlocks(t, ch, 5, fake)
	cancel()
	<-done

	logged := logbuf.String()
	if !strings.Contains(logged, "stack") {
		t.Fatalf("no stack in the log:\n%s", logged)
	}
	if !strings.Contains(logged, "mix.(*Mixer)") {
		t.Errorf("the stack does not name the mixer, so it points nowhere useful:\n%s", logged)
	}
	if !strings.Contains(logged, "chain exploded") {
		t.Errorf("the panic value was dropped:\n%s", logged)
	}
}

// TestMixerRecoverRepeatedPanicsGiveUp. Recovering forever is worse than dying:
// it produces a stream of silence while every health check reports running.
func TestMixerRecoverRepeatedPanicsGiveUp(t *testing.T) {
	ch := &panicChain{inner: NewChain(), panicOn: func(int64) bool { return true }}
	m, fake, _ := recoverMixer(t, ch, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	done := make(chan error, 1)
	go func() { done <- m.Run(context.Background()) }()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("the mixer returned nil after panicking on every block")
			}
			if !strings.Contains(err.Error(), "in a row") {
				t.Errorf("error = %v, want it to say the panics were consecutive", err)
			}
			if got := ch.calls.Load(); got > MaxConsecutivePanics+2 {
				t.Errorf("the chain ran %d times before giving up, want about %d", got, MaxConsecutivePanics)
			}
			return
		case <-deadline:
			t.Fatal("the mixer never gave up; it is spinning on panics forever")
		case <-time.After(5 * time.Millisecond):
			fake.Advance(100 * time.Millisecond)
		}
	}
}

// TestMixerRecoverIsolatedPanicsDoNotAccumulate: a bad block every so often is
// a bug worth logging, not a reason to take the station off air.
func TestMixerRecoverIsolatedPanicsDoNotAccumulate(t *testing.T) {
	ch := &panicChain{inner: NewChain(), panicOn: func(n int64) bool { return n%3 == 0 }}
	m, fake, _ := recoverMixer(t, ch, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	// Far more panics than the ceiling, but never consecutive.
	waitForBlocks(t, ch, 3*MaxConsecutivePanics+6, fake)
	cancel()
	if err := <-done; err != nil && !strings.Contains(err.Error(), "context") {
		t.Errorf("the mixer gave up on non-consecutive panics: %v", err)
	}
}

func waitForBlocks(t *testing.T, ch *panicChain, n int64, fake *clock.Fake) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for ch.calls.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d blocks ran, wanted %d", ch.calls.Load(), n)
		}
		fake.Advance(100 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
}
