// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"path/filepath"
	"testing"
)

// writeDossier puts one dossier row in, bypassing the enrich package so this
// test does not depend on the shape of a struct it is not testing.
func writeDossier(t *testing.T, s *Store, trackID int64, json string) {
	t.Helper()
	// The row the dossier hangs off. A dossier has a foreign key to it, which
	// is the whole reason a redo is safe: deleting one cannot orphan a track.
	writeTrack(t, s, trackID)
	writeDossierBody(t, s, trackID, json, 0)
}

// writeDossierAt is writeDossier with a stated write time, for the cutoff.
func writeDossierAt(t *testing.T, s *Store, trackID int64, json string, at int64) {
	t.Helper()
	writeTrack(t, s, trackID)
	writeDossierBody(t, s, trackID, json, at)
}

func writeTrack(t *testing.T, s *Store, trackID int64) {
	t.Helper()
	if _, err := s.DB().ExecContext(context.Background(),
		`INSERT INTO tracks (id, path, playable) VALUES (?, ?, 1)`,
		trackID, "/m/"+string(rune('a'+trackID))+".mp3"); err != nil {
		t.Fatalf("writing track %d: %v", trackID, err)
	}
}

func writeDossierBody(t *testing.T, s *Store, trackID int64, json string, at int64) {
	t.Helper()
	if _, err := s.DB().ExecContext(context.Background(),
		`INSERT INTO dossiers (track_id, json, confidence, created_at) VALUES (?, ?, 'high', ?)`,
		trackID, json, at); err != nil {
		t.Fatalf("writing dossier %d: %v", trackID, err)
	}
}

// TestMeaninglessDossiersAreTheOnesWithNothingToSay: the whole safety of the
// redo is which rows it matches. A dossier built from real lyrics must survive,
// because re-deriving it from model memory would be a downgrade.
func TestMeaninglessDossiersAreTheOnesWithNothingToSay(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "redo.db"))
	ctx := context.Background()

	// The ones that should go: nothing about what the song is about.
	writeDossier(t, s, 1, `{"subject_summary":"","themes":[]}`)
	writeDossier(t, s, 2, `{"subject_summary":"","themes":null}`)
	// A row missing both keys entirely, which is what an older dossier looks
	// like. coalesce is why this matches rather than silently not.
	writeDossier(t, s, 3, `{"station_tags":["rock"]}`)
	// The ones that must STAY, each for a different reason.
	writeDossier(t, s, 4, `{"subject_summary":"A drive out of a city.","themes":[]}`)
	writeDossier(t, s, 5, `{"subject_summary":"","themes":["leaving home"]}`)

	// A cutoff far in the future, so this case is about WHICH ROWS say nothing
	// rather than about when they were written. The cutoff has its own test.
	const everything = 1 << 40

	n, err := s.CountMeaninglessDossiersBefore(ctx, everything)
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != 3 {
		t.Fatalf("counted %d, want the 3 with no meaning", n)
	}

	got, err := s.ClearMeaninglessDossiersBefore(ctx, everything)
	if err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if got != 3 {
		t.Errorf("cleared %d, want 3", got)
	}

	var left int64
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM dossiers`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 2 {
		t.Errorf("%d dossiers left, want the 2 that said something", left)
	}
	// And named, so a failure says WHICH survived rather than how many.
	for _, id := range []int64{4, 5} {
		var one int64
		if err := s.DB().QueryRowContext(ctx,
			`SELECT count(*) FROM dossiers WHERE track_id = ?`, id).Scan(&one); err != nil {
			t.Fatal(err)
		}
		if one != 1 {
			t.Errorf("track %d had its meaning deleted", id)
		}
	}
}

// TestMeaninglessDossiersOnAnEmptyLibraryIsZeroNotAnError: an operator pressing
// the button on a fresh install gets a count, not a failure.
func TestMeaninglessDossiersOnAnEmptyLibraryIsZeroNotAnError(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "redo.db"))
	ctx := context.Background()

	n, err := s.CountMeaninglessDossiersBefore(ctx, 1<<40)
	if err != nil || n != 0 {
		t.Errorf("count = %d, %v; want 0 and no error", n, err)
	}
	cleared, err := s.ClearMeaninglessDossiersBefore(ctx, 1<<40)
	if err != nil || cleared != 0 {
		t.Errorf("clear = %d, %v; want 0 and no error", cleared, err)
	}
}

// TestMeaninglessDossiersRespectTheCutoff is what stops the redo looping.
//
// Recall does not rescue every track -- a model refuses what it does not know,
// which is correct -- so re-enrichment writes a FRESH meaningless dossier for
// each track it still cannot describe. Without the cutoff the console would
// offer to clear those too, the operator would spend hours of model time
// reproducing them exactly, and it would offer again, forever.
func TestMeaninglessDossiersRespectTheCutoff(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "cutoff.db"))
	ctx := context.Background()
	const switchedOn = 1000

	// Written under the old settings: worth writing again.
	writeDossierAt(t, s, 1, `{"subject_summary":"","themes":[]}`, switchedOn-1)
	// Written since: the model was already asked under the current settings
	// and had nothing. Asking again costs model time and changes nothing.
	writeDossierAt(t, s, 2, `{"subject_summary":"","themes":[]}`, switchedOn+1)
	// A row from before created_at was ever filled counts as old.
	writeDossierAt(t, s, 3, `{"subject_summary":"","themes":[]}`, 0)
	// AND A ROW WITH NO WRITE TIME AT ALL. The column is nullable, so an older
	// database or a hand-edited row can hold NULL -- and under a bare
	// "created_at < ?" every one of those is silently excluded FOREVER, which
	// is a track that can never be re-enriched and no error to say so.
	writeTrack(t, s, 4)
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO dossiers (track_id, json, confidence, created_at)
		 VALUES (4, '{"subject_summary":"","themes":[]}', 'none', NULL)`); err != nil {
		t.Fatal(err)
	}

	n, err := s.CountMeaninglessDossiersBefore(ctx, switchedOn)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("counted %d, want the 3 written before the settings changed", n)
	}

	if _, err := s.ClearMeaninglessDossiersBefore(ctx, switchedOn); err != nil {
		t.Fatal(err)
	}
	var left int64
	if err := s.DB().QueryRowContext(ctx,
		`SELECT track_id FROM dossiers`).Scan(&left); err != nil {
		t.Fatalf("expected exactly the one recent dossier to survive: %v", err)
	}
	if left != 2 {
		t.Errorf("track %d survived, want the recent one (2)", left)
	}

	// AND A SECOND PRESS CLEARS NOTHING. That is the loop terminating.
	again, err := s.ClearMeaninglessDossiersBefore(ctx, switchedOn)
	if err != nil || again != 0 {
		t.Errorf("a second redo cleared %d (%v), want 0", again, err)
	}
}

// TestMeaninglessDossiersReportAFailedQuery: the operator presses a button that
// deletes things, so a failure has to come back as an error rather than as
// "cleared 0" -- which reads exactly like success with nothing to do.
//
// The database is closed underneath the store, which is the bluntest real
// failure available and needs no fake.
func TestMeaninglessDossiersReportAFailedQuery(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "broken.db"))
	ctx := context.Background()
	writeDossier(t, s, 1, `{"subject_summary":"","themes":[]}`)

	if err := s.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	if n, err := s.CountMeaninglessDossiersBefore(ctx, 1<<40); err == nil {
		t.Errorf("counting on a closed database returned %d and no error", n)
	}
	n, err := s.ClearMeaninglessDossiersBefore(ctx, 1<<40)
	if err == nil {
		t.Errorf("clearing on a closed database returned %d and no error", n)
	}
	if n != 0 {
		t.Errorf("a failed clear reported %d rows gone", n)
	}
}

// TestOneUnreadableDossierDoesNotBreakTheWholeLibrary.
//
// json_extract does not SKIP a malformed value, it RAISES. One bad row failed
// the count and the delete for every track, with "SQL logic error: malformed
// JSON" as the whole explanation -- and Overview swallows that error, so the
// console would quietly stop offering the redo and nothing would say why.
//
// StoreDossier always marshals valid JSON, so such a row can only come from a
// hand-edited database or corruption. That is exactly when an operator needs
// the button to work rather than to be the second thing broken.
func TestOneUnreadableDossierDoesNotBreakTheWholeLibrary(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "bad.db"))
	ctx := context.Background()

	writeDossier(t, s, 1, `{"subject_summary":"","themes":[]}`)         // ordinary, due a redo
	writeDossier(t, s, 2, `{"subject_summary":"A drive.","themes":[]}`) // has meaning, keep
	// Not JSON at all.
	writeTrack(t, s, 3)
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO dossiers (track_id, json, confidence, created_at)
		 VALUES (3, 'not json at all', 'none', 0)`); err != nil {
		t.Fatal(err)
	}
	// Valid JSON, but themes is a string rather than an array.
	writeTrack(t, s, 4)
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO dossiers (track_id, json, confidence, created_at)
		 VALUES (4, '{"subject_summary":"","themes":"nope"}', 'none', 0)`); err != nil {
		t.Fatal(err)
	}

	n, err := s.CountMeaninglessDossiersBefore(ctx, 1<<40)
	if err != nil {
		t.Fatalf("one unreadable row broke the count for every track: %v", err)
	}
	// 1 ordinary, 3 unreadable, 4 with a themes value nothing can read as a
	// list. The one with a real summary stays.
	if n != 3 {
		t.Errorf("counted %d, want 3", n)
	}

	cleared, err := s.ClearMeaninglessDossiersBefore(ctx, 1<<40)
	if err != nil {
		t.Fatalf("one unreadable row broke the delete for every track: %v", err)
	}
	if cleared != 3 {
		t.Errorf("cleared %d, want 3", cleared)
	}
	var left int64
	if err := s.DB().QueryRowContext(ctx, `SELECT track_id FROM dossiers`).Scan(&left); err != nil {
		t.Fatalf("expected exactly the one dossier with meaning to survive: %v", err)
	}
	if left != 2 {
		t.Errorf("track %d survived, want the one with a summary (2)", left)
	}
}
