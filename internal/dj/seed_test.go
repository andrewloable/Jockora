// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

func seedStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSeedJocksFromDir(t *testing.T) {
	s := seedStore(t)

	n, err := SeedJocks(context.Background(), s, "testdata/personas")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("inserted %d, want 2", n)
	}
	list, err := s.ListJocks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("%d rows, want 2", len(list))
	}
	if list[0].Name != "Alpha Jock" || list[0].VoiceID != "kokoro:am_puck" {
		t.Errorf("first row = %+v", list[0])
	}
	if len(list[0].Forbidden) != 2 {
		t.Errorf("the forbidden list did not survive: %#v", list[0].Forbidden)
	}
}

func TestSeedJocksIdempotent(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()

	if _, err := SeedJocks(ctx, s, "testdata/personas"); err != nil {
		t.Fatal(err)
	}
	n, err := SeedJocks(ctx, s, "testdata/personas")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the second seed inserted %d, want 0", n)
	}
	list, _ := s.ListJocks(ctx)
	if len(list) != 2 {
		t.Errorf("%d rows after seeding twice, want 2", len(list))
	}
}

// TestSeedJocksDoesNotOverwriteEdits is the rule that makes the console usable.
//
// The cards are a SEED, not a source of truth. An operator who rewrites a jock
// must not find the shipped text quietly restored on the next restart -- that
// is the kind of bug someone loses an evening's work to and then stops trusting
// the tool.
func TestSeedJocksDoesNotOverwriteEdits(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()

	if _, err := SeedJocks(ctx, s, "testdata/personas"); err != nil {
		t.Fatal(err)
	}
	edited, err := s.GetJock(ctx, "alpha_jock")
	if err != nil {
		t.Fatal(err)
	}
	edited.Personality = "Rewritten by the operator, at some length."
	edited.GoodForGenres = []string{"jazz"}
	if err := s.UpsertJock(ctx, edited); err != nil {
		t.Fatal(err)
	}

	if _, err := SeedJocks(ctx, s, "testdata/personas"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetJock(ctx, "alpha_jock")
	if err != nil {
		t.Fatal(err)
	}
	if got.Personality != "Rewritten by the operator, at some length." {
		t.Errorf("the edit was overwritten by the seed: %q", got.Personality)
	}
	if len(got.GoodForGenres) != 1 || got.GoodForGenres[0] != "jazz" {
		t.Errorf("the edited genres were overwritten: %#v", got.GoodForGenres)
	}
}

// TestSeedJocksRoundTripsPersona: the console edits a row and the break writer
// reads a Persona, so anything lost in conversion is a jock that sounds subtly
// different after an edit that changed nothing.
func TestSeedJocksRoundTripsPersona(t *testing.T) {
	cards, err := filepath.Glob("../../personas/*.toml")
	if err != nil || len(cards) == 0 {
		t.Fatalf("no persona cards found: %v", err)
	}

	for _, card := range cards {
		original, err := LoadPersona(card)
		if err != nil {
			t.Fatalf("%s: %v", card, err)
		}
		back := FromRecord(original.Record())
		if !reflect.DeepEqual(original, back) {
			t.Errorf("%s did not survive Record/FromRecord:\n  before %+v\n  after  %+v",
				filepath.Base(card), original, back)
		}
	}
}

// TestSeedJocksBadCardSkippedNotFatal: one malformed file in a directory of
// nine must not leave the station with no jocks at all.
func TestSeedJocksBadCardSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	good, err := os.ReadFile("testdata/personas/alpha.toml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.toml"), good, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.toml"),
		[]byte("this is not = valid toml ][\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A card that parses but is missing a required field must also be skipped.
	if err := os.WriteFile(filepath.Join(dir, "incomplete.toml"),
		[]byte("id = \"x\"\nname = \"X\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := seedStore(t)
	n, err := SeedJocks(context.Background(), s, dir)

	if n != 1 {
		t.Errorf("inserted %d, want 1 -- the good card must still get in", n)
	}
	if err == nil {
		t.Fatal("the bad cards were skipped silently; nothing says which failed")
	}
	if !strings.Contains(err.Error(), "broken.toml") {
		t.Errorf("the error does not name the malformed file: %v", err)
	}
	list, _ := s.ListJocks(context.Background())
	if len(list) != 1 || list[0].ID != "alpha_jock" {
		t.Errorf("rows = %+v", list)
	}
}

// TestSeedJocksSurfacesStoreErrors: seeding against a broken database must not
// report success.
func TestSeedJocksSurfacesStoreErrors(t *testing.T) {
	s := seedStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedJocks(context.Background(), s, "testdata/personas"); err == nil {
		t.Error("seeding a closed database reported success")
	}
}

// TestSeedJocksBadGlobIsAnError guards the one path that is not a card problem.
func TestSeedJocksBadGlobIsAnError(t *testing.T) {
	s := seedStore(t)
	if _, err := SeedJocks(context.Background(), s, "["); err == nil {
		t.Error("a malformed directory pattern reported success")
	}
}

// TestSeedJocksInsertFailureIsCollected: a card that loads but cannot be
// written must be reported rather than counted.
func TestSeedJocksInsertFailureIsCollected(t *testing.T) {
	s := seedStore(t)
	s.DB().SetMaxOpenConns(1)
	if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.DB().Exec(`PRAGMA query_only = 0`) })

	n, err := SeedJocks(context.Background(), s, "testdata/personas")
	if n != 0 {
		t.Errorf("inserted %d against a read-only database", n)
	}
	if err == nil {
		t.Error("a failed insert was not reported")
	}
}
