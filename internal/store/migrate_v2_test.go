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

// v2Tables are the tables migration v2 must create.
// v01SchemaVersion is the schema a v0.1 database is left at.
const v01SchemaVersion = 5

var v2Tables = []string{"users", "sources", "jocks", "stations", "station_tracks", "settings", "track_tags"}

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateV2CreatesTables(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "v2.db"))

	for _, name := range v2Tables {
		var got string
		err := s.DB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
		if err != nil {
			t.Errorf("table %q missing: %v", name, err)
		}
	}
	for _, col := range []struct{ table, column string }{
		{"tracks", "source_id"}, {"tracks", "missing_at"}, {"tracks", "genre"},
		{"ads", "station_id"}, {"break_feedback", "user_id"},
	} {
		if !hasColumn(t, s, col.table, col.column) {
			t.Errorf("%s.%s missing", col.table, col.column)
		}
	}
	var idx string
	if err := s.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_station_tracks_track'`).
		Scan(&idx); err != nil {
		t.Errorf("idx_station_tracks_track missing: %v", err)
	}
}

func hasColumn(t *testing.T, s *Store, table, column string) bool {
	t.Helper()
	rows, err := s.DB().Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck // read-only
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	return false
}

// TestMigrateV2Idempotent: Open runs migrations, so opening the same file twice
// must be a no-op rather than a second application.
func TestMigrateV2Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.db")

	first := openAt(t, path)
	var before int
	if err := first.DB().QueryRow(`SELECT version FROM schema_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	first.Close() //nolint:errcheck // reopening below

	second := openAt(t, path)
	var after int
	if err := second.DB().QueryRow(`SELECT version FROM schema_version`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("schema_version %d became %d on reopen", before, after)
	}
}

// TestMigrateV2UpgradesV01Database is the one that matters most.
//
// Dossiers cost hours of model time and said_lines reference tracks, so an
// upgrade that renamed or dropped a column would be a re-enrichment. Every
// existing value must survive byte-identical.
func TestMigrateV2UpgradesV01Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")

	// A database at the PREVIOUS schema version, with real rows in it.
	old := openAt(t, path)
	if _, err := old.DB().Exec(
		`INSERT INTO tracks (id, path, artist, title, album, year, duration_s, playable, loudness_lufs)
		 VALUES (1, '/m/a.mp3', 'Radiohead', 'Creep', 'Pablo Honey', 1993, 238.5, 1, -14.25)`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence) VALUES (1, '{"station_tags":["rock"]}', 'high')`); err != nil {
		t.Fatal(err)
	}
	// Make it a GENUINE v0.1 database rather than a v0.2 one wearing an old
	// version number. Dropping the v2 tables and then rewinding the version is
	// the only way to reach a state that could actually exist on disk; setting
	// the number alone produces a database no upgrade path ever creates, and
	// the migration then fails on "table users already exists" -- which is the
	// fixture being wrong, not the migration.
	for _, table := range v2Tables {
		if _, err := old.DB().Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.DB().Exec(`DROP INDEX IF EXISTS idx_station_tracks_track`); err != nil {
		t.Fatal(err)
	}
	// The VIEW too. A view is not a table, so the loop above does not reach it,
	// and a leftover one makes migration 9 fail on a fixture that is supposed
	// to predate it.
	if _, err := old.DB().Exec(`DROP VIEW IF EXISTS effective_tags`); err != nil {
		t.Fatal(err)
	}
	// The ADD COLUMNs survive a table drop, so they have to come off too.
	for _, c := range []struct{ table, column string }{
		{"tracks", "source_id"}, {"tracks", "missing_at"}, {"tracks", "genre"},
		{"ads", "station_id"}, {"break_feedback", "user_id"},
	} {
		if _, err := old.DB().Exec(`ALTER TABLE ` + c.table + ` DROP COLUMN ` + c.column); err != nil {
			t.Fatalf("rewinding %s.%s: %v", c.table, c.column, err)
		}
	}
	// The LAST v0.1 VERSION, written as the number it is. It used to be
	// CurrentSchemaVersion-1, which silently stopped meaning "before v0.2" the
	// moment a seventh migration was added: the fixture then rewound to 6, so
	// migration 6 never re-ran and the columns it adds were simply missing.
	if _, err := old.DB().Exec(`UPDATE schema_version SET version = ?`, v01SchemaVersion); err != nil {
		t.Fatal(err)
	}
	old.Close() //nolint:errcheck // reopening to migrate

	upgraded := openAt(t, path)

	var (
		p, artist, title, album string
		year                    int
		duration, lufs          float64
		playable                int
	)
	if err := upgraded.DB().QueryRow(
		`SELECT path, artist, title, album, year, duration_s, playable, loudness_lufs
		   FROM tracks WHERE id = 1`).
		Scan(&p, &artist, &title, &album, &year, &duration, &playable, &lufs); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ got, want any }{
		{p, "/m/a.mp3"}, {artist, "Radiohead"}, {title, "Creep"}, {album, "Pablo Honey"},
		{year, 1993}, {duration, 238.5}, {playable, 1}, {lufs, -14.25},
	} {
		if c.got != c.want {
			t.Errorf("existing value changed: got %v, want %v", c.got, c.want)
		}
	}

	var doc, confidence string
	if err := upgraded.DB().QueryRow(
		`SELECT json, confidence FROM dossiers WHERE track_id = 1`).Scan(&doc, &confidence); err != nil {
		t.Fatalf("the dossier did not survive the upgrade: %v", err)
	}
	if doc != `{"station_tags":["rock"]}` || confidence != "high" {
		t.Errorf("dossier changed: %q / %q", doc, confidence)
	}

	// The new columns exist and are NULL, not zero.
	var sourceID, missingAt sql.NullInt64
	if err := upgraded.DB().QueryRow(
		`SELECT source_id, missing_at FROM tracks WHERE id = 1`).Scan(&sourceID, &missingAt); err != nil {
		t.Fatal(err)
	}
	if sourceID.Valid || missingAt.Valid {
		t.Errorf("new columns are not NULL on an upgraded row: %v / %v", sourceID, missingAt)
	}
}

// TestMigrateV2RoleCheck: a role outside the vocabulary must be refused by the
// database, not merely by whatever code happens to insert.
func TestMigrateV2RoleCheck(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "role.db"))

	_, err := s.DB().Exec(
		`INSERT INTO users (name, pw_hash, role, created_at) VALUES ('x', 'h', 'dj', 1)`)
	if err == nil {
		t.Fatal("a user with role 'dj' was accepted")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "constraint") {
		t.Errorf("expected a constraint error, got %v", err)
	}

	if _, err := s.DB().Exec(
		`INSERT INTO users (name, pw_hash, role, created_at) VALUES ('a', 'h', 'admin', 1)`); err != nil {
		t.Errorf("a valid role was rejected: %v", err)
	}
}

// TestMigrateV2ForeignKeysOn: the cascade below is decorative unless this is on,
// and it has to be on for EVERY pooled connection, not just the first.
func TestMigrateV2ForeignKeysOn(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "fk.db"))

	var on int
	if err := s.DB().QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Errorf("PRAGMA foreign_keys = %d, want 1", on)
	}
}

// TestMigrateV2CascadeDeletesStationTracks: deleting a station takes its
// playlist with it and leaves the TRACKS alone. A cascade that reached the
// tracks would delete the library.
func TestMigrateV2CascadeDeletesStationTracks(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "cascade.db"))

	mustExec(t, s, `INSERT INTO tracks (id, path, playable) VALUES (1, '/m/a.mp3', 1)`)
	mustExec(t, s, `INSERT INTO tracks (id, path, playable) VALUES (2, '/m/b.mp3', 1)`)
	mustExec(t, s, `INSERT INTO stations (id, name, genre, created_at) VALUES (1, 'Rock', 'rock', 1)`)
	mustExec(t, s, `INSERT INTO station_tracks (station_id, track_id) VALUES (1, 1)`)
	mustExec(t, s, `INSERT INTO station_tracks (station_id, track_id) VALUES (1, 2)`)

	mustExec(t, s, `DELETE FROM stations WHERE id = 1`)

	var playlist, tracks int
	if err := s.DB().QueryRow(`SELECT count(*) FROM station_tracks`).Scan(&playlist); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM tracks`).Scan(&tracks); err != nil {
		t.Fatal(err)
	}
	if playlist != 0 {
		t.Errorf("%d station_tracks rows survived the station", playlist)
	}
	if tracks != 2 {
		t.Errorf("the cascade reached the library: %d tracks left of 2", tracks)
	}
}

func mustExec(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.DB().Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// TestMigrateV2RefusesToOrphanAPlaylistRow.
//
// station_tracks.track_id has NO ON DELETE clause, which with foreign keys on
// means deleting a track that sits on a playlist FAILS rather than silently
// removing it. That is the behaviour the schema asks for and it is worth
// pinning: the design never deletes a track anyway -- a track a scan stops
// seeing gets missing_at set, so its dossier survives a source coming back --
// so a delete that succeeds here would mean something has started deleting the
// library.
//
// Written because a falsification exposed the gap: adding ON DELETE CASCADE to
// track_id passed every test I had, so nothing was checking this direction.
func TestMigrateV2RefusesToOrphanAPlaylistRow(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "orphan.db"))

	mustExec(t, s, `INSERT INTO tracks (id, path, playable) VALUES (1, '/m/a.mp3', 1)`)
	mustExec(t, s, `INSERT INTO stations (id, name, genre, created_at) VALUES (1, 'Rock', 'rock', 1)`)
	mustExec(t, s, `INSERT INTO station_tracks (station_id, track_id) VALUES (1, 1)`)

	if _, err := s.DB().Exec(`DELETE FROM tracks WHERE id = 1`); err == nil {
		t.Fatal("a track on a playlist was deleted; the playlist row is now an orphan")
	}

	// And a track NOT on any playlist deletes normally, so the constraint is
	// not simply refusing everything.
	mustExec(t, s, `INSERT INTO tracks (id, path, playable) VALUES (2, '/m/b.mp3', 1)`)
	if _, err := s.DB().Exec(`DELETE FROM tracks WHERE id = 2`); err != nil {
		t.Errorf("an unreferenced track could not be deleted: %v", err)
	}
}

// TestMigrateV2JockDeleteLeavesTheStation: ON DELETE SET NULL, not CASCADE.
// Deleting a jock must not delete the stations that were using it -- they carry
// on jockless until one is assigned.
func TestMigrateV2JockDeleteLeavesTheStation(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "jock.db"))

	mustExec(t, s, `INSERT INTO jocks (id, name, voice_id, good_for_genres, good_for_moods,
		speech_style, personality, forbidden, updated_at)
		VALUES ('dutch', 'Dutch', 'kokoro:am_fenrir', '[]', '[]', 's', 'p', '[]', 1)`)
	mustExec(t, s, `INSERT INTO stations (id, name, genre, jock_id, created_at)
		VALUES (1, 'Rock', 'rock', 'dutch', 1)`)

	mustExec(t, s, `DELETE FROM jocks WHERE id = 'dutch'`)

	var count int
	var jockID sql.NullString
	if err := s.DB().QueryRow(`SELECT count(*) FROM stations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("%d stations survived deleting their jock, want 1", count)
	}
	if err := s.DB().QueryRow(`SELECT jock_id FROM stations WHERE id = 1`).Scan(&jockID); err != nil {
		t.Fatal(err)
	}
	if jockID.Valid {
		t.Errorf("jock_id = %q after the jock was deleted, want NULL", jockID.String)
	}
}
