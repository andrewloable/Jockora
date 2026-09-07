// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package presence

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
)

const grace = 30 * time.Second

func tracker(t *testing.T) (*Tracker, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	return New(clk, grace), clk
}

func TestPresenceCountsDistinctSessions(t *testing.T) {
	tr, _ := tracker(t)

	tr.Touch(1, "s1")
	tr.Touch(1, "s2")
	if got := tr.Count(1); got != 2 {
		t.Errorf("Count(1) = %d, want 2", got)
	}

	// The same session polling again is the SAME listener. An HLS client
	// re-fetches the playlist every few seconds, so counting fetches would
	// report one person as a crowd.
	tr.Touch(1, "s1")
	if got := tr.Count(1); got != 2 {
		t.Errorf("Count(1) = %d after a re-poll, want 2", got)
	}
	if tr.Idle(1) {
		t.Error("Idle says nobody is listening to a station with two listeners")
	}
	if got := tr.Count(2); got != 0 {
		t.Errorf("Count(2) = %d, want 0", got)
	}
	if !tr.Idle(2) {
		t.Error("a station nobody has ever touched is not idle")
	}
}

func TestPresenceExpiresAfterGrace(t *testing.T) {
	tr, clk := tracker(t)
	tr.Touch(1, "s1")

	clk.Advance(grace - time.Second)
	if got := tr.Count(1); got != 1 {
		t.Errorf("Count(1) = %d just inside the grace period, want 1", got)
	}

	clk.Advance(2 * time.Second)
	if got := tr.Count(1); got != 0 {
		t.Errorf("Count(1) = %d after the grace period, want 0", got)
	}
	if !tr.Idle(1) {
		t.Error("Idle(1) is false after everyone left")
	}
	if got := tr.Stations(); len(got) != 0 {
		t.Errorf("Stations() = %v after everyone left", got)
	}
}

// TestPresenceRefreshExtends: a listener who is still there keeps polling, and
// each poll is a fresh statement that they are.
func TestPresenceRefreshExtends(t *testing.T) {
	tr, clk := tracker(t)

	tr.Touch(1, "s1")
	clk.Advance(20 * time.Second)
	tr.Touch(1, "s1")
	clk.Advance(20 * time.Second)
	tr.Touch(1, "s1")

	clk.Advance(15 * time.Second) // 55s after the first touch, 15s after the last
	if got := tr.Count(1); got != 1 {
		t.Errorf("Count(1) = %d, want 1 -- the listener never stopped polling", got)
	}
}

// TestPresenceSessionMovesStation: changing station is the escape hatch this
// product gives instead of a skip button, so it has to be instant. A grace
// period on the station just left would keep the old pipeline running and count
// one listener twice.
func TestPresenceSessionMovesStation(t *testing.T) {
	tr, _ := tracker(t)

	tr.Touch(1, "s1")
	tr.Touch(2, "s1")

	if got := tr.Count(1); got != 0 {
		t.Errorf("Count(1) = %d right after the listener tuned away, want 0", got)
	}
	if got := tr.Count(2); got != 1 {
		t.Errorf("Count(2) = %d, want 1", got)
	}
	if !tr.Idle(1) {
		t.Error("the station that was left is not idle")
	}
}

func TestPresenceStationsLists(t *testing.T) {
	tr, clk := tracker(t)

	tr.Touch(7, "s1")
	tr.Touch(3, "s2")
	tr.Touch(3, "s3")
	// Sorted, so a manager reconciling runtimes does the same work in the same
	// order every time rather than depending on map iteration.
	if got := tr.Stations(); !reflect.DeepEqual(got, []int64{3, 7}) {
		t.Errorf("Stations() = %v, want [3 7]", got)
	}

	clk.Advance(grace + time.Second)
	tr.Touch(3, "s2")
	if got := tr.Stations(); !reflect.DeepEqual(got, []int64{3}) {
		t.Errorf("Stations() = %v, want only the station still being listened to", got)
	}
}

func TestPresenceConcurrentSafe(t *testing.T) {
	tr, _ := tracker(t)
	var wg sync.WaitGroup

	for g := 0; g < 100; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				tr.Touch(int64(g%4), fmt.Sprintf("s%d", g))
				tr.Count(int64(i % 4))
				tr.Idle(int64(i % 4))
				tr.Stations()
			}
		}(g)
	}
	wg.Wait()

	total := 0
	for _, st := range tr.Stations() {
		total += tr.Count(st)
	}
	if total != 100 {
		t.Errorf("%d listeners across all stations, want 100 distinct sessions", total)
	}
}

// TestPresenceForgetsExpiredSessions: the map is the only state here, and a
// server that never drops a session grows for as long as it runs.
func TestPresenceForgetsExpiredSessions(t *testing.T) {
	tr, clk := tracker(t)

	for i := 0; i < 50; i++ {
		tr.Touch(1, fmt.Sprintf("s%d", i))
	}
	clk.Advance(grace + time.Second)
	tr.Count(1)

	if n := tr.size(); n != 0 {
		t.Errorf("%d expired sessions are still held, want 0", n)
	}
}
