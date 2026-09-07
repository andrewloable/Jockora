// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

//go:build gates

package test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/encode"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/sched"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// gateEncoder stands in for ffmpeg so this gate runs anywhere. What it proves
// is which TRACKS air, not what they sound like.
type gateEncoder struct{ mu sync.Mutex }

func (g *gateEncoder) Writer() io.Writer { return io.Discard }
func (g *gateEncoder) Stop() error       { return nil }
func (g *gateEncoder) Restarts() int     { return 0 }
func (g *gateEncoder) Degraded() string  { return "" }
func (g *gateEncoder) PID() int          { return 0 }

// playlistFixtureStore builds a library where `matching` tracks carry the genre
// and the rest do not.
func playlistFixtureStore(t *testing.T, genre string, matching, other int) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	id := 0
	add := func(tags []string) {
		id++
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (id, path, artist, title, playable) VALUES (?, ?, 'A', ?, 1)`,
			id, fmt.Sprintf("/m/%d.mp3", id), fmt.Sprintf("Track %d", id)); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"station_tags": tags, "mood": []string{"raw"}})
		if _, err := s.DB().Exec(
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
			id, string(raw), enrich.ConfidenceHigh); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < matching; i++ {
		add([]string{genre})
	}
	for i := 0; i < other; i++ {
		add([]string{"jazz"})
	}
	return s
}

func gateStation(t *testing.T, s *store.Store, genre string) int64 {
	t.Helper()
	id, err := s.CreateStation(context.Background(), store.Station{
		Name: strings.ToUpper(genre), Genre: genre, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := station.Regenerate(context.Background(), s, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestPlaylistGatePlaysOnlyPlaylist is the row's claim: what the operator sees
// in the console is what airs. A runtime drawing from anywhere else would play
// tracks they excluded and miss the ones they pinned.
func TestPlaylistGatePlaysOnlyPlaylist(t *testing.T) {
	ctx := context.Background()
	s := playlistFixtureStore(t, "rock", 12, 18)
	id := gateStation(t, s, "rock")

	// One outsider pinned on, one insider excluded: the playlist deliberately
	// disagrees with the genre, so "plays the playlist" and "plays the genre"
	// cannot both be true.
	if err := s.ReplaceStationTracks(ctx, id, append(idsUpTo(12), 13)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, id, 13, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExcluded(ctx, id, 3, true); err != nil {
		t.Fatal(err)
	}

	want, err := s.StationTrackIDs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[int64]bool{}
	for _, tid := range want {
		allowed[tid] = true
	}
	if !allowed[13] {
		t.Fatal("the pinned outsider is not on the playlist; the fixture is wrong")
	}
	if allowed[3] {
		t.Fatal("the excluded track is still on the playlist; the fixture is wrong")
	}

	deps := station.Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: clock.Real{},
		SampleRate: mix.SampleRate, Channels: mix.Channels, SegmentSeconds: 1, ListSize: 5,
		StartEncoder: func(context.Context, encode.Config, *slog.Logger) (station.Encoder, error) {
			return &gateEncoder{}, nil
		},
		Feed: func(ctx context.Context, _ *mix.Ring, _ *sched.Queue) { <-ctx.Done() },
	}
	rt, err := station.NewRuntimeForStation(ctx, s, deps, id,
		filepath.Join(t.TempDir(), "seg"), store.SelectorState{}, false)
	if err != nil {
		t.Fatal(err)
	}

	// Twenty boundaries: more than one full cycle, so the reshuffle is
	// exercised too.
	for i := 0; i < 20; i++ {
		aired, err := rt.Selector().Next()
		if err != nil {
			t.Fatalf("boundary %d: %v", i, err)
		}
		if !allowed[aired] {
			t.Fatalf("boundary %d aired track %d, which is not on the playlist %v", i, aired, want)
		}
		if aired == 3 {
			t.Fatalf("boundary %d aired the excluded track", i)
		}
	}
}

// TestPlaylistGateRescanThenRegenerate: the playlist follows the library, and
// the operator's decisions survive it doing so.
func TestPlaylistGateRescanThenRegenerate(t *testing.T) {
	ctx := context.Background()
	s := playlistFixtureStore(t, "rock", 12, 6)
	id := gateStation(t, s, "rock")

	// A pinned outsider and an excluded insider, as an operator would leave
	// them.
	if err := s.ReplaceStationTracks(ctx, id, append(idsUpTo(12), 13)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, id, 13, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExcluded(ctx, id, 4, true); err != nil {
		t.Fatal(err)
	}
	before, err := s.ListStationTracks(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	// Three new matching files arrive and are enriched, which is what a rescan
	// followed by the enrichment worker leaves behind.
	for i := 0; i < 3; i++ {
		tid := 19 + i
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (id, path, artist, title, playable) VALUES (?, ?, 'A', ?, 1)`,
			tid, fmt.Sprintf("/m/%d.mp3", tid), fmt.Sprintf("New %d", tid)); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{
			"station_tags": []string{"rock"}, "mood": []string{"raw"}})
		if _, err := s.DB().Exec(
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
			tid, string(raw), enrich.ConfidenceHigh); err != nil {
			t.Fatal(err)
		}
	}

	diff, err := station.Regenerate(ctx, s, id)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Added != 3 {
		t.Errorf("Diff = %+v, want exactly the three new tracks added", diff)
	}

	after, err := s.ListStationTracks(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+3 {
		t.Errorf("%d rows, want %d", len(after), len(before)+3)
	}
	var pinned, excluded bool
	for _, row := range after {
		if row.TrackID == 13 {
			pinned = row.Pinned
		}
		if row.TrackID == 4 {
			excluded = row.Excluded
		}
	}
	if !pinned {
		t.Error("the pinned outsider was dropped by the regeneration")
	}
	if !excluded {
		t.Error("the exclusion was cleared by the regeneration")
	}
	// And the exclusion still keeps it off the air.
	ids, _ := s.StationTrackIDs(ctx, id)
	for _, tid := range ids {
		if tid == 4 {
			t.Error("the excluded track is playable again")
		}
	}
}

// TestPlaylistGateSubThresholdRefuses: below ten tracks a station repeats
// inside forty minutes. The refusal names the count, or an operator is left
// guessing how many more they need.
func TestPlaylistGateSubThresholdRefuses(t *testing.T) {
	ctx := context.Background()
	s := playlistFixtureStore(t, "rock", 9, 10)
	id, err := s.CreateStation(ctx, store.Station{Name: "ROCK", Genre: "rock"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := station.Regenerate(ctx, s, id); err != nil {
		t.Fatal(err)
	}

	err = station.Enable(ctx, s, id)
	if !errors.Is(err, station.ErrTooFewTracks) {
		t.Fatalf("enabling a nine-track station = %v, want ErrTooFewTracks", err)
	}
	if !strings.Contains(err.Error(), "9") || !strings.Contains(err.Error(), "10") {
		t.Errorf("the refusal does not say 9 of 10: %v", err)
	}
	st, err := s.GetStation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled {
		t.Error("the station was enabled anyway")
	}

	// One more matching track and it goes on the dial.
	if _, err := s.DB().Exec(
		`INSERT INTO tracks (id, path, artist, title, playable) VALUES (100, '/m/100.mp3', 'A', 'Tenth', 1)`); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"station_tags": []string{"rock"}, "mood": []string{"raw"}})
	if _, err := s.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence) VALUES (100, ?, ?)`,
		string(raw), enrich.ConfidenceHigh); err != nil {
		t.Fatal(err)
	}
	if _, err := station.Regenerate(ctx, s, id); err != nil {
		t.Fatal(err)
	}
	if err := station.Enable(ctx, s, id); err != nil {
		t.Fatalf("a ten-track station was refused: %v", err)
	}
	if st, _ := s.GetStation(ctx, id); !st.Enabled {
		t.Error("the station is still off the dial")
	}
}

func idsUpTo(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}
