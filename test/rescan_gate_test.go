// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

//go:build gates

// Package test holds the row gates: integration tests that run against real
// files and the real tools, which is the whole point of them. Every unit task
// behind row 11 used fakes, and a fake cannot tell you whether ffprobe was
// actually called.
//
// Build-tagged so `go test ./...` neither compiles nor runs them.
package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/library"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// probeCounter puts a shim ahead of the real ffprobe on PATH and counts how
// often the scanner reaches for it.
//
// This is the only way to prove the skip actually skips. A scan that re-probed
// every unchanged file would still pass every unit test in 11a and 11b -- and
// would turn a nightly rescan of 7,595 tracks from seconds into an hour.
type probeCounter struct{ path string }

func newProbeCounter(t *testing.T) *probeCounter {
	t.Helper()
	real, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skipf("ffprobe is not installed: %v", err)
	}
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	shim := filepath.Join(dir, "ffprobe")
	script := "#!/bin/sh\necho x >> " + strconv.Quote(counter) + "\nexec " +
		strconv.Quote(real) + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &probeCounter{path: counter}
}

func (p *probeCounter) count(t *testing.T) int {
	t.Helper()
	body, err := os.ReadFile(p.path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(body), "x")
}

func (p *probeCounter) reset(t *testing.T) {
	t.Helper()
	if err := os.Remove(p.path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func makeAudio(t *testing.T, dir, name, artist, title string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=1",
		"-ac", "2", "-metadata", "artist="+artist, "-metadata", "title="+title,
		"-metadata", "album=Gate", "-metadata", "date=1987", path).CombinedOutput()
	if err != nil {
		t.Fatalf("making %s: %v: %s", name, err, out)
	}
	return path
}

func gateStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// realSource builds a folder of three real audio files and scans it.
func realSource(t *testing.T) (*store.Store, string, *probeCounter) {
	t.Helper()
	counter := newProbeCounter(t)
	s := gateStore(t)
	root := t.TempDir()
	for _, n := range []string{"one.mp3", "two.mp3", "three.mp3"} {
		makeAudio(t, root, n, "Artist", "Title "+n)
	}
	if _, err := s.CreateSource(context.Background(), store.Source{
		Kind: store.SourceFolder, Locator: root, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := library.ScanSources(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}
	return s, root, counter
}

func TestRescanGateDeleteIsMarked(t *testing.T) {
	s, root, counter := realSource(t)
	ctx := context.Background()

	if counter.count(t) < 3 {
		t.Fatalf("the first scan probed %d files, want at least 3", counter.count(t))
	}
	// Tag everything, so the tag pool would hold all three if nothing filtered.
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

	gone := filepath.Join(root, "two.mp3")
	var goneID int64
	if err := s.DB().QueryRow(`SELECT id FROM tracks WHERE path = ?`, gone).Scan(&goneID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	counter.reset(t)
	stats, err := library.ScanSources(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}

	// NOT ONE ffprobe. The two surviving files are unchanged, and re-probing
	// them is the difference between a rescan that takes seconds and one that
	// takes an hour.
	if n := counter.count(t); n != 0 {
		t.Errorf("the rescan probed %d unchanged files, want 0", n)
	}
	if stats.Marked != 1 {
		t.Errorf("Marked = %d, want 1", stats.Marked)
	}

	var missingAt *int64
	if err := s.DB().QueryRow(`SELECT missing_at FROM tracks WHERE id = ?`, goneID).
		Scan(&missingAt); err != nil {
		t.Fatal(err)
	}
	if missingAt == nil {
		t.Error("the deleted file was not marked missing")
	}

	// A Selector built from the pool must never be able to offer it: the
	// stream would find out mid-transition that the file is not there.
	pool, err := station.PoolForTag(ctx, s, "rock")
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 2 {
		t.Fatalf("the pool holds %d tracks, want 2", len(pool))
	}
	sel := station.NewSelector(pool, 1)
	for i := 0; i < 20; i++ {
		id, err := sel.Next()
		if err != nil {
			t.Fatal(err)
		}
		if id == goneID {
			t.Fatalf("the selector offered the deleted track on draw %d", i)
		}
	}
}

func TestRescanGateAddAppears(t *testing.T) {
	s, root, counter := realSource(t)
	ctx := context.Background()

	makeAudio(t, root, "four.mp3", "New Artist", "New Title")
	counter.reset(t)
	stats, err := library.ScanSources(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Added != 1 {
		t.Errorf("Added = %d, want 1", stats.Added)
	}
	// Exactly one: the new file, and none of the three that had not changed.
	if n := counter.count(t); n != 1 {
		t.Errorf("the rescan probed %d files, want only the new one", n)
	}

	var artist, title string
	var playable int
	if err := s.DB().QueryRow(
		`SELECT artist, title, playable FROM tracks WHERE path = ?`,
		filepath.Join(root, "four.mp3")).Scan(&artist, &title, &playable); err != nil {
		t.Fatalf("the new file did not appear: %v", err)
	}
	if artist != "New Artist" || title != "New Title" || playable != 1 {
		t.Errorf("the new row reads %q / %q / playable=%d", artist, title, playable)
	}
	if stats.Marked != 0 {
		t.Errorf("Marked = %d when nothing was removed", stats.Marked)
	}
}

func TestRescanGateFolderAndSubsonicCoexist(t *testing.T) {
	s, _, _ := realSource(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{
				"status": "ok", "album": map[string]any{"song": []map[string]any{
					{"id": "s1", "title": "Remote", "artist": "Remote Artist",
						"album": "Remote Album", "year": 2001, "duration": 100, "size": 900},
				}}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	subID, err := s.CreateSource(ctx, store.Source{Kind: store.SourceSubsonic,
		Locator: srv.URL, Username: "andrew", Password: "hunter2", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := library.ScanSources(ctx, s, nil); err != nil {
		t.Fatal(err)
	}

	rows, err := s.DB().Query(`SELECT path, source_id FROM tracks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	bySource := map[int64][]string{}
	for rows.Next() {
		var path string
		var sid int64
		if err := rows.Scan(&path, &sid); err != nil {
			t.Fatal(err)
		}
		bySource[sid] = append(bySource[sid], path)
	}
	if len(bySource) != 2 {
		t.Fatalf("%d distinct source_ids, want 2: %v", len(bySource), bySource)
	}
	if len(bySource[subID]) != 1 {
		t.Errorf("the subsonic source owns %d tracks, want 1", len(bySource[subID]))
	}

	seen := map[string]bool{}
	for sid, paths := range bySource {
		for _, p := range paths {
			if seen[p] {
				t.Errorf("two sources share the path %q", p)
			}
			seen[p] = true
			if sid == subID {
				if !strings.HasPrefix(p, "subsonic:") {
					t.Errorf("remote track stored as %q, want a locator", p)
				}
				// The credential must not be in the row; it lives on the
				// source, so changing the password changes no path.
				if strings.Contains(p, "hunter2") || strings.Contains(p, "t=") {
					t.Errorf("a credential reached the tracks table: %q", p)
				}
			} else if !filepath.IsAbs(p) {
				t.Errorf("local track stored as %q, want an absolute path", p)
			}
		}
	}

	// And the locator has to resolve back to something a decoder can open.
	url, err := library.ResolvePath(ctx, s, bySource[subID][0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(url, srv.URL+"/rest/stream.view") || !strings.Contains(url, "id=s1") {
		t.Errorf("the locator resolved to %q", url)
	}
}
