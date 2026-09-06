// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/errs"
	"github.com/andrewloable/jockora/internal/store"
)

// makeAudio writes a real audio file with real tags, so the scanner is tested
// against what ffprobe actually reports rather than a mock.
func makeAudio(t *testing.T, dir, name, artist, title, album string, seconds int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=" + strconv.Itoa(seconds),
		"-ac", "2",
	}
	if artist != "" {
		args = append(args, "-metadata", "artist="+artist)
	}
	if title != "" {
		args = append(args, "-metadata", "title="+title)
	}
	if album != "" {
		args = append(args, "-metadata", "album="+album)
	}
	args = append(args, "-metadata", "date=1987", path)

	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("making %s: %v: %s", name, err, out)
	}
	return path
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// library builds a root with three audio files, two non-audio files and one
// unreadable one.
func library(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	makeAudio(t, root, "a.mp3", "Artist A", "Title A", "Album A", 2)
	makeAudio(t, root, "b.flac", "Artist B", "Title B", "Album B", 2)
	sub := filepath.Join(root, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	makeAudio(t, sub, "c.m4a", "", "", "", 2) // no tags at all, which is common

	for _, f := range []struct{ name, body string }{
		{"cover.jpg", "not audio"},
		{"notes.txt", "also not audio"},
	} {
		if err := os.WriteFile(filepath.Join(root, f.name), []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func countTracks(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM tracks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScanFindsAudioFiles(t *testing.T) {
	s := openStore(t)
	root := library(t)

	stats, err := Scan(context.Background(), s, root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if got := countTracks(t, s); got != 3 {
		t.Errorf("%d track rows, want 3 (the two non-audio files must be ignored)", got)
	}
	if stats.Added != 3 {
		t.Errorf("Stats.Added = %d, want 3", stats.Added)
	}

	var artist, title, album string
	var year int
	var duration float64
	err = s.DB().QueryRow(
		`SELECT artist, title, album, year, duration_s FROM tracks WHERE path = ?`,
		filepath.Join(root, "a.mp3"),
	).Scan(&artist, &title, &album, &year, &duration)
	if err != nil {
		t.Fatalf("reading a.mp3 row: %v", err)
	}
	if artist != "Artist A" || title != "Title A" || album != "Album A" {
		t.Errorf("tags = %q/%q/%q, want Artist A/Title A/Album A", artist, title, album)
	}
	if year != 1987 {
		t.Errorf("year = %d, want 1987", year)
	}
	if duration < 1.9 || duration > 2.1 {
		t.Errorf("duration_s = %v, want about 2", duration)
	}
}

// TestScanFindsUntaggedFiles: artist and title are frequently empty in a real
// library and must not cause a skip.
func TestScanFindsUntaggedFiles(t *testing.T) {
	s := openStore(t)
	root := library(t)

	if _, err := Scan(context.Background(), s, root); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	var playable int
	err := s.DB().QueryRow(
		`SELECT playable FROM tracks WHERE path = ?`,
		filepath.Join(root, "nested", "c.m4a"),
	).Scan(&playable)
	if err != nil {
		t.Fatalf("the untagged nested file was not recorded: %v", err)
	}
	if playable != 1 {
		t.Errorf("an untagged but perfectly playable file was marked playable=%d", playable)
	}
}

func TestScanIsIdempotent(t *testing.T) {
	s := openStore(t)
	root := library(t)

	if _, err := Scan(context.Background(), s, root); err != nil {
		t.Fatal(err)
	}
	stats, err := Scan(context.Background(), s, root)
	if err != nil {
		t.Fatal(err)
	}

	if got := countTracks(t, s); got != 3 {
		t.Errorf("%d rows after two scans, want 3", got)
	}
	if stats.Added != 0 {
		t.Errorf("second scan added %d rows, want 0", stats.Added)
	}
	if stats.Updated != 3 {
		t.Errorf("second scan updated %d rows, want 3", stats.Updated)
	}
}

// TestScanPreservesEnrichmentOnRescan is the expensive mistake to avoid: a
// rescan must refresh tags without discarding the loudness, dossier-adjacent and
// ramp columns that cost hours of work to produce.
func TestScanPreservesEnrichmentOnRescan(t *testing.T) {
	s := openStore(t)
	root := library(t)
	if _, err := Scan(context.Background(), s, root); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "a.mp3")
	if _, err := s.DB().Exec(
		`UPDATE tracks SET loudness_lufs = ?, ramp_s = ?, outro_s = ?, ramp_confidence = ?, bpm = ?, no_crossfade_next = 1
		 WHERE path = ?`, -14.5, 12.5, 180.0, "high", 128.0, path); err != nil {
		t.Fatal(err)
	}

	if _, err := Scan(context.Background(), s, root); err != nil {
		t.Fatal(err)
	}

	var lufs, ramp, outro, bpm float64
	var conf string
	var noXf int
	err := s.DB().QueryRow(
		`SELECT loudness_lufs, ramp_s, outro_s, ramp_confidence, bpm, no_crossfade_next FROM tracks WHERE path = ?`,
		path).Scan(&lufs, &ramp, &outro, &conf, &bpm, &noXf)
	if err != nil {
		t.Fatal(err)
	}
	if lufs != -14.5 || ramp != 12.5 || outro != 180.0 || conf != "high" || bpm != 128.0 || noXf != 1 {
		t.Errorf("a rescan discarded enrichment: lufs=%v ramp=%v outro=%v conf=%q bpm=%v no_crossfade=%d",
			lufs, ramp, outro, conf, bpm, noXf)
	}
}

func TestScanSkipsUnreadableFile(t *testing.T) {
	s := openStore(t)
	root := library(t)
	empty := filepath.Join(root, "broken.mp3")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := Scan(context.Background(), s, root)
	if err != nil {
		t.Fatalf("Scan aborted on an unreadable file: %v", err)
	}

	if got := countTracks(t, s); got != 4 {
		t.Errorf("%d rows, want 4: the broken file should be recorded, not skipped", got)
	}
	var playable int
	if err := s.DB().QueryRow(`SELECT playable FROM tracks WHERE path = ?`, empty).Scan(&playable); err != nil {
		t.Fatalf("the broken file was not recorded at all: %v", err)
	}
	if playable != 0 {
		t.Errorf("playable = %d for a 0-byte file, want 0", playable)
	}
	if stats.Unplayable != 1 {
		t.Errorf("Stats.Unplayable = %d, want 1", stats.Unplayable)
	}
	// And the good files were still ingested: one bad file must not cost the run.
	var good int
	if err := s.DB().QueryRow(`SELECT count(*) FROM tracks WHERE playable = 1`).Scan(&good); err != nil {
		t.Fatal(err)
	}
	if good != 3 {
		t.Errorf("%d playable tracks, want 3", good)
	}
}

// TestScanNeverWritesToLibrary is the invariant, not a preference: Jockora
// layers on top of Navidrome and must never become a competing writer.
func TestScanNeverWritesToLibrary(t *testing.T) {
	s := openStore(t)
	root := library(t)

	type snap struct {
		mode os.FileMode
		size int64
		mod  time.Time
	}
	before := map[string]snap{}
	walk := func(into map[string]snap) {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			into[p] = snap{fi.Mode(), fi.Size(), fi.ModTime()}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	walk(before)

	if _, err := Scan(context.Background(), s, root); err != nil {
		t.Fatal(err)
	}

	after := map[string]snap{}
	walk(after)

	if len(before) != len(after) {
		t.Fatalf("the library gained or lost files: %d before, %d after", len(before), len(after))
	}
	for p, b := range before {
		a, ok := after[p]
		if !ok {
			t.Errorf("%s disappeared", p)
			continue
		}
		if a != b {
			t.Errorf("%s was modified: mode %v->%v size %d->%d mtime %v->%v",
				p, b.mode, a.mode, b.size, a.size, b.mod, a.mod)
		}
	}
}

func TestScanRejectsMissingRoot(t *testing.T) {
	s := openStore(t)
	if _, err := Scan(context.Background(), s, filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("Scan accepted a root that does not exist")
	}
}

func TestScanHonoursContextCancel(t *testing.T) {
	s := openStore(t)
	root := library(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Scan(ctx, s, root); err == nil {
		t.Error("Scan ignored a cancelled context")
	}
}

// TestScanRecordsScannedAt: the enrichment queue needs to know what is new.
func TestScanRecordsScannedAt(t *testing.T) {
	s := openStore(t)
	root := library(t)
	start := time.Now().Unix()

	if _, err := Scan(context.Background(), s, root); err != nil {
		t.Fatal(err)
	}

	var at int64
	if err := s.DB().QueryRow(`SELECT scanned_at FROM tracks WHERE path = ?`,
		filepath.Join(root, "a.mp3")).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at < start-5 || at > time.Now().Unix()+5 {
		t.Errorf("scanned_at = %d, want about %d", at, start)
	}
}

// TestScanNamesTheFilesItRejected. A count of unplayable files sends an
// operator hunting through ten thousand of them for the four that failed. The
// class is errs.ErrUnsupportedFormat so a caller can tell a bad file from an
// I/O error, and it is detected at SCAN time -- a scan may take an hour, a
// stream may not discover mid-transition that the next track is a video file.
func TestScanNamesTheFilesItRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "not-audio.mp3"), []byte("this is not an mp3"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := openStore(t)
	stats, err := Scan(context.Background(), s, dir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if stats.Unplayable != 1 {
		t.Fatalf("Unplayable = %d, want 1", stats.Unplayable)
	}
	if len(stats.Rejected) != 1 {
		t.Fatalf("Rejected has %d entries, want 1", len(stats.Rejected))
	}
	if !errors.Is(stats.Rejected[0], errs.ErrUnsupportedFormat) {
		t.Errorf("rejection = %v, want ErrUnsupportedFormat", stats.Rejected[0])
	}
	if !strings.Contains(stats.Rejected[0].Error(), "not-audio.mp3") {
		t.Errorf("rejection does not name the file: %v", stats.Rejected[0])
	}

	// And it is recorded as unplayable, so selection never offers it.
	var playable int
	if err := s.DB().QueryRow(`SELECT playable FROM tracks WHERE path LIKE '%not-audio.mp3'`).Scan(&playable); err != nil {
		t.Fatal(err)
	}
	if playable != 0 {
		t.Error("a file ffprobe could not read is still marked playable")
	}
}

// TestScanReportsProgress. A ten-thousand-file library takes minutes because
// every file is probed with ffprobe, and a scan that prints nothing is
// indistinguishable from a hang -- which is how it was read the first time a
// real library was pointed at a redeployed station.
func TestScanReportsProgress(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		makeAudio(t, dir, "t"+strconv.Itoa(i)+".mp3", "Artist", "Title", "Album", 1)
	}

	var seen []int
	var lastPath string
	s := openStore(t)
	stats, err := ScanWithProgress(context.Background(), s, dir, func(found int, path string) {
		seen = append(seen, found)
		lastPath = path
	})
	if err != nil {
		t.Fatalf("ScanWithProgress: %v", err)
	}

	if len(seen) != stats.Found {
		t.Errorf("progress fired %d times for %d files", len(seen), stats.Found)
	}
	for i, n := range seen {
		if n != i+1 {
			t.Errorf("progress reported %d at call %d; the count must be monotonic", n, i+1)
		}
	}
	if lastPath == "" {
		t.Error("progress reported no path, so a scan stuck on one file cannot be identified")
	}

	// Scan without a callback must behave identically.
	if _, err := Scan(context.Background(), openStore(t), dir); err != nil {
		t.Errorf("Scan without progress: %v", err)
	}
}
