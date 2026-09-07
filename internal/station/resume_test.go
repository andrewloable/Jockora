// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

// TestResumeContinuesSequence: a station that stops must come back where it
// was, not at the top. Restarting the shuffle every time the last listener
// leaves would make the same few tracks open every session.
func TestResumeContinuesSequence(t *testing.T) {
	pool := ids(12)
	original := NewSelector(pool, 99)
	full := drawN(t, original, 12)

	stopped := NewSelector(pool, 99)
	drawN(t, stopped, 5)
	seed, cursor := stopped.State()
	if seed != 99 || cursor != 5 {
		t.Fatalf("State() = %d, %d, want 99, 5", seed, cursor)
	}

	resumed := NewSelectorAt(pool, seed, cursor, nil)
	if got := drawN(t, resumed, 7); !reflect.DeepEqual(got, full[5:]) {
		t.Errorf("resumed sequence = %v, want %v", got, full[5:])
	}
}

func TestResumeDoesNotRepeatBoundary(t *testing.T) {
	pool := ids(12)
	stopped := NewSelector(pool, 7)
	played := drawN(t, stopped, 5)
	seed, cursor := stopped.State()

	resumed := NewSelectorAt(pool, seed, cursor, played)
	first, err := resumed.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first == played[len(played)-1] {
		t.Errorf("the first track after resuming (%d) is the last one played", first)
	}
}

// TestResumeAfterPoolChange: a rescan between stop and resume is normal, and a
// cursor into a pool that no longer exists cannot be honoured. A fresh shuffle
// that still avoids what was just heard is the honest answer.
func TestResumeAfterPoolChange(t *testing.T) {
	pool := ids(12)
	stopped := NewSelector(pool, 42)
	played := drawN(t, stopped, 5)
	seed, cursor := stopped.State()

	smaller := make([]int64, 0, 11)
	for _, id := range pool {
		if id != played[0] {
			smaller = append(smaller, id)
		}
	}

	resumed := NewSelectorAt(smaller, seed, cursor, played)
	next := drawN(t, resumed, 5)
	for _, id := range next {
		for _, recent := range played {
			if id == recent {
				t.Errorf("track %d was played again straight after the resume", id)
			}
		}
	}
}

// TestResumeCursorAtExhaustion: a station stopped exactly at the end of a cycle
// must open the next one with a different order, not replay the one it just
// finished.
func TestResumeCursorAtExhaustion(t *testing.T) {
	pool := ids(12)
	stopped := NewSelector(pool, 5)
	full := drawN(t, stopped, 12)
	seed, cursor := stopped.State()
	if cursor != 12 {
		t.Fatalf("cursor = %d after exhausting the pool, want 12", cursor)
	}

	resumed := NewSelectorAt(pool, seed, cursor, full[len(full)-3:])
	newSeed, newCursor := resumed.State()
	if newSeed == seed {
		t.Error("a spent cycle resumed on the same seed, so it will replay")
	}
	if newCursor != 0 {
		t.Errorf("cursor = %d after reshuffling, want 0", newCursor)
	}
	if got := drawN(t, resumed, 12); reflect.DeepEqual(got, full) {
		t.Error("the new cycle replayed the old order exactly")
	}

	// A negative cursor is a corrupt row, not a reason to panic or to index
	// backwards into the pool.
	if got := NewSelectorAt(pool, seed, -3, nil); got.Len() != 12 {
		t.Errorf("a negative cursor produced a pool of %d", got.Len())
	}
}

func TestResumeStateRoundTripsStore(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	stationID, err := s.CreateStation(ctx, store.Station{Name: "ROCK", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	pool := ids(12)
	live := NewSelector(pool, 1234)
	want := drawN(t, live, 5)
	seed, cursor := live.State()

	if err := s.SetSelectorState(ctx, stationID,
		store.SelectorState{Seed: seed, Cursor: cursor}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetSelectorState(ctx, stationID)
	if err != nil || !ok {
		t.Fatalf("GetSelectorState: ok = %v, err = %v", ok, err)
	}

	// The whole point: the two numbers that survived the database rebuild the
	// same sequence the running station had.
	replay := NewSelectorAt(pool, got.Seed, 0, nil)
	if first := drawN(t, replay, 5); !reflect.DeepEqual(first, want) {
		t.Errorf("replayed %v, want %v", first, want)
	}
	resumed := NewSelectorAt(pool, got.Seed, got.Cursor, nil)
	rest := drawN(t, resumed, 7)
	for _, id := range rest {
		for _, played := range want {
			if id == played {
				t.Errorf("track %d repeated inside one cycle after a store round trip", id)
			}
		}
	}
}

// TestSelectorLenAndTune covers the two methods the rest of the package never
// calls: tuning is driven from an HTTP handler, so nothing here exercised it.
func TestSelectorLenAndTune(t *testing.T) {
	s := NewSelector(ids(4), 1)
	if s.Len() != 4 {
		t.Errorf("Len() = %d, want 4", s.Len())
	}

	if err := s.Tune(ids(9)); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 9 {
		t.Errorf("Len() = %d after tuning, want 9", s.Len())
	}

	// A new seed, or State would report one that no longer reproduces the
	// order and the station would resume into a different sequence.
	if seed, cursor := s.State(); seed == 1 || cursor != 0 {
		t.Errorf("State() = %d, %d after tuning; want a fresh seed and cursor 0", seed, cursor)
	}

	// An empty pool is refused rather than accepted: it would starve the mixer,
	// and refusing leaves the listener on the station they were enjoying.
	before := s.Len()
	if err := s.Tune(nil); err != ErrEmptyPool {
		t.Errorf("Tune(nil) = %v, want ErrEmptyPool", err)
	}
	if s.Len() != before {
		t.Errorf("a refused tune changed the pool to %d", s.Len())
	}
}

// TestSelectorPoolsSurfaceFailures: a pool query that fails quietly returns an
// empty library, and an empty library is a station that plays nothing.
func TestSelectorPoolsSurfaceFailures(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := PlayablePool(ctx, s); err == nil {
		t.Error("PlayablePool succeeded on a closed store")
	}
	for _, tag := range []string{"", "all", UnsortedTag, "rock"} {
		if _, err := PoolForTag(ctx, s, tag); err == nil {
			t.Errorf("PoolForTag(%q) succeeded on a closed store", tag)
		}
	}
}
