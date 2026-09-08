// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Jockora-69n.2. The ring in obs holds the recent past in memory, and a crash
// loop empties it of exactly the evidence somebody wanted. This is the durable
// half: WARN and above, and nothing else.

// logsSchemaVersion is the schema BEFORE this migration, as a literal.
// migrate_v2_test.go records what CurrentSchemaVersion-1 cost the last time.
const logsSchemaVersion = 11

func logRec(at time.Time, level, msg string, attrs string) LogRecord {
	return LogRecord{At: at, Level: level, Message: msg, Attrs: attrs}
}

func TestLogStoreRoundTrip(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "logs.db"))
	ctx := context.Background()
	base := time.Unix(1788840000, 0)

	err := s.AppendLogs(ctx, []LogRecord{
		logRec(base, "WARN", "model unavailable", `{"attempt":"1"}`),
		logRec(base.Add(time.Second), "ERROR", "enrichment stopped", `{"err":"rejected the API key"}`),
	})
	if err != nil {
		t.Fatalf("AppendLogs: %v", err)
	}

	got, err := s.RecentLogs(ctx, "WARN", 10)
	if err != nil {
		t.Fatalf("RecentLogs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("RecentLogs returned %d, want 2", len(got))
	}
	// NEWEST FIRST: an operator opening the page is looking at what just went
	// wrong, not at what went wrong first.
	if got[0].Message != "enrichment stopped" {
		t.Errorf("RecentLogs[0] = %q, want the newest", got[0].Message)
	}
	if got[0].Attrs != `{"err":"rejected the API key"}` {
		t.Errorf("attrs JSON was mangled: %q", got[0].Attrs)
	}
	if !got[0].At.Equal(base.Add(time.Second)) {
		t.Errorf("At = %v, want %v", got[0].At, base.Add(time.Second))
	}
	// An empty batch is not an error and must not touch the database.
	if err := s.AppendLogs(ctx, nil); err != nil {
		t.Errorf("AppendLogs(nil): %v", err)
	}
}

func TestLogStoreLevelFilter(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "filter.db"))
	ctx := context.Background()
	at := time.Unix(1788840000, 0)

	if err := s.AppendLogs(ctx, []LogRecord{
		logRec(at, "INFO", "on air", ""),
		logRec(at.Add(time.Second), "WARN", "break dropped", ""),
		logRec(at.Add(2*time.Second), "ERROR", "enrichment stopped", ""),
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		min  string
		want int
	}{{"INFO", 3}, {"WARN", 2}, {"ERROR", 1}} {
		got, err := s.RecentLogs(ctx, tc.min, 10)
		if err != nil {
			t.Fatalf("RecentLogs(%s): %v", tc.min, err)
		}
		if len(got) != tc.want {
			t.Errorf("RecentLogs at %s returned %d, want %d", tc.min, len(got), tc.want)
		}
	}
	// The limit is honoured, or a console asking for twenty gets five thousand.
	if got, err := s.RecentLogs(ctx, "INFO", 2); err != nil || len(got) != 2 {
		t.Errorf("RecentLogs with a limit of 2 returned %d (%v)", len(got), err)
	}
	// A level nobody defines is refused rather than quietly matching nothing.
	if _, err := s.RecentLogs(ctx, "verbose", 10); err == nil {
		t.Error("RecentLogs accepted a level nobody defines")
	}
}

func TestLogStoreClear(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "clear.db"))
	ctx := context.Background()
	if err := s.AppendLogs(ctx, []LogRecord{
		logRec(time.Unix(1, 0), "WARN", "before", ""),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearLogs(ctx); err != nil {
		t.Fatalf("ClearLogs: %v", err)
	}
	got, err := s.RecentLogs(ctx, "INFO", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("%d records survived ClearLogs", len(got))
	}
}

// TestLogStorePrunes: a row cap rather than retention by age, because a cap is
// one number and needs no clock.
func TestLogStorePrunes(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "prune.db"))
	ctx := context.Background()
	base := time.Unix(1788840000, 0)

	// Written in two batches so the prune has to consider rows already there,
	// not just the ones in front of it.
	for batch := 0; batch < 2; batch++ {
		var recs []LogRecord
		for i := 0; i < MaxLogRows; i++ {
			n := batch*MaxLogRows + i
			recs = append(recs, logRec(base.Add(time.Duration(n)*time.Second),
				"WARN", fmt.Sprintf("record %d", n), ""))
		}
		if err := s.AppendLogs(ctx, recs); err != nil {
			t.Fatalf("AppendLogs batch %d: %v", batch, err)
		}
	}

	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM log_records`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != MaxLogRows {
		t.Errorf("%d rows after writing %d, want the cap of %d", n, 2*MaxLogRows, MaxLogRows)
	}
	// THE OLDEST WENT, not the newest. Pruning the wrong end would keep the
	// evidence nobody needs and delete what just happened.
	got, err := s.RecentLogs(ctx, "WARN", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Message != fmt.Sprintf("record %d", 2*MaxLogRows-1) {
		t.Errorf("the newest record is %q; the prune took the wrong end", got[0].Message)
	}
}

// TestLogStoreMigrates: a database at the previous version gains the table and
// keeps everything else.
func TestLogStoreMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	old := openAt(t, path)
	ctx := context.Background()

	if _, err := old.CreateAd(ctx, Ad{Brand: "Stillwater", Script: "One mug, still."}); err != nil {
		t.Fatal(err)
	}
	// THROUGH rewindTo, not by hand. Undoing only this migration's own table
	// left every LATER migration's columns in place, so the next one to add a
	// column to a v0.1 table failed here on "duplicate column name" -- which is
	// what Jockora-e9a.55's ads.enabled did. rewindTo knows every undo, so this
	// keeps working as migrations land.
	rewindTo(t, old, logsSchemaVersion)
	old.Close() //nolint:errcheck // reopening to migrate

	up := openAt(t, path)
	if err := up.AppendLogs(ctx, []LogRecord{
		logRec(time.Unix(1, 0), "WARN", "after the migration", ""),
	}); err != nil {
		t.Fatalf("the table was not created: %v", err)
	}
	ads, err := up.ListAds(ctx)
	if err != nil || len(ads) != 1 {
		t.Errorf("the migration lost the ads: %d (%v)", len(ads), err)
	}
}

// TestLogStoreReportsADatabaseThatWillNotAnswer: this is the LOG store, so its
// failures are the ones nobody will see reported anywhere else. They still have
// to be reported rather than swallowed.
func TestLogStoreReportsADatabaseThatWillNotAnswer(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "broken.db"))
	ctx := context.Background()
	rec := []LogRecord{logRec(time.Unix(1, 0), "WARN", "x", "")}

	if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLogs(ctx, rec); err == nil {
		t.Error("AppendLogs reported success against a read-only database")
	}
	// The PRUNE half of the same call, which runs after the inserts and is the
	// half that would silently let the table grow without bound.
	if _, err := s.DB().Exec(`PRAGMA query_only = 0`); err != nil {
		t.Fatal(err)
	}
	var many []LogRecord
	for i := 0; i <= MaxLogRows; i++ {
		many = append(many, logRec(time.Unix(int64(i), 0), "WARN", "x", ""))
	}
	if err := s.AppendLogs(ctx, many); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`
		CREATE TRIGGER no_prune BEFORE DELETE ON log_records
		BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLogs(ctx, rec); err == nil {
		t.Error("AppendLogs reported success when the prune was refused")
	}
	if _, err := s.DB().Exec(`DROP TRIGGER no_prune`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearLogs(ctx); err == nil {
		t.Error("ClearLogs reported success against a read-only database")
	}
	if _, err := s.DB().Exec(`PRAGMA query_only = 0`); err != nil {
		t.Fatal(err)
	}

	// A row this build cannot scan.
	if _, err := s.DB().Exec(
		`INSERT INTO log_records (at, level, msg) VALUES ('not a number', 'WARN', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecentLogs(ctx, "WARN", 10); err == nil {
		t.Error("RecentLogs returned a row whose timestamp is not a time")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecentLogs(ctx, "WARN", 10); err == nil {
		t.Error("RecentLogs reported success against a closed database")
	}
	if err := s.AppendLogs(ctx, rec); err == nil {
		t.Error("AppendLogs reported success against a closed database")
	}
	if err := s.ClearLogs(ctx); err == nil {
		t.Error("ClearLogs reported success against a closed database")
	}
	_ = errors.New("")
}
