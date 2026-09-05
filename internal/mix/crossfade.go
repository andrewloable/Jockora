// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import "math"

// Crossfade writes len(out) frames of a fading into b, where startPos is the
// position of out[0] within a fade of total frames.
//
// The curve is equal-power (cos/sin), not linear. Two different songs are
// uncorrelated sources, so their powers add rather than their amplitudes; a
// linear fade dips about 3 dB at the midpoint and it is audible on every single
// transition.
//
// The caller supplies out. Nothing here allocates, clamps or limits: limiting
// happens once, later in the chain.
func Crossfade(out, a, b []Frame, startPos, total int) {
	for i := range out {
		gA, gB := equalPowerGains(startPos+i, total)
		out[i].L = a[i].L*gA + b[i].L*gB
		out[i].R = a[i].R*gA + b[i].R*gB
	}
}

// equalPowerGains returns the two gains at pos within a fade of total frames.
//
// The endpoints are snapped rather than computed: math.Cos(math.Pi/2) is 6.1e-17
// and not zero, so without this the far end of every fade leaks a residue of the
// outgoing track.
func equalPowerGains(pos, total int) (gA, gB float32) {
	if total <= 0 || pos >= total {
		return 0, 1
	}
	if pos <= 0 {
		return 1, 0
	}
	t := float64(pos) / float64(total)
	return float32(math.Cos(t * math.Pi / 2)), float32(math.Sin(t * math.Pi / 2))
}
