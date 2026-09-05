// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"math"
	"testing"
)

func TestChainLatencyIsLimiterLatency(t *testing.T) {
	c := NewChain()
	if got := c.LatencyFrames(); got != 240 {
		t.Errorf("LatencyFrames() = %d, want 240", got)
	}
	if got, want := c.LatencyFrames(), NewLimiter().LatencyFrames(); got != want {
		t.Errorf("chain latency %d does not match the limiter's %d: crossfade and duck must add none", got, want)
	}
}

// TestChainOrderDuckBeforeLimiter proves the order by consequence rather than by
// inspection. Ducking first means the limiter sees a quieter mix and has less
// work to do; limiting first wastes headroom on music that was about to be
// turned down anyway.
func TestChainOrderDuckBeforeLimiter(t *testing.T) {
	const n = SampleRate / 2
	music := sine(n, 220, 0.9)
	speech := sine(n, 1000, 0.7)

	// Correct: duck the music, add speech, then limit.
	correct := NewChain()
	out := make([]Frame, n)
	correct.Process(out, music, nil, speech, State{Speaking: true})
	correctGain := correct.LimiterGain()

	// Wrong: add speech to unducked music, limit, then duck the result.
	wrongBuf := append([]Frame(nil), music...)
	for i := range wrongBuf {
		wrongBuf[i].L += speech[i].L
		wrongBuf[i].R += speech[i].R
	}
	lim := NewLimiter()
	lim.Process(wrongBuf)
	wrongGain := lim.Gain()

	if !(correctGain > wrongGain) {
		t.Errorf("limiter gain with ducking first = %.4f, with ducking last = %.4f;\n"+
			"ducking first must leave the limiter MORE headroom, not less", correctGain, wrongGain)
	}
	t.Logf("limiter gain: duck-first %.4f (%.2f dB reduction), duck-last %.4f (%.2f dB reduction)",
		correctGain, 20*math.Log10(float64(correctGain)), wrongGain, 20*math.Log10(float64(wrongGain)))
}

func TestChainSpeechOverCrossfade(t *testing.T) {
	const n = SampleRate

	a := sine(n, 220, 0.95)
	b := sine(n, 660, 0.95)
	speech := sine(n, 1000, 0.8)

	out := make([]Frame, n)
	NewChain().Process(out, a, b, speech, State{FadeTotal: n, FadePos: 0, Speaking: true})

	for i, f := range out {
		for _, v := range []float32{f.L, f.R} {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				t.Fatalf("frame %d is not a number: %v", i, v)
			}
			if math.Abs(float64(v)) > float64(ceiling)+1e-6 {
				t.Fatalf("frame %d = %v exceeds the -1 dBTP ceiling", i, v)
			}
		}
	}
}

// TestChainSubBlockSize is the one the DO NOT list calls for: a block smaller
// than the limiter's 240-frame delay line must produce exactly the same audio as
// one large block. It fails if any stage resets its state between calls.
func TestChainSubBlockSize(t *testing.T) {
	const n = SampleRate / 2
	const block = 64

	a := sine(n, 220, 0.9)
	b := sine(n, 660, 0.9)
	speech := sine(n, 1000, 0.6)
	st := State{FadeTotal: n, Speaking: true}

	whole := make([]Frame, n)
	NewChain().Process(whole, a, b, speech, st)

	chunked := make([]Frame, n)
	c := NewChain()
	for i := 0; i < n; i += block {
		end := min(i+block, n)
		s := st
		s.FadePos = i
		c.Process(chunked[i:end], a[i:end], b[i:end], speech[i:end], s)
	}

	for i := range whole {
		if d := math.Abs(float64(whole[i].L - chunked[i].L)); d > 1e-6 {
			t.Fatalf("frame %d differs between one big block and %d-frame blocks: %v vs %v",
				i, block, whole[i].L, chunked[i].L)
		}
	}
}

// TestChainNoCrossfadePassesMusicA: with no fade in progress musicB is ignored
// entirely, and musicB may be nil.
func TestChainNoCrossfadePassesMusicA(t *testing.T) {
	const n = 1024
	a := sine(n, 440, 0.2)
	want := append([]Frame(nil), a...)

	out := make([]Frame, n)
	c := NewChain()
	c.Process(out, a, nil, nil, State{})

	lat := c.LatencyFrames()
	for i := lat; i < n; i++ {
		if out[i] != want[i-lat] {
			t.Fatalf("frame %d = %+v, want musicA delayed by %d: %+v", i, out[i], lat, want[i-lat])
		}
	}
}

// TestChainSilentWhenNothingPlaying guards the degenerate call the scheduler
// makes when a break is late and there is nothing to mix.
func TestChainSilentWhenNothingPlaying(t *testing.T) {
	out := make([]Frame, 512)
	NewChain().Process(out, nil, nil, nil, State{})

	for i, f := range out {
		if f != (Frame{}) {
			t.Fatalf("frame %d = %+v, want silence", i, f)
		}
	}
}

// TestChainSpeechIsNotDucked: the ducker attenuates music only. If speech went
// through it the DJ would duck himself.
func TestChainSpeechIsNotDucked(t *testing.T) {
	const n = SampleRate / 2
	speech := sine(n, 1000, 0.5)

	out := make([]Frame, n)
	c := NewChain()
	c.Process(out, nil, nil, speech, State{Speaking: true})

	// With no music, the ducked bus is silent and the output is speech alone.
	lat := c.LatencyFrames()
	peak := float32(0)
	for i := lat; i < n; i++ {
		peak = max(peak, abs32(out[i].L))
	}
	if peak < 0.49 {
		t.Errorf("speech peaked at %v, want ~0.5: it was attenuated by the music ducker", peak)
	}
}

func TestChainNoAllocation(t *testing.T) {
	const n = 4096
	a, b, speech := sine(n, 220, 0.5), sine(n, 660, 0.5), sine(n, 1000, 0.4)
	out := make([]Frame, n)
	c := NewChain()
	st := State{FadeTotal: SampleRate, Speaking: true}

	if got := testing.AllocsPerRun(20, func() { c.Process(out, a, b, speech, st) }); got != 0 {
		t.Errorf("Process allocated %v times per run, want 0", got)
	}
}

// TestChainFadeMatchesCrossfade guards the one piece of duplicated logic in the
// chain: Process runs its own fade loop so it can tolerate nil and short
// buffers, and that loop must stay identical to Crossfade for full ones.
func TestChainFadeMatchesCrossfade(t *testing.T) {
	const n = 2048
	a, b := sine(n, 220, 0.4), sine(n, 660, 0.3)

	want := make([]Frame, n)
	Crossfade(want, a, b, 0, n)

	// A chain with ducking idle and no speech leaves the fade untouched apart
	// from the limiter's delay, and 0.4+0.3 stays under the ceiling.
	c := NewChain()
	got := make([]Frame, n)
	c.Process(got, a, b, nil, State{FadeTotal: n})

	lat := c.LatencyFrames()
	for i := lat; i < n; i++ {
		if d := math.Abs(float64(got[i].L - want[i-lat].L)); d > 1e-6 {
			t.Fatalf("frame %d: chain fade %v, Crossfade %v", i, got[i].L, want[i-lat].L)
		}
	}
}
