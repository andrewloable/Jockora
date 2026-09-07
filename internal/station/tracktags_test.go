// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/store"
)

// THE SCANNER READS THE SLEEVE AND THE DJ NEVER SAW IT. names() selected two
// columns out of a row that also holds album and year, so an unenriched record
// reached the writer as a bare title.

func tagWriterStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.DB().Exec(`
		INSERT INTO tracks (id, path, artist, title, album, year, playable)
		     VALUES (1, '/m/1.mp3', 'Bloc Party', 'Banquet', 'Silent Alarm', 2005, 1);
		INSERT INTO tracks (id, path, artist, title, playable)
		     VALUES (2, '/m/2.mp3', 'Nobody', 'Untitled', 1)`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTrackTagsWriterReadsAlbumAndYear(t *testing.T) {
	w := &BreakWriter{Store: tagWriterStore(t)}
	artist, title, album, year := w.names(context.Background(), 1)
	if artist != "Bloc Party" || title != "Banquet" {
		t.Errorf("names = %q / %q", artist, title)
	}
	if album != "Silent Alarm" || year != 2005 {
		t.Errorf("sleeve = %q (%d), want the columns the scanner wrote", album, year)
	}
}

func TestTrackTagsWriterMissingColumnsAreEmpty(t *testing.T) {
	// NULL album and NULL year are the normal case: plenty of files carry
	// neither, and the row still has to read back.
	w := &BreakWriter{Store: tagWriterStore(t)}
	_, _, album, year := w.names(context.Background(), 2)
	if album != "" || year != 0 {
		t.Errorf("sleeve = %q (%d), want empty", album, year)
	}

	// And a track that is not there at all.
	artist, title, album, year := w.names(context.Background(), 404)
	if artist != "" || title != "" || album != "" || year != 0 {
		t.Errorf("a missing track read back as %q / %q / %q (%d)", artist, title, album, year)
	}
}

func TestTrackTagsWriterPutsTheSleeveInThePrompt(t *testing.T) {
	// The wiring, which is the half that gets forgotten: a query widened and
	// nothing passing the new columns on reads exactly like the old bug.
	s := tagWriterStore(t)
	all, err := dj.LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}
	w := &BreakWriter{Store: s, Persona: all[0], Session: dj.NewSession()}
	w.SetContext(0, 1, 2, "", "")

	in, err := w.promptInput(context.Background(), 20, mix.PlacementRamp)
	if err != nil {
		t.Fatal(err)
	}
	if in.CurrentAlbum != "Silent Alarm" || in.CurrentYear != 2005 {
		t.Errorf("prompt input carries %q (%d), want the sleeve", in.CurrentAlbum, in.CurrentYear)
	}
	if in.NextAlbum != "" || in.NextYear != 0 {
		t.Errorf("the untagged next track carries %q (%d)", in.NextAlbum, in.NextYear)
	}
}
