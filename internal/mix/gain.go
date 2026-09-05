// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import "math"

// MaxNormalisationDB bounds how far a track may be pushed towards the loudness
// target. A mis-measured or near-silent track would otherwise demand +30 dB and
// destroy the mix.
const MaxNormalisationDB = 12.0

// GainFor converts a track's measured integrated loudness into the linear gain
// that brings it to the bus target.
//
// A track with no usable measurement plays at its own level. Treating an unset
// value as 0 LUFS would slam every unscanned track 16 dB down, which is worse
// than the level jump normalisation exists to fix.
//
// The value comes from tracks.loudness_lufs, measured once at scan time. Nothing
// re-measures at playback.
func GainFor(loudnessLUFS float64) float32 {
	if loudnessLUFS == 0 || math.IsNaN(loudnessLUFS) || math.IsInf(loudnessLUFS, 0) {
		return 1
	}

	db := MusicLUFS - loudnessLUFS
	db = math.Min(math.Max(db, -MaxNormalisationDB), MaxNormalisationDB)
	if db == 0 {
		return 1
	}
	return float32(math.Pow(10, db/20))
}
