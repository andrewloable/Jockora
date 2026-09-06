// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"math"

	"github.com/andrewloable/jockora/internal/store"
)

// BPMWindow is how far apart two consecutive tracks may be before the selector
// prefers something closer.
//
// Twenty is about the distance between a slow rock song and a mid-tempo one:
// wide enough that the order still feels shuffled, narrow enough to stop 70
// landing directly against 170.
const BPMWindow = 20.0

// MaxLookahead is how far down the remaining queue a swap may reach.
//
// BOUNDED ON PURPOSE. Scanning the whole remaining pool would sort the station
// into a tempo ramp, which is energy-arc programming -- a much larger idea, and
// explicitly not this. This is a smoothing pass: it looks at the next few
// candidates and prefers one that does not jar.
const MaxLookahead = 12

// EnergyMatcher reorders upcoming candidates so consecutive tracks sit closer
// in tempo.
//
// IT REORDERS, IT NEVER RE-ADMITS. Shuffle-without-replacement and the
// no-repeat guarantee are properties of the Selector and this must not weaken
// either: a swap only ever exchanges two tracks that were both already going to
// play in this cycle.
type EnergyMatcher struct {
	bpm map[int64]float64
}

// LoadEnergy reads what tempos are known. Tracks without one are simply absent,
// and absence means "no opinion" rather than zero.
func LoadEnergy(ctx context.Context, s *store.Store) (*EnergyMatcher, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT id, bpm FROM tracks WHERE playable = 1 AND bpm IS NOT NULL AND bpm > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only

	m := &EnergyMatcher{bpm: map[int64]float64{}}
	for rows.Next() {
		var id int64
		var bpm float64
		if err := rows.Scan(&id, &bpm); err != nil {
			return nil, err
		}
		m.bpm[id] = bpm
	}
	return m, rows.Err()
}

// Known reports how many tracks have a tempo.
func (m *EnergyMatcher) Known() int {
	if m == nil {
		return 0
	}
	return len(m.bpm)
}

// Pick chooses which of the upcoming candidates should play next after prev.
//
// Returns an INDEX INTO candidates, so the caller swaps rather than removes and
// the cycle keeps exactly the tracks it had. Index 0 -- leave the order alone --
// is returned whenever there is no reason to prefer anything else: no tempo for
// the previous track, no tempo for any candidate, or the natural next track is
// already inside the window.
//
// THE WINDOW WIDENS RATHER THAN FAILING. If nothing sits within BPMWindow the
// closest candidate wins, because refusing to play anything is never the right
// answer for a smoothing pass.
func (m *EnergyMatcher) Pick(prev int64, candidates []int64) int {
	if m == nil || len(candidates) == 0 {
		return 0
	}
	prevBPM, ok := m.bpm[prev]
	if !ok || prevBPM <= 0 {
		return 0
	}
	if len(candidates) > MaxLookahead {
		candidates = candidates[:MaxLookahead]
	}

	// Already fine: do not disturb a shuffle that did not need help.
	if bpm, ok := m.bpm[candidates[0]]; ok && math.Abs(bpm-prevBPM) <= BPMWindow {
		return 0
	}

	best, bestDelta := 0, math.Inf(1)
	for i, id := range candidates {
		bpm, ok := m.bpm[id]
		if !ok {
			continue
		}
		if d := math.Abs(bpm - prevBPM); d < bestDelta {
			best, bestDelta = i, d
		}
	}
	if math.IsInf(bestDelta, 1) {
		// Nothing ahead has a tempo. Leave the shuffle alone.
		return 0
	}
	return best
}
