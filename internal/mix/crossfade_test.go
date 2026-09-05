// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"math"
	"testing"
)

func constFrames(n int, v float32) []Frame {
	f := make([]Frame, n)
	for i := range f {
		f[i] = Frame{L: v, R: v}
	}
	return f
}

// gainsAt recovers the two fade gains independently by crossfading a unit
// buffer against a silent one, and then the reverse.
func gainsAt(pos, total int) (gA, gB float64) {
	one, zero := constFrames(1, 1), constFrames(1, 0)
	out := make([]Frame, 1)

	Crossfade(out, one, zero, pos, total)
	gA = float64(out[0].L)
	Crossfade(out, zero, one, pos, total)
	gB = float64(out[0].L)
	return gA, gB
}

// TestEqualPowerMidpointHoldsPower is the whole point of the component. Two
// different songs are uncorrelated, so their POWERS add, not their amplitudes.
// A linear fade satisfies gA+gB==1 at the midpoint and still dips about 3 dB,
// audibly, on every single transition.
func TestCrossfadeEqualPowerMidpoint(t *testing.T) {
	const total = SampleRate

	gA, gB := gainsAt(total/2, total)
	if power := gA*gA + gB*gB; math.Abs(power-1.0) > 0.01 {
		t.Errorf("midpoint gA^2+gB^2 = %.4f (gA=%.4f gB=%.4f), want 1.0 +/- 0.01", power, gA, gB)
	}
}

// TestEqualPowerHoldsThroughout extends the guarantee past the midpoint: a fade
// that only held power at the halfway mark would still dip either side of it.
func TestCrossfadeEqualPowerThroughout(t *testing.T) {
	const total = SampleRate

	for _, pos := range []int{0, total / 8, total / 4, total / 3, total / 2, 3 * total / 4, 7 * total / 8, total} {
		gA, gB := gainsAt(pos, total)
		if power := gA*gA + gB*gB; math.Abs(power-1.0) > 0.01 {
			t.Errorf("pos %d/%d: gA^2+gB^2 = %.4f, want 1.0 +/- 0.01", pos, total, power)
		}
	}
}

func TestCrossfadeEndpoints(t *testing.T) {
	const total = SampleRate
	a := []Frame{{L: 0.5, R: -0.25}}
	b := []Frame{{L: -0.75, R: 0.125}}
	out := make([]Frame, 1)

	Crossfade(out, a, b, 0, total)
	if out[0] != a[0] {
		t.Errorf("at pos 0 out = %+v, want buffer A %+v", out[0], a[0])
	}

	Crossfade(out, a, b, total, total)
	if out[0] != b[0] {
		t.Errorf("at pos total out = %+v, want buffer B %+v", out[0], b[0])
	}
}

// TestCrossfadeEndpointsAreExactAgainstSilence is the sharp version: cos(pi/2)
// is not exactly zero in floating point, so a naive implementation leaks a
// residue of A into the end of the fade.
func TestCrossfadeEndpointsAreExactAgainstSilence(t *testing.T) {
	const total = SampleRate
	a := constFrames(1, 1.0)
	b := constFrames(1, 0.0)
	out := make([]Frame, 1)

	Crossfade(out, a, b, total, total)
	if out[0] != (Frame{}) {
		t.Errorf("at the end of the fade out = %+v, want exact silence", out[0])
	}

	Crossfade(out, b, a, 0, total)
	if out[0] != (Frame{}) {
		t.Errorf("at the start of the fade out = %+v, want exact silence", out[0])
	}
}

func TestCrossfadeRampsMonotonically(t *testing.T) {
	const total = 1000
	one, zero := constFrames(total+1, 1), constFrames(total+1, 0)
	out := make([]Frame, total+1)

	Crossfade(out, one, zero, 0, total) // pure gain A over the whole fade
	for i := 1; i <= total; i++ {
		if out[i].L > out[i-1].L {
			t.Fatalf("gain A rose at frame %d: %v after %v", i, out[i].L, out[i-1].L)
		}
	}
	if out[0].L != 1 || out[total].L != 0 {
		t.Errorf("gain A ran %v..%v, want 1..0", out[0].L, out[total].L)
	}
}

func TestCrossfadeNoAllocation(t *testing.T) {
	const n = 512
	a, b := constFrames(n, 0.5), constFrames(n, -0.5)
	out := make([]Frame, n)

	if got := testing.AllocsPerRun(100, func() { Crossfade(out, a, b, 0, SampleRate) }); got != 0 {
		t.Errorf("Crossfade allocated %v times per run, want 0", got)
	}
}

// TestCrossfadeZeroTotal guards a degenerate schedule: a zero-length fade is an
// instant switch, not a division by zero producing NaN audio.
func TestCrossfadeZeroTotal(t *testing.T) {
	a, b := constFrames(1, 1.0), constFrames(1, -1.0)
	out := make([]Frame, 1)

	Crossfade(out, a, b, 0, 0)
	if out[0] != b[0] {
		t.Errorf("zero-length fade produced %+v, want an instant switch to B %+v", out[0], b[0])
	}
}
