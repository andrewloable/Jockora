// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
)

// Track is what placement needs to know about one item of music.
//
// Deliberately not the database row. Placement is arithmetic over five numbers,
// and a type that carried the whole row would invite it to reach for things it
// has no business deciding on.
type Track struct {
	// RampS is how long the DJ may talk over the intro. The 1.5s safety margin
	// is ALREADY subtracted, at ingest, by whichever source produced it.
	RampS float64
	// OutroS is how long the instrumental tail runs after the last vocal.
	OutroS float64
	// DurationS is the track length.
	DurationS float64
	// RampConfidence is the provenance of RampS and OutroS: LRC, analysis, or
	// none. Anything but a validated value means the numbers are not usable.
	RampConfidence string
	// NoCrossfadeNext marks gapless material: a live record, a DJ mix, a
	// concert segue, a classical movement.
	NoCrossfadeNext bool
}

// usableRamp reports whether this track's ramp and outro may be trusted.
//
// "none" means a lookup happened and found nothing usable. An empty string
// means nothing looked at all. Neither is a number, and the difference matters
// only to the coverage report -- to placement they are the same answer.
func (t *Track) usableRamp() bool {
	return t != nil &&
		(t.RampConfidence == enrich.ConfidenceLRC || t.RampConfidence == enrich.ConfidenceAnalysis)
}

// ChoosePlacement decides where a break sits at the boundary from cur to next.
//
// Hitting the post -- stopping exactly before the vocal starts -- is the single
// craft rule that separates radio from a podcast playing over music. Everything
// here exists to guarantee the speech has finished before the singing begins.
//
// Preference order is ramp, outro, span, between. Ramp first because talking
// over an intro and landing on the downbeat is the signature move; span last of
// the three because it is the widest window and taking it early would use a
// segue where a clean intro would have done.
func ChoosePlacement(cur, next *Track, speechSeconds, fadeSeconds float64) mix.Placement {
	transition := mix.TransitionFor(cur != nil && cur.NoCrossfadeNext, int(fadeSeconds*mix.SampleRate))

	// Zero-length speech has nothing to place, and no window can be negative.
	if speechSeconds <= 0 {
		return mix.PlaceBreak(mix.PlacementBetween, transition)
	}

	// The margin is NOT subtracted again here. RampS already carries it, and
	// applying it twice would quietly halve every short ramp.
	var rampBudget, outroBudget float64
	if next.usableRamp() {
		rampBudget = next.RampS
	}
	if cur.usableRamp() {
		outroBudget = cur.OutroS
	}

	want := mix.PlacementBetween
	switch {
	case speechSeconds <= rampBudget:
		want = mix.PlacementRamp
	case speechSeconds <= outroBudget:
		want = mix.PlacementOutro
	case speechSeconds <= outroBudget+fadeSeconds+rampBudget:
		// The classic segue: out of the tail, through the crossfade, into the
		// next intro. Only reachable when at least one end has usable numbers.
		if rampBudget > 0 || outroBudget > 0 {
			want = mix.PlacementSpan
		}
	}

	// mix owns the gapless rule; do not re-derive it here. In practice
	// station.Cadence never offers a slot at a gapless boundary, so this is a
	// second line of defence rather than the enforcement point.
	return mix.PlaceBreak(want, transition)
}
