// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CurrentSchemaVersion is the schema this build writes.
//
// It exists from the very first migration, not from the first time the schema
// changes. Retrofitting versioning after dossiers exist means either discarding
// hours of enrichment or hand-writing a recovery script.
const CurrentSchemaVersion = 4

// migrations are applied in order; index i brings the schema to version i+1.
var migrations = []string{
	// 1: the initial shape.
	`
	CREATE TABLE schema_version (version INTEGER NOT NULL);

	CREATE TABLE tracks (
		id INTEGER PRIMARY KEY,
		path TEXT UNIQUE NOT NULL,
		artist TEXT,
		title TEXT,
		album TEXT,
		year INTEGER,
		duration_s REAL,
		playable INTEGER NOT NULL DEFAULT 1,
		loudness_lufs REAL,
		no_crossfade_next INTEGER NOT NULL DEFAULT 0,
		ramp_s REAL,
		outro_s REAL,
		ramp_confidence TEXT,
		bpm REAL,
		scanned_at INTEGER
	);

	CREATE TABLE dossiers (
		track_id INTEGER PRIMARY KEY REFERENCES tracks(id),
		json TEXT NOT NULL,
		confidence TEXT NOT NULL,
		created_at INTEGER
	);

	CREATE TABLE said_lines (
		id INTEGER PRIMARY KEY,
		jock_id TEXT NOT NULL,
		text TEXT NOT NULL,
		opening_norm TEXT NOT NULL,
		ngrams TEXT NOT NULL,
		aired_at INTEGER NOT NULL
	);

	CREATE TABLE ads (
		id INTEGER PRIMARY KEY,
		jock_id TEXT,
		text TEXT NOT NULL,
		wav_path TEXT,
		last_aired_at INTEGER
	);

	CREATE INDEX idx_tracks_playable ON tracks(playable);
	CREATE INDEX idx_said_jock_time ON said_lines(jock_id, aired_at);
	CREATE INDEX idx_ads_last_aired ON ads(last_aired_at);
	`,

	// 2: the single-instance lock for the enrichment worker.
	//
	// Two workers would double the LLM spend and the MusicBrainz quota, and the
	// second would be silently throttled into uselessness. The CHECK pins the
	// table to exactly one row, so "the lock" cannot accidentally become "a
	// lock".
	`
	CREATE TABLE enrich_lock (
		id        INTEGER PRIMARY KEY CHECK (id = 1),
		owner     TEXT    NOT NULL,
		heartbeat INTEGER NOT NULL
	);
	`,

	// 3: what each track's enrichment actually cost.
	//
	// Kept per track rather than as a running average so the sample can be
	// re-examined: a projection built from an average nobody can audit is a
	// number people stop believing the first time it is wrong.
	`
	CREATE TABLE enrich_cost (
		track_id    INTEGER PRIMARY KEY REFERENCES tracks(id),
		tokens      INTEGER NOT NULL,
		wall_seconds REAL   NOT NULL
	);
	`,

	// 4: enough to tell whether a file has changed since it was scanned.
	//
	// Startup blocks on the scan, and the scan ffprobed every file every time.
	// Measured on a 7,696-track library over a network mount: eight minutes of
	// silence before the first note, on EVERY restart, scaling linearly with
	// the library. Size and mtime answer "has this changed" without opening
	// the file.
	//
	// NULL means "scanned before this column existed", which re-probes once
	// and then stops. That is the whole upgrade path.
	`
	ALTER TABLE tracks ADD COLUMN size_bytes INTEGER;
	ALTER TABLE tracks ADD COLUMN modified_at INTEGER;
	`,
}

// migrate brings the database up to CurrentSchemaVersion.
func (s *Store) migrate(ctx context.Context) error {
	have, err := s.readVersion(ctx)
	if err != nil {
		return err
	}

	if have > CurrentSchemaVersion {
		// Refuse before touching anything. A newer Jockora may have reshaped
		// these tables, and writing into a shape this build does not understand
		// is how a library's enrichment gets silently corrupted.
		// The message names the fix, not just the problem. An operator meeting
		// this has already lost a stream and needs to know that the answer is
		// to upgrade the binary, NOT to delete the database -- which is what
		// people do to an error that only says "incompatible", and which throws
		// away every dossier the library ever built.
		return fmt.Errorf("%w: found version %d, this build understands %d. "+
			"Upgrade jockora to a build that understands version %d; do not delete the database, "+
			"it holds every dossier already enriched",
			ErrSchemaTooNew, have, CurrentSchemaVersion, have)
	}
	if have == CurrentSchemaVersion {
		return nil
	}

	for v := have; v < CurrentSchemaVersion; v++ {
		if err := s.applyMigration(ctx, v); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs migration index v, taking the schema from v to v+1, in one
// transaction so a failure leaves no half-migrated database behind.
func (s *Store) applyMigration(ctx context.Context, v int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: migration %d: %w", v+1, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
		return fmt.Errorf("store: migration %d: %w", v+1, err)
	}

	if v == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, v+1); err != nil {
			return fmt.Errorf("store: migration %d: recording version: %w", v+1, err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE schema_version SET version = ?`, v+1); err != nil {
			return fmt.Errorf("store: migration %d: recording version: %w", v+1, err)
		}
	}

	return tx.Commit()
}

// readVersion returns the recorded schema version, or 0 for a database that has
// never been migrated.
//
// Only a missing schema_version table counts as "fresh". Every other failure is
// returned: a corrupt or unreadable file must not be mistaken for a new one and
// then migrated over.
func (s *Store) readVersion(ctx context.Context) (int, error) {
	var exists int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'`,
	).Scan(&exists)
	if err != nil {
		return 0, fmt.Errorf("store: inspecting database: %w", err)
	}
	if exists == 0 {
		return 0, nil // never migrated
	}

	var v int
	err = s.db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		// The table is there but empty. Treat it as unmigrated rather than
		// guessing a version.
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: reading schema version: %w", err)
	}
	return v, nil
}
