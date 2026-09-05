// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package station decides what plays next.
package station

import (
	"context"
	"errors"
	"fmt"
	"math/rand"

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
	pool []int64
	idx  int
	rnd  *rand.Rand
	last int64
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
func (s *Selector) Len() int { return len(s.pool) }

// Next returns the next track to play.
func (s *Selector) Next() (int64, error) {
	if len(s.pool) == 0 {
		return 0, ErrEmptyPool
	}

	if s.idx >= len(s.pool) {
		s.shuffle()
		s.idx = 0
	}

	id := s.pool[s.idx]
	s.idx++
	s.last = id
	return id, nil
}

// shuffle reorders the pool and keeps the cycle boundary from repeating.
func (s *Selector) shuffle() {
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
