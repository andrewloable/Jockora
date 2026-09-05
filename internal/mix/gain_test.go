// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"math"
	"testing"
)

func TestGainFromLoudness(t *testing.T) {
	// -22 LUFS is 6 dB below the -16 target, so it needs +6 dB.
	if got := GainFor(-22.0); math.Abs(float64(got)-1.9953) > 0.001 {
		t.Errorf("GainFor(-22.0) = %v, want 1.9953 (+6 dB)", got)
	}
	// -10 LUFS is 6 dB too loud, so it comes down.
	if got := GainFor(-10.0); math.Abs(float64(got)-0.5012) > 0.001 {
		t.Errorf("GainFor(-10.0) = %v, want 0.5012 (-6 dB)", got)
	}
}

func TestGainForTargetIsUnity(t *testing.T) {
	if got := GainFor(MusicLUFS); got != 1.0 {
		t.Errorf("GainFor(%v) = %v, want exactly 1.0", MusicLUFS, got)
	}
}

// TestMissingLoudnessReturnsUnity: an unscanned or unmeasurable track must play
// at its own level, not be treated as 0 LUFS and slammed 16 dB down.
func TestGainMissingLoudnessReturnsUnity(t *testing.T) {
	for _, v := range []float64{0, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := GainFor(v); got != 1.0 {
			t.Errorf("GainFor(%v) = %v, want exactly 1.0", v, got)
		}
	}
}

func TestGainIsClampedToTwelveDB(t *testing.T) {
	up := float32(math.Pow(10, 12.0/20))
	down := float32(math.Pow(10, -12.0/20))

	// -40 LUFS would demand +24 dB.
	if got := GainFor(-40.0); math.Abs(float64(got-up)) > 1e-5 {
		t.Errorf("GainFor(-40.0) = %v, want it clamped to %v (+12 dB)", got, up)
	}
	// A track measured hotter than full scale would demand a huge cut.
	if got := GainFor(2.0); math.Abs(float64(got-down)) > 1e-5 {
		t.Errorf("GainFor(2.0) = %v, want it clamped to %v (-12 dB)", got, down)
	}
}

// TestGainAppliedBeforeCrossfade is the point of the task. Two tracks needing
// different corrections must each be corrected before they are blended: a
// single gain applied to the mixed result cannot reproduce this, so the fade
// itself would carry the level jump.
func TestGainAppliedBeforeCrossfade(t *testing.T) {
	const n = 4096

	a := sine(n, 220, 0.2)
	b := sine(n, 660, 0.1)
	gainA, gainB := GainFor(-22.0), GainFor(-10.0) // +6 dB and -6 dB

	out := make([]Frame, n)
	c := NewChain()
	c.Process(out, a, b, nil, State{FadeTotal: n, GainA: gainA, GainB: gainB})

	lat := c.LatencyFrames()
	for i := lat; i < n; i++ {
		j := i - lat
		gA, gB := equalPowerGains(j, n)
		want := a[j].L*gainA*gA + b[j].L*gainB*gB
		if d := math.Abs(float64(out[i].L - want)); d > 1e-6 {
			t.Fatalf("frame %d = %v, want %v: per-track gain was not applied before the fade",
				i, out[i].L, want)
		}
	}
}

// TestGainZeroValueIsUnity keeps State's zero value safe. A State built without
// gains must play music, not silence it.
func TestGainZeroValueIsUnity(t *testing.T) {
	const n = 512
	a := sine(n, 440, 0.3)

	out := make([]Frame, n)
	c := NewChain()
	c.Process(out, a, nil, nil, State{}) // no gains set at all

	lat := c.LatencyFrames()
	for i := lat; i < n; i++ {
		if out[i] != a[i-lat] {
			t.Fatalf("frame %d = %+v, want %+v: an unset gain silenced the music", i, out[i], a[i-lat])
		}
	}
}

// TestGainCorrectsALevelJump is the audible symptom in one assertion: two
// tracks mastered 12 dB apart must arrive at the fade at the same level.
func TestGainCorrectsALevelJump(t *testing.T) {
	const n = 4096
	quiet := sine(n, 440, 0.1) // measured at -22 LUFS
	loud := sine(n, 440, 0.4)  // measured at -10 LUFS

	peak := func(buf []Frame, g float32) float32 {
		p := float32(0)
		for _, f := range buf {
			p = max(p, abs32(f.L*g))
		}
		return p
	}

	pq, pl := peak(quiet, GainFor(-22.0)), peak(loud, GainFor(-10.0))
	ratioDB := 20 * math.Log10(float64(pl/pq))
	if math.Abs(ratioDB) > 3.0 {
		t.Errorf("after correction the two tracks still differ by %.1f dB, want under 3 dB", ratioDB)
	}
}
