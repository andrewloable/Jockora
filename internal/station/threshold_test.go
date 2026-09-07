// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/store"
)

// TestThresholdConstants: the numbers are the product decision, not an
// implementation detail, so they are asserted by value.
func TestThresholdConstants(t *testing.T) {
	if MinStationTracks != 10 {
		t.Errorf("MinStationTracks = %d, want 10", MinStationTracks)
	}
	if WarnStationTracks != 50 {
		t.Errorf("WarnStationTracks = %d, want 50", WarnStationTracks)
	}
}

// TestThresholdCheck: "too small to work" and "smaller than most people want"
// are different things and a console says different words about them.
func TestThresholdCheck(t *testing.T) {
	for _, tc := range []struct {
		n        int
		ok, warn bool
	}{
		{0, false, false},
		{9, false, false},
		{10, true, true},
		{49, true, true},
		{50, true, false},
		{500, true, false},
	} {
		ok, warn := CheckThreshold(tc.n)
		if ok != tc.ok || warn != tc.warn {
			t.Errorf("CheckThreshold(%d) = %v, %v; want %v, %v", tc.n, ok, warn, tc.ok, tc.warn)
		}
	}
}

// TestThresholdPoolForStation: what the operator sees in the console is what
// airs. Not the tag -- the playlist, including every pin they added and minus
// every track they excluded.
func TestThresholdPoolForStation(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		// Not rock at all: only a pin puts it on the station.
		filterTrack{tags: []string{"jazz"}, moods: []string{"warm"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}, missing: true},
	)
	ctx := context.Background()
	id, err := s.CreateStation(ctx, store.Station{Name: "S", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStationTracks(ctx, id, []int64{1, 2, 3, 4, 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExcluded(ctx, id, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, id, 4, true); err != nil {
		t.Fatal(err)
	}

	got, err := PoolForStation(ctx, s, id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int64{1, 3, 4}) {
		t.Errorf("= %v, want the playable unexcluded tracks plus the pin", got)
	}

	// An empty playlist cannot start a station, and saying so is clearer than
	// a mixer starving.
	empty, err := s.CreateStation(ctx, store.Station{Name: "E", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PoolForStation(ctx, s, empty); err == nil {
		t.Error("a station with no playlist reported a pool")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := PoolForStation(ctx, s, id); err == nil {
		t.Error("PoolForStation succeeded on a closed store")
	}
}

// TestThresholdRuntimeUsesStationPool is the wiring: a runtime that drew from
// the tag would play tracks the operator had excluded and miss the ones they
// pinned, and the console would be describing a station that does not exist.
func TestThresholdRuntimeUsesStationPool(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"jazz"}, moods: []string{"warm"}},
	)
	ctx := context.Background()
	id, err := s.CreateStation(ctx, store.Station{Name: "S", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// A playlist that deliberately disagrees with the tag: one rock track
	// excluded, one jazz track pinned on.
	if err := s.ReplaceStationTracks(ctx, id, []int64{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExcluded(ctx, id, 3, true); err != nil {
		t.Fatal(err)
	}

	want, err := PoolForStation(ctx, s, id)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntimeForStation(ctx, s, runtimeDeps(&fakeEncoder{}), id,
		filepath.Join(t.TempDir(), "seg"), store.SelectorState{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := rt.Selector().Len(); got != len(want) {
		t.Errorf("the runtime draws from %d tracks, the playlist has %d", got, len(want))
	}
	// And it really is the playlist, not the tag: the excluded track never
	// comes up in a full cycle.
	for i := 0; i < len(want); i++ {
		next, err := rt.Selector().Next()
		if err != nil {
			t.Fatal(err)
		}
		if next == 3 {
			t.Fatal("the runtime offered a track the operator excluded")
		}
	}

	// Resuming uses the stored position over the same playlist.
	resumed, err := NewRuntimeForStation(ctx, s, runtimeDeps(&fakeEncoder{}), id,
		filepath.Join(t.TempDir(), "seg"), store.SelectorState{Seed: 42, Cursor: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	if seed, cursor := resumed.Selector().State(); seed != 42 || cursor != 1 {
		t.Errorf("resumed at %d/%d, want 42/1", seed, cursor)
	}

	// A station with no playlist cannot be built at all.
	bare, err := s.CreateStation(ctx, store.Station{Name: "B", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntimeForStation(ctx, s, runtimeDeps(nil), bare,
		t.TempDir(), store.SelectorState{}, false); err == nil {
		t.Error("a station with an empty playlist built a runtime")
	}
}

// TestThresholdSeedRespectsMinimum: below ten tracks a station repeats inside
// forty minutes, which is where it stops sounding like a station.
func TestThresholdSeedRespectsMinimum(t *testing.T) {
	personas, err := dj.LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}
	s := dialStore(t, map[string][]string{
		"rock": rep("aggressive", 9), // one short
		"jazz": rep("warm", 10),      // exactly enough
	}, 0)
	ctx := context.Background()

	if _, err := dj.SeedJocks(ctx, s, "../../personas"); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedStations(ctx, s, personas); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListStations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	genres := map[string]bool{}
	for _, st := range list {
		genres[st.Genre] = true
	}
	if genres["rock"] {
		t.Error("a nine-track bucket became a station")
	}
	if !genres["jazz"] {
		t.Errorf("a ten-track bucket was not seeded: %+v", list)
	}
	// The nine are not lost -- they are in the catch-all.
	if !genres[UnsortedTag] {
		t.Errorf("the folded tracks have nowhere to play: %+v", list)
	}
}

// TestThresholdPoolForTagUnchanged: PoolForTag is still what the SEED uses, and
// the change of pool for running stations must not have altered it.
func TestThresholdPoolForTagUnchanged(t *testing.T) {
	s := filterStore(t,
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"rock"}, moods: []string{"raw"}},
		filterTrack{tags: []string{"jazz"}, moods: []string{"warm"}},
	)
	ctx := context.Background()

	got, err := PoolForTag(ctx, s, "rock")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("PoolForTag = %v, want the two rock tracks", got)
	}
	// It answers from the TAG, with no station and no playlist in sight.
	if list, _ := s.ListStations(ctx); len(list) != 0 {
		t.Fatal("this test is meant to run with no stations at all")
	}
}
