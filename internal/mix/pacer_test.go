// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
)

var epoch = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func TestPacerWaitsForWallClock(t *testing.T) {
	f := clock.NewFake(epoch)
	p := NewPacer(f)

	p.Wait(SampleRate) // exactly one second of audio

	calls := f.SleepCalls()
	if len(calls) != 1 {
		t.Fatalf("Sleep called %d times, want 1: %v", len(calls), calls)
	}
	if calls[0] != time.Second {
		t.Errorf("slept %v, want 1s", calls[0])
	}
	if d := p.Drift(); d != 0 {
		t.Errorf("Drift = %v, want 0 after a clean paced second", d)
	}
}

// TestPacerDriftStaysBounded simulates thirty minutes of real work. Each block
// costs a randomised 0-20 ms of compute the pacer must absorb. A per-block
// ticker would accumulate roughly 21000 x 10 ms = over three minutes of drift
// here; absolute-target pacing must stay inside 100 ms.
func TestPacerDriftStaysBounded(t *testing.T) {
	const blockFrames = 4096
	const blocks = 30 * 60 * SampleRate / blockFrames

	f := clock.NewFake(epoch)
	p := NewPacer(f)
	rng := rand.New(rand.NewSource(1))

	for i := 0; i < blocks; i++ {
		f.Advance(time.Duration(rng.Intn(20)) * time.Millisecond) // compute cost
		p.Wait(blockFrames)
	}

	wall := f.Now().Sub(epoch)
	audio := FramesToDuration(blocks * blockFrames)
	if drift := wall - audio; drift > 100*time.Millisecond || drift < -100*time.Millisecond {
		t.Errorf("after %v of audio, wall clock is off by %v, want within 100ms", audio, drift)
	}
	if d := p.Drift(); d > 100*time.Millisecond || d < -100*time.Millisecond {
		t.Errorf("Drift() = %v, want within 100ms", d)
	}
}

// TestPacerCatchesUpAfterStall injects a stall far longer than one block. The
// pacer must never ask to sleep a negative duration, and must be back on
// schedule once it has run without sleeping for long enough to absorb it.
func TestPacerCatchesUpAfterStall(t *testing.T) {
	const blockFrames = 4096

	f := clock.NewFake(epoch)
	p := NewPacer(f)

	for i := 0; i < 10; i++ {
		p.Wait(blockFrames)
	}

	f.Advance(2 * time.Second) // the stall
	if d := p.Drift(); d < time.Second {
		t.Fatalf("Drift = %v immediately after a 2s stall, expected to be well behind", d)
	}

	for i := 0; i < 200; i++ {
		p.Wait(blockFrames)
	}

	for i, d := range f.SleepCalls() {
		if d <= 0 {
			t.Fatalf("Sleep call %d was %v: the pacer must never sleep a non-positive duration", i, d)
		}
	}
	if d := p.Drift(); d > 100*time.Millisecond || d < -100*time.Millisecond {
		t.Errorf("Drift = %v after catching up, want within 100ms", d)
	}
}

// TestPacerNeverSleepsWhenBehind is the negative-sleep guard on its own: while
// the pacer is behind schedule it must not sleep at all.
func TestPacerNeverSleepsWhenBehind(t *testing.T) {
	f := clock.NewFake(epoch)
	p := NewPacer(f)

	f.Advance(10 * time.Second)
	p.Wait(SampleRate) // one second of audio, ten seconds late

	if calls := f.SleepCalls(); len(calls) != 0 {
		t.Errorf("slept %v while ten seconds behind, want no sleep at all", calls)
	}
}

// TestPacerSourceHasNoDirectClockCalls keeps the injected clock injected. A
// stray time.Sleep would not fail any other test here; it would just make the
// thirty-minute drift test take thirty minutes.
func TestPacerSourceHasNoDirectClockCalls(t *testing.T) {
	src, err := os.ReadFile("pacer.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"time.Sleep", "time.Now"} {
		if strings.Contains(string(src), banned) {
			t.Errorf("pacer.go calls %s directly; it must go through the injected clock", banned)
		}
	}
}

// TestPacerLongRunNoOverflow covers a self-hoster leaving the server up for
// weeks. Frame counts are converted with a split division precisely so the
// nanosecond product cannot overflow int64 partway through a long run.
func TestPacerLongRunNoOverflow(t *testing.T) {
	const week = 7 * 24 * time.Hour
	frames := int64(week/time.Second) * SampleRate

	if got := framesToDuration64(frames); got != week {
		t.Errorf("a week of frames converts to %v, want %v", got, week)
	}
	if got := framesToDuration64(frames + SampleRate/2); got != week+500*time.Millisecond {
		t.Errorf("sub-second remainder lost over a long run: %v", got-week)
	}
}
