// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

// Jockora-g1t.3. "mostly 80s", "calm but driving" and "nothing over four
// minutes" have to survive the trip from the brief into the playlist, which
// means the filter selects on the three numeric columns as well as the tags.

// rangeTrack is one row: its tags, and the three numbers a station can select
// on. A zero year, bpm or duration is stored as NULL, which is the case that
// matters most.
type rangeTrack struct {
	tags     []string
	moods    []string
	year     int
	bpm      float64
	duration float64
}

func rangeStore(t *testing.T, tracks ...rangeTrack) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	null := func(v float64) any {
		if v == 0 {
			return nil
		}
		return v
	}
	for i, tr := range tracks {
		id := int64(i + 1)
		var year any
		if tr.year != 0 {
			year = tr.year
		}
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (id, path, playable, year, bpm, duration_s)
			 VALUES (?, ?, 1, ?, ?, ?)`,
			id, fmt.Sprintf("/m/%d.mp3", id), year, null(tr.bpm), null(tr.duration)); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"station_tags": tr.tags, "mood": tr.moods})
		if _, err := s.DB().Exec(
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
			id, string(raw), enrich.ConfidenceHigh); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func rangeIDs(t *testing.T, s *store.Store, f Filter) []int64 {
	t.Helper()
	got, err := f.TrackIDs(context.Background(), s)
	if err != nil {
		t.Fatalf("TrackIDs: %v", err)
	}
	return got
}

func wantIDs(t *testing.T, got []int64, want ...int64) {
	t.Helper()
	if want == nil {
		want = []int64{}
	}
	if len(got) == 0 {
		got = []int64{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selected %v, want %v", got, want)
	}
}

// The decades fixture: 1979, 1985, 1989, 1991, and one with no year at all.
func decades(t *testing.T) *store.Store {
	return rangeStore(t,
		rangeTrack{tags: []string{"rock"}, year: 1979},
		rangeTrack{tags: []string{"rock"}, year: 1985},
		rangeTrack{tags: []string{"rock"}, year: 1989},
		rangeTrack{tags: []string{"rock"}, year: 1991},
		rangeTrack{tags: []string{"rock"}},
	)
}

func TestFilterYearBounds(t *testing.T) {
	s := decades(t)
	// 2 and 3 are in the decade; 5 has no year and is never excluded.
	wantIDs(t, rangeIDs(t, s, Filter{Genres: []string{"rock"}, YearMin: 1980, YearMax: 1989}),
		2, 3, 5)
}

// TestFilterYearNullNeverExcluded: the same rule NULL bpm already follows. On a
// fresh library most tracks have a year from tags, but not all do -- and
// dropping the unknown would make a decade station look broken for a reason
// nothing on screen explains.
func TestFilterYearNullNeverExcluded(t *testing.T) {
	s := rangeStore(t,
		rangeTrack{tags: []string{"rock"}, year: 1995},
		rangeTrack{tags: []string{"rock"}},
	)
	wantIDs(t, rangeIDs(t, s, Filter{Genres: []string{"rock"}, YearMin: 1980, YearMax: 1989}), 2)
}

func TestFilterYearOpenEnded(t *testing.T) {
	s := decades(t)
	// A floor only: 1989 and later, plus the unknown.
	wantIDs(t, rangeIDs(t, s, Filter{Genres: []string{"rock"}, YearMin: 1989}), 3, 4, 5)
	// A ceiling only: 1985 and earlier, plus the unknown.
	wantIDs(t, rangeIDs(t, s, Filter{Genres: []string{"rock"}, YearMax: 1985}), 1, 2, 5)
}

func TestFilterYearWithGenre(t *testing.T) {
	s := rangeStore(t,
		rangeTrack{tags: []string{"rock"}, year: 1985},
		rangeTrack{tags: []string{"jazz"}, year: 1985},
		rangeTrack{tags: []string{"rock"}, year: 1995},
	)
	wantIDs(t, rangeIDs(t, s, Filter{Genres: []string{"rock"}, YearMin: 1980, YearMax: 1989}), 1)
}

// TestFilterYearCatchAll: the catch-all is a different query and gets the same
// clause. Missing a path is the defect this task exists to prevent.
func TestFilterYearCatchAll(t *testing.T) {
	s := rangeStore(t,
		rangeTrack{tags: []string{}, year: 1985},
		rangeTrack{tags: []string{}, year: 1995},
	)
	wantIDs(t, rangeIDs(t, s,
		Filter{Genres: []string{enrich.FallbackStationTag}, YearMin: 1980, YearMax: 1989}), 1)
}

// TestFilterYearOnly: no genres, no moods, only years -- and it must NOT return
// the whole library. That third path is the one an implementation forgets.
func TestFilterYearOnly(t *testing.T) {
	s := decades(t)
	wantIDs(t, rangeIDs(t, s, Filter{YearMin: 1980, YearMax: 1989}), 2, 3, 5)
}

func TestFilterTempoOnly(t *testing.T) {
	s := rangeStore(t,
		rangeTrack{tags: []string{"rock"}, bpm: 80},
		rangeTrack{tags: []string{"rock"}, bpm: 140},
		rangeTrack{tags: []string{"rock"}},
	)
	wantIDs(t, rangeIDs(t, s, Filter{TempoMin: 120, TempoMax: 180}), 2, 3)
}

func TestFilterLengthOnly(t *testing.T) {
	s := rangeStore(t,
		rangeTrack{tags: []string{"rock"}, duration: 120},
		rangeTrack{tags: []string{"rock"}, duration: 400},
		rangeTrack{tags: []string{"rock"}},
	)
	wantIDs(t, rangeIDs(t, s, Filter{DurationMinS: 60, DurationMaxS: 240}), 1, 3)
}

// TestFilterTempoNullNeverExcluded: bpm is filled by a slow background pass, so
// on a fresh library a tempo bound admits nearly everything. That is the right
// rule -- a station empty until analysis finishes is worse than one briefly too
// wide -- and it is why the derive tells the operator when it applies.
func TestFilterTempoNullNeverExcluded(t *testing.T) {
	s := rangeStore(t,
		rangeTrack{tags: []string{"rock"}, bpm: 60},
		rangeTrack{tags: []string{"rock"}},
	)
	wantIDs(t, rangeIDs(t, s, Filter{Genres: []string{"rock"}, TempoMin: 120, TempoMax: 180}), 2)
}

// TestFilterTempoOverridesMood IS THE WHOLE DESIGN DECISION.
//
// Five of sixteen moods already imply a tempo. Intersecting an explicit 120-180
// with calm's 50-95 gives an EMPTY station, and an operator who wrote "calm but
// driving" would get silence with nothing on screen to explain it. The explicit
// range is the operator's own words and it REPLACES the implication.
func TestFilterTempoOverridesMood(t *testing.T) {
	s := rangeStore(t,
		rangeTrack{tags: []string{"rock"}, moods: []string{"calm"}, bpm: 70},
		rangeTrack{tags: []string{"rock"}, moods: []string{"calm"}, bpm: 150},
	)
	calm := Filter{Genres: []string{"rock"}, Moods: []string{"calm"}}

	// Today's behaviour, unchanged when nothing explicit is set: calm implies
	// 50 to 95, so the slow one.
	wantIDs(t, rangeIDs(t, s, calm), 1)

	// And with an explicit range, the FAST one -- not an empty station.
	driving := calm
	driving.TempoMin, driving.TempoMax = 120, 180
	wantIDs(t, rangeIDs(t, s, driving), 2)
}

func TestFilterYearValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    Filter
		ok   bool
	}{
		{"no bounds at all", Filter{}, true},
		{"a real decade", Filter{YearMin: 1985, YearMax: 1989}, true},
		{"a floor only", Filter{YearMin: 1985}, true},
		{"before recorded music", Filter{YearMin: 1200}, false},
		{"a year that has not happened", Filter{YearMax: 9999}, false},
		{"an inverted pair", Filter{YearMin: 1990, YearMax: 1980}, false},
		{"a real tempo", Filter{TempoMin: 90, TempoMax: 140}, true},
		{"a tempo the sidecar refuses", Filter{TempoMin: 10}, false},
		{"a tempo nothing plays at", Filter{TempoMax: 400}, false},
		{"an inverted tempo", Filter{TempoMin: 180, TempoMax: 120}, false},
		{"a real length", Filter{DurationMinS: 60, DurationMaxS: 600}, true},
		{"a length shorter than a break", Filter{DurationMinS: 5}, false},
		{"a length longer than an album", Filter{DurationMaxS: 99999}, false},
		{"an inverted length", Filter{DurationMinS: 600, DurationMaxS: 60}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.f.Validate()
			if tc.ok && err != nil {
				t.Errorf("Validate() = %v, want it accepted", err)
			}
			// REFUSED HERE, REPAIRED IN enrich. DeriveStationParams already
			// fixes what a model returns; this is the boundary that catches a
			// hand-edited database or a direct API call.
			if !tc.ok && err == nil {
				t.Error("Validate() accepted it")
			}
			if !tc.ok && err != nil && !errors.Is(err, ErrBadVocabulary) &&
				!errors.Is(err, ErrBadRange) {
				t.Errorf("Validate() = %v, want a named refusal", err)
			}
		})
	}
}

// TestFilterYearFromStation: the station row is where these live, and a filter
// built without reading them is a brief that stops at the database.
func TestFilterYearFromStation(t *testing.T) {
	s := decades(t)
	ctx := context.Background()

	id, err := s.CreateStation(ctx, store.Station{
		Name: "EIGHTIES", Genre: "rock", Enabled: true,
		YearMin: 1980, YearMax: 1989,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Regenerate(ctx, s, id); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	got, err := s.StationTrackIDs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	wantIDs(t, got, 2, 3, 5)
}

// TestFilterTempoFromStation: the same for the other three, so a brief that
// said "calm but driving, nothing over four minutes" reaches the playlist.
func TestFilterTempoFromStation(t *testing.T) {
	s := rangeStore(t,
		rangeTrack{tags: []string{"rock"}, moods: []string{"calm"}, bpm: 70, duration: 200},
		rangeTrack{tags: []string{"rock"}, moods: []string{"calm"}, bpm: 150, duration: 200},
		rangeTrack{tags: []string{"rock"}, moods: []string{"calm"}, bpm: 150, duration: 900},
	)
	ctx := context.Background()

	id, err := s.CreateStation(ctx, store.Station{
		Name: "DRIVING", Genre: "rock", Mood: "calm", Enabled: true,
		TempoMin: 120, TempoMax: 180, DurationMinS: 60, DurationMaxS: 240,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Regenerate(ctx, s, id); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	got, err := s.StationTrackIDs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Only the fast, short one: the explicit tempo beat calm's implication and
	// the length bound dropped the nine-minute track.
	wantIDs(t, got, 2)
}
