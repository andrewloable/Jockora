// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"testing"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
)

// A THREE-SECOND WINDOW IS SEVEN WORDS, AND SEVEN WORDS IS NOT A BREAK.
//
// MinBreakSeconds was 3.0, which the prompt turns into "LENGTH: 8 words
// MAXIMUM" -- a persona, a backsell, two record names and a handoff, in eight
// words. Measured against the deployed model at three budgets from an
// otherwise identical prompt:
//
//	 8 words   "The You Get"
//	20 words   "Yeah, Bayside just destroyed that castle. Barry Gibb on the
//	            floor. That's the kind of pure, unadulterated rock 'n' roll..."
//	40 words   the prompt recited back, in capitals
//
// Every one of the first eight breaks this station aired was degenerate --
// "nobody nobody, nobody, nobody, nobody, nobody, nobody nobody" is exactly
// eight words -- and none of them was a model fault. They were the writer
// doing as it was told.
//
// Every test here is TestShortWindow*, which is the -run pattern for this fix.

func TestShortWindowFallsThroughToTheGap(t *testing.T) {
	// A 3.5s intro used to be a "usable" ramp. It is not: it is nine words.
	short := &Track{RampS: 3.5, RampConfidence: enrich.ConfidenceLRC, DurationS: 200}
	cur := &Track{OutroS: 3.5, RampConfidence: enrich.ConfidenceLRC, DurationS: 200}

	placement, window := PreferredWindow(cur, short, 4)
	if placement == mix.PlacementRamp || placement == mix.PlacementOutro {
		t.Errorf("a 3.5s window was aimed at as %v; it is only %d words",
			placement, dj.WordTarget(window))
	}
	if got := dj.WordTarget(window); got < MinWindowWords {
		t.Errorf("window is %d words, want at least the %d-word floor", got, MinWindowWords)
	}
}

func TestShortWindowStillTakesARoomyIntro(t *testing.T) {
	// The signature move survives: a real intro is still talked over.
	roomy := &Track{RampS: 20, RampConfidence: enrich.ConfidenceLRC, DurationS: 200}
	cur := &Track{DurationS: 200}
	placement, window := PreferredWindow(cur, roomy, 4)
	if placement != mix.PlacementRamp {
		t.Errorf("placement = %v, want ramp over a 20s intro", placement)
	}
	if window != 20 {
		t.Errorf("window = %v, want the whole 20s intro", window)
	}
}

func TestShortWindowNeverAsksForFewerThanTheFloor(t *testing.T) {
	// The property that matters, over every combination of ramp and outro:
	// whatever PreferredWindow returns, it is writable.
	for ramp := 0.0; ramp <= 30; ramp += 0.5 {
		for outro := 0.0; outro <= 30; outro += 0.5 {
			cur := &Track{OutroS: outro, RampConfidence: enrich.ConfidenceLRC, DurationS: 200}
			next := &Track{RampS: ramp, RampConfidence: enrich.ConfidenceLRC, DurationS: 200}
			_, window := PreferredWindow(cur, next, 4)
			if got := dj.WordTarget(window); got < MinWindowWords {
				t.Fatalf("ramp %.1f outro %.1f gave a %d-word window, below the %d-word floor",
					ramp, outro, got, MinWindowWords)
			}
		}
	}
}
