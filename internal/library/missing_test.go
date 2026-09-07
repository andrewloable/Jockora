// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// missingOf reads the mark. A pointer, because NULL and a timestamp are the
// whole distinction being tested.
func missingOf(t *testing.T, s *store.Store, path string) *int64 {
	t.Helper()
	var at *int64
	if err := s.DB().QueryRow(`SELECT missing_at FROM tracks WHERE path = ?`, path).
		Scan(&at); err != nil {
		t.Fatalf("reading missing_at for %s: %v", path, err)
	}
	return at
}

// threeTrackSource builds a folder source with three scanned files.
func threeTrackSource(t *testing.T) (*store.Store, string, int64) {
	t.Helper()
	s := openStore(t)
	root := t.TempDir()
	for _, n := range []string{"a.mp3", "b.mp3", "c.mp3"} {
		makeAudio(t, root, n, "Artist", "Title "+n, "Album", 1)
	}
	id := addSource(t, s, store.Source{Kind: store.SourceFolder, Locator: root, Enabled: true})
	if _, err := ScanSources(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}
	return s, root, id
}

func TestMissingMarkedAfterScan(t *testing.T) {
	s, root, _ := threeTrackSource(t)
	gone := filepath.Join(root, "b.mp3")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	stats, err := ScanSources(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Marked != 1 {
		t.Errorf("Stats.Marked = %d, want 1", stats.Marked)
	}
	if missingOf(t, s, gone) == nil {
		t.Error("the deleted file was not marked missing")
	}
	for _, still := range []string{"a.mp3", "c.mp3"} {
		if at := missingOf(t, s, filepath.Join(root, still)); at != nil {
			t.Errorf("%s was marked missing at %d and is still there", still, *at)
		}
	}

	// The ROW SURVIVES. Deleting it would lose the dossier and orphan the
	// said_lines that reference it.
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM tracks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("%d rows, want 3 -- a missing file is marked, never deleted", n)
	}
}

// TestMissingKeepsDossier: a dossier is minutes of model time. A file that
// comes back must not have to be enriched again.
func TestMissingKeepsDossier(t *testing.T) {
	s, root, _ := threeTrackSource(t)
	ctx := context.Background()
	gone := filepath.Join(root, "b.mp3")

	var id int64
	if err := s.DB().QueryRow(`SELECT id FROM tracks WHERE path = ?`, gone).Scan(&id); err != nil {
		t.Fatal(err)
	}
	const doc = `{"subject_summary":"a song about b"}`
	if _, err := s.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, 'high')`,
		id, doc); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSources(ctx, s, nil); err != nil {
		t.Fatal(err)
	}

	var got, conf string
	if err := s.DB().QueryRow(
		`SELECT json, confidence FROM dossiers WHERE track_id = ?`, id).Scan(&got, &conf); err != nil {
		t.Fatalf("the dossier went with the file: %v", err)
	}
	if got != doc || conf != "high" {
		t.Errorf("the dossier changed: %q %q", got, conf)
	}
}

// TestMissingExcludedFromPools is the point of the mark: a marked track must
// not be selectable anywhere, or the stream picks a file that is not there and
// discovers it mid-transition.
func TestMissingExcludedFromPools(t *testing.T) {
	s, root, _ := threeTrackSource(t)
	ctx := context.Background()
	gone := filepath.Join(root, "b.mp3")

	var goneID int64
	if err := s.DB().QueryRow(`SELECT id FROM tracks WHERE path = ?`, gone).Scan(&goneID); err != nil {
		t.Fatal(err)
	}
	// Every track tagged, so the tag pool would otherwise contain all three.
	rows, err := s.DB().Query(`SELECT id FROM tracks`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.DB().Exec(
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, 'high')`,
			id, `{"station_tags":["rock"]}`); err != nil {
			t.Fatal(err)
		}
	}

	stationID, err := s.CreateStation(ctx, store.Station{Name: "ROCK", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStationTracks(ctx, stationID, ids); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSources(ctx, s, nil); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		get  func() ([]int64, error)
	}{
		{"PlayablePool", func() ([]int64, error) { return station.PlayablePool(ctx, s) }},
		{"PoolForTag", func() ([]int64, error) { return station.PoolForTag(ctx, s, "rock") }},
		{"StationTrackIDs", func() ([]int64, error) { return s.StationTrackIDs(ctx, stationID) }},
	} {
		got, err := tc.get()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != 2 {
			t.Errorf("%s returned %d tracks, want 2", tc.name, len(got))
		}
		for _, id := range got {
			if id == goneID {
				t.Errorf("%s still offers the missing track", tc.name)
			}
		}
	}

	// The console must still SHOW it, or an operator cannot tell a missing
	// track from one that was never there.
	full, err := s.ListStationTracks(ctx, stationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 3 {
		t.Fatalf("ListStationTracks returned %d, want all 3", len(full))
	}
	var flagged bool
	for _, st := range full {
		if st.TrackID == goneID {
			flagged = st.Missing
		}
	}
	if !flagged {
		t.Error("the missing track is listed but not flagged as missing")
	}
}

// TestMissingClearedOnReturn: an unplugged drive comes back. The mark must
// clear WITHOUT re-probing, because an unchanged file is skipped and a skip
// that leaves the mark would keep a whole library off the air.
func TestMissingClearedOnReturn(t *testing.T) {
	s, root, _ := threeTrackSource(t)
	ctx := context.Background()
	gone := filepath.Join(root, "b.mp3")

	body, err := os.ReadFile(gone)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(gone)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanSources(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	if missingOf(t, s, gone) == nil {
		t.Fatal("the file was not marked in the first place")
	}

	// Same bytes, same modification time: exactly what remounting a drive
	// looks like.
	if err := os.WriteFile(gone, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(gone, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	stats, err := ScanSources(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if at := missingOf(t, s, gone); at != nil {
		t.Errorf("the mark survived the file's return: %d", *at)
	}
	if stats.Skipped != 3 {
		t.Errorf("Skipped = %d, want 3 -- an unchanged file must not be re-probed", stats.Skipped)
	}
	if stats.Marked != 0 {
		t.Errorf("Marked = %d on a complete library, want 0", stats.Marked)
	}
}

// TestMissingScopedToSource: a Navidrome that is down must not declare the
// folder library missing, and vice versa.
func TestMissingScopedToSource(t *testing.T) {
	s, rootA, _ := threeTrackSource(t)
	ctx := context.Background()

	rootB := t.TempDir()
	makeAudio(t, rootB, "z.mp3", "Artist", "Title Z", "Album", 1)
	addSource(t, s, store.Source{Kind: store.SourceFolder, Locator: rootB, Enabled: true})
	if _, err := ScanSources(ctx, s, nil); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(rootA, "b.mp3")); err != nil {
		t.Fatal(err)
	}
	stats, err := ScanSources(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Marked != 1 {
		t.Errorf("Marked = %d, want 1 -- only source A lost a file", stats.Marked)
	}
	if missingOf(t, s, filepath.Join(rootB, "z.mp3")) != nil {
		t.Error("a file missing from source A marked source B's track")
	}
}

// TestMissingNotMarkedOnAbortedWalk: a scan that dies halfway has seen only
// part of the library. Marking then would take everything it had not reached
// off the air.
func TestMissingNotMarkedOnAbortedWalk(t *testing.T) {
	s, root, _ := threeTrackSource(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stats, err := ScanSources(ctx, s, nil)
	if err == nil {
		t.Fatal("a cancelled scan reported success")
	}
	if stats.Marked != 0 {
		t.Errorf("Marked = %d on an aborted walk, want 0", stats.Marked)
	}
	for _, n := range []string{"a.mp3", "b.mp3", "c.mp3"} {
		if missingOf(t, s, filepath.Join(root, n)) != nil {
			t.Errorf("%s was marked missing by a scan that never ran", n)
		}
	}
}

// TestMissingNotMarkedOnFailedWalk is the unplugged-drive case, and it is not
// the same as a cancelled one: the context is fine, the source is not. Marking
// here would flag an entire library in one go, empty every station built on it,
// and drop them below their minimum track thresholds -- for a fault that may
// last a minute.
func TestMissingNotMarkedOnFailedWalk(t *testing.T) {
	s, root, _ := threeTrackSource(t)

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	stats, err := ScanSources(context.Background(), s, nil)
	if err == nil {
		t.Fatal("a source whose root is gone reported success")
	}
	if stats.Marked != 0 {
		t.Errorf("Marked = %d after a failed walk, want 0", stats.Marked)
	}
	var marked int
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM tracks WHERE missing_at IS NOT NULL`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if marked != 0 {
		t.Errorf("%d rows were marked missing by a walk that failed", marked)
	}
}

func TestMissingSurfacesFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("closed store", func(t *testing.T) {
		s, root, id := threeTrackSource(t)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := MarkMissing(ctx, s, id, time.Now().Unix()); err == nil {
			t.Error("MarkMissing succeeded on a closed store")
		}
		// A swallowed failure here would look exactly like a library that had
		// not changed, which is the one thing removal detection must not say
		// by accident.
		if err := touch(ctx, s, filepath.Join(root, "a.mp3"), 1); err == nil {
			t.Error("touch succeeded on a closed store")
		}
		if _, err := scanStamp(ctx, s, id); err == nil {
			t.Error("scanStamp succeeded on a closed store")
		}
	})

	t.Run("read-only store", func(t *testing.T) {
		s, _, id := threeTrackSource(t)
		s.DB().SetMaxOpenConns(1)
		if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
			t.Fatal(err)
		}
		// A stamp from the future, so every row counts as unseen and the
		// write is actually attempted.
		if _, err := MarkMissing(ctx, s, id, time.Now().Unix()+3600); err == nil {
			t.Error("MarkMissing reported success against a read-only database")
		}
	})
}
