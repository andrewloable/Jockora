// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/store"
)

func seedStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSeedSourcesFromFolderFlag(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()

	n, err := SeedSources(ctx, s, &config.Config{LibraryPath: "/music"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("seeded %d sources, want 1", n)
	}
	list, err := s.ListSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("%d rows, want 1", len(list))
	}
	if list[0].Kind != store.SourceFolder || list[0].Locator != "/music" {
		t.Errorf("seeded %+v, want a folder source at /music", list[0])
	}
	if !list[0].Enabled {
		t.Error("the seeded source arrived disabled, so a v0.1 config would scan nothing")
	}
}

func TestSeedSourcesFromSubsonicFlag(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()

	n, err := SeedSources(ctx, s, &config.Config{
		SubsonicURL: "http://nas:4533", SubsonicUser: "andrew", SubsonicPassword: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("seeded %d sources, want 1", n)
	}
	list, _ := s.ListSources(ctx)
	if list[0].Kind != store.SourceSubsonic || list[0].Locator != "http://nas:4533" {
		t.Errorf("seeded %+v, want a subsonic source", list[0])
	}
	// Without the credentials the row is useless: every OpenSubsonic request
	// re-derives a salted token from the password.
	if list[0].Username != "andrew" || list[0].Password != "hunter2" {
		t.Errorf("credentials were not carried across: %+v", list[0])
	}
}

// TestSeedSourcesSubsonicWinsOverFolder pins the precedence to the one
// librarySource already uses in cmd/jockora/serve.go. If the seed disagreed,
// the first run after upgrading would scan a different library than the last
// run before it, with nothing to say why.
func TestSeedSourcesSubsonicWinsOverFolder(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()

	n, err := SeedSources(ctx, s, &config.Config{
		LibraryPath: "/music", SubsonicURL: "http://nas:4533", SubsonicUser: "andrew"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("seeded %d sources, want 1", n)
	}
	list, _ := s.ListSources(ctx)
	if list[0].Kind != store.SourceSubsonic {
		t.Errorf("seeded a %s source, want subsonic to win as librarySource does", list[0].Kind)
	}
}

func TestSeedSourcesSkipsWhenNotEmpty(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()
	if _, err := s.CreateSource(ctx, store.Source{
		Kind: store.SourceFolder, Locator: "/somewhere/else", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	// Even a DISABLED row counts. An operator who turned their only source off
	// must not find it silently replaced by the old flag on the next restart.
	if err := s.SetSourceEnabled(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	n, err := SeedSources(ctx, s, &config.Config{LibraryPath: "/music"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("seeded %d sources over an existing one, want 0", n)
	}
	list, _ := s.ListSources(ctx)
	if len(list) != 1 || list[0].Locator != "/somewhere/else" {
		t.Errorf("the operator's source was disturbed: %+v", list)
	}
}

// TestSeedSourcesNoFlagsNoRows: a config with neither flag is how v0.2 will
// normally start once sources are managed in the console. It is not an error.
func TestSeedSourcesNoFlagsNoRows(t *testing.T) {
	s := seedStore(t)
	ctx := context.Background()

	n, err := SeedSources(ctx, s, &config.Config{})
	if err != nil {
		t.Fatalf("an unconfigured library was treated as an error: %v", err)
	}
	if n != 0 {
		t.Fatalf("seeded %d sources from no flags", n)
	}
	if list, _ := s.ListSources(ctx); len(list) != 0 {
		t.Errorf("rows appeared from nowhere: %+v", list)
	}
}

func TestSeedSourcesSurfacesFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("closed store", func(t *testing.T) {
		s := seedStore(t)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := SeedSources(ctx, s, &config.Config{LibraryPath: "/music"}); err == nil {
			t.Error("SeedSources succeeded on a closed store")
		}
	})

	// A closed store fails at the read and never reaches the insert, so the
	// write branch needs a database that reads but does not write.
	t.Run("read-only store", func(t *testing.T) {
		s := seedStore(t)
		s.DB().SetMaxOpenConns(1) // or the pragma lands on another connection
		if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
			t.Fatal(err)
		}
		if _, err := SeedSources(ctx, s, &config.Config{LibraryPath: "/music"}); err == nil {
			t.Error("SeedSources reported a seeded source it could not write")
		}
	})
}
