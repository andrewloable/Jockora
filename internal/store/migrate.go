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
const CurrentSchemaVersion = 9

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

	// 5: what a listener thought of a break.
	//
	// THE ONLY SIGNAL THE WRITING EVER GETS FROM A REAL EAR. Everything else
	// judging break quality in this project is a machine checking rules it was
	// given; a thumbs-down is a person saying that one was bad, which is the
	// input the rubric explicitly cannot supply.
	//
	// The TEXT is stored, not a break id, because breaks are not rows: they are
	// written, aired and gone. What a later prompt-tuning pass needs is the
	// sentence somebody disliked.
	`
	CREATE TABLE break_feedback (
		id       INTEGER PRIMARY KEY,
		jock_id  TEXT,
		text     TEXT NOT NULL,
		verdict  TEXT NOT NULL,
		aired_at INTEGER,
		at       INTEGER NOT NULL
	);
	CREATE INDEX idx_feedback_verdict ON break_feedback(verdict, at);
	`,

	// 6: v0.2 -- accounts, sources, jocks, stations and playlists.
	//
	// ADDITIVE ONLY. Nothing existing is renamed or dropped: a dossier costs
	// minutes of model time and there are thousands of them, said_lines
	// reference tracks, and a lost column is a re-enrichment rather than an
	// inconvenience. Every change here is a CREATE or an ADD COLUMN.
	//
	// NO SEED ROWS. Seeding jocks from personas/, stations from the proposed
	// dial and sources from the existing flags is CONDITIONAL -- it happens
	// only when a table is empty -- and a migration runs exactly once whether
	// or not the condition holds. Seeding lives in the stores.
	`
	CREATE TABLE users (
		id         INTEGER PRIMARY KEY,
		name       TEXT UNIQUE NOT NULL,
		pw_hash    TEXT NOT NULL,
		-- Enforced by the DATABASE rather than by whatever code inserts. Two
		-- roles is the whole vocabulary; a third would be a design change.
		role       TEXT NOT NULL CHECK(role IN ('admin','listener')),
		disabled   INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);

	-- A source is somewhere music comes from. password is stored AS GIVEN:
	-- there is no secret encryption here and pretending otherwise would be
	-- worse than saying so. It is the same trust boundary as the config file
	-- the credential arrives in.
	CREATE TABLE sources (
		id         INTEGER PRIMARY KEY,
		kind       TEXT NOT NULL CHECK(kind IN ('folder','subsonic')),
		locator    TEXT NOT NULL,
		username   TEXT,
		password   TEXT,
		enabled    INTEGER NOT NULL DEFAULT 1,
		created_at INTEGER NOT NULL
	);

	-- A jock is a persona card in the database rather than only on disk, so an
	-- operator can edit one without shell access. good_for_genres,
	-- good_for_moods and forbidden are JSON arrays.
	CREATE TABLE jocks (
		id              TEXT PRIMARY KEY,
		name            TEXT NOT NULL,
		voice_id        TEXT NOT NULL,
		good_for_genres TEXT NOT NULL,
		good_for_moods  TEXT NOT NULL,
		speech_style    TEXT NOT NULL,
		personality     TEXT NOT NULL,
		forbidden       TEXT NOT NULL,
		updated_at      INTEGER NOT NULL
	);

	-- ON DELETE SET NULL, not CASCADE: deleting a jock must not delete the
	-- stations that happened to be using it. They carry on jockless until one
	-- is assigned.
	CREATE TABLE stations (
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		genre      TEXT NOT NULL,
		mood       TEXT,
		jock_id    TEXT REFERENCES jocks(id) ON DELETE SET NULL,
		enabled    INTEGER NOT NULL DEFAULT 1,
		sel_seed   INTEGER,
		sel_cursor INTEGER,
		created_at INTEGER NOT NULL
	);

	-- The materialized playlist. ON DELETE CASCADE from stations so a deleted
	-- station takes its playlist with it -- and deliberately NOT from tracks,
	-- because a cascade that reached the library would delete the music.
	CREATE TABLE station_tracks (
		station_id INTEGER NOT NULL REFERENCES stations(id) ON DELETE CASCADE,
		track_id   INTEGER NOT NULL REFERENCES tracks(id),
		pinned     INTEGER NOT NULL DEFAULT 0,
		excluded   INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(station_id, track_id)
	);

	ALTER TABLE tracks ADD COLUMN source_id INTEGER;
	-- missing_at marks a track the last scan did not see. NOT a delete: the
	-- dossier stays, so a source that comes back does not cost a re-enrichment.
	ALTER TABLE tracks ADD COLUMN missing_at INTEGER;
	ALTER TABLE ads ADD COLUMN station_id INTEGER;
	ALTER TABLE break_feedback ADD COLUMN user_id INTEGER;

	CREATE INDEX idx_station_tracks_track ON station_tracks(track_id);
	`,

	// 7: server settings that must outlive a restart.
	//
	// Small and deliberately generic: the first thing in it is the session
	// signing key, and a key generated fresh on every start would log every
	// listener out whenever the operator restarted the server.
	`
	CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	`,

	// 8: the file's own genre tag, kept rather than discarded.
	//
	// The scanner always read it and threw it away, because a station's genre
	// comes from the DOSSIER, not from whatever somebody typed into an ID3 tag
	// years ago. That is still true for deciding what a station contains.
	//
	// But it makes enrichment ORDER possible, which is the point. A station's
	// tracks are selected by dossier, so before enrichment there is nothing
	// station-scoped to enrich -- the one pre-enrichment signal about what a
	// track might be is this tag. Enriching tag-matching tracks first turns
	// "your rock station is usable in four days" into minutes, and costs
	// nothing when the tag is absent or wrong: it is a hint about ORDER, never
	// about membership.
	`
	-- No index. Enrichment picks ONE track every thirty to forty seconds, and
	-- the query that finds it already scans for tracks without a dossier, so
	-- an index on genre would buy nothing measurable and would have to be
	-- dropped before the column on any future schema change.
	ALTER TABLE tracks ADD COLUMN genre TEXT;
	`,

	// 9: an operator's own tags for a track, and ONE definition of what a
	// track's tags are.
	//
	// A HAND EDIT IS NOT A DOSSIER EDIT. Genres and moods live inside the
	// dossier the enrichment wrote, and a dossier is rewritten whenever it is
	// missing -- which includes the requeue path an operator reaches for after
	// a model change. An edit written INTO the dossier would therefore survive
	// until the next re-enrichment and then vanish, which is worse than not
	// offering the edit at all.
	//
	// So the override is a row of its own that OUTRANKS the dossier, and
	// effective_tags is the only place the two are combined. Every reader --
	// the station filter, the playlist listing, the catch-all -- goes through
	// it, so a future reader cannot forget the override exists.
	`
	CREATE TABLE track_tags (
		track_id     INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
		station_tags TEXT NOT NULL,
		mood         TEXT NOT NULL,
		updated_at   INTEGER NOT NULL
	);

	-- overridden is carried because the CATCH-ALL needs it: a dossier that
	-- could assert nothing belongs in "unsorted", but a track an operator has
	-- since placed by hand does not, even though its dossier still says
	-- nothing.
	CREATE VIEW effective_tags AS
	SELECT t.id AS track_id,
	       coalesce(o.station_tags, json_extract(d.json, '$.station_tags'), '[]') AS station_tags,
	       coalesce(o.mood,         json_extract(d.json, '$.mood'),         '[]') AS mood,
	       coalesce(d.confidence, '') AS confidence,
	       o.track_id IS NOT NULL AS overridden
	  FROM tracks t
	  LEFT JOIN dossiers d   ON d.track_id = t.id
	  LEFT JOIN track_tags o ON o.track_id = t.id;
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
