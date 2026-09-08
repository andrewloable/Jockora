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

	// seed is kept so State can hand it back. Without it a station could be
	// stopped but never resumed: the sequence is reproducible from the seed
	// and nothing else.
	seed int64

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
		seed: seed,
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

	// A FRESH SEED, derived so it stays deterministic. Reusing the old one
	// while the generator has already been advanced by an unknown number of
	// draws would make State report a seed that no longer reproduces the
	// order -- a station that resumed into a different sequence than the one
	// it was stopped in.
	s.seed = nextSeed(s.seed)
	s.rnd = rand.New(rand.NewSource(s.seed))
	s.pool = append([]int64(nil), trackIDs...)
	s.idx = 0
	s.shuffleLocked()
	return nil
}

// State is everything needed to resume this station's shuffle.
//
// Two numbers, because the order is a pure function of the seed and the pool:
// storing the shuffled list itself would go stale the moment the library
// changed, and storing nothing would restart every station from the top on
// every restart.
func (s *Selector) State() (seed int64, cursor int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seed, s.idx
}

// NewSelectorAt rebuilds a selector part way through its cycle.
//
// recent is the handful of tracks played just before the stop. With the same
// pool it is unnecessary -- the cursor already covers it -- but a pool that
// CHANGED between stop and resume cannot be replayed exactly, and the honest
// answer there is a fresh shuffle that still avoids what was just heard rather
// than a panic or a silent repeat.
func NewSelectorAt(ids []int64, seed int64, cursor int, recent []int64) *Selector {
	s := &Selector{
		pool: append([]int64(nil), ids...),
		rnd:  rand.New(rand.NewSource(seed)),
		seed: seed,
	}
	if len(recent) > 0 {
		s.last = recent[len(recent)-1]
	}
	s.shuffleLocked()

	// A cursor past the end is a cycle that finished, or a pool that shrank
	// below where it had got to. Either way the old order is spent: start a
	// new one rather than resuming into nothing.
	if cursor < 0 || cursor >= len(s.pool) {
		s.seed = nextSeed(seed)
		s.rnd = rand.New(rand.NewSource(s.seed))
		s.shuffleLocked()
		cursor = 0
	}
	s.idx = cursor
	s.deferRecentLocked(recent)
	return s
}

// deferRecentLocked pushes anything just played to the end of what is left, so
// the tracks a listener heard a minute ago are the last they hear again.
func (s *Selector) deferRecentLocked(recent []int64) {
	if len(recent) == 0 {
		return
	}
	just := make(map[int64]bool, len(recent))
	for _, id := range recent {
		just[id] = true
	}
	rest := s.pool[s.idx:]
	keep := make([]int64, 0, len(rest))
	defer_ := make([]int64, 0, len(recent))
	for _, id := range rest {
		if just[id] {
			defer_ = append(defer_, id)
			continue
		}
		keep = append(keep, id)
	}
	copy(rest, append(keep, defer_...))
}

// nextSeed derives the following seed. An LCG step: deterministic, so a
// resumed station is still reproducible, and far enough from its input that the
// new shuffle bears no relation to the old.
func nextSeed(seed int64) int64 {
	return seed*6364136223846793005 + 1442695040888963407
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
		`SELECT id FROM tracks WHERE playable = 1 AND missing_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("station: reading the track pool: %w", err)
	}
	return scanIDs(rows)
}

// PoolForStation is what a station actually plays: its MATERIALISED playlist.
//
// Not its tag. What the operator sees in the console is what airs, including
// every pin they added and minus every track they excluded -- and a station
// whose playlist is empty cannot start, which is a clearer failure than a
// mixer starving.
func PoolForStation(ctx context.Context, s *store.Store, stationID int64) ([]int64, error) {
	pool, err := s.StationTrackIDs(ctx, stationID)
	if err != nil {
		return nil, err
	}
	if len(pool) == 0 {
		return nil, fmt.Errorf("%w: station %d has an empty playlist", ErrEmptyPool, stationID)
	}
	return pool, nil
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
		// NOTHING SAYS WHAT IT IS, through the view rather than "has no
		// dossier": a track an operator filed by hand is placed, even if the
		// enrichment never reached it.
		rows, err := s.DB().QueryContext(ctx, `
			SELECT t.id FROM tracks t
			  JOIN effective_tags e ON e.track_id = t.id
			 WHERE t.playable = 1 AND t.missing_at IS NULL
			   AND json_array_length(e.station_tags) = 0
			 ORDER BY t.id`)
		if err != nil {
			return nil, fmt.Errorf("station: reading the unsorted pool: %w", err)
		}
		return scanIDs(rows)

	default:
		// json_each rather than LIKE: matching '%rock%' in raw JSON would also
		// match a mood, a theme, or the word inside a subject summary. And
		// through effective_tags, so an operator's own filing decides this the
		// way it decides everything else about what a station contains.
		rows, err := s.DB().QueryContext(ctx, `
			SELECT t.id FROM tracks t
			  JOIN effective_tags e ON e.track_id = t.id
			  JOIN json_each(e.station_tags) tag
			 WHERE t.playable = 1 AND t.missing_at IS NULL AND lower(tag.value) = lower(?)
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
	// Joined rather than returned early: the only column is an integer primary
	// key, so a driver failing here is unreachable from a working database, and
	// a branch no test can enter is a guess about what the message will say.
	// Every caller discards out when the error is non-nil.
	var failures error
	for rows.Next() {
		var id int64
		failures = errors.Join(failures, rows.Scan(&id))
		out = append(out, id)
	}
	return out, errors.Join(failures, rows.Err())
}
