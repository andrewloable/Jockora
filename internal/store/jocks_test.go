// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func jockStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleJock() Jock {
	return Jock{
		ID: "dutch_mahoney", Name: `Dutch "The Hammer" Mahoney`,
		VoiceID:       "kokoro:am_fenrir",
		GoodForGenres: []string{"rock", "metal", "punk"},
		GoodForMoods:  []string{"aggressive", "raw"},
		SpeechStyle:   "LOUD. Short bursts.",
		Personality:   "Has been shouting about rock and roll for years.",
		Forbidden:     []string{"never claims a fact he has not been given"},
	}
}

func TestJocksUpsertAndGet(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()
	want := sampleJock()

	if err := s.UpsertJock(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetJock(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.ID != want.ID || got.Name != want.Name || got.VoiceID != want.VoiceID {
		t.Errorf("identity did not round-trip: %+v", got)
	}
	if got.SpeechStyle != want.SpeechStyle || got.Personality != want.Personality {
		t.Errorf("prose did not round-trip: %+v", got)
	}
	// The list columns are JSON in the database and must come back as slices,
	// not as a string a caller has to parse.
	if len(got.GoodForGenres) != 3 || got.GoodForGenres[0] != "rock" {
		t.Errorf("GoodForGenres = %#v", got.GoodForGenres)
	}
	if len(got.GoodForMoods) != 2 || got.GoodForMoods[1] != "raw" {
		t.Errorf("GoodForMoods = %#v", got.GoodForMoods)
	}
	if len(got.Forbidden) != 1 {
		t.Errorf("Forbidden = %#v", got.Forbidden)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}

	if _, err := s.GetJock(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetJock(nobody) = %v, want ErrNotFound", err)
	}
}

func TestJocksList(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()

	roxy := sampleJock()
	roxy.ID, roxy.Name = "roxy", "Roxy Sinclair"
	if err := s.UpsertJock(ctx, roxy); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertJock(ctx, sampleJock()); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListJocks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d jocks, want 2", len(list))
	}
	if list[0].Name >= list[1].Name {
		t.Errorf("not ordered by name: %q then %q", list[0].Name, list[1].Name)
	}
}

// TestJocksUpsertUpdatesInPlace: the console edits a jock by writing it back,
// so a second write of the same id must replace rather than duplicate.
func TestJocksUpsertUpdatesInPlace(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()

	j := sampleJock()
	if err := s.UpsertJock(ctx, j); err != nil {
		t.Fatal(err)
	}
	first, err := s.GetJock(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}

	j.Personality = "Rewritten by the operator."
	j.GoodForGenres = []string{"rock"}
	if err := s.UpsertJock(ctx, j); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListJocks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("an upsert duplicated the row: %d rows", len(list))
	}
	got, err := s.GetJock(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Personality != "Rewritten by the operator." {
		t.Errorf("Personality = %q", got.Personality)
	}
	if len(got.GoodForGenres) != 1 {
		t.Errorf("the list column was not replaced: %#v", got.GoodForGenres)
	}
	if got.UpdatedAt.Before(first.UpdatedAt) {
		t.Error("UpdatedAt went backwards")
	}
}

// TestJocksDeleteUnassignsStations: SET NULL, not CASCADE. A station losing its
// host is recoverable; a station vanishing because its DJ was deleted is not.
func TestJocksDeleteUnassignsStations(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()

	j := sampleJock()
	if err := s.UpsertJock(ctx, j); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO stations (id, name, genre, jock_id, created_at) VALUES (1, 'Rock', 'rock', ?, 1)`,
		j.ID); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteJock(ctx, j.ID); err != nil {
		t.Fatal(err)
	}

	var count int
	var jockID sql.NullString
	if err := s.DB().QueryRow(`SELECT count(*) FROM stations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("the station disappeared with its jock: %d stations", count)
	}
	if err := s.DB().QueryRow(`SELECT jock_id FROM stations WHERE id = 1`).Scan(&jockID); err != nil {
		t.Fatal(err)
	}
	if jockID.Valid {
		t.Errorf("jock_id = %q after the jock was deleted, want NULL", jockID.String)
	}

	if err := s.DeleteJock(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a missing jock = %v, want ErrNotFound", err)
	}
}

// TestJocksSurfaceDatabaseErrors: a swallowed failure here means the console
// tells an operator their edit saved when it did not.
func TestJocksSurfaceDatabaseErrors(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()
	if err := s.UpsertJock(ctx, sampleJock()); err != nil {
		t.Fatal(err)
	}

	s.DB().SetMaxOpenConns(1)
	if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertJock(ctx, sampleJock()); err == nil {
		t.Error("UpsertJock reported success against a read-only database")
	}
	if err := s.DeleteJock(ctx, "dutch_mahoney"); err == nil {
		t.Error("DeleteJock reported success against a read-only database")
	}
	if _, err := s.DB().Exec(`PRAGMA query_only = 0`); err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetJock(ctx, "dutch_mahoney"); err == nil {
		t.Error("GetJock reported success against a closed database")
	}
	if _, err := s.ListJocks(ctx); err == nil {
		t.Error("ListJocks reported success against a closed database")
	}
}

// TestJocksRejectCorruptListColumn: the list columns are JSON, and JSON that
// will not parse must be an error rather than a jock with no genres -- which
// would silently stop that jock matching any station.
func TestJocksRejectCorruptListColumn(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()

	if _, err := s.DB().Exec(`INSERT INTO jocks
		(id, name, voice_id, good_for_genres, good_for_moods, speech_style, personality, forbidden, updated_at)
		VALUES ('broken', 'B', 'v', 'not json', '[]', 's', 'p', '[]', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetJock(ctx, "broken"); err == nil {
		t.Error("a jock with an unparseable genre list was returned as valid")
	}
	if _, err := s.ListJocks(ctx); err == nil {
		t.Error("ListJocks silently dropped a corrupt row")
	}
}

// TestJocksListSurfacesAnUnscannableRow covers the per-row Scan failure, which
// is distinct from a corrupt JSON list: this is a column that will not read as
// its declared type at all.
//
// SQLite has type affinity rather than strict types, so a text value can sit in
// an INTEGER column and only fails when something reads it as a number. A
// swallowed Scan here would drop a jock from the console's list -- an operator
// looking at a roster that is quietly missing somebody.
func TestJocksListSurfacesAnUnscannableRow(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()

	if err := s.UpsertJock(ctx, sampleJock()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO jocks
		(id, name, voice_id, good_for_genres, good_for_moods, speech_style, personality, forbidden, updated_at)
		VALUES ('bad-time', 'B', 'v', '[]', '[]', 's', 'p', '[]', 'not-a-timestamp')`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ListJocks(ctx); err == nil {
		t.Error("a jock whose row cannot be scanned was silently dropped from the list")
	}
	if _, err := s.GetJock(ctx, "bad-time"); err == nil {
		t.Error("GetJock returned a jock from an unscannable row")
	}
}

// TestJocksNilListsBecomeEmptyNotNull.
//
// A jock with no declared genres is a real thing -- prosper_okonkwo ships with
// an empty forbidden list, and the console can clear any of them. Storing nil
// as JSON "null" instead of "[]" would make every reader handle both shapes,
// and json.Unmarshal of "null" into a []string leaves it nil, so the difference
// would propagate rather than being normalised once here.
func TestJocksNilListsBecomeEmptyNotNull(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()

	bare := Jock{ID: "bare", Name: "Bare", VoiceID: "kokoro:am_puck",
		SpeechStyle: "s", Personality: "p"} // all three lists nil
	if err := s.UpsertJock(ctx, bare); err != nil {
		t.Fatal(err)
	}

	var genres, moods, forbidden string
	if err := s.DB().QueryRow(
		`SELECT good_for_genres, good_for_moods, forbidden FROM jocks WHERE id = 'bare'`).
		Scan(&genres, &moods, &forbidden); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"good_for_genres": genres, "good_for_moods": moods, "forbidden": forbidden,
	} {
		if raw != "[]" {
			t.Errorf("%s stored as %q, want \"[]\"", name, raw)
		}
	}

	got, err := s.GetJock(ctx, "bare")
	if err != nil {
		t.Fatal(err)
	}
	if got.GoodForGenres == nil || len(got.GoodForGenres) != 0 {
		t.Errorf("GoodForGenres = %#v, want an empty slice", got.GoodForGenres)
	}
}

// TestJocksStationJock: the jock a station presents, which is what actually
// reaches the microphone. Read as ONE query, so there is one answer to every
// way a station has nobody to put on air.
func TestJocksStationJock(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()
	if err := s.UpsertJock(ctx, sampleJock()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		INSERT INTO stations (id, name, genre, jock_id, created_at) VALUES
			(1, 'Rock',  'rock', 'dutch_mahoney', 1),
			(2, 'Quiet', 'ambient', NULL, 1)`); err != nil {
		t.Fatal(err)
	}

	got, err := s.StationJock(ctx, 1)
	if err != nil {
		t.Fatalf("StationJock: %v", err)
	}
	if got.ID != "dutch_mahoney" || got.VoiceID != "kokoro:am_fenrir" {
		t.Errorf("StationJock = %q / %q, want the assigned jock and its voice", got.ID, got.VoiceID)
	}
	// The lists come back decoded, not as raw JSON: the persona is built from
	// this row and matches stations on them.
	if len(got.GoodForGenres) != 3 {
		t.Errorf("genres = %v, want them decoded", got.GoodForGenres)
	}

	// A station with no jock, and a station that does not exist, are the same
	// answer: there is nobody to put on air.
	if _, err := s.StationJock(ctx, 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("unassigned station = %v, want ErrNotFound", err)
	}
	if _, err := s.StationJock(ctx, 404); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing station = %v, want ErrNotFound", err)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StationJock(ctx, 1); err == nil {
		t.Error("StationJock reported success against a closed database")
	}
}
