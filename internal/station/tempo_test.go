// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import "testing"

func TestMoodTempoRangeOfOneMood(t *testing.T) {
	lo, hi, ok := TempoRange([]string{"calm"})
	if !ok || lo != 50 || hi != 95 {
		t.Errorf("calm = %v-%v ok=%v, want 50-95", lo, hi, ok)
	}
}

func TestMoodTempoUnionsSeveralRanges(t *testing.T) {
	// Filter.Moods is already an OR, so the tempo has to be one too: a station
	// that plays calm OR aggressive plays both tempos, not the gap between.
	lo, hi, ok := TempoRange([]string{"calm", "aggressive"})
	if !ok || lo != 50 || hi != 200 {
		t.Errorf("calm+aggressive = %v-%v ok=%v, want the widest span", lo, hi, ok)
	}
}

func TestMoodTempoIsUnconstrainedWithoutARangedMood(t *testing.T) {
	// Eleven of the sixteen moods have no characteristic tempo. Inventing one
	// for "romantic" would quietly halve that station for no reason.
	for _, moods := range [][]string{nil, {}, {"romantic"}, {"cold", "lonely"}} {
		if _, _, ok := TempoRange(moods); ok {
			t.Errorf("%v is ranged, want no tempo constraint", moods)
		}
	}
}

func TestMoodTempoOneUnrangedMoodWidensToAny(t *testing.T) {
	// A station playing calm OR romantic admits every romantic track, and a
	// romantic track may be any tempo at all. Narrowing here would drop music
	// the operator asked for.
	if _, _, ok := TempoRange([]string{"calm", "romantic"}); ok {
		t.Error("calm+romantic is ranged, want any tempo")
	}
}

func TestMoodTempoRangesAreSane(t *testing.T) {
	// Guards the table itself: a transposed pair reads as a range that can
	// never match, and the query would silently return nothing.
	for mood, r := range moodTempo {
		if r[0] <= 0 || r[0] >= r[1] {
			t.Errorf("%s = %v, want a low bound below a high one", mood, r)
		}
	}
}
