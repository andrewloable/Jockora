// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package station decides what plays next.
package station

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"sync"

	"github.com/andrewloable/jockora/internal/store"
)

// ErrEmptyPool means there is nothing playable to choose from.
var ErrEmptyPool = errors.New("station: no playable tracks")

// Selector draws tracks in a shuffled order without replacement.
//
// Drawing randomly with replacement produces audible repeats within minutes, and
// that "dead shuffle" is precisely what this product exists to beat. Every track
// plays once per cycle, and the cycle boundary is guarded so the last track of
// one pass is never the first of the next: that is the one repeat a listener
// definitely notices.
type Selector struct {
	// mu guards the pool, because TUNING replaces it from an HTTP handler
	// while the mixer is drawing from it on another goroutine. Every method
	// that touches pool, idx or last takes it.
	mu   sync.Mutex
	pool []int64
	idx  int
	rnd  *rand.Rand
	last int64

	// Energy, when set, smooths the tempo jump between consecutive tracks by
	// REORDERING what is already queued. Optional, and nil on a library with no
	// measured tempos, which is every library until the analyser has run.
	Energy *EnergyMatcher
}

// NewSelector returns a selector over trackIDs, seeded for reproducibility.
//
// The seed is injected rather than taken from the global source so that tests
// are deterministic and a station's order can be reproduced.
func NewSelector(trackIDs []int64, seed int64) *Selector {
	s := &Selector{
		pool: append([]int64(nil), trackIDs...),
		rnd:  rand.New(rand.NewSource(seed)),
	}
	s.shuffle()
	return s
}

// Len reports the pool size.
func (s *Selector) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pool)
}

// Tune replaces the pool, which is how a listener changes station.
//
// The CURRENT track is not interrupted -- the mixer has already buffered it,
// and yanking audio mid-song to honour a click is exactly the kind of stutter
// this whole design avoids. The change is heard at the next boundary.
//
// An empty pool is refused rather than accepted: a station with nothing in it
// would starve the mixer, and refusing leaves the listener on the station they
// were already enjoying.
func (s *Selector) Tune(trackIDs []int64) error {
	if len(trackIDs) == 0 {
		return ErrEmptyPool
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.pool = append([]int64(nil), trackIDs...)
	s.idx = 0
	s.shuffleLocked()
	return nil
}

// Next returns the next track to play.
func (s *Selector) Next() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.pool) == 0 {
		return 0, ErrEmptyPool
	}

	if s.idx >= len(s.pool) {
		s.shuffleLocked()
		s.idx = 0
	}

	// Energy matching happens HERE, between choosing the position and taking
	// the track, so it can only ever swap two tracks that were both already
	// going to play in this cycle. Shuffle-without-replacement and the
	// no-repeat guarantee are untouched by construction rather than by care.
	if s.Energy != nil && s.last != 0 {
		if off := s.Energy.Pick(s.last, s.pool[s.idx:]); off > 0 {
			s.pool[s.idx], s.pool[s.idx+off] = s.pool[s.idx+off], s.pool[s.idx]
		}
	}

	id := s.pool[s.idx]
	s.idx++
	s.last = id
	return id, nil
}

// shuffle reorders the pool and keeps the cycle boundary from repeating.
func (s *Selector) shuffle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shuffleLocked()
}

func (s *Selector) shuffleLocked() {
	s.rnd.Shuffle(len(s.pool), func(i, j int) {
		s.pool[i], s.pool[j] = s.pool[j], s.pool[i]
	})

	// If the new cycle would open with the track that just closed the last one,
	// swap it with the second. With a pool of one there is nothing to swap with
	// and a repeat is unavoidable, which is correct rather than a hang.
	if len(s.pool) > 1 && s.pool[0] == s.last {
		s.pool[0], s.pool[1] = s.pool[1], s.pool[0]
	}
}

// PlayablePool returns the ids of every playable track, in a stable order.
//
// The order is fixed by id because SQLite makes no promise about row order
// otherwise, and an unstable pool would make a seeded selector produce a
// different sequence on every run of the same library.
func PlayablePool(ctx context.Context, s *store.Store) ([]int64, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT id FROM tracks WHERE playable = 1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("station: reading the track pool: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("station: reading the track pool: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("station: reading the track pool: %w", err)
	}
	return out, nil
}

// PoolForTag returns the playable tracks belonging to one station.
//
// A station is a VIEW of the library, not a partition of it: a track tagged
// both rock and alternative appears on both, which is why this queries the
// dossiers rather than a station column that does not exist.
//
// UnsortedTag is the tracks with no dossier YET, which on a fresh library is
// most of them -- tuning to it is how a listener plays the part of their
// library the DJ cannot talk about yet. An empty tag means the whole library.
func PoolForTag(ctx context.Context, s *store.Store, tag string) ([]int64, error) {
	switch tag {
	case "", "all":
		return PlayablePool(ctx, s)

	case UnsortedTag:
		rows, err := s.DB().QueryContext(ctx, `
			SELECT id FROM tracks
			 WHERE playable = 1 AND id NOT IN (SELECT track_id FROM dossiers)
			 ORDER BY id`)
		if err != nil {
			return nil, fmt.Errorf("station: reading the unsorted pool: %w", err)
		}
		return scanIDs(rows)

	default:
		// json_each rather than LIKE: matching '%rock%' in raw JSON would also
		// match a mood, a theme, or the word inside a subject summary.
		rows, err := s.DB().QueryContext(ctx, `
			SELECT t.id FROM tracks t
			  JOIN dossiers d ON d.track_id = t.id
			  JOIN json_each(json_extract(d.json, '$.station_tags')) tag
			 WHERE t.playable = 1 AND lower(tag.value) = lower(?)
			 ORDER BY t.id`, tag)
		if err != nil {
			return nil, fmt.Errorf("station: reading the %s pool: %w", tag, err)
		}
		return scanIDs(rows)
	}
}

func scanIDs(rows *sql.Rows) ([]int64, error) {
	defer rows.Close() //nolint:errcheck // read-only

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
