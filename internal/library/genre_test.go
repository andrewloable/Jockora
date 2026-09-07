// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"database/sql"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// The file's genre tag is kept so enrichment can be ordered by what the
// operator's stations need. Two things have to hold for that to be worth
// anything: the tag has to land, and an EXISTING library has to fill the column
// in -- migration 8 leaves every old row NULL, and the scanner skips unchanged
// files, so without care the column stays empty forever and the whole feature
// is a no-op on the only libraries that matter.

func makeGenreAudio(t *testing.T, dir, name, genre string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=" + strconv.Itoa(1),
		"-ac", "2", "-metadata", "artist=A", "-metadata", "title=T",
	}
	if genre != "" {
		args = append(args, "-metadata", "genre="+genre)
	}
	args = append(args, path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("making %s: %v: %s", name, err, out)
	}
	return path
}

func storedGenre(t *testing.T, s interface{ DB() *sql.DB }, path string) sql.NullString {
	t.Helper()
	var g sql.NullString
	if err := s.DB().QueryRow(`SELECT genre FROM tracks WHERE path = ?`, path).Scan(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGenreTagIsKept(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t)
	tagged := makeGenreAudio(t, dir, "rock.mp3", "Hard Rock")
	untagged := makeGenreAudio(t, dir, "plain.mp3", "")

	if _, err := (Folder{Root: dir}).Scan(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}

	if got := storedGenre(t, s, tagged); got.String != "Hard Rock" {
		t.Errorf("genre = %q, want %q", got.String, "Hard Rock")
	}
	// A file with no tag stores EMPTY, not NULL. The difference is what stops
	// the backfill below re-probing it on every scan for the rest of time.
	if got := storedGenre(t, s, untagged); !got.Valid {
		t.Error("a file with no genre tag stored NULL; it must store empty")
	}
}

func TestGenreBackfillsAnExistingLibraryOnce(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t)
	path := makeGenreAudio(t, dir, "rock.mp3", "Rock")

	ctx := context.Background()
	if _, err := (Folder{Root: dir}).Scan(ctx, s, nil); err != nil {
		t.Fatal(err)
	}

	// Exactly what migration 8 leaves behind on a library scanned by an older
	// build: the row is there, size and mtime match, genre is NULL.
	if _, err := s.DB().Exec(`UPDATE tracks SET genre = NULL WHERE path = ?`, path); err != nil {
		t.Fatal(err)
	}

	stats, err := (Folder{Root: dir}).Scan(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 0 {
		t.Errorf("skipped %d: a row missing the new column is not unchanged", stats.Skipped)
	}
	if got := storedGenre(t, s, path); got.String != "Rock" {
		t.Errorf("after the backfill genre = %q, want Rock", got.String)
	}

	// And ONCE: the next scan skips it again, or an established library pays
	// for a full re-probe on every scan forever.
	again, err := (Folder{Root: dir}).Scan(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Skipped != 1 {
		t.Errorf("skipped %d on the third scan, want 1: the backfill must not repeat", again.Skipped)
	}
}

func TestGenreUntaggedFileIsNotReprobedForever(t *testing.T) {
	// The case the empty-versus-NULL distinction exists for.
	dir := t.TempDir()
	s := openStore(t)
	makeGenreAudio(t, dir, "plain.mp3", "")

	ctx := context.Background()
	if _, err := (Folder{Root: dir}).Scan(ctx, s, nil); err != nil {
		t.Fatal(err)
	}
	stats, err := (Folder{Root: dir}).Scan(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Skipped != 1 {
		t.Errorf("skipped %d, want 1: a file with no genre tag must still be skipped", stats.Skipped)
	}
}
