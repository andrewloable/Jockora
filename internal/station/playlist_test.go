// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

// playlistFixture builds a library and one station over it.
func playlistFixture(t *testing.T, genre, mood string, tracks ...filterTrack) (*store.Store, int64) {
	t.Helper()
	s := filterStore(t, tracks...)
	id, err := s.CreateStation(context.Background(), store.Station{
		Name: "S", Genre: genre, Mood: mood, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

func rockTracks(n int) []filterTrack {
	out := make([]filterTrack, n)
	for i := range out {
		out[i] = filterTrack{tags: []string{"rock"}, moods: []string{"raw"}}
	}
	return out
}

func playlistOf(t *testing.T, s *store.Store, id int64) []store.StationTrack {
	t.Helper()
	rows, err := s.ListStationTracks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func regenerate(t *testing.T, s *store.Store, id int64) Diff {
	t.Helper()
	d, err := Regenerate(context.Background(), s, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestMaterializeFromEmpty(t *testing.T) {
	s, id := playlistFixture(t, "rock", "", rockTracks(5)...)

	got := regenerate(t, s, id)
	if got != (Diff{Added: 5}) {
		t.Errorf("Diff = %+v, want 5 added and nothing else", got)
	}
	ids, err := s.StationTrackIDs(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []int64{1, 2, 3, 4, 5}) {
		t.Errorf("playlist = %v", ids)
	}
}

// TestMaterializeKeepsPins: a pin is the operator saying "this one stays". A
// regeneration that dropped it would make pinning meaningless the moment the
// library was rescanned.
func TestMaterializeKeepsPins(t *testing.T) {
	tracks := append(rockTracks(3), filterTrack{tags: []string{"jazz"}, moods: []string{"warm"}})
	s, id := playlistFixture(t, "rock", "", tracks...)
	ctx := context.Background()

	regenerate(t, s, id)
	// A track the filter does not select at all, pinned on by hand.
	if err := s.ReplaceStationTracks(ctx, id, []int64{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, id, 4, true); err != nil {
		t.Fatal(err)
	}

	got := regenerate(t, s, id)
	if got.Removed != 0 || got.Kept != 4 {
		t.Errorf("Diff = %+v, want everything kept including the pin", got)
	}
	rows := playlistOf(t, s, id)
	var found bool
	for _, r := range rows {
		if r.TrackID == 4 {
			found = r.Pinned
		}
	}
	if !found {
		t.Errorf("the pinned track outside the filter was dropped: %+v", rows)
	}
	ids, _ := s.StationTrackIDs(ctx, id)
	if !reflect.DeepEqual(ids, []int64{1, 2, 3, 4}) {
		t.Errorf("playlist = %v, want the pin still playable", ids)
	}
}

// TestMaterializeKeepsExcludes: without keeping the row, the next regeneration
// adds the track straight back and the operator has to ban it forever.
func TestMaterializeKeepsExcludes(t *testing.T) {
	s, id := playlistFixture(t, "rock", "", rockTracks(4)...)
	ctx := context.Background()

	regenerate(t, s, id)
	if err := s.SetExcluded(ctx, id, 2, true); err != nil {
		t.Fatal(err)
	}

	got := regenerate(t, s, id)
	if got.Added != 0 || got.Removed != 0 {
		t.Errorf("Diff = %+v, want nothing to change", got)
	}
	rows := playlistOf(t, s, id)
	if len(rows) != 4 {
		t.Fatalf("%d rows, want the excluded one kept: %+v", len(rows), rows)
	}
	var excluded bool
	for _, r := range rows {
		if r.TrackID == 2 {
			excluded = r.Excluded
		}
	}
	if !excluded {
		t.Error("the exclusion was cleared by regenerating")
	}
	// Present to the console, absent from the pool.
	ids, _ := s.StationTrackIDs(ctx, id)
	if !reflect.DeepEqual(ids, []int64{1, 3, 4}) {
		t.Errorf("playlist = %v, want the excluded track omitted", ids)
	}
}

func TestMaterializeAddsNewMatches(t *testing.T) {
	s, id := playlistFixture(t, "rock", "", rockTracks(3)...)
	ctx := context.Background()
	regenerate(t, s, id)

	// A track that arrived with the last scan and has just been enriched.
	if _, err := s.DB().Exec(
		`INSERT INTO tracks (id, path, playable) VALUES (4, '/m/4.mp3', 1)`); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"station_tags": []string{"rock"}, "mood": []string{"raw"}})
	if _, err := s.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence) VALUES (4, ?, ?)`,
		string(raw), enrich.ConfidenceHigh); err != nil {
		t.Fatal(err)
	}

	got := regenerate(t, s, id)
	if got.Added != 1 || got.Removed != 0 || got.Kept != 3 {
		t.Errorf("Diff = %+v, want one added and three kept", got)
	}
	ids, _ := s.StationTrackIDs(ctx, id)
	if !reflect.DeepEqual(ids, []int64{1, 2, 3, 4}) {
		t.Errorf("playlist = %v", ids)
	}
}

func TestMaterializeRemovesNonMatches(t *testing.T) {
	tracks := append(rockTracks(3),
		filterTrack{tags: []string{"jazz"}, moods: []string{"warm"}},
		filterTrack{tags: []string{"jazz"}, moods: []string{"warm"}})
	s, id := playlistFixture(t, "rock", "", tracks...)
	ctx := context.Background()
	regenerate(t, s, id)

	// The operator changes what the station is.
	st, err := s.GetStation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	st.Genre = "jazz"
	if err := s.UpdateStation(ctx, st); err != nil {
		t.Fatal(err)
	}

	got := regenerate(t, s, id)
	if got.Removed != 3 || got.Added != 2 || got.Kept != 0 {
		t.Errorf("Diff = %+v, want the three rock tracks out and two jazz in", got)
	}
	ids, _ := s.StationTrackIDs(ctx, id)
	if !reflect.DeepEqual(ids, []int64{4, 5}) {
		t.Errorf("playlist = %v, want only the jazz", ids)
	}
}

// TestMaterializeTransactional: a half-regenerated station is a station with a
// hole in it, and the hole is discovered at play time.
func TestMaterializeTransactional(t *testing.T) {
	s, id := playlistFixture(t, "rock", "", rockTracks(5)...)
	ctx := context.Background()

	// A trigger that refuses one specific insert, part way through.
	if _, err := s.DB().Exec(`CREATE TRIGGER no_four BEFORE INSERT ON station_tracks
		WHEN NEW.track_id = 4 BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}

	if _, err := Regenerate(ctx, s, id); err == nil {
		t.Fatal("a regeneration that could not finish reported success")
	}
	rows := playlistOf(t, s, id)
	if len(rows) != 0 {
		t.Errorf("%d rows survived a failed regeneration, want none: %+v", len(rows), rows)
	}

	// And with the obstruction gone it completes.
	if _, err := s.DB().Exec(`DROP TRIGGER no_four`); err != nil {
		t.Fatal(err)
	}
	if got := regenerate(t, s, id); got.Added != 5 {
		t.Errorf("Diff = %+v after the retry, want 5 added", got)
	}
}

// TestMaterializeTransactionalOnRemoval: the same guarantee on the way out.
func TestMaterializeTransactionalOnRemoval(t *testing.T) {
	tracks := append(rockTracks(3),
		filterTrack{tags: []string{"jazz"}, moods: []string{"warm"}})
	s, id := playlistFixture(t, "rock", "", tracks...)
	ctx := context.Background()
	regenerate(t, s, id)

	if _, err := s.DB().Exec(`CREATE TRIGGER no_delete BEFORE DELETE ON station_tracks
		WHEN OLD.track_id = 2 BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	st, err := s.GetStation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	st.Genre = "jazz"
	if err := s.UpdateStation(ctx, st); err != nil {
		t.Fatal(err)
	}

	if _, err := Regenerate(ctx, s, id); err == nil {
		t.Fatal("a regeneration that could not finish reported success")
	}
	ids, _ := s.StationTrackIDs(ctx, id)
	if !reflect.DeepEqual(ids, []int64{1, 2, 3}) {
		t.Errorf("playlist = %v, want the original three untouched", ids)
	}
}

func TestMaterializeIdempotent(t *testing.T) {
	s, id := playlistFixture(t, "rock", "", rockTracks(6)...)
	ctx := context.Background()

	if got := regenerate(t, s, id); got.Added != 6 {
		t.Fatalf("first = %+v", got)
	}
	// NOTHING TOUCHED, proven rather than asserted: triggers that refuse every
	// write to the playlist. A regeneration that rewrote an unchanged station
	// would fail here, and on a real library that is a transaction per
	// regeneration for no change at all.
	for _, ddl := range []string{
		`CREATE TRIGGER no_ins BEFORE INSERT ON station_tracks
		   BEGIN SELECT RAISE(ABORT, 'wrote for no reason'); END`,
		`CREATE TRIGGER no_del BEFORE DELETE ON station_tracks
		   BEGIN SELECT RAISE(ABORT, 'wrote for no reason'); END`,
	} {
		if _, err := s.DB().Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if got := regenerate(t, s, id); got != (Diff{Kept: 6}) {
			t.Errorf("run %d = %+v, want everything kept and nothing touched", i+2, got)
		}
	}
	for _, name := range []string{"no_ins", "no_del"} {
		if _, err := s.DB().Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
	ids, _ := s.StationTrackIDs(ctx, id)
	if len(ids) != 6 {
		t.Errorf("%d tracks after repeated regeneration, want 6", len(ids))
	}
}

func TestMaterializeSurfacesFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("no such station", func(t *testing.T) {
		s, _ := playlistFixture(t, "rock", "", rockTracks(1)...)
		if _, err := Regenerate(ctx, s, 9999); err == nil {
			t.Error("regenerating a station that does not exist reported success")
		}
	})

	t.Run("genre outside the vocabulary", func(t *testing.T) {
		// The database's own CHECK does not constrain genre, so a station can
		// hold one no dossier could ever carry.
		s, id := playlistFixture(t, "rock", "", rockTracks(1)...)
		if _, err := s.DB().Exec(`UPDATE stations SET genre = 'vaporwave'`); err != nil {
			t.Fatal(err)
		}
		if _, err := Regenerate(ctx, s, id); err == nil {
			t.Error("a station whose genre is a typo regenerated silently")
		}
	})

	t.Run("closed store", func(t *testing.T) {
		s, id := playlistFixture(t, "rock", "", rockTracks(1)...)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Regenerate(ctx, s, id); err == nil {
			t.Error("regenerating on a closed store reported success")
		}
	})

	t.Run("playlist unreadable", func(t *testing.T) {
		s, id := playlistFixture(t, "rock", "", rockTracks(2)...)
		if _, err := s.DB().Exec(`ALTER TABLE station_tracks DROP COLUMN pinned`); err != nil {
			t.Fatal(err)
		}
		if _, err := Regenerate(ctx, s, id); err == nil {
			t.Error("a playlist that could not be read regenerated anyway")
		}
	})

	t.Run("read-only store", func(t *testing.T) {
		s, id := playlistFixture(t, "rock", "", rockTracks(2)...)
		s.DB().SetMaxOpenConns(1)
		if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
			t.Fatal(err)
		}
		if _, err := Regenerate(ctx, s, id); err == nil {
			t.Error("a regeneration that could not be written reported success")
		}
	})
}
