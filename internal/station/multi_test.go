// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"slices"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

// A STATION WAS ONE GENRE AND AT MOST ONE MOOD. "Rock and punk", or "anything
// nocturnal or melancholic", meant two stations on the dial that a listener has
// to flip between -- and the dial is the listener's whole UI.
//
// Stored comma-joined in the columns that already exist, so a station written
// before this reads back as a one-element list and no migration is needed. The
// vocabularies are closed and contain no commas, which is what makes the
// separator safe.
//
// Every test here is TestMulti*, which is the -run pattern for this fix.

func TestMultiSplitAndJoinRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"rock", []string{"rock"}},
		{"rock,punk", []string{"rock", "punk"}},
		// Tolerated on the way IN, because an operator editing the database by
		// hand is a thing that happens.
		{" rock , punk ", []string{"rock", "punk"}},
		{"rock,,punk", []string{"rock", "punk"}},
	} {
		got := SplitList(tc.raw)
		if !slices.Equal(got, tc.want) {
			t.Errorf("SplitList(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}

	if got := JoinList([]string{"rock", "punk"}); got != "rock,punk" {
		t.Errorf("JoinList = %q, want rock,punk", got)
	}
	if got := JoinList(nil); got != "" {
		t.Errorf("JoinList(nil) = %q, want empty", got)
	}
}

func TestMultiFilterValidatesEveryValue(t *testing.T) {
	if err := (Filter{Genres: []string{"rock", "punk"}}).Validate(); err != nil {
		t.Errorf("two real genres were refused: %v", err)
	}
	// ONE bad value poisons the filter: a station that silently dropped it
	// would select more music than the operator asked for.
	if err := (Filter{Genres: []string{"rock", "wizard"}}).Validate(); err == nil {
		t.Error("a genre outside the vocabulary was accepted")
	}
	if err := (Filter{Genres: []string{"rock"}, Moods: []string{"calm", "wizard"}}).Validate(); err == nil {
		t.Error("a mood outside the vocabulary was accepted")
	}
	// NO GENRE MEANS ANY GENRE, the same way no mood already meant any mood.
	// An operator who wants "everything nocturnal" should not have to name
	// every genre in the vocabulary to get it.
	if err := (Filter{}).Validate(); err != nil {
		t.Errorf("a filter with no genre was refused: %v", err)
	}
	if err := (Filter{Moods: []string{"calm"}}).Validate(); err != nil {
		t.Errorf("mood-only was refused: %v", err)
	}
}

func TestMultiFilterWithNoGenreTakesEverything(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"calm"}},
		filterTrack{tags: []string{"pop"}, moods: []string{"nocturnal"}},
		filterTrack{noDossier: true},
		filterTrack{tags: []string{"metal"}, moods: []string{"calm"}},
		filterTrack{tags: []string{"rock"}, unplayable: true},
		filterTrack{tags: []string{"rock"}, missing: true},
	)
	all := picked(t, Filter{}, s)
	// Every playable, present track -- INCLUDING the one with no dossier,
	// because "any genre" is not "any genre we managed to work out".
	if len(all) != 4 {
		t.Errorf("no filter selected %d tracks, want the 4 playable present ones: %v", len(all), all)
	}
	// Unplayable and missing are still excluded: those are not taste, they are
	// tracks that cannot be played.
	if slices.Contains(all, int64(5)) || slices.Contains(all, int64(6)) {
		t.Errorf("an unplayable or missing track was selected: %v", all)
	}

	// A mood with no genre narrows by feeling alone, and now needs a dossier
	// to know the feeling -- so the untagged track drops out.
	calm := picked(t, Filter{Moods: []string{"calm"}}, s)
	if len(calm) != 2 {
		t.Errorf("mood-only selected %d, want the 2 calm ones: %v", len(calm), calm)
	}
}

func TestMultiFilterSelectsTheUnionOfItsGenres(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"calm"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"nocturnal"}},
		filterTrack{tags: []string{"pop"}, moods: []string{"calm"}},
		// Tagged BOTH, which is what makes the de-duplication real rather than
		// theoretical: enrichment gives a track up to three station tags.
		filterTrack{tags: []string{"rock", "pop"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"metal"}, moods: []string{"aggressive"}},
	)
	rock := picked(t, Filter{Genres: []string{"rock"}}, s)
	pop := picked(t, Filter{Genres: []string{"pop"}}, s)
	both := picked(t, Filter{Genres: []string{"rock", "pop"}}, s)

	if len(rock) == 0 || len(pop) == 0 {
		t.Fatalf("fixture is not useful: rock=%d pop=%d", len(rock), len(pop))
	}
	for _, id := range append(append([]int64{}, rock...), pop...) {
		if !slices.Contains(both, id) {
			t.Errorf("track %d is in one of the genres but not in the union", id)
		}
	}
	// A track tagged BOTH must appear once, not twice: the pool feeds a
	// selector, and a duplicate is a track that plays twice as often.
	seen := map[int64]bool{}
	for _, id := range both {
		if seen[id] {
			t.Errorf("track %d appears twice in the union", id)
		}
		seen[id] = true
	}
	// And still ordered, or a seeded selector is not reproducible.
	if !slices.IsSorted(both) {
		t.Error("the union is not in id order")
	}
}

func TestMultiFilterMoodsAreAnyOfThem(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"calm"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"nocturnal"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"aggressive"}},
	)
	calm := picked(t, Filter{Genres: []string{"rock"}, Moods: []string{"calm"}}, s)
	either := picked(t, Filter{
		Genres: []string{"rock"},
		Moods:  []string{"calm", "nocturnal"},
	}, s)

	for _, id := range calm {
		if !slices.Contains(either, id) {
			t.Errorf("track %d matched one mood but not the wider set", id)
		}
	}
	if len(either) < len(calm) {
		t.Errorf("widening the moods selected fewer tracks: %d then %d", len(calm), len(either))
	}
}

// picked runs a filter, so a test reads as the question it is asking.
func picked(t *testing.T, f Filter, s *store.Store) []int64 {
	t.Helper()
	got, err := f.TrackIDs(context.Background(), s)
	if err != nil {
		t.Fatalf("TrackIDs%v: %v", f.Genres, err)
	}
	return got
}
