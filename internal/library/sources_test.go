// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/store"
)

// fakeLib is an OpenSubsonic server with a caller-chosen song list, which is
// what the collision test needs: a song whose id IS an absolute file path.
type fakeLib struct {
	songs []map[string]any
	delay time.Duration

	mu       sync.Mutex
	requests int
	started  time.Time
	ended    time.Time
}

func (f *fakeLib) start(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if f.requests == 0 {
			f.started = time.Now()
		}
		f.requests++
		f.ended = time.Now()
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/ping.view"):
			fmt.Fprint(w, `{"subsonic-response":{"status":"ok"}}`)
		case strings.HasSuffix(r.URL.Path, "/getAlbumList2.view"):
			if r.URL.Query().Get("offset") == "0" {
				fmt.Fprint(w, `{"subsonic-response":{"status":"ok","albumList2":{"album":[{"id":"al-1"}]}}}`)
				return
			}
			fmt.Fprint(w, `{"subsonic-response":{"status":"ok","albumList2":{"album":[]}}}`)
		case strings.HasSuffix(r.URL.Path, "/getAlbum.view"):
			time.Sleep(f.delay)
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{
				"status": "ok", "album": map[string]any{"song": f.songs}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (f *fakeLib) hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

var errStub = fmt.Errorf("the connection went away")

func song(id, title string) map[string]any {
	return map[string]any{"id": id, "title": title, "artist": "A", "album": "B",
		"year": 1999, "duration": 100, "size": 1000}
}

func addSource(t *testing.T, s *store.Store, src store.Source) int64 {
	t.Helper()
	id, err := s.CreateSource(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func trackRows(t *testing.T, s *store.Store) map[string]int64 {
	t.Helper()
	rows, err := s.DB().Query(`SELECT path, coalesce(source_id, 0) FROM tracks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var p string
		var sid int64
		if err := rows.Scan(&p, &sid); err != nil {
			t.Fatal(err)
		}
		out[p] = sid
	}
	return out
}

func TestScanSourcesFolderAndSubsonic(t *testing.T) {
	s := openStore(t)
	root := t.TempDir()
	makeAudio(t, root, "a.mp3", "Artist A", "Title A", "Album A", 1)

	f := &fakeLib{songs: []map[string]any{song("s1", "Creep"), song("s2", "Bones")}}
	folderID := addSource(t, s, store.Source{Kind: store.SourceFolder, Locator: root, Enabled: true})
	subID := addSource(t, s, store.Source{Kind: store.SourceSubsonic, Locator: f.start(t),
		Username: "andrew", Password: "hunter2", Enabled: true})

	stats, err := ScanSources(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Found != 3 {
		t.Errorf("Found = %d, want 3 across both sources", stats.Found)
	}

	rows := trackRows(t, s)
	if len(rows) != 3 {
		t.Fatalf("%d track rows, want 3: %v", len(rows), rows)
	}
	if got := rows[filepath.Join(root, "a.mp3")]; got != folderID {
		t.Errorf("the folder track carries source_id %d, want %d", got, folderID)
	}
	// A subsonic path is a LOCATOR, not a stream URL. A URL embeds the auth
	// token, so it would put a credential in every row and change every path
	// the moment the password changed -- duplicating the whole library.
	want := fmt.Sprintf("subsonic:%d:s1", subID)
	if got, ok := rows[want]; !ok || got != subID {
		t.Errorf("no row at %q with source_id %d: %v", want, subID, rows)
	}
	for p := range rows {
		if strings.Contains(p, "?") || strings.Contains(p, "t=") {
			t.Errorf("a credential-bearing URL was stored as a path: %q", p)
		}
	}
}

func TestScanSourcesSkipsDisabled(t *testing.T) {
	s := openStore(t)
	f := &fakeLib{songs: []map[string]any{song("s1", "Creep")}}
	id := addSource(t, s, store.Source{Kind: store.SourceSubsonic, Locator: f.start(t),
		Username: "andrew", Password: "hunter2", Enabled: true})
	if err := s.SetSourceEnabled(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}

	stats, err := ScanSources(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.hits() != 0 {
		t.Errorf("a disabled source was contacted %d times", f.hits())
	}
	if stats.Found != 0 || len(trackRows(t, s)) != 0 {
		t.Errorf("a disabled source produced rows: %+v", stats)
	}
}

// TestScanSourcesNoCollision: a subsonic server is free to use absolute file
// paths as song ids -- Navidrome does not, but nothing stops one that does.
func TestScanSourcesNoCollision(t *testing.T) {
	s := openStore(t)
	root := t.TempDir()
	makeAudio(t, root, "x.mp3", "Artist A", "Title A", "Album A", 1)
	folderPath := filepath.Join(root, "x.mp3")

	f := &fakeLib{songs: []map[string]any{song(folderPath, "Impostor")}}
	addSource(t, s, store.Source{Kind: store.SourceFolder, Locator: root, Enabled: true})
	subID := addSource(t, s, store.Source{Kind: store.SourceSubsonic, Locator: f.start(t),
		Username: "andrew", Password: "hunter2", Enabled: true})

	if _, err := ScanSources(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}
	rows := trackRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2 -- the song and the file are different tracks: %v", len(rows), rows)
	}
	if _, ok := rows[folderPath]; !ok {
		t.Errorf("the folder track is missing: %v", rows)
	}
	if _, ok := rows[fmt.Sprintf("subsonic:%d:%s", subID, folderPath)]; !ok {
		t.Errorf("the subsonic song is missing: %v", rows)
	}
}

// TestScanSourcesV01RowsAdopted: 7,595 rows exist with no source_id. They must
// be claimed, not re-added -- a duplicate carries no dossier, and a dossier is
// minutes of model time.
func TestScanSourcesV01RowsAdopted(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	root := t.TempDir()
	makeAudio(t, root, "a.mp3", "Artist A", "Title A", "Album A", 1)

	// Exactly what v0.1 left behind: scanned, with a dossier, no source.
	if _, err := Scan(ctx, s, root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE tracks SET source_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence) VALUES (1, '{}', 'high')`); err != nil {
		t.Fatal(err)
	}
	before := len(trackRows(t, s))

	folderID := addSource(t, s, store.Source{Kind: store.SourceFolder, Locator: root, Enabled: true})
	if _, err := ScanSources(ctx, s, nil); err != nil {
		t.Fatal(err)
	}

	rows := trackRows(t, s)
	if len(rows) != before {
		t.Fatalf("%d rows after adoption, want %d -- nothing may be duplicated", len(rows), before)
	}
	if got := rows[filepath.Join(root, "a.mp3")]; got != folderID {
		t.Errorf("source_id = %d, want %d: the v0.1 row was not adopted", got, folderID)
	}
	var dossiers int
	if err := s.DB().QueryRow(`SELECT count(*) FROM dossiers`).Scan(&dossiers); err != nil {
		t.Fatal(err)
	}
	if dossiers != 1 {
		t.Errorf("%d dossiers survived adoption, want 1", dossiers)
	}
}

// TestScanSourcesAdoptsV01SubsonicRows: v0.1 stored the stream URL as the path.
// Left alone, the rescan would add every song again under its new locator and
// silently double the library.
func TestScanSourcesAdoptsV01SubsonicRows(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := &fakeLib{songs: []map[string]any{song("s1", "Creep")}}
	base := f.start(t)

	old := base + "/rest/stream.view?id=s1&u=andrew&t=deadbeef&s=cafe&v=1.16.1&c=jockora"
	if _, err := s.DB().Exec(
		`INSERT INTO tracks (path, artist, title, playable) VALUES (?, 'A', 'Creep', 1)`,
		old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence) VALUES (1, '{}', 'high')`); err != nil {
		t.Fatal(err)
	}

	subID := addSource(t, s, store.Source{Kind: store.SourceSubsonic, Locator: base,
		Username: "andrew", Password: "hunter2", Enabled: true})
	if _, err := ScanSources(ctx, s, nil); err != nil {
		t.Fatal(err)
	}

	rows := trackRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1 -- the v0.1 URL row must be rewritten, not duplicated: %v",
			len(rows), rows)
	}
	want := fmt.Sprintf("subsonic:%d:s1", subID)
	if got, ok := rows[want]; !ok || got != subID {
		t.Errorf("expected the row at %q with source %d: %v", want, subID, rows)
	}
	var trackID int64
	if err := s.DB().QueryRow(`SELECT track_id FROM dossiers`).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	if trackID != 1 {
		t.Errorf("the dossier no longer points at the track: track_id = %d", trackID)
	}
}

// TestScanSourcesOneSourceFailingDoesNotAbort: a dead Navidrome must not stop a
// folder scan. The operator is told, and the music they do have still plays.
func TestScanSourcesOneSourceFailingDoesNotAbort(t *testing.T) {
	s := openStore(t)
	root := t.TempDir()
	makeAudio(t, root, "a.mp3", "Artist A", "Title A", "Album A", 1)

	// Listed FIRST, so the folder only scans if the failure did not abort.
	addSource(t, s, store.Source{Kind: store.SourceSubsonic,
		Locator: "http://127.0.0.1:1", Username: "u", Password: "p", Enabled: true})
	folderID := addSource(t, s, store.Source{Kind: store.SourceFolder, Locator: root, Enabled: true})

	stats, err := ScanSources(context.Background(), s, nil)
	if err == nil {
		t.Fatal("an unreachable source was reported as success")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("the error does not name the source that failed: %v", err)
	}
	if stats.Found != 1 || stats.Added != 1 {
		t.Errorf("stats = %+v, want the folder's one track", stats)
	}
	if got := trackRows(t, s)[filepath.Join(root, "a.mp3")]; got != folderID {
		t.Errorf("the folder did not scan after the failure: source_id = %d", got)
	}
}

// TestScanSourcesSequential: ffprobe is the cost and there is one disk, so
// sources run one at a time.
func TestScanSourcesSequential(t *testing.T) {
	s := openStore(t)
	a := &fakeLib{songs: []map[string]any{song("s1", "One")}, delay: 60 * time.Millisecond}
	b := &fakeLib{songs: []map[string]any{song("s2", "Two")}, delay: 60 * time.Millisecond}
	for _, f := range []*fakeLib{a, b} {
		addSource(t, s, store.Source{Kind: store.SourceSubsonic, Locator: f.start(t),
			Username: "u", Password: "p", Enabled: true})
	}

	if _, err := ScanSources(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	b.mu.Lock()
	defer a.mu.Unlock()
	defer b.mu.Unlock()
	if b.started.Before(a.ended) {
		t.Errorf("the second source started at %v, before the first ended at %v",
			b.started, a.ended)
	}
}

// TestScanSourcesResolvesLocators: a locator is not something a decoder can
// open, so playback has to turn it back into a URL. Without this the change of
// path format would silently take every subsonic library off air.
func TestScanSourcesResolvesLocators(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := &fakeLib{songs: []map[string]any{song("s1", "Creep")}}
	base := f.start(t)
	subID := addSource(t, s, store.Source{Kind: store.SourceSubsonic, Locator: base,
		Username: "andrew", Password: "hunter2", Enabled: true})

	got, err := ResolvePath(ctx, s, fmt.Sprintf("subsonic:%d:s1", subID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, base+"/rest/stream.view") || !strings.Contains(got, "id=s1") {
		t.Errorf("resolved to %q, want a stream URL for s1", got)
	}
	if !strings.Contains(got, "t=") || !strings.Contains(got, "s=") {
		t.Errorf("resolved URL carries no credentials, so it will 401: %q", got)
	}

	// A folder path is already openable and must come back untouched.
	if got, err := ResolvePath(ctx, s, "/music/a.mp3"); err != nil || got != "/music/a.mp3" {
		t.Errorf("ResolvePath(/music/a.mp3) = %q, %v", got, err)
	}
	// A colon in a filename must not be mistaken for a locator.
	if got, err := ResolvePath(ctx, s, "/music/9:30 Club.mp3"); err != nil || got != "/music/9:30 Club.mp3" {
		t.Errorf("a path with a colon was treated as a locator: %q, %v", got, err)
	}

	for _, bad := range []string{"subsonic:nope:s1", "subsonic:999:s1", "subsonic:1"} {
		if _, err := ResolvePath(ctx, s, bad); err == nil {
			t.Errorf("ResolvePath(%q) reported success", bad)
		}
	}
	// A locator pointing at a FOLDER source is a corrupt row, not a stream.
	folderID := addSource(t, s, store.Source{Kind: store.SourceFolder, Locator: "/m", Enabled: true})
	if _, err := ResolvePath(ctx, s, fmt.Sprintf("subsonic:%d:s1", folderID)); err == nil {
		t.Error("a locator naming a folder source resolved to a stream")
	}
}

// TestScanSourcesSurviveAPlainRescan: 'jockora scan -library-path X' still
// exists and passes no source. It must not UNCLAIM the rows it touches --
// every source-aware thing after this, removal detection included, keys on
// source_id, and the damage would be invisible until a station emptied.
func TestScanSourcesSurviveAPlainRescan(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	root := t.TempDir()
	makeAudio(t, root, "a.mp3", "Artist A", "Title A", "Album A", 1)
	folderID := addSource(t, s, store.Source{Kind: store.SourceFolder, Locator: root, Enabled: true})

	if _, err := ScanSources(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	// Force a re-probe: an unchanged file is skipped, and a skipped row proves
	// nothing about what an upsert would have written.
	if _, err := s.DB().Exec(`UPDATE tracks SET size_bytes = 1, modified_at = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := Scan(ctx, s, root); err != nil {
		t.Fatal(err)
	}
	if got := trackRows(t, s)[filepath.Join(root, "a.mp3")]; got != folderID {
		t.Errorf("source_id = %d after a plain scan, want %d", got, folderID)
	}
}

func TestScanSourcesSurfacesFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("closed store", func(t *testing.T) {
		s := openStore(t)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanSources(ctx, s, nil); err == nil {
			t.Error("ScanSources succeeded on a closed store")
		}
		if _, err := ResolvePath(ctx, s, "subsonic:1:s1"); err == nil {
			t.Error("ResolvePath succeeded on a closed store")
		}
	})

	// A stream URL whose query cannot be read is LEFT ALONE, not mangled: the
	// rescan adds the song fresh, and an orphan row is recoverable where a
	// wrong locator is not.
	t.Run("unrecoverable v0.1 urls", func(t *testing.T) {
		s := openStore(t)
		f := &fakeLib{songs: []map[string]any{song("s1", "Creep")}}
		base := f.start(t)
		for i, path := range []string{
			base + "/rest/stream.view?%zz",      // not a query at all
			base + "/rest/stream.view?u=andrew", // no id to recover
		} {
			if _, err := s.DB().Exec(
				`INSERT INTO tracks (id, path, playable) VALUES (?, ?, 1)`, 100+i, path); err != nil {
				t.Fatal(err)
			}
		}
		addSource(t, s, store.Source{Kind: store.SourceSubsonic, Locator: base,
			Username: "u", Password: "p", Enabled: true})
		if _, err := ScanSources(ctx, s, nil); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"%zz", "u=andrew"} {
			var n int
			if err := s.DB().QueryRow(
				`SELECT count(*) FROM tracks WHERE path LIKE '%' || ? || '%'`, want).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("the row containing %q was rewritten or lost", want)
			}
		}
	})

	t.Run("no tracks table", func(t *testing.T) {
		s := openStore(t)
		f := &fakeLib{songs: []map[string]any{song("s1", "Creep")}}
		addSource(t, s, store.Source{Kind: store.SourceSubsonic, Locator: f.start(t),
			Username: "u", Password: "p", Enabled: true})
		if _, err := s.DB().Exec(`DROP TABLE station_tracks; DROP TABLE tracks`); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanSources(ctx, s, nil); err == nil {
			t.Error("adoption reported success with no tracks table")
		}
	})

	// The stamp is read from tracks.scanned_at. Losing just that column leaves
	// adoption working and breaks only the stamp, which is the one ordering
	// this loop depends on.
	t.Run("no scanned_at column", func(t *testing.T) {
		s := openStore(t)
		addSource(t, s, store.Source{Kind: store.SourceFolder, Locator: t.TempDir(), Enabled: true})
		if _, err := s.DB().Exec(`ALTER TABLE tracks DROP COLUMN scanned_at`); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanSources(ctx, s, nil); err == nil {
			t.Error("a scan with no way to stamp its rows reported success")
		}
	})

	// A trigger that refuses exactly the write MarkMissing makes, so the walk
	// and adoption still succeed and only the marking fails. A read-only
	// database cannot show this: adoption would fail first.
	t.Run("marking refused", func(t *testing.T) {
		s := openStore(t)
		id := addSource(t, s, store.Source{Kind: store.SourceFolder,
			Locator: t.TempDir(), Enabled: true})
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (path, playable, source_id, scanned_at) VALUES ('/gone.mp3', 1, ?, 1)`,
			id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(`CREATE TRIGGER no_mark BEFORE UPDATE OF missing_at ON tracks
			BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanSources(ctx, s, nil); err == nil {
			t.Error("a scan that could not mark a missing track reported success")
		}
	})

	t.Run("read-only subsonic adoption", func(t *testing.T) {
		s := openStore(t)
		f := &fakeLib{songs: []map[string]any{song("s1", "Creep")}}
		base := f.start(t)
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (path, playable) VALUES (?, 1)`,
			base+"/rest/stream.view?id=s1&u=andrew"); err != nil {
			t.Fatal(err)
		}
		addSource(t, s, store.Source{Kind: store.SourceSubsonic, Locator: base,
			Username: "u", Password: "p", Enabled: true})
		s.DB().SetMaxOpenConns(1)
		if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanSources(ctx, s, nil); err == nil {
			t.Error("claiming a row reported success against a read-only database")
		}
	})

	// Both places a driver can fail part way through a result set share one
	// wrapper, and neither is reachable against a working SQLite file.
	t.Run("mid-iteration driver failure", func(t *testing.T) {
		if readFailure(nil, "x") != nil {
			t.Error("readFailure invented an error")
		}
		err := readFailure(errStub, "http://nas:4533")
		if err == nil || !strings.Contains(err.Error(), "http://nas:4533") {
			t.Errorf("readFailure lost the source: %v", err)
		}
	})

	t.Run("adoption on a read-only store", func(t *testing.T) {
		s := openStore(t)
		addSource(t, s, store.Source{Kind: store.SourceFolder, Locator: t.TempDir(), Enabled: true})
		s.DB().SetMaxOpenConns(1)
		if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
			t.Fatal(err)
		}
		if _, err := ScanSources(ctx, s, nil); err == nil {
			t.Error("adoption reported success against a read-only database")
		}
	})
}
