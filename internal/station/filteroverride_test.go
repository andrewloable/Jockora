// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"reflect"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

// AN OPERATOR'S EDIT DECIDES WHAT A STATION CONTAINS. Showing the edit in the
// playlist while selecting on the dossier would be a control that appears to
// work and changes nothing -- the failure this whole feature exists to avoid.

func TestFilterOverrideMovesATrackBetweenStations(t *testing.T) {
	ctx := context.Background()
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
	)

	rock := Filter{Genres: []string{"rock"}}
	synth := Filter{Genres: []string{"synthwave"}}
	if got, _ := rock.TrackIDs(ctx, s); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("rock = %v, want both", got)
	}

	if err := s.SetTrackTags(ctx, 2, []string{"synthwave"}, []string{"nocturnal"}); err != nil {
		t.Fatal(err)
	}

	if got, _ := rock.TrackIDs(ctx, s); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("rock = %v, want the re-tagged track gone", got)
	}
	if got, _ := synth.TrackIDs(ctx, s); !reflect.DeepEqual(got, []int64{2}) {
		t.Errorf("synthwave = %v, want the re-tagged track", got)
	}
	// The MOOD moved with it, or a mood-narrowed station would still be
	// selecting on what enrichment said.
	night := Filter{Genres: []string{"synthwave"}, Moods: []string{"nocturnal"}}
	if got, _ := night.TrackIDs(ctx, s); !reflect.DeepEqual(got, []int64{2}) {
		t.Errorf("synthwave + nocturnal = %v, want the re-tagged track", got)
	}
}

func TestFilterOverridePlacesATrackWithNoDossier(t *testing.T) {
	// The point of allowing the edit at all: an operator who can see a track in
	// the catch-all should not have to wait for enrichment to file it.
	ctx := context.Background()
	s := filterStore(t, filterTrack{noDossier: true})

	unsorted := Filter{Genres: []string{"unsorted"}}
	if got, _ := unsorted.TrackIDs(ctx, s); !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("unsorted = %v, want the unenriched track", got)
	}

	if err := s.SetTrackTags(ctx, 1, []string{"folk"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := (Filter{Genres: []string{"folk"}}).TrackIDs(ctx, s); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("folk = %v, want the hand-placed track", got)
	}
	// AND IT LEAVES THE CATCH-ALL. A track somebody has placed is not unsorted,
	// however little the enrichment managed to say about it.
	if got, _ := unsorted.TrackIDs(ctx, s); len(got) != 0 {
		t.Errorf("unsorted = %v, want the hand-placed track gone", got)
	}
}

func TestFilterOverrideKeepsAnEmptyEditInTheCatchAll(t *testing.T) {
	// Clearing every tag is a real answer -- "this is not anything" -- and it
	// belongs in the catch-all, which is where a listener finds it.
	ctx := context.Background()
	s := filterStore(t, filterTrack{tags: []string{"rock"}})
	if err := s.SetTrackTags(ctx, 1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := (Filter{Genres: []string{"rock"}}).TrackIDs(ctx, s); len(got) != 0 {
		t.Errorf("rock = %v, want the emptied track gone", got)
	}
	if got, _ := (Filter{Genres: []string{"unsorted"}}).TrackIDs(ctx, s); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("unsorted = %v, want the emptied track", got)
	}
}

func TestFilterOverrideOnADossierThatAssertedNothing(t *testing.T) {
	// confidence "none" is what puts a track in the catch-all even though it
	// HAS a dossier. An operator placing it by hand takes it out; the dossier
	// still says nothing, and that must stop mattering.
	ctx := context.Background()
	s := filterStore(t, filterTrack{confidence: ConfidenceNone})

	unsorted := Filter{Genres: []string{"unsorted"}}
	if got, _ := unsorted.TrackIDs(ctx, s); !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("unsorted = %v, want the track that asserted nothing", got)
	}
	if err := s.SetTrackTags(ctx, 1, []string{"jazz"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := unsorted.TrackIDs(ctx, s); len(got) != 0 {
		t.Errorf("unsorted = %v, want the placed track gone", got)
	}
	if got, _ := (Filter{Genres: []string{"jazz"}}).TrackIDs(ctx, s); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("jazz = %v, want the placed track", got)
	}
}

// An override never resurrects a track the library has lost or cannot play.
func TestFilterOverrideDoesNotOverrideThePlayableRules(t *testing.T) {
	ctx := context.Background()
	s := filterStore(t,
		filterTrack{noDossier: true, unplayable: true},
		filterTrack{noDossier: true, missing: true},
	)
	for _, id := range []int64{1, 2} {
		if err := s.SetTrackTags(ctx, id, []string{"folk"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := (Filter{Genres: []string{"folk"}}).TrackIDs(ctx, s); len(got) != 0 {
		t.Errorf("folk = %v, want nothing: one is unplayable, the other is gone", got)
	}
	var _ = store.TrackTags{}
}

// A MOOD IMPLIES A TEMPO. The dossier tag says what a track feels like; the
// analyser has already measured how fast it is, and nothing joined the two.

func TestMoodTempoNarrowsAStationPlaylist(t *testing.T) {
	ctx := context.Background()
	s := filterStore(t,
		filterTrack{tags: []string{"electronic"}, moods: []string{"calm"}},
		filterTrack{tags: []string{"electronic"}, moods: []string{"calm"}},
	)
	setBPM(t, s, 1, 70)
	setBPM(t, s, 2, 174)

	got, err := (Filter{Moods: []string{"calm"}}).TrackIDs(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("calm = %v, want only the slow one", got)
	}
}

func TestMoodTempoKeepsATrackNobodyHasMeasured(t *testing.T) {
	// Analysis is a slow background pass. On a fresh library almost nothing has
	// a tempo yet, and a range that dropped those would turn every mood station
	// into an empty station.
	ctx := context.Background()
	s := filterStore(t,
		filterTrack{tags: []string{"electronic"}, moods: []string{"calm"}},
		filterTrack{tags: []string{"electronic"}, moods: []string{"calm"}},
	)
	setBPM(t, s, 2, 174)

	got, _ := (Filter{Moods: []string{"calm"}}).TrackIDs(ctx, s)
	if !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("calm = %v, want the unmeasured track kept and the fast one dropped", got)
	}
}

func TestMoodTempoLeavesAnUnrangedMoodAlone(t *testing.T) {
	ctx := context.Background()
	s := filterStore(t,
		filterTrack{tags: []string{"pop"}, moods: []string{"romantic"}},
		filterTrack{tags: []string{"pop"}, moods: []string{"romantic"}},
	)
	setBPM(t, s, 1, 40)
	setBPM(t, s, 2, 240)

	got, _ := (Filter{Moods: []string{"romantic"}}).TrackIDs(ctx, s)
	if !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("romantic = %v, want every tempo", got)
	}
}

func TestMoodTempoUnionsTwoRangesInTheQuery(t *testing.T) {
	ctx := context.Background()
	s := filterStore(t,
		filterTrack{tags: []string{"electronic"}, moods: []string{"calm"}},
		filterTrack{tags: []string{"electronic"}, moods: []string{"aggressive"}},
		filterTrack{tags: []string{"electronic"}, moods: []string{"calm"}},
	)
	setBPM(t, s, 1, 70)
	setBPM(t, s, 2, 180)
	setBPM(t, s, 3, 210) // outside both

	got, _ := (Filter{Moods: []string{"calm", "aggressive"}}).TrackIDs(ctx, s)
	if !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("calm+aggressive = %v, want both ends and not the gap beyond", got)
	}
}

func TestMoodTempoNarrowsTheCatchAllToo(t *testing.T) {
	// The catch-all takes a mood like any other station, so it has to answer
	// the same question about tempo.
	ctx := context.Background()
	s := filterStore(t,
		filterTrack{tags: []string{"other"}, moods: []string{"calm"}},
		filterTrack{tags: []string{"other"}, moods: []string{"calm"}},
	)
	setBPM(t, s, 1, 70)
	setBPM(t, s, 2, 174)

	got, _ := (Filter{Genres: []string{"other"}, Moods: []string{"calm"}}).TrackIDs(ctx, s)
	if !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("unsorted + calm = %v, want only the slow one", got)
	}
}

func setBPM(t *testing.T, s *store.Store, id int64, bpm float64) {
	t.Helper()
	if _, err := s.DB().Exec(`UPDATE tracks SET bpm = ? WHERE id = ?`, bpm, id); err != nil {
		t.Fatal(err)
	}
}

// TestFilterOverrideDecidesTheSeededPoolsToo: PoolForTag builds the pool for a
// station the first run PROPOSED, and it read dossiers directly. An enrichment
// file imported into a fresh install carries overrides with it, so the operator
// could have filed tracks before any station existed -- and the seeded station
// would then have played by the enrichment's answer, not theirs.
func TestFilterOverrideDecidesTheSeededPoolsToo(t *testing.T) {
	ctx := context.Background()
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}},
		filterTrack{noDossier: true},
	)
	if err := s.SetTrackTags(ctx, 1, []string{"folk"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTrackTags(ctx, 2, []string{"folk"}, nil); err != nil {
		t.Fatal(err)
	}

	got, err := PoolForTag(ctx, s, "folk")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("folk pool = %v, want both re-filed tracks", got)
	}
	if got, _ := PoolForTag(ctx, s, "rock"); len(got) != 0 {
		t.Errorf("rock pool = %v, want the re-filed track gone", got)
	}
	// AND THE UNSORTED POOL LETS THEM GO. A track somebody has placed is not
	// unsorted, however little the enrichment managed to say about it.
	if got, _ := PoolForTag(ctx, s, UnsortedTag); len(got) != 0 {
		t.Errorf("unsorted pool = %v, want the hand-placed tracks gone", got)
	}
}
