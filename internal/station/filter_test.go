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

// filterTrack is one row of the fixture: what it is tagged, and whether the
// library still has it.
type filterTrack struct {
	tags       []string
	moods      []string
	confidence string
	unplayable bool
	missing    bool
	noDossier  bool
}

func filterStore(t *testing.T, tracks ...filterTrack) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	for i, tr := range tracks {
		id := int64(i + 1)
		playable, missing := 1, any(nil)
		if tr.unplayable {
			playable = 0
		}
		if tr.missing {
			missing = int64(1)
		}
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (id, path, playable, missing_at) VALUES (?, ?, ?, ?)`,
			id, fmt.Sprintf("/m/%d.mp3", id), playable, missing); err != nil {
			t.Fatal(err)
		}
		if tr.noDossier {
			continue
		}
		conf := tr.confidence
		if conf == "" {
			conf = enrich.ConfidenceHigh
		}
		raw, _ := json.Marshal(map[string]any{"station_tags": tr.tags, "mood": tr.moods})
		if _, err := s.DB().Exec(
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
			id, string(raw), conf); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func filterIDs(t *testing.T, s *store.Store, f Filter) []int64 {
	t.Helper()
	got, err := f.TrackIDs(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFilterGenreOnly(t *testing.T) {
	var rows []filterTrack
	for i := 0; i < 5; i++ {
		rows = append(rows, filterTrack{tags: []string{"synthwave"}, moods: []string{"nocturnal"}})
	}
	for i := 0; i < 3; i++ {
		rows = append(rows, filterTrack{tags: []string{"jazz"}, moods: []string{"warm"}})
	}
	s := filterStore(t, rows...)

	got := filterIDs(t, s, Filter{Genres: SplitList("synthwave")})
	if !reflect.DeepEqual(got, []int64{1, 2, 3, 4, 5}) {
		t.Errorf("= %v, want the five synthwave tracks in id order", got)
	}
}

func TestFilterGenreAndMood(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"synthwave"}, moods: []string{"nocturnal"}},
		filterTrack{tags: []string{"synthwave"}, moods: []string{"euphoric"}},
		filterTrack{tags: []string{"synthwave"}, moods: []string{"nocturnal", "cold"}},
		filterTrack{tags: []string{"jazz"}, moods: []string{"nocturnal"}},
	)

	got := filterIDs(t, s, Filter{Genres: SplitList("synthwave"), Moods: SplitList("nocturnal")})
	if !reflect.DeepEqual(got, []int64{1, 3}) {
		t.Errorf("= %v, want only the nocturnal synthwave", got)
	}
}

// TestFilterEmptyMoodMeansAny: most stations want a genre. Narrowing rock to
// one feeling leaves a fraction of the rock in a library.
func TestFilterEmptyMoodMeansAny(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"aggressive"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"warm"}},
		filterTrack{tags: []string{"rock"}, moods: nil},
	)

	got := filterIDs(t, s, Filter{Genres: SplitList("rock")})
	if !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Errorf("= %v, want every rock track including the one with no mood", got)
	}
}

// TestFilterExcludesMissingAndUnplayable: a station that offered a file the
// library no longer has would discover it mid-transition.
func TestFilterExcludesMissingAndUnplayable(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}, missing: true},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}, unplayable: true},
	)

	if got := filterIDs(t, s, Filter{Genres: SplitList("rock")}); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("= %v, want only the track that is there and plays", got)
	}
	// The catch-all is held to the same rule.
	if got := filterIDs(t, s, Filter{Genres: SplitList("other")}); len(got) != 0 {
		t.Errorf("the catch-all offered %v", got)
	}
}

func TestFilterUnknownGenreErrors(t *testing.T) {
	s := filterStore(t, filterTrack{tags: []string{"synthwave"}})

	// Case and near-misses are still refused. EMPTY IS NO LONGER HERE: an
	// absent genre means "any genre" now, the same way an absent mood has
	// always meant any mood. And an untrimmed "rock " never reaches Validate
	// because SplitList is the parser and trimming is what it is for -- so it
	// is asserted against the LIST directly, where a caller could still put it.
	for _, genre := range []string{"vaporwave", "Rock", "ROCK", "rock "} {
		if err := (Filter{Genres: []string{genre}}).Validate(); !errors.Is(err, ErrBadVocabulary) {
			t.Errorf("genre %q validated: %v", genre, err)
		}
		if _, err := (Filter{Genres: []string{genre}}).TrackIDs(context.Background(), s); !errors.Is(err, ErrBadVocabulary) {
			t.Errorf("genre %q selected tracks: %v", genre, err)
		}
	}
}

func TestFilterUnknownMoodErrors(t *testing.T) {
	s := filterStore(t, filterTrack{tags: []string{"rock"}, moods: []string{"raw"}})

	for _, mood := range []string{"sad", "Raw", "happy"} {
		f := Filter{Genres: SplitList("rock"), Moods: SplitList(mood)}
		if err := f.Validate(); !errors.Is(err, ErrBadVocabulary) {
			t.Errorf("mood %q validated: %v", mood, err)
		}
		if _, err := f.TrackIDs(context.Background(), s); !errors.Is(err, ErrBadVocabulary) {
			t.Errorf("mood %q selected tracks: %v", mood, err)
		}
	}
	if err := (Filter{Genres: SplitList("rock"), Moods: SplitList("raw")}).Validate(); err != nil {
		t.Errorf("a mood in the vocabulary was refused: %v", err)
	}
}

// TestFilterOtherIsCatchAll: "other" is not only the tracks tagged "other". It
// is everything the enrichment could not place -- including tracks with no
// dossier at all, which on a fresh library is most of them, and they have to be
// listenable.
func TestFilterOtherIsCatchAll(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"other"}, moods: []string{"warm"}},
		filterTrack{noDossier: true},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}, confidence: ConfidenceNone},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
	)

	got := filterIDs(t, s, Filter{Genres: SplitList("other")})
	if !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Errorf("= %v, want the tagged, the unenriched and the unplaceable", got)
	}
	// And a placed track is NOT in the catch-all.
	for _, id := range got {
		if id == 4 {
			t.Error("a track the enrichment placed is in the catch-all")
		}
	}
}

// TestFilterSecondaryTagMatches: a station is a VIEW of the library, not a
// partition of it. A track tagged both electronic and synthwave belongs on both
// stations.
func TestFilterSecondaryTagMatches(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"electronic", "synthwave"}, moods: []string{"nocturnal"}},
		filterTrack{tags: []string{"electronic"}, moods: []string{"nocturnal"}},
	)

	if got := filterIDs(t, s, Filter{Genres: SplitList("synthwave")}); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("synthwave = %v, want the track whose second tag it is", got)
	}
	if got := filterIDs(t, s, Filter{Genres: SplitList("electronic")}); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("electronic = %v, want both", got)
	}
	// Once each, not once per matching tag.
	if got := filterIDs(t, s, Filter{Genres: SplitList("electronic")}); len(got) != 2 {
		t.Errorf("a track with two tags was returned %d times", len(got))
	}
}

// TestFilterMatchesExactly: the vocabularies are exact BY DESIGN. Matching
// case-insensitively or with LIKE at query time would quietly reintroduce the
// near-duplicate buckets the closed vocabulary exists to prevent -- and a
// dossier written by an older build really can hold a value in the wrong case.
func TestFilterMatchesExactly(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"Rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"Raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock and roll"}, moods: []string{"raw"}},
	)

	if got := filterIDs(t, s, Filter{Genres: SplitList("rock")}); !reflect.DeepEqual(got, []int64{2, 3}) {
		t.Errorf("genre = %v, want only the exactly-tagged tracks", got)
	}
	if got := filterIDs(t, s, Filter{Genres: SplitList("rock"), Moods: SplitList("raw")}); !reflect.DeepEqual(got, []int64{3}) {
		t.Errorf("genre and mood = %v, want only the exact match on both", got)
	}
}

// TestFilterCountsATrackOnce: a dossier that lists a tag twice is a corrupt
// row, not two tracks. Without DISTINCT it would appear twice in the pool and
// play twice as often as everything else.
func TestFilterCountsATrackOnce(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock", "rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw", "raw"}},
	)

	if got := filterIDs(t, s, Filter{Genres: SplitList("rock")}); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("= %v, want each track once", got)
	}
	if got := filterIDs(t, s, Filter{Genres: SplitList("rock"), Moods: SplitList("raw")}); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("with a mood = %v, want each track once", got)
	}
}

// TestFilterExcludesMissingFromEveryQuery pins the guard on the genre query
// itself, not only on the catch-all.
func TestFilterExcludesMissingFromEveryQuery(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}, missing: true},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}, unplayable: true},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
	)
	for _, f := range []Filter{{Genres: []string{"rock"}}, {Genres: []string{"rock"}, Moods: []string{"raw"}}} {
		if got := filterIDs(t, s, f); !reflect.DeepEqual(got, []int64{3}) {
			t.Errorf("%+v = %v, want only the playable, present track", f, got)
		}
	}
}

func TestFilterDeterministicOrder(t *testing.T) {
	var rows []filterTrack
	for i := 0; i < 12; i++ {
		rows = append(rows, filterTrack{tags: []string{"rock"}, moods: []string{"raw"}})
	}
	s := filterStore(t, rows...)

	first := filterIDs(t, s, Filter{Genres: SplitList("rock")})
	for i := 0; i < 5; i++ {
		// An unstable pool makes a seeded selector produce a different
		// sequence on every run of the same library.
		if got := filterIDs(t, s, Filter{Genres: SplitList("rock")}); !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d gave %v, the first gave %v", i, got, first)
		}
	}
	if len(first) != 12 {
		t.Errorf("%d tracks, want 12", len(first))
	}
}

func TestFilterSurfacesFailures(t *testing.T) {
	s := filterStore(t, filterTrack{tags: []string{"rock"}})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := (Filter{Genres: SplitList("rock")}).TrackIDs(ctx, s); err == nil {
		t.Error("a genre filter succeeded on a closed store")
	}
	if _, err := (Filter{Genres: SplitList("other")}).TrackIDs(ctx, s); err == nil {
		t.Error("the catch-all succeeded on a closed store")
	}
}

// TestFilterRangeBoundsComeFromEnrich: the filter refuses outside exactly what
// the derive is allowed to produce.
//
// They were four numbers written out twice, and the year pair had already
// drifted into two constants with a comment on one saying it matched the other.
// A sampler bounded at 30 to 250 feeding a validator that refuses outside 40 to
// 200 is a machine for rejecting your own output -- the same failure the advert
// character cap already had once.
func TestFilterRangeBoundsComeFromEnrich(t *testing.T) {
	if slowestBPM != enrich.SlowestBPM || fastestBPM != enrich.FastestBPM {
		t.Errorf("tempo bounds %v..%v do not match the derive's %v..%v",
			slowestBPM, fastestBPM, enrich.SlowestBPM, enrich.FastestBPM)
	}
	if shortestTrackS != enrich.ShortestTrackS || longestTrackS != enrich.LongestTrackS {
		t.Errorf("length bounds %v..%v do not match the derive's %v..%v",
			shortestTrackS, longestTrackS, enrich.ShortestTrackS, enrich.LongestTrackS)
	}
	if earliestYear != enrich.EarliestBriefYear {
		t.Errorf("year floor %v does not match the derive's %v",
			earliestYear, enrich.EarliestBriefYear)
	}
	// AND A VALUE AT EACH BOUND IS ACCEPTED, not merely equal to a constant:
	// an off-by-one in Validate would pass every check above.
	for _, f := range []Filter{
		{Genres: []string{"rock"}, TempoMin: enrich.SlowestBPM, TempoMax: enrich.FastestBPM},
		{Genres: []string{"rock"}, DurationMinS: enrich.ShortestTrackS,
			DurationMaxS: enrich.LongestTrackS},
		{Genres: []string{"rock"}, YearMin: enrich.EarliestBriefYear},
	} {
		if err := f.Validate(); err != nil {
			t.Errorf("a filter exactly on the bounds was refused: %v", err)
		}
	}
}
