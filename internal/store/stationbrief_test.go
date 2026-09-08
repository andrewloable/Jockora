// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// Jockora-g1t.1. A station is a tag list today. It becomes a DESCRIPTION -- the
// operator writes a sentence and the AI derives every parameter a song can be
// selected on. This is the schema half: the brief, the year range, the tempo
// range and the length range, plus a back-fill so the console has one shape to
// show and not two.

// briefSchemaVersion is the schema BEFORE this migration, written as the number
// it is.
//
// migrate_v2_test.go records why, in so many words: a fixture that said
// CurrentSchemaVersion-1 silently stopped meaning what it meant the moment
// another migration was added, so migration 6 never re-ran and its columns were
// simply missing from the test.
const briefSchemaVersion = 9

func TestStationBriefRoundTrip(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "brief.db"))
	ctx := context.Background()

	want := Station{
		Name: "NIGHT ROCK", Genre: "rock", Mood: "nocturnal", Enabled: true,
		Brief:        "Late-night rock for driving, nothing after 2005.",
		YearMin:      1975,
		YearMax:      2005,
		TempoMin:     90,
		TempoMax:     140,
		DurationMinS: 120,
		DurationMaxS: 420,
	}
	id, err := s.CreateStation(ctx, want)
	if err != nil {
		t.Fatalf("CreateStation: %v", err)
	}

	got, err := s.GetStation(ctx, id)
	if err != nil {
		t.Fatalf("GetStation: %v", err)
	}
	for _, c := range []struct {
		field    string
		got, exp any
	}{
		{"Brief", got.Brief, want.Brief},
		{"YearMin", got.YearMin, want.YearMin},
		{"YearMax", got.YearMax, want.YearMax},
		{"TempoMin", got.TempoMin, want.TempoMin},
		{"TempoMax", got.TempoMax, want.TempoMax},
		{"DurationMinS", got.DurationMinS, want.DurationMinS},
		{"DurationMaxS", got.DurationMaxS, want.DurationMaxS},
	} {
		if c.got != c.exp {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.exp)
		}
	}

	// And through the list, which reads the same column set by a different path.
	list, err := s.ListStations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Brief != want.Brief || list[0].TempoMax != want.TempoMax {
		t.Errorf("ListStations lost the new columns: %+v", list)
	}
}

// TestStationBriefEmptyIsNull: ZERO MEANS UNSET and is written as NULL, so "no
// upper bound" and "the year 0" cannot be confused. It matters more for tempo
// and length than for years, because 0 BPM and 0 seconds are values SQL would
// happily compare a track against.
func TestStationBriefEmptyIsNull(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "null.db"))
	ctx := context.Background()

	id, err := s.CreateStation(ctx, Station{Name: "PLAIN", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatalf("CreateStation: %v", err)
	}

	got, err := s.GetStation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Brief != "" || got.YearMin != 0 || got.YearMax != 0 ||
		got.TempoMin != 0 || got.TempoMax != 0 || got.DurationMinS != 0 || got.DurationMaxS != 0 {
		t.Errorf("unset columns did not read back as zero: %+v", got)
	}

	// THE DATABASE, not the struct: the point is that these are NULL and not
	// an empty string or a literal 0 that a WHERE clause would match on.
	for _, col := range []string{"brief", "year_min", "year_max",
		"tempo_min", "tempo_max", "duration_min_s", "duration_max_s"} {
		var v any
		row := s.DB().QueryRowContext(ctx, `SELECT `+col+` FROM stations WHERE id = ?`, id)
		if err := row.Scan(&v); err != nil {
			t.Fatalf("reading %s: %v", col, err)
		}
		if v != nil {
			t.Errorf("%s stored %#v, want NULL", col, v)
		}
	}
}

func TestStationBriefUpdate(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "update.db"))
	ctx := context.Background()

	id, err := s.CreateStation(ctx, Station{Name: "N", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.GetStation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	st.Brief = "Slow, sad, mostly the nineties."
	st.YearMin, st.YearMax = 1990, 1999
	st.TempoMin, st.TempoMax = 50, 95
	st.DurationMinS, st.DurationMaxS = 90, 600
	if err := s.UpdateStation(ctx, st); err != nil {
		t.Fatalf("UpdateStation: %v", err)
	}
	got, err := s.GetStation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Brief != st.Brief || got.YearMax != 1999 || got.TempoMin != 50 || got.DurationMaxS != 600 {
		t.Errorf("update did not stick: %+v", got)
	}

	// CLEARING BACK TO UNSET has to reach NULL, or a range an operator removed
	// would go on filtering the playlist for ever.
	got.Brief, got.YearMin, got.YearMax = "", 0, 0
	got.TempoMin, got.TempoMax, got.DurationMinS, got.DurationMaxS = 0, 0, 0, 0
	if err := s.UpdateStation(ctx, got); err != nil {
		t.Fatal(err)
	}
	var brief sql.NullString
	var tempoMin sql.NullFloat64
	row := s.DB().QueryRowContext(ctx, `SELECT brief, tempo_min FROM stations WHERE id = ?`, id)
	if err := row.Scan(&brief, &tempoMin); err != nil {
		t.Fatal(err)
	}
	if brief.Valid || tempoMin.Valid {
		t.Errorf("cleared values did not reach NULL: brief=%v tempo_min=%v", brief, tempoMin)
	}
}

// TestStationBriefBackfill: every station that already exists gets a brief
// written from the tags it already has, so the console has one shape to show.
func TestStationBriefBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backfill.db")
	old := openAt(t, path)
	ctx := context.Background()

	for _, st := range []Station{
		{Name: "NIGHT ROCK", Genre: "rock,punk", Mood: "aggressive,raw", Enabled: true},
		{Name: "AMBIENT", Genre: "ambient", Enabled: true},
		{Name: "EVERYTHING ELSE", Genre: "unsorted", Enabled: true},
	} {
		if _, err := old.CreateStation(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	// Undo everything above version 9 from the ONE list every fixture in this
	// package shares, so the next migration does not break this test the way
	// migrations 11 and 12 each did in turn.
	rewindTo(t, old, briefSchemaVersion)

	old.Close() //nolint:errcheck // reopening to migrate

	up := openAt(t, path)
	// THE WHOLE CHAIN RAN, not only migration 10. A fixture that rewinds too
	// far and a migration that silently no-ops look identical from here
	// otherwise.
	var version int
	if err := up.DB().QueryRowContext(ctx, `SELECT version FROM schema_version`).
		Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != CurrentSchemaVersion {
		t.Fatalf("schema is at version %d after migrating, want %d", version, CurrentSchemaVersion)
	}

	list, err := up.ListStations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("%d stations survived the migration, want 3", len(list))
	}

	// ENGLISH, not a tag dump, and the stored commas spaced out.
	want := []string{
		"Plays rock, punk. Feels aggressive, raw.",
		"Plays ambient.",
		"Plays unsorted.",
	}
	for i, st := range list {
		if st.Brief != want[i] {
			t.Errorf("station %q brief = %q, want %q", st.Name, st.Brief, want[i])
		}
		// NOT BACK-FILLED: no station has ever expressed a year, a tempo or a
		// length, and inventing one would silently shrink a playlist that works
		// today. Five of sixteen moods already imply a tempo through TempoRange,
		// and an explicit range would override that implication.
		if st.YearMin != 0 || st.YearMax != 0 || st.TempoMin != 0 ||
			st.TempoMax != 0 || st.DurationMinS != 0 || st.DurationMaxS != 0 {
			t.Errorf("station %q had a range invented for it: %+v", st.Name, st)
		}
		// And nothing else on the row moved.
		if !st.Enabled {
			t.Errorf("station %q was disabled by the migration", st.Name)
		}
	}
	if list[0].Genre != "rock,punk" || list[0].Mood != "aggressive,raw" {
		t.Errorf("the migration altered the tags: %+v", list[0])
	}
}

// TestStationBriefSeeded: seed.go is the other writer of this table and it runs
// on a FRESH install, where the back-fill has nothing to back-fill. Without
// this the very first dial an operator ever sees is the one dial with empty
// briefs, which is exactly the two-shapes problem the back-fill removes.
func TestStationBriefSeeded(t *testing.T) {
	if got := BriefFor("rock", ""); got != "Plays rock." {
		t.Errorf("BriefFor(rock) = %q", got)
	}
	if got := BriefFor("rock,punk", "aggressive,raw"); got != "Plays rock, punk. Feels aggressive, raw." {
		t.Errorf("BriefFor with a mood = %q", got)
	}
	// A blank genre is not a sentence fragment; it is nothing.
	if got := BriefFor("", ""); got != "" {
		t.Errorf("BriefFor(\"\") = %q, want empty", got)
	}
	// Whitespace around the stored commas does not double up.
	if got := BriefFor("rock, punk", " raw "); got != "Plays rock, punk. Feels raw." {
		t.Errorf("BriefFor with spaced tags = %q", got)
	}
}

// TestStationBriefSeededStations proves the seed path actually writes it, not
// merely that the helper exists: seed.go is the other writer of this table and
// it is the one that runs where the back-fill cannot.
func TestStationBriefSeededStations(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "seeded.db"))
	ctx := context.Background()

	id, err := s.CreateStation(ctx, Station{
		Name: "ROCK", Genre: "rock", Brief: BriefFor("rock", ""), Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetStation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// IDENTICAL to what a back-filled station of the same tag reads, which is
	// the whole point of sharing the helper.
	if got.Brief != "Plays rock." {
		t.Errorf("a seeded station's brief = %q, want the back-filled sentence", got.Brief)
	}
}

// TestStationBriefBackfillRollsBack: the back-fill runs INSIDE the migration's
// transaction, so a failure leaves the schema exactly as it was rather than
// half-applied -- a database with the columns and no briefs, migrated to
// version 10 and never coming back for them.
func TestStationBriefBackfillRollsBack(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "rollback.db"))
	ctx := context.Background()

	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // the point of the test

	// A migration index that is not 10 does nothing at all.
	if err := backfill(ctx, tx, 3); err != nil {
		t.Errorf("backfill on an unrelated migration: %v", err)
	}

	// And with no stations table to read, it reports rather than panicking.
	if _, err := tx.ExecContext(ctx, `DROP TABLE station_tracks`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE stations`); err != nil {
		t.Fatal(err)
	}
	err = backfill(ctx, tx, 9)
	if err == nil {
		t.Fatal("backfill succeeded with no stations table")
	}
	if !strings.Contains(err.Error(), "migration 10") {
		t.Errorf("error = %v, want it to name the migration", err)
	}
}

// TestStationBriefBackfillSkipsATaglessStation: a station with no genre has no
// sentence to write, and writing "Plays ." would be worse than leaving it NULL.
func TestStationBriefBackfillSkipsATaglessStation(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "tagless.db"))
	ctx := context.Background()

	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO stations (name, genre, enabled, created_at) VALUES ('X', '', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only assertion below
	if err := backfill(ctx, tx, 9); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var brief sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT brief FROM stations WHERE name = 'X'`).
		Scan(&brief); err != nil {
		t.Fatal(err)
	}
	if brief.Valid {
		t.Errorf("a tagless station was given the brief %q", brief.String)
	}
}

// TestStationBriefBackfillOnAWrongSchema: the two ways a hand-edited or
// corrupted database breaks the back-fill. Neither should panic, and both
// should name the migration so the operator knows which upgrade stopped.
func TestStationBriefBackfillOnAWrongSchema(t *testing.T) {
	ctx := context.Background()

	// A stations.id that is not a number. Scan refuses it rather than reading
	// a zero and back-filling the wrong row.
	t.Run("id is not an integer", func(t *testing.T) {
		s := openAt(t, filepath.Join(t.TempDir(), "badid.db"))
		tx, err := s.DB().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck // the point of the test
		for _, q := range []string{
			`DROP TABLE station_tracks`,
			`DROP TABLE stations`,
			`CREATE TABLE stations (id TEXT, genre TEXT, mood TEXT, brief TEXT)`,
			`INSERT INTO stations (id, genre) VALUES ('not-a-number', 'rock')`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		err = backfill(ctx, tx, 9)
		if err == nil || !strings.Contains(err.Error(), "migration 10") {
			t.Errorf("err = %v, want a migration 10 failure", err)
		}
	})

	// A stations table that refuses the write. The rows read fine and the
	// UPDATE is rejected, which is the half that runs after the cursor closes.
	t.Run("the update is refused", func(t *testing.T) {
		s := openAt(t, filepath.Join(t.TempDir(), "noupdate.db"))
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO stations (name, genre, enabled, created_at) VALUES ('R', 'rock', 1, 0)`); err != nil {
			t.Fatal(err)
		}
		tx, err := s.DB().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck // the point of the test
		if _, err := tx.ExecContext(ctx, `
			CREATE TRIGGER no_brief BEFORE UPDATE ON stations
			BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
			t.Fatal(err)
		}
		err = backfill(ctx, tx, 9)
		if err == nil || !strings.Contains(err.Error(), "migration 10") {
			t.Errorf("err = %v, want a migration 10 failure naming the station", err)
		}
	})
}
