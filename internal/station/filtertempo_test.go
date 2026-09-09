// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"testing"
)

// Jockora-8d0. filter.go's own comment states the guarantee: "A NULL bpm never
// excludes: analysis is a slow background pass, and on a fresh library dropping
// the unmeasured would make every mood station empty."
//
// Jockora-ffh then made a FAILED measurement store a zero, so the analysis
// queue drains instead of handing the same unmeasurable file back for ever. A
// zero is not NULL -- so without this, every track the sidecar cannot measure
// starts being excluded by any station with a tempo bound. A mood implies a
// tempo for five of the sixteen moods, so it reaches stations nobody set a
// tempo on, and a deployment whose sidecar cannot measure at all would have
// every mood station empty.
//
// Every test here is TestFilterTempo*, which is the -run pattern for this fix.

func TestFilterTempoKeepsATrackThatCouldNotBeMeasured(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
	)
	ctx := context.Background()
	// One never measured, one measured and unmeasurable. Both mean "no tempo".
	if _, err := s.DB().ExecContext(ctx, `UPDATE tracks SET bpm = 0 WHERE id = 2`); err != nil {
		t.Fatal(err)
	}

	got := filterIDs(t, s, Filter{Genres: []string{"rock"}, TempoMin: 90, TempoMax: 140})
	if len(got) != 2 {
		t.Errorf("the station selected %v, want both: a tempo that could not be measured "+
			"must not exclude, the same as one that was never attempted", got)
	}
}

// TestFilterTempoStillExcludesAMeasuredMismatch: the bound has to keep working,
// or this fix would turn every tempo range into no range at all.
func TestFilterTempoStillExcludesAMeasuredMismatch(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
	)
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx, `UPDATE tracks SET bpm = 120 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE tracks SET bpm = 200 WHERE id = 2`); err != nil {
		t.Fatal(err)
	}

	got := filterIDs(t, s, Filter{Genres: []string{"rock"}, TempoMin: 90, TempoMax: 140})
	if len(got) != 1 || got[0] != 1 {
		t.Errorf("the station selected %v, want only the 120 BPM track", got)
	}
}
