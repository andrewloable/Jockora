// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Warnings and errors that survive a restart.
//
// The ring in package obs holds the recent past in memory and is the right
// thing for a live view. It is the wrong thing for the case that matters most:
// the station breaking at three in the morning with nobody watching, where a
// crash loop empties the ring of exactly the evidence somebody wanted.

// MaxLogRows caps the table.
//
// 5000 warnings and errors is weeks of a healthy station and hours of a sick
// one, which is the right way round. A ROW CAP rather than retention by age,
// because a cap is one number and needs no clock -- and a station that has been
// off for a month should still be able to say why it stopped.
const MaxLogRows = 5000

// MinPersistedLevel is the floor. INFO is chatter and a station logs a great
// deal of it; persisting all of it would turn a music library into a log
// database.
const MinPersistedLevel = "WARN"

// LogRecord is one persisted line.
//
// Attrs is JSON, REDACTED BEFORE IT ARRIVES by the sink that produced it, so
// this layer never sees a secret and cannot leak one by forgetting to.
type LogRecord struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Attrs   string    `json:"attrs,omitempty"`
}

// logLevelRank orders the four levels for filtering. Stored as text rather than
// a number so a row is readable in a SQL prompt, which is where somebody looks
// when the console itself is what is broken.
var logLevelRank = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}

// AppendLogs writes a batch.
//
// BATCHED, in one transaction: SQLite must not be asked for a write per log
// line, and a station under load produces them in bursts.
func (s *Store) AppendLogs(ctx context.Context, recs []LogRecord) error {
	if len(recs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: appending logs: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	for _, r := range recs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO log_records (at, level, msg, attrs) VALUES (?, ?, ?, ?)`,
			r.At.Unix(), r.Level, r.Message, nullable(r.Attrs)); err != nil {
			return fmt.Errorf("store: appending logs: %w", err)
		}
	}

	// PRUNED HERE, in the same transaction as the write that overflowed it,
	// rather than on a timer nobody would notice failing.
	//
	// ONE STATEMENT, expressed as "keep the newest MaxLogRows" rather than
	// counting first and then deleting the difference. It costs an indexed
	// scan on every batch, which is nothing beside the write it accompanies,
	// and it buys a prune that is correct whatever the row count is -- where
	// count-then-delete has a second failure mode between the two queries and
	// a branch that cannot be reached while the table exists.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM log_records WHERE id NOT IN (
			SELECT id FROM log_records ORDER BY at DESC, id DESC LIMIT ?)`,
		MaxLogRows); err != nil {
		return fmt.Errorf("store: pruning logs: %w", err)
	}
	return wrapErr(tx.Commit(), "appending logs")
}

// RecentLogs returns up to limit records at or above min, newest first.
func (s *Store) RecentLogs(ctx context.Context, min string, limit int) ([]LogRecord, error) {
	rank, ok := logLevelRank[strings.ToUpper(strings.TrimSpace(min))]
	if !ok {
		return nil, fmt.Errorf("store: %q is not a log level; use debug, info, warn or error", min)
	}
	// Ranked in Go and matched by NAME, because SQLite has no ordering for
	// these strings and "ERROR" sorts before "WARN" alphabetically -- which
	// would silently hide every error from a warn-and-above filter.
	var want []any
	var marks []string
	for level, r := range logLevelRank {
		if r >= rank {
			want = append(want, level)
			marks = append(marks, "?")
		}
	}
	want = append(want, limit)

	rows, err := s.db.QueryContext(ctx,
		`SELECT at, level, msg, coalesce(attrs, '') FROM log_records
		  WHERE level IN (`+strings.Join(marks, ",")+`)
		  ORDER BY at DESC, id DESC LIMIT ?`, want...)
	if err != nil {
		return nil, fmt.Errorf("store: reading logs: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []LogRecord{}
	for rows.Next() {
		var r LogRecord
		var at int64
		if err := rows.Scan(&at, &r.Level, &r.Message, &r.Attrs); err != nil {
			return nil, fmt.Errorf("store: reading logs: %w", err)
		}
		r.At = time.Unix(at, 0)
		out = append(out, r)
	}
	return out, wrapErr(rows.Err(), "reading logs")
}

// ClearLogs empties the table. The operator asked; the clear itself is logged
// immediately afterwards by the caller, so the record of who emptied the log
// survives the emptying.
func (s *Store) ClearLogs(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM log_records`)
	return wrapErr(err, "clearing logs")
}
