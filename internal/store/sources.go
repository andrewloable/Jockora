// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Source kinds. The set is closed by a CHECK constraint in the schema, so a
// typo is rejected by the database rather than becoming a source nothing can
// scan -- whose only symptom would be a library that never appears.
const (
	SourceFolder   = "folder"
	SourceSubsonic = "subsonic"
)

// Source is one place music is read from. READ-ONLY, always: Jockora layers on
// a library it does not own.
type Source struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Locator  string `json:"locator"` // a folder path, or an OpenSubsonic base URL
	Username string `json:"username,omitempty"`
	// Password cannot be hashed -- OpenSubsonic re-derives a salted token from
	// it on every request, so the server has to keep the original. Never
	// serialised: the console renders this struct, and a password in a JSON
	// body is a password in a log, a proxy cache and a browser devtools pane.
	Password  string    `json:"-"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

const sourceColumns = `id, kind, locator, username, password, enabled, created_at`

// CreateSource adds a library source.
func (s *Store) CreateSource(ctx context.Context, src Source) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO sources (kind, locator, username, password, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		src.Kind, src.Locator, nullable(src.Username), nullable(src.Password),
		src.Enabled, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("store: creating %s source %q: %w", src.Kind, src.Locator, err)
	}
	return idOf(res, src.Locator)
}

// GetSource reads one source. Needed at PLAY TIME to turn a track locator back
// into a signed stream URL, which is why the credential lives here and not in
// the tracks table.
func (s *Store) GetSource(ctx context.Context, id int64) (Source, error) {
	rows, err := s.listSources(ctx, `WHERE id = ?`, id)
	if err != nil {
		return Source{}, err
	}
	if len(rows) == 0 {
		return Source{}, ErrNotFound
	}
	return rows[0], nil
}

// ListSources returns every source, disabled ones included -- the console has
// to show a disabled source or the operator cannot turn it back on.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	return s.listSources(ctx, "")
}

// ListEnabledSources returns the sources a scan should actually read.
func (s *Store) ListEnabledSources(ctx context.Context) ([]Source, error) {
	return s.listSources(ctx, `WHERE enabled = 1`)
}

func (s *Store) listSources(ctx context.Context, where string, args ...any) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sourceColumns+` FROM sources `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: listing sources: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []Source{}
	for rows.Next() {
		var src Source
		var user, pass sql.NullString
		var created int64
		if err := rows.Scan(&src.ID, &src.Kind, &src.Locator, &user, &pass,
			&src.Enabled, &created); err != nil {
			return nil, fmt.Errorf("store: listing sources: %w", err)
		}
		src.Username, src.Password = user.String, pass.String
		src.CreatedAt = time.Unix(created, 0)
		out = append(out, src)
	}
	return out, wrapErr(rows.Err(), "listing sources")
}

// SetSourceEnabled turns a source on or off without losing it. Disabling is not
// deleting: the tracks it contributed stay in the library rather than
// disappearing from every station that was playing them.
func (s *Store) SetSourceEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sources SET enabled = ? WHERE id = ?`, enabled, id)
	if err != nil {
		return fmt.Errorf("store: enabling source %d: %w", id, err)
	}
	return requireOneRow(res, id)
}

// DeleteSource removes a source.
func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: deleting source %d: %w", id, err)
	}
	return requireOneRow(res, id)
}

// RetireSourceTracks flags every track of a source as missing.
//
// Used when a source is removed. The rows are MARKED, NEVER DELETED, exactly as
// a vanished file is: they carry dossiers worth minutes of model time each and
// are referenced by said_lines, and re-adding the source later should find them
// waiting rather than have to enrich them all again.
func (s *Store) RetireSourceTracks(ctx context.Context, sourceID int64) (int, error) {
	var n int
	const unretired = `source_id = ? AND missing_at IS NULL`
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM tracks WHERE `+unretired, sourceID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting tracks of source %d: %w", sourceID, err)
	}
	if n == 0 {
		return 0, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tracks SET missing_at = ? WHERE `+unretired,
		time.Now().Unix(), sourceID); err != nil {
		return 0, fmt.Errorf("store: retiring tracks of source %d: %w", sourceID, err)
	}
	return n, nil
}
