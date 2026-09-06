// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"math"
	"sort"
	"testing"
)

func matcher(bpm map[int64]float64) *EnergyMatcher { return &EnergyMatcher{bpm: bpm} }

// TestEnergyPrefersACloserTempo is the whole point: stop the selector slamming
// a 170 BPM track straight after a 70 BPM one.
func TestEnergyPrefersACloserTempo(t *testing.T) {
	m := matcher(map[int64]float64{1: 70, 2: 170, 3: 128, 4: 75})

	// Natural order would play 2 (170) after 1 (70). Track 4 at 75 is closer.
	if got := m.Pick(1, []int64{2, 3, 4}); got != 2 {
		t.Errorf("Pick = %d, want index 2 (the 75 BPM track) after a 70 BPM one", got)
	}
}

// TestEnergyLeavesAGoodShuffleAlone: this is a smoothing pass, not a curation
// engine. If the next track already fits, do not disturb the shuffle.
func TestEnergyLeavesAGoodShuffleAlone(t *testing.T) {
	m := matcher(map[int64]float64{1: 120, 2: 128, 3: 121})

	if got := m.Pick(1, []int64{2, 3}); got != 0 {
		t.Errorf("Pick = %d, want 0: 128 is already inside the %.0f BPM window of 120", got, BPMWindow)
	}
}

// TestEnergyWidensRatherThanFailing: the rule is to widen the window when
// nothing fits, never to refuse.
func TestEnergyWidensRatherThanFailing(t *testing.T) {
	m := matcher(map[int64]float64{1: 70, 2: 180, 3: 150, 4: 200})

	got := m.Pick(1, []int64{2, 3, 4})
	if got != 1 {
		t.Errorf("Pick = %d, want index 1 (150, the closest) though nothing is inside the window", got)
	}
}

// TestEnergyHasNoOpinionWithoutTempos. Until the analyser has run, no track has
// a BPM. An unmeasured library must behave exactly as it did before.
func TestEnergyHasNoOpinionWithoutTempos(t *testing.T) {
	m := matcher(map[int64]float64{})
	if got := m.Pick(1, []int64{2, 3, 4}); got != 0 {
		t.Errorf("Pick = %d on an unmeasured library, want 0", got)
	}
	// A previous track with no tempo is equally no reason to reorder.
	m2 := matcher(map[int64]float64{2: 120, 3: 121})
	if got := m2.Pick(1, []int64{2, 3}); got != 0 {
		t.Errorf("Pick = %d when the previous track has no tempo, want 0", got)
	}
	var nilMatcher *EnergyMatcher
	if got := nilMatcher.Pick(1, []int64{2}); got != 0 {
		t.Errorf("a nil matcher returned %d", got)
	}
}

// TestEnergyIsBounded: scanning the whole queue would sort the station into a
// tempo ramp, which is energy-arc programming and explicitly not this.
func TestEnergyIsBounded(t *testing.T) {
	bpm := map[int64]float64{1: 70}
	var candidates []int64
	for i := int64(2); i < 60; i++ {
		bpm[i] = 200 // everything far away...
		candidates = append(candidates, i)
	}
	bpm[50] = 71 // ...except one perfect match, beyond the lookahead
	m := matcher(bpm)

	if got := m.Pick(1, candidates); got >= MaxLookahead {
		t.Errorf("Pick reached index %d, beyond the %d-track lookahead", got, MaxLookahead)
	}
}

// TestSelectorKeepsItsGuaranteesWithEnergyMatching is the constraint the task
// names explicitly: energy matching REORDERS candidates, it never re-admits an
// already-played track.
func TestSelectorKeepsItsGuaranteesWithEnergyMatching(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	bpm := map[int64]float64{1: 60, 2: 180, 3: 65, 4: 175, 5: 70, 6: 170, 7: 75, 8: 165}

	s := NewSelector(ids, 42)
	s.Energy = matcher(bpm)

	// One full cycle must contain every track exactly once.
	seen := map[int64]int{}
	var order []int64
	for i := 0; i < len(ids); i++ {
		id, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		seen[id]++
		order = append(order, id)
	}
	if len(seen) != len(ids) {
		t.Errorf("a cycle played %d distinct tracks of %d: %v", len(seen), len(ids), order)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("track %d played %d times in one cycle", id, n)
		}
	}
	got := append([]int64(nil), order...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	for i, id := range got {
		if id != ids[i] {
			t.Errorf("the cycle is not a permutation of the pool: %v", order)
			break
		}
	}
}

// TestSelectorSmoothsRealJumps: with tempos this polarised, matching should
// measurably reduce the average jump between consecutive tracks.
func TestSelectorSmoothsRealJumps(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	bpm := map[int64]float64{1: 60, 2: 180, 3: 65, 4: 175, 5: 70, 6: 170, 7: 75, 8: 165}

	avgJump := func(withEnergy bool) float64 {
		s := NewSelector(ids, 7)
		if withEnergy {
			s.Energy = matcher(bpm)
		}
		var prev int64
		var total float64
		var n int
		for i := 0; i < len(ids); i++ {
			id, _ := s.Next()
			if prev != 0 {
				total += math.Abs(bpm[id] - bpm[prev])
				n++
			}
			prev = id
		}
		return total / float64(n)
	}

	plain, smoothed := avgJump(false), avgJump(true)
	if smoothed >= plain {
		t.Errorf("average tempo jump %.1f with matching against %.1f without; it is not smoothing", smoothed, plain)
	}
	t.Logf("average jump %.1f BPM -> %.1f BPM", plain, smoothed)
}
