// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "jockora.db")
}

func TestMigrateFreshDBSetsVersion(t *testing.T) {
	s, err := Open(context.Background(), tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != CurrentSchemaVersion {
		t.Errorf("schema_version = %d, want %d", v, CurrentSchemaVersion)
	}
	if CurrentSchemaVersion < 1 {
		t.Errorf("CurrentSchemaVersion = %d", CurrentSchemaVersion)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := tempDB(t)

	for i := 0; i < 3; i++ {
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		v, err := s.SchemaVersion(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if v != CurrentSchemaVersion {
			t.Errorf("after open %d, version = %d, want %d", i, v, CurrentSchemaVersion)
		}
		s.Close()
	}

	// And exactly one row, not one per open.
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM schema_version`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("schema_version holds %d rows, want exactly 1", n)
	}
}

// TestRefusesUnknownNewerVersion is the whole reason the version column exists.
// A newer Jockora may have reshaped the tables; an older binary writing into
// them would corrupt hours of enrichment.
func TestRefusesUnknownNewerVersion(t *testing.T) {
	path := tempDB(t)

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE schema_version SET version = ?`, 999); err != nil {
		t.Fatal(err)
	}
	// Leave a row behind so we can prove nothing touched it.
	if _, err := s.DB().Exec(
		`INSERT INTO tracks (path, artist, title) VALUES (?, ?, ?)`,
		"/music/canary.mp3", "Canary", "Do Not Touch"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	reopened, err := Open(context.Background(), path)
	if err == nil {
		reopened.Close()
		t.Fatal("Open accepted a database written by a newer Jockora")
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
	if !strings.Contains(err.Error(), "999") {
		t.Errorf("error %q should name the version it found", err)
	}

	// Nothing may have been altered. Verify with a plain driver connection.
	raw, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	var version int
	if err := raw.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 999 {
		t.Errorf("schema_version is now %d: the refused open rewrote it", version)
	}
	var artist string
	if err := raw.QueryRow(`SELECT artist FROM tracks WHERE path = ?`, "/music/canary.mp3").Scan(&artist); err != nil {
		t.Fatalf("the canary row was lost: %v", err)
	}
	if artist != "Canary" {
		t.Errorf("canary artist = %q, want Canary", artist)
	}
}

// TestSchemaMatchesTheSpecifiedShape guards the columns every later task writes
// to. A missing column here surfaces as a runtime error hours into an enrichment
// run.
func TestSchemaMatchesTheSpecifiedShape(t *testing.T) {
	s, err := Open(context.Background(), tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	want := map[string][]string{
		"tracks": {"id", "path", "artist", "title", "album", "year", "duration_s",
			"playable", "loudness_lufs", "no_crossfade_next", "ramp_s", "outro_s",
			"ramp_confidence", "bpm", "scanned_at"},
		"dossiers":    {"track_id", "json", "confidence", "created_at"},
		"said_lines":  {"id", "jock_id", "text", "opening_norm", "ngrams", "aired_at"},
		"ads":         {"id", "jock_id", "text", "wav_path", "last_aired_at"},
		"enrich_lock": {"id", "owner", "heartbeat"},
	}

	for table, columns := range want {
		got := map[string]bool{}
		rows, err := s.DB().Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			got[name] = true
		}
		rows.Close()

		if len(got) == 0 {
			t.Errorf("table %s does not exist", table)
			continue
		}
		for _, c := range columns {
			if !got[c] {
				t.Errorf("%s is missing column %s", table, c)
			}
		}
	}
}

func TestIndexesExist(t *testing.T) {
	s, err := Open(context.Background(), tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got := map[string]bool{}
	rows, err := s.DB().Query(`SELECT name FROM sqlite_master WHERE type = 'index'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got[name] = true
	}

	for _, idx := range []string{"idx_tracks_playable", "idx_said_jock_time", "idx_ads_last_aired"} {
		if !got[idx] {
			t.Errorf("index %s is missing", idx)
		}
	}
}

// TestTrackPathIsUnique: the scanner reruns constantly and must not duplicate.
func TestTrackPathIsUnique(t *testing.T) {
	s, err := Open(context.Background(), tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.DB().Exec(`INSERT INTO tracks (path) VALUES (?)`, "/music/a.mp3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO tracks (path) VALUES (?)`, "/music/a.mp3"); err == nil {
		t.Error("the same path was inserted twice; path must be UNIQUE")
	}
}

func TestDefaultsAreSet(t *testing.T) {
	s, err := Open(context.Background(), tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.DB().Exec(`INSERT INTO tracks (path) VALUES (?)`, "/music/a.mp3"); err != nil {
		t.Fatal(err)
	}
	var playable, noCrossfade int
	if err := s.DB().QueryRow(
		`SELECT playable, no_crossfade_next FROM tracks WHERE path = ?`, "/music/a.mp3",
	).Scan(&playable, &noCrossfade); err != nil {
		t.Fatal(err)
	}
	if playable != 1 {
		t.Errorf("playable defaults to %d, want 1: an unscanned track should be playable", playable)
	}
	if noCrossfade != 0 {
		t.Errorf("no_crossfade_next defaults to %d, want 0", noCrossfade)
	}
}

// TestForeignKeysAreEnforced: SQLite ignores REFERENCES unless foreign_keys is
// turned on per connection, which is a silent data-integrity hole.
func TestForeignKeysAreEnforced(t *testing.T) {
	s, err := Open(context.Background(), tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
		4242, "{}", "low")
	if err == nil {
		t.Error("a dossier was written for a track that does not exist; foreign keys are off")
	}
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "jockora.db")

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("database file was not created: %v", err)
	}
}

// TestNoQueryBuildsSQLWithSprintf is structural: every query must use ?
// placeholders. String-built SQL is how untrusted tag metadata becomes an
// injection, and this program reads tags from files it does not control.
func TestNoQueryBuildsSQLWithSprintf(t *testing.T) {
	for _, name := range []string{"store.go", "migrate.go"} {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"fmt.Sprintf(\"SELECT", "fmt.Sprintf(\"INSERT",
			"fmt.Sprintf(\"UPDATE", "fmt.Sprintf(\"DELETE", "fmt.Sprintf(`SELECT",
			"fmt.Sprintf(`INSERT", "fmt.Sprintf(`UPDATE", "fmt.Sprintf(`DELETE"} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s builds SQL with %s; every query must use ? placeholders", name, banned)
			}
		}
	}
}

// TestOpenRejectsACorruptFile: a file that is not a database must not be
// mistaken for a fresh one and migrated over.
func TestOpenRejectsACorruptFile(t *testing.T) {
	path := tempDB(t)
	if err := os.WriteFile(path, []byte("this is not a database, it is a text file"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), path)
	if err == nil {
		s.Close()
		t.Fatal("Open accepted a file that is not a database")
	}
	if errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("a corrupt file was reported as a newer schema: %v", err)
	}
}

// TestMigrationIsAtomic: a failed migration must leave no half-built schema.
func TestMigrationIsAtomic(t *testing.T) {
	// Migration 1 creates several tables. Pre-create one of them so the
	// migration fails partway, then assert nothing else survived.
	path := tempDB(t)
	raw, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE said_lines (bogus TEXT)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	if s, err := Open(context.Background(), path); err == nil {
		s.Close()
		t.Fatal("Open succeeded despite a conflicting table")
	}

	check, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var n int
	if err := check.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('tracks','dossiers','ads','schema_version')`,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d tables from the failed migration survived; it was not atomic", n)
	}
}

// TestMigrateStepsThroughEveryVersion: a database created at version 1 must be
// carried forward, not rebuilt. This is the path a real upgrade takes and it is
// the one that would silently discard hours of enrichment if it were wrong.
func TestMigrateStepsThroughEveryVersion(t *testing.T) {
	path := tempDB(t)

	// Build a version-1 database by hand, with a row worth preserving.
	raw, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_version (version) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO tracks (path, artist, loudness_lufs) VALUES (?, ?, ?)`,
		"/music/precious.mp3", "Expensive To Rebuild", -14.5); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("upgrading a v1 database: %v", err)
	}
	defer s.Close()

	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != CurrentSchemaVersion {
		t.Errorf("version = %d after upgrade, want %d", v, CurrentSchemaVersion)
	}

	var artist string
	var lufs float64
	if err := s.DB().QueryRow(`SELECT artist, loudness_lufs FROM tracks WHERE path = ?`,
		"/music/precious.mp3").Scan(&artist, &lufs); err != nil {
		t.Fatalf("the pre-existing row did not survive the upgrade: %v", err)
	}
	if artist != "Expensive To Rebuild" || lufs != -14.5 {
		t.Errorf("row was altered: artist=%q lufs=%v", artist, lufs)
	}

	// And the new table exists.
	if _, err := s.DB().Exec(`INSERT INTO enrich_lock (id, owner, heartbeat) VALUES (1, 'x', 0)`); err != nil {
		t.Errorf("the version-2 table is missing: %v", err)
	}
}

func TestEnrichLockHoldsOneRow(t *testing.T) {
	s, err := Open(context.Background(), tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.DB().Exec(`INSERT INTO enrich_lock (id, owner, heartbeat) VALUES (1, 'a', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO enrich_lock (id, owner, heartbeat) VALUES (2, 'b', 1)`); err == nil {
		t.Error("a second lock row was accepted; the lock must be a single row")
	}
}
