// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"math"
	"testing"
)

// runPair pushes A then B through one chain and returns the whole output. The
// chain is stateful across blocks, so this is exactly what the mixer does.
func runPair(c *Chain, a, b []Frame, st State) []Frame {
	out := make([]Frame, len(a)+len(b))
	c.Process(out[:len(a)], a, nil, nil, st)
	c.Process(out[len(a):], b, nil, nil, st)
	return out
}

func TestAdjacentFlaggedPairSplicesAdjacent(t *testing.T) {
	const n = 2048
	a, b := sine(n, 220, 0.3), sine(n, 660, 0.3)

	tr := TransitionFor(true, SampleRate) // no_crossfade_next set
	if tr.FadeFrames != 0 {
		t.Fatalf("FadeFrames = %d, want 0: a flagged pair must not fade at all", tr.FadeFrames)
	}

	c := NewChain()
	out := runPair(c, a, b, State{FadeTotal: tr.FadeFrames})
	lat := c.LatencyFrames()

	// The last sample of A and the first of B must be adjacent and unmodified:
	// no overlap, no ramp on either side.
	if got, want := out[lat+n-1], a[n-1]; got != want {
		t.Errorf("frame before the splice = %+v, want A's final sample %+v", got, want)
	}
	if got, want := out[lat+n], b[0]; got != want {
		t.Errorf("frame after the splice = %+v, want B's first sample %+v", got, want)
	}
	// And nothing either side was ramped.
	for i := 0; i < n; i++ {
		if out[lat+i] != a[i] {
			t.Fatalf("A frame %d = %+v, want %+v: a gain ramp was applied", i, out[lat+i], a[i])
		}
	}
}

func TestAdjacentUnflaggedPairStillCrossfades(t *testing.T) {
	const n = 4096

	tr := TransitionFor(false, n)
	if tr.FadeFrames != n {
		t.Fatalf("FadeFrames = %d, want the default %d", tr.FadeFrames, n)
	}

	a, b := sine(n, 220, 0.3), sine(n, 660, 0.3)
	out := make([]Frame, n)
	c := NewChain()
	c.Process(out, a, b, nil, State{FadeTotal: tr.FadeFrames})

	// Midway through a real fade both sources are present, so the output
	// matches neither one alone.
	mid := n/2 + c.LatencyFrames()
	if mid < len(out) {
		j := mid - c.LatencyFrames()
		if out[mid] == a[j] || out[mid] == b[j] {
			t.Errorf("frame %d = %+v matches a single source: no crossfade happened", mid, out[mid])
		}
	}
}

// TestAdjacentSpliceHasNoDiscontinuityInSampleCount: a crossfade consumes the
// two tracks simultaneously and yields fewer frames than it was given. An
// adjacent splice must yield every frame of both.
func TestAdjacentSpliceHasNoDiscontinuityInSampleCount(t *testing.T) {
	const nA, nB = 1500, 2500
	a, b := sine(nA, 220, 0.3), sine(nB, 660, 0.3)

	tr := TransitionFor(true, SampleRate)
	c := NewChain()
	out := runPair(c, a, b, State{FadeTotal: tr.FadeFrames})

	if len(out) != nA+nB {
		t.Fatalf("splice produced %d frames, want exactly lenA+lenB = %d", len(out), nA+nB)
	}
	// Every input frame is present, in order, delayed only by the limiter.
	lat := c.LatencyFrames()
	want := append(append([]Frame(nil), a...), b...)
	for i := lat; i < len(out); i++ {
		if out[i] != want[i-lat] {
			t.Fatalf("frame %d = %+v, want %+v: frames were lost or overlapped", i, out[i], want[i-lat])
		}
	}
}

// TestAdjacentBreakFallsBackToBetween: there is no fade window to talk over, so
// a break wanting ramp or outro is relocated, never dropped.
func TestAdjacentBreakFallsBackToBetween(t *testing.T) {
	adjacent := TransitionFor(true, SampleRate)
	normal := TransitionFor(false, SampleRate)

	for _, want := range []Placement{PlacementRamp, PlacementOutro, PlacementBetween} {
		if got := PlaceBreak(want, adjacent); got != PlacementBetween {
			t.Errorf("PlaceBreak(%v, adjacent) = %v, want %v", want, got, PlacementBetween)
		}
		if got := PlaceBreak(want, normal); got != want {
			t.Errorf("PlaceBreak(%v, normal) = %v, want it unchanged", want, got)
		}
	}
}

// TestAdjacentLoudnessGainStillApplies is the mistake the DO NOT list warns
// about: suppressing the fade must not suppress normalisation. Two
// differently-mastered tracks spliced with no fade need the correction MORE,
// because the level jump is instantaneous.
func TestAdjacentLoudnessGainStillApplies(t *testing.T) {
	const n = 2048
	a := sine(n, 220, 0.2)
	gain := GainFor(-22.0) // +6 dB

	tr := TransitionFor(true, SampleRate)
	c := NewChain()
	out := make([]Frame, n)
	c.Process(out, a, nil, nil, State{FadeTotal: tr.FadeFrames, GainA: gain})

	lat := c.LatencyFrames()
	for i := lat; i < n; i++ {
		want := a[i-lat].L * gain
		if d := math.Abs(float64(out[i].L - want)); d > 1e-6 {
			t.Fatalf("frame %d = %v, want %v: the loudness gain was suppressed along with the fade",
				i, out[i].L, want)
		}
	}
}
