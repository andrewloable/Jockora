// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/server"

	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

// A station's playlist is materialised when it is created, from whatever had a
// dossier at that moment. On a fresh library that is almost nothing, and
// enrichment then spends days classifying the rest -- so without this the
// station an operator made on day one is still that size on day three unless
// somebody opens the console and presses Regenerate.

func refillTrack(t *testing.T, s *store.Store, id int64, tags string) {
	t.Helper()
	if _, err := s.DB().Exec(
		`INSERT INTO tracks (id, path, artist, title, duration_s, playable) VALUES (?, ?, 'A', 'T', 200, 1)`,
		id, "/m/"+string(rune('a'+id))+".mp3"); err != nil {
		t.Fatal(err)
	}
	if tags != "" {
		if _, err := s.DB().Exec(
			`INSERT INTO dossiers (track_id, json, confidence, created_at) VALUES (?, ?, 'high', 1)`,
			id, `{"station_tags":["`+tags+`"],"mood":[]}`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRefillGrowsAStationAsEnrichmentLands(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()

	// Wipe the fixture's own content so the counts here are unambiguous.
	for _, q := range []string{`DELETE FROM station_tracks`, `DELETE FROM stations`,
		`DELETE FROM dossiers`, `DELETE FROM tracks`} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	refillTrack(t, s, 1, "rock")
	id, err := s.CreateStation(ctx, store.Station{Name: "Rock", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := station.Regenerate(ctx, s, id); err != nil {
		t.Fatal(err)
	}
	before, err := s.StationTrackIDs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("the station started with %d tracks, want 1", len(before))
	}

	// Enrichment classifies two more, exactly as the queue would.
	refillTrack(t, s, 2, "rock")
	refillTrack(t, s, 3, "rock")

	a := &App{opts: Options{Library: &Library{Store: s}}, log: quietLogger()}
	a.refillOnce(ctx)

	after, err := s.StationTrackIDs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 3 {
		t.Errorf("after a refill the station has %d tracks, want 3: it must grow "+
			"without anybody pressing Regenerate", len(after))
	}
}

func TestRefillKeepsWhatTheOperatorDecided(t *testing.T) {
	// ReplaceStationTracks only deletes the rows the filter owns, so a pinned
	// track survives a refill. An operator's choice outranks the filter.
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	for _, q := range []string{`DELETE FROM station_tracks`, `DELETE FROM stations`,
		`DELETE FROM dossiers`, `DELETE FROM tracks`} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	refillTrack(t, s, 1, "rock")
	refillTrack(t, s, 2, "") // no dossier: the filter will never select it
	id, err := s.CreateStation(ctx, store.Station{Name: "Rock", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := station.Regenerate(ctx, s, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO station_tracks (station_id, track_id, pinned, excluded) VALUES (?, 2, 1, 0)`,
		id); err != nil {
		t.Fatal(err)
	}

	a := &App{opts: Options{Library: &Library{Store: s}}, log: quietLogger()}
	a.refillOnce(ctx)

	ids, err := s.StationTrackIDs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, got := range ids {
		if got == 2 {
			found = true
		}
	}
	if !found {
		t.Error("a refill dropped a pinned track the filter does not select")
	}
}

func TestRefillSurvivesAStoreThatCannotBeRead(t *testing.T) {
	// One broken station must not stop the others, and a dead store must not
	// take the loop down: the next tick is the retry.
	s := openUpgraded(t, v01Fixture(t))
	s.Close() //nolint:errcheck // deliberately closed

	a := &App{opts: Options{Library: &Library{Store: s}}, log: quietLogger()}
	a.refillOnce(context.Background()) // must not panic
}

func TestRefillStopsWhenTheContextEnds(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	a := &App{opts: Options{Library: &Library{Store: s}}, log: quietLogger()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { a.refill(ctx); close(done) }()
	<-done // returns rather than ticking forever on a dead app
}

func TestRefillSkipsAStationItCannotRebuild(t *testing.T) {
	// One station with a genre outside the vocabulary must not stop the others
	// being refilled. Inserted with SQL because the API would refuse it -- the
	// case this guards is a row that got in some other way, or a vocabulary
	// that shrank under an existing station.
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	for _, q := range []string{`DELETE FROM station_tracks`, `DELETE FROM stations`,
		`DELETE FROM dossiers`, `DELETE FROM tracks`} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	refillTrack(t, s, 1, "rock")
	if _, err := s.DB().Exec(
		`INSERT INTO stations (id, name, genre, enabled, created_at) VALUES (1, 'Bogus', 'nonsense', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	good, err := s.CreateStation(ctx, store.Station{Name: "Rock", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	a := &App{opts: Options{Library: &Library{Store: s}}, log: quietLogger()}
	a.refillOnce(ctx)

	ids, err := s.StationTrackIDs(ctx, good)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Errorf("the good station has %d tracks, want 1: a broken neighbour stopped the loop", len(ids))
	}
}

func TestEnrichmentDoneCountsWhatIsLeft(t *testing.T) {
	// It decides whether the readiness gate applies at all, so it has to be
	// right in both directions.
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	for _, q := range []string{`DELETE FROM dossiers`, `DELETE FROM tracks`} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{opts: Options{Library: &Library{Store: s}}, log: quietLogger()}

	if !a.enrichmentDone(ctx) {
		t.Error("an empty library has nothing left to enrich")
	}

	refillTrack(t, s, 1, "")
	if a.enrichmentDone(ctx) {
		t.Error("a track with no dossier means enrichment is not done")
	}

	refillTrack(t, s, 2, "rock")
	if a.enrichmentDone(ctx) {
		t.Error("one enriched of two is not done")
	}

	if _, err := s.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence, created_at) VALUES (1, '{}', 'none', 1)`); err != nil {
		t.Fatal(err)
	}
	if !a.enrichmentDone(ctx) {
		t.Error("every track has a dossier now")
	}
}

func TestEnrichmentDoneKeepsTheGateOnWhenItCannotTell(t *testing.T) {
	// The safe direction: the alternative is offering a listener a station
	// that may be three tracks long.
	s := openUpgraded(t, v01Fixture(t))
	s.Close() //nolint:errcheck // deliberately closed

	a := &App{opts: Options{Library: &Library{Store: s}}, log: quietLogger()}
	if a.enrichmentDone(context.Background()) {
		t.Error("a store that cannot be read reported enrichment finished")
	}
}

// The readiness gate, through the real Dial and Tune rather than through
// station.Ready alone: the rule being right is not the same as it being wired.

func gateApp(t *testing.T) (*App, *store.Store, int64, int64) {
	t.Helper()
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	for _, q := range []string{`DELETE FROM station_tracks`, `DELETE FROM stations`,
		`DELETE FROM dossiers`, `DELETE FROM tracks`} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// One rock track, and one with no dossier so enrichment is never "done".
	refillTrack(t, s, 1, "rock")
	refillTrack(t, s, 2, "")

	thin, err := s.CreateStation(ctx, store.Station{Name: "Night Rock", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	catchAll, err := s.CreateStation(ctx,
		store.Station{Name: "UNSORTED", Genre: station.UnsortedTag, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{thin, catchAll} {
		if _, err := station.Regenerate(ctx, s, id); err != nil {
			t.Fatal(err)
		}
	}
	return &App{opts: Options{Library: &Library{Store: s}}, log: quietLogger()}, s, thin, catchAll
}

func TestDialMarksAThinStationAsStillFilling(t *testing.T) {
	a, _, thin, catchAll := gateApp(t)

	dial, err := a.Dial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, d := range dial {
		seen[d.ID] = d.Ready
		if d.ID == thin && d.Preparing == "" {
			t.Error("a station that is not ready must say why")
		}
	}
	if seen[thin] {
		t.Error("a one-track rock station was offered as ready")
	}
	if !seen[catchAll] {
		t.Error("the catch-all was gated; it is the one station that works on day one")
	}
}

func TestTuneRefusesAStationThatIsStillFilling(t *testing.T) {
	a, _, thin, catchAll := gateApp(t)
	ctx := context.Background()

	_, err := a.Tune(ctx, "sid", thin)
	if !errors.Is(err, server.ErrStationPreparing) {
		t.Errorf("tuning a thin station gave %v, want ErrStationPreparing", err)
	}
	// It says which station and how far along, so the listener is not guessing.
	if err != nil && !strings.Contains(err.Error(), "Night Rock") {
		t.Errorf("the refusal does not name the station: %v", err)
	}

	if _, err := a.Tune(ctx, "sid", catchAll); err != nil {
		t.Errorf("tuning the catch-all failed: %v", err)
	}
}

func TestTuneAllowsEverythingOnceEnrichmentIsDone(t *testing.T) {
	// A small station is then small because that is all the music there is.
	a, s, thin, _ := gateApp(t)
	if _, err := s.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence, created_at) VALUES (2, '{}', 'none', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Tune(context.Background(), "sid", thin); err != nil {
		t.Errorf("the gate stayed shut after enrichment finished: %v", err)
	}
}

// THE CADENCE IS A DECISION, AND DECISIONS SURVIVE RESTARTS. It was kept only
// in memory and in a config field, so every restart silently reverted it to
// whatever the compose file said. Reported after it went back to 4 twice.

func TestCadenceIsRememberedAcrossARestart(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()

	pipe := &station.Pipeline{Cadence: station.NewCadence(4)}
	a := &App{
		opts: Options{Library: &Library{Store: s}, Breaks: pipe},
		cfg:  testConfig(t),
		log:  quietLogger(),
	}
	a.cfg.BreakEveryNTracks = 4

	if err := a.SetCadence(1); err != nil {
		t.Fatalf("SetCadence: %v", err)
	}
	if got := pipe.Cadence.EveryN(); got != 1 {
		t.Fatalf("live cadence = %d, want 1", got)
	}

	// The restart: a brand new pipeline built from the CONFIG default, exactly
	// as main.go does before app.New runs.
	restarted := &station.Pipeline{Cadence: station.NewCadence(4)}
	b := &App{
		opts: Options{Library: &Library{Store: s}, Breaks: restarted},
		cfg:  testConfig(t),
		log:  quietLogger(),
	}
	b.cfg.BreakEveryNTracks = 4
	b.applyStoredCadence(ctx, restarted)

	if got := restarted.Cadence.EveryN(); got != 1 {
		t.Errorf("after a restart the cadence is %d, want the operator's 1", got)
	}
	if b.cfg.BreakEveryNTracks != 1 {
		t.Errorf("config still says %d", b.cfg.BreakEveryNTracks)
	}
}

func TestCadenceStoredNonsenseKeepsTheConfiguredOne(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	if err := s.SetSetting(ctx, cadenceSetting, "banana"); err != nil {
		t.Fatal(err)
	}
	pipe := &station.Pipeline{Cadence: station.NewCadence(4)}
	a := &App{opts: Options{Library: &Library{Store: s}, Breaks: pipe}, cfg: testConfig(t), log: quietLogger()}
	a.cfg.BreakEveryNTracks = 4

	a.applyStoredCadence(ctx, pipe)
	if got := pipe.Cadence.EveryN(); got != 4 {
		t.Errorf("cadence = %d, want the configured 4", got)
	}

	// And a number outside the allowed range is refused the same way.
	if err := s.SetSetting(ctx, cadenceSetting, "9999"); err != nil {
		t.Fatal(err)
	}
	a.applyStoredCadence(ctx, pipe)
	if got := pipe.Cadence.EveryN(); got != 4 {
		t.Errorf("cadence = %d, want the configured 4", got)
	}
}

func TestCadenceWithNothingStoredIsQuiet(t *testing.T) {
	// A fresh database is the normal case, not a fault.
	s := openUpgraded(t, v01Fixture(t))
	pipe := &station.Pipeline{Cadence: station.NewCadence(4)}
	a := &App{opts: Options{Library: &Library{Store: s}, Breaks: pipe}, cfg: testConfig(t), log: quietLogger()}
	a.applyStoredCadence(context.Background(), pipe)
	if got := pipe.Cadence.EveryN(); got != 4 {
		t.Errorf("cadence = %d, want 4", got)
	}

	// And with no pipeline or no store at all it must not panic.
	(&App{opts: Options{Library: &Library{Store: s}}, log: quietLogger()}).applyStoredCadence(context.Background(), nil)
	(&App{opts: Options{}, log: quietLogger()}).applyStoredCadence(context.Background(), pipe)
}
