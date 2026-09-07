// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// A HAND EDIT OUTRANKS THE DOSSIER AND SURVIVES RE-ENRICHMENT. Writing the edit
// into the dossier would lose it the next time the track is enriched, which the
// requeue path does to whole libraries at once.

func tagStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.DB().Exec(`
		INSERT INTO tracks (id, path, playable) VALUES (1, '/m/1.mp3', 1), (2, '/m/2.mp3', 1);
		INSERT INTO dossiers (track_id, json, confidence)
		     VALUES (1, '{"station_tags":["rock"],"mood":["raw"]}', 'high')`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTrackTagsOverrideTheDossier(t *testing.T) {
	s := tagStore(t)
	ctx := context.Background()

	// Before any edit, the dossier IS the answer.
	got, err := s.TrackTags(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Genres) != 1 || got.Genres[0] != "rock" || got.Overridden {
		t.Fatalf("before the edit = %+v, want the dossier's own tags", got)
	}

	if err := s.SetTrackTags(ctx, 1, []string{"synthwave", "electronic"}, []string{"nocturnal"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.TrackTags(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Genres) != 2 || got.Genres[0] != "synthwave" || got.Moods[0] != "nocturnal" {
		t.Errorf("after the edit = %+v, want the operator's tags", got)
	}
	if !got.Overridden {
		t.Error("overridden = false after an edit")
	}

	// AND IT SURVIVES RE-ENRICHMENT. This is the whole reason it is a row of
	// its own: the dossier is rewritten, the edit stands.
	if _, err := s.DB().Exec(
		`UPDATE dossiers SET json = '{"station_tags":["pop"],"mood":["bright"]}' WHERE track_id = 1`); err != nil {
		t.Fatal(err)
	}
	got, _ = s.TrackTags(ctx, 1)
	if got.Genres[0] != "synthwave" {
		t.Errorf("after re-enrichment = %+v, want the edit to outrank the new dossier", got)
	}

	// A SECOND EDIT REPLACES the first rather than adding a row.
	if err := s.SetTrackTags(ctx, 1, []string{"ambient"}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = s.TrackTags(ctx, 1)
	if len(got.Genres) != 1 || got.Genres[0] != "ambient" || len(got.Moods) != 0 {
		t.Errorf("second edit = %+v, want it to replace the first", got)
	}
}

func TestTrackTagsClearGoesBackToTheDossier(t *testing.T) {
	s := tagStore(t)
	ctx := context.Background()
	if err := s.SetTrackTags(ctx, 1, []string{"ambient"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearTrackTags(ctx, 1); err != nil {
		t.Fatal(err)
	}
	got, err := s.TrackTags(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Overridden || got.Genres[0] != "rock" {
		t.Errorf("after clearing = %+v, want the dossier back", got)
	}
	// Clearing an edit that is not there is ErrNotFound, so a console can tell
	// "reverted" from "there was nothing to revert".
	if err := s.ClearTrackTags(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("clearing nothing = %v, want ErrNotFound", err)
	}
}

func TestTrackTagsOnATrackWithNoDossier(t *testing.T) {
	s := tagStore(t)
	ctx := context.Background()

	// Track 2 has never been enriched: empty lists, not an error.
	got, err := s.TrackTags(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Genres) != 0 || len(got.Moods) != 0 || got.Overridden {
		t.Fatalf("unenriched = %+v, want empty", got)
	}

	// And it can still be placed by hand, which is the point: an operator
	// should not have to wait for enrichment to file a track they can see.
	if err := s.SetTrackTags(ctx, 2, []string{"folk"}, []string{"gentle"}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.TrackTags(ctx, 2)
	if got.Genres[0] != "folk" || !got.Overridden {
		t.Errorf("hand-placed = %+v", got)
	}
}

func TestTrackTagsRefuseATrackTheLibraryDoesNotHave(t *testing.T) {
	s := tagStore(t)
	ctx := context.Background()
	if err := s.SetTrackTags(ctx, 404, []string{"folk"}, nil); err == nil {
		t.Error("tagged a track that does not exist")
	}
	if _, err := s.TrackTags(ctx, 404); !errors.Is(err, ErrNotFound) {
		t.Errorf("reading a missing track = %v, want ErrNotFound", err)
	}
	if err := s.ClearTrackTags(ctx, 404); !errors.Is(err, ErrNotFound) {
		t.Errorf("clearing a missing track = %v, want ErrNotFound", err)
	}
}

func TestTrackTagsSurfaceDatabaseErrors(t *testing.T) {
	s := tagStore(t)
	ctx := context.Background()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTrackTags(ctx, 1, []string{"folk"}, nil); err == nil {
		t.Error("SetTrackTags reported success against a closed database")
	}
	if _, err := s.TrackTags(ctx, 1); err == nil {
		t.Error("TrackTags reported success against a closed database")
	}
	if err := s.ClearTrackTags(ctx, 1); err == nil {
		t.Error("ClearTrackTags reported success against a closed database")
	}
}

// TestTrackTagsRejectACorruptList: the columns are JSON, and JSON that will not
// parse must be an error rather than a track with no tags -- which would move
// it into the catch-all and read as enrichment having failed.
func TestTrackTagsRejectACorruptList(t *testing.T) {
	s := tagStore(t)
	ctx := context.Background()
	if err := s.SetTrackTags(ctx, 1, []string{"folk"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE track_tags SET mood = 'not json' WHERE track_id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrackTags(ctx, 1); err == nil {
		t.Error("a corrupt list read back as a track with no mood")
	}
}

// TestTrackTagsNullListsReadAsEmpty: json_extract returns SQL NULL for a
// dossier whose mood key is absent, and the view coalesces that to "[]" --
// but a stored NULL inside the JSON itself decodes to a nil slice, which
// would marshal as null and make a console render "null" as a tag.
func TestTrackTagsNullListsReadAsEmpty(t *testing.T) {
	var got TrackTags
	if err := decodeTagLists(&got, "null", "null"); err != nil {
		t.Fatal(err)
	}
	if got.Genres == nil || got.Moods == nil {
		t.Errorf("= %+v, want empty lists rather than nil", got)
	}
}

// TestTrackTagsShowInThePlaylistRow: the playlist is where the edit is MADE, so
// it is the one place that must never still be showing the dossier afterwards.
func TestTrackTagsShowInThePlaylistRow(t *testing.T) {
	s := tagStore(t)
	ctx := context.Background()
	if _, err := s.DB().Exec(`
		INSERT INTO stations (id, name, genre, created_at) VALUES (1, 'Rock', 'rock', 1);
		INSERT INTO station_tracks (station_id, track_id) VALUES (1, 1)`); err != nil {
		t.Fatal(err)
	}

	rows, _, err := s.StationTrackPage(ctx, 1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Genres[0] != "rock" || rows[0].Overridden {
		t.Fatalf("before the edit = %+v, want the dossier's tags", rows)
	}

	if err := s.SetTrackTags(ctx, 1, []string{"synthwave"}, []string{"nocturnal"}); err != nil {
		t.Fatal(err)
	}
	rows, _, err = s.StationTrackPage(ctx, 1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Genres[0] != "synthwave" || rows[0].Moods[0] != "nocturnal" {
		t.Errorf("after the edit = %+v, want the operator's tags", rows[0])
	}
	// The console has to be able to offer "put it back", which means knowing
	// there is something to put back.
	if !rows[0].Overridden {
		t.Error("overridden = false on an edited row")
	}
}

// TestMoodTempoShowsInThePlaylistRow: the analyser has measured the tempo all
// along and the operator could not see it, so a track kept or dropped by a
// mood's range had nothing on screen to explain why.
func TestMoodTempoShowsInThePlaylistRow(t *testing.T) {
	s := tagStore(t)
	ctx := context.Background()
	if _, err := s.DB().Exec(`
		INSERT INTO stations (id, name, genre, created_at) VALUES (1, 'Rock', 'rock', 1);
		INSERT INTO station_tracks (station_id, track_id) VALUES (1, 1), (1, 2);
		UPDATE tracks SET bpm = 128.5 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	rows, _, err := s.StationTrackPage(ctx, 1, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].BPM != 128.5 {
		t.Errorf("bpm = %v, want the measured tempo", rows[0].BPM)
	}
	// ZERO MEANS UNMEASURED, not "silent". The column is NULL until the
	// background pass reaches the track, which on a fresh library is most of
	// them.
	if rows[1].BPM != 0 {
		t.Errorf("unmeasured bpm = %v, want 0", rows[1].BPM)
	}
}
