// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/andrewloable/jockora/internal/store"
)

// MarkMissing flags the tracks of one source that a completed walk did not see.
//
// MARKED, NEVER DELETED. A row carries a dossier that cost minutes of model
// time and is referenced by said_lines; deleting it would throw away the first
// and orphan the second, to save a row. Marking is reversible, deletion is not.
//
// Scoped to ONE SOURCE and to a walk that FINISHED. A scan that dies halfway
// has seen part of the library, and marking then would take everything it never
// reached off the air.
func MarkMissing(ctx context.Context, s *store.Store, sourceID, walkStamp int64) (int, error) {
	const unseen = `source_id = ? AND missing_at IS NULL
	                AND (scanned_at IS NULL OR scanned_at < ?)`

	// Counted rather than read back from RowsAffected, so the healthy case --
	// a complete library, where the count is zero -- does no write at all.
	var n int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM tracks WHERE `+unseen, sourceID, walkStamp).Scan(&n); err != nil {
		return 0, fmt.Errorf("library: counting missing tracks of source %d: %w", sourceID, err)
	}
	if n == 0 {
		return 0, nil
	}

	if _, err := s.DB().ExecContext(ctx,
		`UPDATE tracks SET missing_at = ? WHERE `+unseen,
		time.Now().Unix(), sourceID, walkStamp); err != nil {
		return 0, fmt.Errorf("library: marking missing tracks of source %d: %w", sourceID, err)
	}
	return n, nil
}

// touch records that a file is still there without re-reading it.
//
// The scanner SKIPS unchanged files -- ffprobe is the whole cost of a scan --
// and a skipped file never reaches upsert. Without this, every unchanged track
// would look unseen to MarkMissing and a healthy library would go dark on its
// second scan. It also clears the mark, which is what a remounted drive needs:
// the files come back byte-identical and are all skipped.
func touch(ctx context.Context, s *store.Store, path string, stamp int64) error {
	_, err := s.DB().ExecContext(ctx,
		`UPDATE tracks SET scanned_at = ?, missing_at = NULL WHERE path = ?`,
		stamp, path)
	if err != nil {
		return fmt.Errorf("library: touching %s: %w", path, err)
	}
	return nil
}

// scanStamp is the value one walk marks every row it sees with.
//
// Normally the clock. But scanned_at counts SECONDS, and a rescan started
// inside the same second as the last one would stamp rows with a value that is
// not less than the walk's own -- so nothing would ever look unseen and a
// deleted file would never be marked. Taking one past the newest existing stamp
// keeps the comparison strict without giving up a column an operator can read
// as a time.
func scanStamp(ctx context.Context, s *store.Store, sourceID int64) (int64, error) {
	var newest sql.NullInt64
	err := s.DB().QueryRowContext(ctx,
		`SELECT max(scanned_at) FROM tracks WHERE source_id = ?`, sourceID).Scan(&newest)
	if err != nil {
		return 0, fmt.Errorf("library: reading the last scan of source %d: %w", sourceID, err)
	}
	now := time.Now().Unix()
	if newest.Valid && newest.Int64 >= now {
		return newest.Int64 + 1, nil
	}
	return now, nil
}
