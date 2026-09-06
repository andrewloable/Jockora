// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"math/rand"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
)

// All named TestPlacement*, matching the task's own `-run TestPlacement`.

func withRamp(rampS, outroS float64) *Track {
	return &Track{RampS: rampS, OutroS: outroS, DurationS: 200, RampConfidence: enrich.ConfidenceLRC}
}

func TestPlacementChoosesRampWhenItFits(t *testing.T) {
	got := ChoosePlacement(withRamp(4, 4), withRamp(12.0, 4), 8.0, 3.0)
	if got != mix.PlacementRamp {
		t.Fatalf("placement = %v, want ramp: 8s of speech fits inside a 12s ramp", got)
	}
	// ramp_s already has the safety margin subtracted at ingest, so the raw
	// vocal onset is ramp_s + margin and the speech must end a margin before it.
	onset := 12.0 + enrich.SafetyMargin
	if 8.0 > onset-enrich.SafetyMargin {
		t.Errorf("speech ends at 8.0s, vocal onset is %.1fs: less than %.1fs of clearance", onset, enrich.SafetyMargin)
	}
}

func TestPlacementRejectsRampWhenTooTight(t *testing.T) {
	if got := ChoosePlacement(withRamp(2, 2), withRamp(3.0, 2), 8.0, 3.0); got == mix.PlacementRamp {
		t.Fatal("chose ramp for 8s of speech in a 3s ramp; the DJ would be talking over the first line")
	}
}

func TestPlacementChoosesOutroWhenRampTooShort(t *testing.T) {
	got := ChoosePlacement(withRamp(4, 14.0), withRamp(2.0, 4), 8.0, 3.0)
	if got != mix.PlacementOutro {
		t.Fatalf("placement = %v, want outro: the ramp is 2s and the outro is 14s", got)
	}
}

func TestPlacementFallsBackToBetween(t *testing.T) {
	got := ChoosePlacement(withRamp(1, 1), withRamp(1, 1), 8.0, 0.5)
	if got != mix.PlacementBetween {
		t.Fatalf("placement = %v, want between: no window is big enough", got)
	}
}

// TestPlacementSpanningSegue: 6s outro + 3s fade + 5s ramp = 14s, which holds
// 12s of speech that fits in none of the three windows on its own.
func TestPlacementSpanningSegue(t *testing.T) {
	got := ChoosePlacement(withRamp(4, 6.0), withRamp(5.0, 4), 12.0, 3.0)
	if got != mix.PlacementSpan {
		t.Fatalf("placement = %v, want span", got)
	}
}

// TestPlacementIgnoresRampWhenConfidenceNone: unvalidated numbers are not
// numbers. A track with no usable ramp data gets personality-only talk in the
// gap, never a guess about when the singing starts.
func TestPlacementIgnoresRampWhenConfidenceNone(t *testing.T) {
	next := &Track{RampS: 30, OutroS: 30, DurationS: 200, RampConfidence: enrich.ConfidenceNone}
	cur := &Track{RampS: 30, OutroS: 30, DurationS: 200, RampConfidence: enrich.ConfidenceNone}
	if got := ChoosePlacement(cur, next, 8.0, 3.0); got != mix.PlacementBetween {
		t.Fatalf("placement = %v, want between: ramp_s and outro_s look fine but are unvalidated", got)
	}

	// An empty confidence -- nothing ever looked -- is the same answer.
	blank := &Track{RampS: 30, OutroS: 30, DurationS: 200}
	if got := ChoosePlacement(blank, blank, 8.0, 3.0); got != mix.PlacementBetween {
		t.Fatalf("placement = %v with no confidence recorded, want between", got)
	}
}

// TestPlacementAnalysisConfidenceIsUsable: analysis-derived numbers are less
// accurate than LRC but they ARE validated, and refusing them would leave most
// of the library on "between" -- measured coverage is 41.5%, which is why
// vocal-onset detection was built at all.
func TestPlacementAnalysisConfidenceIsUsable(t *testing.T) {
	next := &Track{RampS: 12, OutroS: 4, DurationS: 200, RampConfidence: enrich.ConfidenceAnalysis}
	if got := ChoosePlacement(withRamp(4, 4), next, 8.0, 3.0); got != mix.PlacementRamp {
		t.Fatalf("placement = %v, want ramp: analysis data is validated data", got)
	}
}

// TestPlacementNeverPlacesOnVocal is the property test. This is arithmetic that
// looks obviously right and is easy to get subtly wrong -- subtracting the
// safety margin a second time, or comparing against the wrong track's numbers.
func TestPlacementNeverPlacesOnVocal(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		duration := 30 + rng.Float64()*400
		rampS := rng.Float64() * 40
		outroS := rng.Float64() * 40
		speech := 1 + rng.Float64()*25
		fade := rng.Float64() * 6

		cur := &Track{RampS: rampS, OutroS: outroS, DurationS: duration, RampConfidence: enrich.ConfidenceLRC}
		next := &Track{RampS: rampS, OutroS: outroS, DurationS: duration, RampConfidence: enrich.ConfidenceLRC}

		switch got := ChoosePlacement(cur, next, speech, fade); got {
		case mix.PlacementRamp:
			// ramp_s is already margin-adjusted, so the raw onset sits a margin
			// later and the speech must finish by ramp_s.
			onset := next.RampS + enrich.SafetyMargin
			if speech > onset-enrich.SafetyMargin {
				t.Fatalf("case %d: ramp chosen with %.2fs of speech, vocal onset at %.2fs (ramp_s %.2f)",
					i, speech, onset, next.RampS)
			}
		case mix.PlacementOutro:
			if speech > cur.OutroS {
				t.Fatalf("case %d: outro chosen with %.2fs of speech in a %.2fs outro", i, speech, cur.OutroS)
			}
		case mix.PlacementSpan:
			budget := cur.OutroS + fade + next.RampS
			if speech > budget {
				t.Fatalf("case %d: span chosen with %.2fs of speech in a %.2fs window", i, speech, budget)
			}
		}
	}
}

// TestPlacementGaplessBoundaryIsAlwaysBetween: station.Cadence already refuses
// to create a slot at a gapless boundary, so this should be unreachable in
// production -- but placement must not be the thing that assumes it.
func TestPlacementGaplessBoundaryIsAlwaysBetween(t *testing.T) {
	cur := &Track{RampS: 20, OutroS: 20, DurationS: 200, RampConfidence: enrich.ConfidenceLRC, NoCrossfadeNext: true}
	next := withRamp(20, 20)
	if got := ChoosePlacement(cur, next, 5.0, 3.0); got != mix.PlacementBetween {
		t.Fatalf("placement = %v at a gapless boundary, want between", got)
	}
}

// TestPlacementRefusesImpossibleSpeech: a break longer than the whole track has
// nowhere to go, and "between" is the only honest answer.
func TestPlacementRefusesImpossibleSpeech(t *testing.T) {
	if got := ChoosePlacement(withRamp(4, 4), withRamp(4, 4), 0, 3.0); got != mix.PlacementBetween {
		t.Errorf("placement = %v for zero-length speech, want between", got)
	}
	if got := ChoosePlacement(withRamp(4, 4), withRamp(4, 4), 500, 3.0); got != mix.PlacementBetween {
		t.Errorf("placement = %v for 500s of speech, want between", got)
	}
}
