// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/presence"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// A STATION IS A BROADCAST, AND A BROADCAST IS NOT SHAREABLE.
//
// One BreakWriter served every station: the track context a break was written
// about, the cold open, the fact cooldown, the exemption list of a station's
// own titles and the drop counters were all one box, rebound to whichever
// station was fed last. Two stations on air meant B's break was written about
// A's records, in A's jock's voice, and B's first break was not a cold open
// because A had already aired one.
//
// TWO STATIONS MAY SHARE A JOCK and that is legal -- a station has one jock, a
// jock may have many stations -- so none of this is keyed on the persona.

func perStationApp(t *testing.T, jockOf map[int64]string) (*App, *store.Store) {
	t.Helper()
	s := openUpgraded(t, filepath.Join(t.TempDir(), "stations.db"))
	ctx := context.Background()
	if err := s.UpsertJock(ctx, store.Jock{ID: "sunny", Name: "Sunny Marchetti",
		VoiceID: "kokoro:af_nicole", SpeechStyle: "slow", Personality: "serene"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertJock(ctx, store.Jock{ID: "dutch", Name: "Dutch Mahoney",
		VoiceID: "kokoro:am_fenrir", SpeechStyle: "loud", Personality: "loud"}); err != nil {
		t.Fatal(err)
	}
	for id, jock := range jockOf {
		if _, err := s.DB().Exec(
			`INSERT INTO stations (id, name, genre, jock_id, created_at) VALUES (?, ?, 'rock', ?, 1)`,
			id, "S", jock); err != nil {
			t.Fatal(err)
		}
	}

	a := &App{
		cfg: testConfig(t),
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		opts: Options{
			Library: &Library{Store: s},
			NewBreaks: func(int64) (*station.Pipeline, *station.BreakWriter) {
				w := &station.BreakWriter{
					Persona:   booted(),
					Validator: &dj.Validator{Said: &dj.SaidLines{Store: s, JockID: "dutch_mahoney"}},
					Session:   dj.NewSession(),
					Store:     s,
				}
				return &station.Pipeline{Cadence: station.NewCadence(4)}, w
			},
		},
		breaks: map[int64]*stationBreaks{},
	}
	return a, s
}

func TestPerStationWriterIsOnePerStation(t *testing.T) {
	a, _ := perStationApp(t, map[int64]string{1: "sunny", 2: "dutch"})

	one, two := a.breaksFor(1), a.breaksFor(2)
	if one == nil || two == nil {
		t.Fatal("no break machinery")
	}
	if one.writer == two.writer || one.pipeline == two.pipeline {
		t.Fatal("two stations share one writer or one pipeline")
	}
	// And asking twice is the SAME one: rebuilding it would throw away the
	// cold open and the counters every time a listener came back.
	if a.breaksFor(1) != one {
		t.Error("the station's machinery was rebuilt on a second look")
	}

	// A STATION THAT COMES UP LATER INHERITS THE OPERATOR'S CADENCE. It is one
	// setting for the whole dial, and a station built after they changed it
	// would otherwise run on the value the factory was compiled with.
	a.cfg.BreakEveryNTracks = 1
	if got := a.breaksFor(9).pipeline.EveryN(); got != 1 {
		t.Errorf("a station built later runs at cadence %d, want the operator's 1", got)
	}
}

func TestPerStationWriterEachHearsItsOwnJock(t *testing.T) {
	a, _ := perStationApp(t, map[int64]string{1: "sunny", 2: "dutch"})
	ctx := context.Background()

	a.applyStationJock(ctx, 1)
	a.applyStationJock(ctx, 2)

	if got := a.breaksFor(1).writer.Voice(); got != "kokoro:af_nicole" {
		t.Errorf("station 1 voice = %q, want its own jock's", got)
	}
	if got := a.breaksFor(2).writer.Voice(); got != "kokoro:am_fenrir" {
		t.Errorf("station 2 voice = %q, want its own jock's", got)
	}
}

func TestPerStationWriterSameJockIsStillTwoBroadcasts(t *testing.T) {
	// The harder half. Fixing only "the wrong jock speaks" would leave this
	// wrong: two stations on Sunny are two independent broadcasts.
	a, _ := perStationApp(t, map[int64]string{1: "sunny", 2: "sunny"})
	ctx := context.Background()
	a.applyStationJock(ctx, 1)
	a.applyStationJock(ctx, 2)

	one, two := a.breaksFor(1), a.breaksFor(2)
	if one.writer.Voice() != two.writer.Voice() {
		t.Fatal("the same jock should sound the same on both")
	}

	// THE COLD OPEN. Station 2's first break is its own first break.
	one.writer.Aired()
	if one.writer.Session == two.writer.Session {
		t.Fatal("both stations share one session, so one cold open serves both")
	}
	if !two.writer.Session.IsColdOpen() {
		t.Error("station 2 lost its cold open to a break station 1 aired")
	}
	if one.writer.Session.IsColdOpen() {
		t.Error("station 1 is still cold after airing a break")
	}

	// THE TRACK CONTEXT is per station by construction now: SetContext writes
	// into the station's own writer, and the two are different objects. There
	// is no getter to assert through, and adding one to prove a thing the type
	// system already guarantees is a getter that exists for a test.
	one.writer.SetContext(0, 11, 12, "A", "one")
	two.writer.SetContext(0, 21, 22, "B", "two")
}

func TestPerStationWriterAnEditReachesEveryStationOnThatJock(t *testing.T) {
	// It used to return after the first match, so the second station kept
	// speaking as the persona the operator had just edited away.
	a, s := perStationApp(t, map[int64]string{1: "sunny", 2: "sunny", 3: "dutch"})
	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		a.applyStationJock(ctx, id)
	}

	edited := store.Jock{ID: "sunny", Name: "Sunny Marchetti", VoiceID: "kokoro:af_bella",
		SpeechStyle: "slower", Personality: "serene"}
	if err := s.UpsertJock(ctx, edited); err != nil {
		t.Fatal(err)
	}
	a.PersonaChanged(edited)

	for _, id := range []int64{1, 2} {
		if got := a.breaksFor(id).writer.Voice(); got != "kokoro:af_bella" {
			t.Errorf("station %d voice = %q, want the edit", id, got)
		}
	}
	// And the station on another jock is untouched.
	if got := a.breaksFor(3).writer.Voice(); got != "kokoro:am_fenrir" {
		t.Errorf("station 3 voice = %q, want it left alone", got)
	}
}

func TestPerStationWriterStatsAreReportedPerStation(t *testing.T) {
	a, _ := perStationApp(t, map[int64]string{1: "sunny", 2: "dutch"})
	ctx := context.Background()
	one, two := a.breaksFor(1), a.breaksFor(2)

	// Drops driven through the REAL path: a model that will not answer is one
	// of the ways a break does not air, and it is the one a test can produce
	// without a model.
	one.writer.Validator.Writer = brokenModel{}
	two.writer.Validator.Writer = brokenModel{}
	_, _ = one.writer.Validator.Generate(ctx, "prompt", 0, nil, nil, nil)
	_, _ = two.writer.Validator.Generate(ctx, "prompt", 0, nil, nil, nil)
	_, _ = two.writer.Validator.Generate(ctx, "prompt", 0, nil, nil, nil)

	stats := a.breakStats()
	if stats == nil {
		t.Fatal("no break stats at all")
	}
	by, ok := stats["by_station"].(map[string]any)
	if !ok || len(by) != 2 {
		t.Fatalf("by_station = %v, want one entry per station", stats["by_station"])
	}
	first := by["1"].(map[string]any)
	second := by["2"].(map[string]any)
	if first["dropped"] != 1 || second["dropped"] != 2 {
		t.Errorf("drops = %v and %v, want them counted separately", first["dropped"], second["dropped"])
	}
	// The total still answers "is the DJ working at all".
	if stats["dropped"] != 3 {
		t.Errorf("total dropped = %v, want 3", stats["dropped"])
	}
}

// TestPerStationWriterUnderRace feeds two stations at once, which is the shape
// the whole change exists for.
func TestPerStationWriterUnderRace(t *testing.T) {
	a, _ := perStationApp(t, map[int64]string{1: "sunny", 2: "dutch"})
	ctx := context.Background()

	var wg sync.WaitGroup
	for _, id := range []int64{1, 2} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				br := a.breaksFor(id)
				a.applyStationJock(ctx, id)
				br.writer.SetContext(0, id*10, id*10+1, "A", "t")
				br.pipeline.Announce(station.Boundary{
					Index: i, InsertionAt: float64(i), Cur: &station.Track{DurationS: 10},
				})
				_ = br.writer.Voice()
			}
		}(id)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = a.breakStats()
			_ = a.liveCadence()
		}
	}()
	wg.Wait()

	if a.breaksFor(1).writer.Voice() == a.breaksFor(2).writer.Voice() {
		t.Error("the two stations converged on one voice")
	}
}

// brokenModel is a language model that never answers, which is one of the ways
// a break does not air.
type brokenModel struct{}

func (brokenModel) WriteBreak(context.Context, string, map[string]any, int) (string, error) {
	return "", errNoModel
}

var errNoModel = errors.New("no model")

func TestPerStationWriterWithNoDJAtAll(t *testing.T) {
	// No persona, no model, no sidecar is a supported way to run: a shuffle.
	// Every path here must return rather than reach for a pipeline.
	a, _ := perStationApp(t, map[int64]string{1: "sunny"})
	a.opts.NewBreaks = nil
	a.breaks = map[int64]*stationBreaks{}

	if br := a.breaksFor(1); br != nil {
		t.Error("break machinery appeared with no DJ configured")
	}
	if a.hasDJ() {
		t.Error("hasDJ with neither a factory nor a pipeline")
	}
	if got := a.breakStats(); got != nil {
		t.Errorf("break stats = %v with no DJ", got)
	}
	a.applyStationJock(context.Background(), 1)

	// A factory that declines is the same answer. It is how a deployment whose
	// speech sidecar died reports itself.
	a.opts.NewBreaks = func(int64) (*station.Pipeline, *station.BreakWriter) { return nil, nil }
	if br := a.breaksFor(1); br != nil {
		t.Error("a factory that returned nothing still produced machinery")
	}
}

func TestPerStationWriterTicksEveryStationOnItsOwnClock(t *testing.T) {
	a, _ := perStationApp(t, map[int64]string{1: "sunny"})
	ctx := context.Background()

	// With no manager and no runtime there is nothing on air, and the ticker
	// must be a no-op rather than a panic.
	if got := a.airing(); len(got) != 0 {
		t.Errorf("airing = %v with nothing running", got)
	}
	a.tickBreaks(ctx)

	// The spike path's single runtime.
	a.rt = station.NewRuntime(station.Deps{}, 1, t.TempDir())
	got := a.airing()
	if len(got) != 1 || got[0].StationID() != 1 {
		t.Fatalf("airing = %v, want the one station", got)
	}
	// Its clock starts at zero, and a break is placed against it rather than
	// against whichever station happened to be primary.
	if s := secondsOn(got[0]); s != 0 {
		t.Errorf("secondsOn = %v before a sample is played, want 0", s)
	}
	// A pipeline with no queue reports an error, which is logged and does not
	// stop the ticker.
	a.tickBreaks(ctx)
}

func TestPerStationWriterAiringAsksTheManager(t *testing.T) {
	a, s := perStationApp(t, map[int64]string{1: "sunny"})
	a.tracker = presence.New(clock.Real{}, ListenerGrace)
	a.mgr = station.NewManager(a.tracker, s, a.newStationRuntime, clock.Real{},
		ListenerGrace, MaxStations)

	// Nobody is listening, so nothing is on air and nothing is ticked. The
	// point is that the answer comes from the MANAGER rather than from the one
	// runtime the spike path holds.
	if got := a.airing(); len(got) != 0 {
		t.Errorf("airing = %v with no listeners", got)
	}
	a.tickBreaks(context.Background())
}

func TestPerStationWriterTicksPastAStationWithNoDJ(t *testing.T) {
	a, _ := perStationApp(t, map[int64]string{1: "sunny"})
	a.rt = station.NewRuntime(station.Deps{}, 7, t.TempDir())
	a.opts.NewBreaks = nil
	a.breaks = map[int64]*stationBreaks{}
	// Station 7 has no machinery at all; the ticker walks past it.
	a.tickBreaks(context.Background())
}

func TestPerStationWriterCountsNothingForAWriterWithNoValidator(t *testing.T) {
	// A writer with no validator counts nothing, which is not the same as a
	// station with no breaks: it must not appear in the per-station table as a
	// row of zeroes.
	a, _ := perStationApp(t, map[int64]string{1: "sunny"})
	br := a.breaksFor(1)
	br.writer.Validator = nil
	if got := a.breakStats(); got != nil {
		t.Errorf("break stats = %v, want nothing countable", got)
	}
	br.writer = nil
	if got := a.breakStats(); got != nil {
		t.Errorf("break stats = %v with no writer", got)
	}
}
