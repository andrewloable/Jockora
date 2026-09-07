// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/library"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// TestLibraryNextResolvesLocators is the wiring test for 11a's change of path
// format. A subsonic track is stored as "subsonic:<source>:<song>", which no
// decoder can open; if next() handed that straight to the decoder every remote
// library would be silently off air, and a resolver nothing calls would be one
// more subsystem built and never connected.
func TestLibraryNextResolvesLocators(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()

	id, err := s.CreateSource(ctx, store.Source{
		Kind: store.SourceSubsonic, Locator: "http://nas:4533",
		Username: "andrew", Password: "hunter2", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO tracks (id, path, artist, title, playable, source_id) VALUES (?, ?, 'A', 'T', 1, ?)`,
		9001, library.SubsonicLocator(id, "s1"), id); err != nil {
		t.Fatal(err)
	}

	l := &Library{Store: s, Selector: station.NewSelector([]int64{9001}, 1)}
	got, err := l.next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.path, "http://nas:4533/rest/stream.view") {
		t.Fatalf("next() returned %q, which no decoder can open", got.path)
	}
	if !strings.Contains(got.path, "id=s1") || !strings.Contains(got.path, "t=") {
		t.Errorf("the resolved URL is missing the song or its credentials: %q", got.path)
	}

	// A local path must survive untouched.
	if _, err := s.DB().Exec(
		`INSERT INTO tracks (id, path, artist, title, playable) VALUES (9002, '/music/a.mp3', 'A', 'T', 1)`,
	); err != nil {
		t.Fatal(err)
	}
	l = &Library{Store: s, Selector: station.NewSelector([]int64{9002}, 1)}
	if got, err := l.next(ctx); err != nil || got.path != "/music/a.mp3" {
		t.Errorf("a local path came back as %q, %v", got.path, err)
	}
}

// TestLibraryRescanReachesNowJson is the wiring half of 11c: a Rescan nobody
// can start, or whose progress never reaches the page, is a subsystem that
// exists rather than one that works.
func TestLibraryRescanReachesNowJson(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	pool, err := station.PlayablePool(context.Background(), s)
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
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	defer func() { cancel(); <-done }()

	// Nothing asked for yet: the block must be absent, not zeroed.
	if a.Status().Rescan != nil {
		t.Error("a station that never rescanned reports a scan")
	}

	// A listener arrives, so a station actually runs: the per-station status
	// reads its ring and its encoder, which only exist once one is on air.
	a.tracker.Touch(1, "s1")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && len(a.mgr.Running()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if len(a.mgr.Running()) == 0 {
		t.Fatal("no station started for a listener")
	}
	running := a.mgr.Running()[0]
	if st := a.StatusForStation(running); st.Health.FFmpeg == "" {
		t.Errorf("a running station reports %+v", st)
	}
	// A station that is not running still answers, without a neighbour's
	// numbers.
	if st := a.StatusForStation(9999); st.Metrics.RingOccupancyS != 0 {
		t.Errorf("an idle station reports occupancy %v", st.Metrics.RingOccupancyS)
	}

	if err := a.StartRescan(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p := a.Status().Rescan; p != nil && !p.Running {
			if p.StartedAt.IsZero() || p.FinishedAt.IsZero() {
				t.Errorf("the scan reached /now.json without its times: %+v", p)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the rescan never reached /now.json: %+v", a.Status().Rescan)
}

// TestLibraryEnrichmentReachesNowJson: the Enrichment block has existed in the
// status type since the spine and was never filled, so /now.json has carried it
// empty and the operator page has always shown nothing there.
func TestLibraryEnrichmentReachesNowJson(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()

	// The fixture is 30 playable tracks, all with dossiers.
	a := &App{opts: Options{Library: &Library{Store: s}},
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), now: map[int64]nowPlaying{}}

	e := a.enrichment()
	if e == nil {
		t.Fatal("no enrichment block")
	}
	if e.Total != 30 || e.Done != 30 || e.Pct != 100 {
		t.Errorf("= %+v, want 30 of 30 at 100%%", e)
	}
	// The confidence split is what says whether enrichment is WORKING: a
	// library 90% enriched into "none" is 90% of nothing.
	if e.ConfidenceCounts["high"] != 30 {
		t.Errorf("confidence counts = %v", e.ConfidenceCounts)
	}

	// Half-done, and the percentage rounds to a tenth rather than moving in
	// ten-thousandths.
	if _, err := s.DB().Exec(`DELETE FROM dossiers WHERE track_id > 17`); err != nil {
		t.Fatal(err)
	}
	if e := a.enrichment(); e.Done != 17 || e.Pct != 56.7 {
		t.Errorf("= %+v, want 17 of 30 at 56.7%%", e)
	}

	// A missing track is not something still to enrich.
	if _, err := s.DB().Exec(`UPDATE tracks SET missing_at = 1 WHERE id > 25`); err != nil {
		t.Fatal(err)
	}
	if e := a.enrichment(); e.Total != 25 {
		t.Errorf("total = %d, want the 25 tracks the library still has", e.Total)
	}

	// And it reaches the endpoint, not only the method.
	if st := a.Status(); st.Enrichment == nil || st.Enrichment.Total != 25 {
		t.Errorf("/now.json carries %+v", st.Enrichment)
	}

	// A dossiers table that cannot be read reports NOTHING rather than a
	// confident zero: "0 of 30 enriched" would send an operator looking at the
	// model when the fault is the database.
	if _, err := s.DB().Exec(`ALTER TABLE dossiers RENAME TO dossiers_gone`); err != nil {
		t.Fatal(err)
	}
	if e := a.enrichment(); e != nil {
		t.Errorf("a broken dossiers table reported %+v", e)
	}
	if _, err := s.DB().Exec(`ALTER TABLE dossiers_gone RENAME TO dossiers`); err != nil {
		t.Fatal(err)
	}
	// And a split that cannot be read still reports the counts it has.
	if _, err := s.DB().Exec(`ALTER TABLE dossiers RENAME COLUMN confidence TO conf`); err != nil {
		t.Fatal(err)
	}
	if e := a.enrichment(); e == nil || e.Done == 0 || len(e.ConfidenceCounts) != 0 {
		t.Errorf("= %+v, want the counts with an empty split", e)
	}
	if _, err := s.DB().Exec(`ALTER TABLE dossiers RENAME COLUMN conf TO confidence`); err != nil {
		t.Fatal(err)
	}

	// No library, no block -- rather than a bar reading zero of zero.
	bare := &App{log: a.log, now: map[int64]nowPlaying{}}
	if bare.enrichment() != nil {
		t.Error("a server with no library reported enrichment progress")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if a.enrichment() != nil {
		t.Error("a closed store reported enrichment progress")
	}
	_ = ctx
}

// TestLibraryStatusPerStation: with several stations running there is no single
// now-playing, so /now.json?station=N asks one of them and gets that one's
// answer -- never a neighbour's.
func TestLibraryStatusPerStation(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	a := &App{opts: Options{Library: &Library{Store: s}},
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), now: map[int64]nowPlaying{}}

	a.setNowPlayingTrack(1, track{artist: "New Order", title: "Blue Monday"})
	a.setNowPlayingTrack(2, track{artist: "Bush", title: "Glycerine"})

	one := a.StatusForStation(1)
	if one.Now == nil || one.Now.Title != "Blue Monday" {
		t.Errorf("station 1 = %+v", one.Now)
	}
	if two := a.StatusForStation(2); two.Now == nil || two.Now.Title != "Glycerine" {
		t.Errorf("station 2 = %+v", two.Now)
	}
	// A station that is not playing says so by omission rather than by
	// claiming somebody else's track.
	if idle := a.StatusForStation(9); idle.Now != nil {
		t.Errorf("a station with nothing on reports %+v", idle.Now)
	}
	// The global fields come with it: health and metrics are about the box.
	if one.Health.FFmpeg == "" {
		t.Error("the per-station answer lost the global fields")
	}
	// A track with no tags still has to be called something.
	a.setNowPlaying(3, "/music/Some Band - Some Song.mp3")
	if three := a.StatusForStation(3); three.Now == nil || three.Now.Title != "Some Band - Some Song" {
		t.Errorf("station 3 = %+v", three.Now)
	}
}
