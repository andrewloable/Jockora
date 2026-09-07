// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package presence counts who is listening, from nothing but playlist fetches.
//
// An HLS client re-fetches stream.m3u8 every target duration whether or not
// anyone is in the room, which makes that fetch the only heartbeat the protocol
// offers. Nothing here touches HTTP or SQLite: it is a map and a clock, so the
// timing can be tested without waiting for any of it.
package presence

import (
	"sort"
	"sync"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
)

// Tracker records which session is listening to which station.
//
// KEYED BY SESSION, NOT BY STATION. A listener is in exactly one place, so
// moving them is one assignment and leaving the old station is automatic --
// which is what makes tuning away instant rather than lingering for a grace
// period on a station nobody is on.
type Tracker struct {
	clk   clock.Clock
	grace time.Duration

	mu   sync.Mutex
	seen map[string]entry
}

type entry struct {
	station int64
	at      time.Time
}

// New builds a tracker. grace is how long after its last fetch a session is
// still counted -- long enough to ride out a slow poll, short enough that a
// closed tab does not hold a station on air.
func New(clk clock.Clock, grace time.Duration) *Tracker {
	return &Tracker{clk: clk, grace: grace, seen: map[string]entry{}}
}

// Touch records a playlist fetch.
//
// Identity is the LOGIN SESSION, never the address: two phones behind one NAT
// are two listeners, and one laptop that changed network is still one.
func (t *Tracker) Touch(station int64, session string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen[session] = entry{station: station, at: t.clk.Now()}
}

// Count returns how many sessions are listening to a station.
func (t *Tracker) Count(station int64) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expire()

	n := 0
	for _, e := range t.seen {
		if e.station == station {
			n++
		}
	}
	return n
}

// Idle reports whether a station has no listeners, which is what stops it.
func (t *Tracker) Idle(station int64) bool { return t.Count(station) == 0 }

// Stations lists the stations with at least one listener, in order.
//
// Sorted so a manager reconciling runtimes against presence does the same work
// in the same order every time, rather than at the mercy of map iteration.
func (t *Tracker) Stations() []int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expire()

	set := map[int64]bool{}
	for _, e := range t.seen {
		set[e.station] = true
	}
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// expire drops sessions that have stopped fetching. Called on READ rather than
// from a goroutine of its own: a sweeper would be one more thing to start, stop
// and get wrong, for a map that is only ever looked at when someone asks.
//
// Caller holds the lock.
func (t *Tracker) expire() {
	cutoff := t.clk.Now().Add(-t.grace)
	for session, e := range t.seen {
		if e.at.Before(cutoff) {
			delete(t.seen, session)
		}
	}
}

// size is the number of sessions held, for the test that proves expiry frees
// them rather than merely hiding them.
func (t *Tracker) size() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.seen)
}
