// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

const personaDir = "../../personas"

// v01Fixture builds a REAL v0.1 database: 30 tracks with dossiers across three
// tags, then everything migration 6 adds is removed and the version wound back.
//
// Built rather than checked in, because a binary fixture stops describing the
// schema the moment the schema changes and nobody notices until it is wrong.
func v01Fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v01.db")
	ctx := context.Background()

	s, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	id := 0
	add := func(tag, mood string, n int) {
		for i := 0; i < n; i++ {
			id++
			if _, err := s.DB().Exec(
				`INSERT INTO tracks (id, path, playable) VALUES (?, ?, 1)`,
				id, fmt.Sprintf("/m/%d.mp3", id)); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(&enrich.Dossier{
				StationTags: []string{tag}, Mood: []string{mood},
				Confidence: enrich.ConfidenceHigh})
			if _, err := s.DB().Exec(
				`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, 'high')`,
				id, string(raw)); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("rock", "aggressive", 12)
	add("classical", "calm", 10)
	add("opm", "wistful", 8)

	// Everything migration 6 created, and everything it added. Dropping the
	// tables alone leaves the ADD COLUMNs behind and the migration then fails
	// on a duplicate column -- which is not a v0.1 database, it is a broken one.
	for _, stmt := range []string{
		`DROP TABLE station_tracks`, `DROP TABLE stations`, `DROP TABLE jocks`,
		`DROP TABLE sources`, `DROP TABLE users`, `DROP TABLE settings`,
		`ALTER TABLE tracks DROP COLUMN source_id`,
		`ALTER TABLE tracks DROP COLUMN missing_at`,
		`ALTER TABLE tracks DROP COLUMN genre`,
		`ALTER TABLE ads DROP COLUMN station_id`,
		`ALTER TABLE break_feedback DROP COLUMN user_id`,
		`PRAGMA user_version = 5`,
	} {
		if _, err := s.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := s.DB().Exec(`UPDATE schema_version SET version = 5`); err != nil {
		t.Fatalf("winding the version back: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// openUpgraded reopens the fixture, which runs migration 6 for real.
func openUpgraded(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("upgrading a v0.1 database in place: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func counts(t *testing.T, s *store.Store) (users, sources, jocks, stations int) {
	t.Helper()
	for _, c := range []struct {
		table string
		into  *int
	}{{"users", &users}, {"sources", &sources}, {"jocks", &jocks}, {"stations", &stations}} {
		if err := s.DB().QueryRow(`SELECT count(*) FROM ` + c.table).Scan(c.into); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func upgradeCfg() *config.Config {
	return &config.Config{LibraryPath: "/music", PersonaPath: personaDir}
}

func roster(t *testing.T) []*dj.Persona {
	t.Helper()
	p, err := dj.LoadPersonas(personaDir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUpgradeFromV01Seeds(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()

	if err := seedFromConfig(ctx, s, upgradeCfg(), roster(t), slog.New(
		slog.NewTextHandler(&bytes.Buffer{}, nil))); err != nil {
		t.Fatal(err)
	}

	users, sources, jocks, stations := counts(t, s)
	// NO USERS. Accounts are admin-created and there is no self-registration;
	// seeding one would be a default credential on every install.
	if users != 0 {
		t.Errorf("%d users were seeded, want 0", users)
	}
	if sources != 1 {
		t.Errorf("%d sources, want the one the v0.1 flags describe", sources)
	}
	if jocks != 9 {
		t.Errorf("%d jocks, want the 9 cards in personas/", jocks)
	}

	d, err := station.ProposeDial(ctx, s, roster(t))
	if err != nil {
		t.Fatal(err)
	}
	if stations != len(d.Stations) {
		t.Errorf("%d stations, want %d -- one per proposed bucket", stations, len(d.Stations))
	}
	if stations == 0 {
		t.Fatal("the fixture proposed no stations at all, so this proves nothing")
	}
}

func TestUpgradeIdempotent(t *testing.T) {
	path := v01Fixture(t)
	s := openUpgraded(t, path)
	ctx := context.Background()
	cfg, personas := upgradeCfg(), roster(t)
	quiet := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	if err := seedFromConfig(ctx, s, cfg, personas, quiet); err != nil {
		t.Fatal(err)
	}
	before := fmt.Sprint(counts(t, s))

	// Twice in the same process, and again after a restart: a server that
	// re-seeded on every boot would duplicate the dial nightly.
	if err := seedFromConfig(ctx, s, cfg, personas, quiet); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openUpgraded(t, path)
	if err := seedFromConfig(ctx, s2, cfg, personas, quiet); err != nil {
		t.Fatal(err)
	}
	if after := fmt.Sprint(counts(t, s2)); after != before {
		t.Errorf("counts changed across restarts: %s then %s", before, after)
	}
}

// TestUpgradeDialEqualsProposal is the listener-facing half of the gate: the
// dial they see today is the dial they see tomorrow.
func TestUpgradeDialEqualsProposal(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	personas := roster(t)

	if err := seedFromConfig(ctx, s, upgradeCfg(), personas, slog.New(
		slog.NewTextHandler(&bytes.Buffer{}, nil))); err != nil {
		t.Fatal(err)
	}

	d, err := station.ProposeDial(ctx, s, personas)
	if err != nil {
		t.Fatal(err)
	}
	var proposed []string
	for _, st := range d.Stations {
		proposed = append(proposed, st.Tag+"/"+st.Jock)
	}
	list, err := s.ListStations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var seeded []string
	for _, st := range list {
		seeded = append(seeded, st.Genre+"/"+st.JockID)
	}
	sort.Strings(proposed)
	sort.Strings(seeded)
	if !reflect.DeepEqual(proposed, seeded) {
		t.Errorf("the seeded dial is %v, the proposal is %v", seeded, proposed)
	}
	// Every non-catch-all station must actually have someone on air.
	for _, st := range list {
		if st.Genre != station.UnsortedTag && st.JockID == "" {
			t.Errorf("station %q has no jock", st.Name)
		}
	}
}

// TestUpgradeSeedOrderLogged: the order is load-bearing, not cosmetic. Stations
// reference jocks by foreign key and the proposal reads the library the sources
// describe, so sources then jocks then stations is the only order that works --
// and the log is how an operator sees it happened.
func TestUpgradeSeedOrderLogged(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	var buf bytes.Buffer

	if err := seedFromConfig(context.Background(), s, upgradeCfg(), roster(t),
		slog.New(slog.NewTextHandler(&buf, nil))); err != nil {
		t.Fatal(err)
	}

	logged := buf.String()
	last := -1
	for _, want := range []string{"seeded sources", "seeded jocks", "seeded stations"} {
		at := strings.Index(logged, want)
		if at < 0 {
			t.Fatalf("%q was never logged:\n%s", want, logged)
		}
		if at < last {
			t.Errorf("%q was logged out of order:\n%s", want, logged)
		}
		last = at
	}
}

func TestUpgradeSurfacesFailures(t *testing.T) {
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	t.Run("closed store", func(t *testing.T) {
		s := openUpgraded(t, v01Fixture(t))
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := seedFromConfig(ctx, s, upgradeCfg(), roster(t), quiet); err == nil {
			t.Error("seeding succeeded on a closed store")
		}
	})

	t.Run("malformed persona card", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "broken.toml"), []byte("this is not toml ["), 0o600); err != nil {
			t.Fatal(err)
		}
		s := openUpgraded(t, v01Fixture(t))
		cfg := upgradeCfg()
		cfg.PersonaPath = dir
		if err := seedFromConfig(ctx, s, cfg, nil, quiet); err == nil {
			t.Error("a malformed persona card was seeded silently")
		}
	})

	// An empty persona path must seed NOTHING, not glob the working directory:
	// filepath.Join("", "*.toml") is "*.toml", which matches whatever TOML
	// happens to sit beside the server. This subtest puts a real persona card
	// there, so removing the guard seeds a jock from a file nobody pointed at.
	t.Run("no personas configured", func(t *testing.T) {
		cwd := t.TempDir()
		card, err := os.ReadFile(filepath.Join(personaDir, "roxy_sinclair.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cwd, "roxy_sinclair.toml"), card, 0o600); err != nil {
			t.Fatal(err)
		}
		s := openUpgraded(t, v01Fixture(t))
		cfg := upgradeCfg()
		cfg.PersonaPath = ""
		t.Chdir(cwd)
		// Not an error: a library with no jocks is a shuffle, which is the
		// state Jockora must degrade to rather than refuse to boot.
		if err := seedFromConfig(ctx, s, cfg, nil, quiet); err != nil {
			t.Fatalf("an unconfigured roster refused to boot: %v", err)
		}
		_, _, jocks, stations := counts(t, s)
		if jocks != 0 {
			t.Errorf("%d jocks seeded from no persona directory", jocks)
		}
		if stations == 0 {
			t.Error("no stations without jocks; the dial must still propose")
		}
	})

	t.Run("no playlist table", func(t *testing.T) {
		s := openUpgraded(t, v01Fixture(t))
		if _, err := s.DB().Exec(`DROP TABLE station_tracks`); err != nil {
			t.Fatal(err)
		}
		if err := seedFromConfig(ctx, s, upgradeCfg(), roster(t), quiet); err == nil {
			t.Error("seeding reported success with no playlist table")
		}
	})
}

// TestUpgradeNewSeedsOnStartup is the wiring half of the gate, and the reason
// it exists is measured: with the call deleted from New, every other test in
// this file still passed. A seeder nothing calls is the failure this project
// has hit a dozen times -- code that exists but is not connected.
func TestUpgradeNewSeedsOnStartup(t *testing.T) {
	path := v01Fixture(t)
	s := openUpgraded(t, path)
	ctx := context.Background()

	pool, err := station.PlayablePool(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.LibraryPath, cfg.PersonaPath = "/music", personaDir

	a, err := New(cfg, Options{
		Library:  &Library{Store: s, Selector: station.NewSelector(pool, 1)},
		Personas: roster(t),
		Log:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// New spawns the encoder under its own context, so it has to be run and
	// stopped rather than dropped on the floor.
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	cancel()
	<-done

	_, sources, jocks, stations := counts(t, s)
	if sources != 1 || jocks != 9 || stations == 0 {
		t.Errorf("starting the app seeded sources=%d jocks=%d stations=%d; "+
			"want 1, 9 and at least one", sources, jocks, stations)
	}
}
