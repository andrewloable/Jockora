// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"reflect"
	"testing"
)

// Every test here is named TestCadence*, because the task's own VERIFY line is
// `go test ./internal/station/ -run TestCadence` and the six names it proposed
// (TestBreakEveryNBoundaries, TestNoBreakAtSessionStart, ...) match none of it.
// That pattern has now been found six times in this plan: a criterion grades
// green while running zero tests.

// slots walks a run of boundaries and returns those that got a break slot.
func slots(c *Cadence, boundaries int, gapless map[int]bool) []int {
	var got []int
	for b := 1; b <= boundaries; b++ {
		if c.SlotAt(b, gapless[b]) {
			got = append(got, b)
		}
	}
	return got
}

func TestCadenceEveryNBoundaries(t *testing.T) {
	c := NewCadence(4)
	got := slots(c, 20, nil)
	want := []int{4, 8, 12, 16, 20}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("slots = %v, want %v", got, want)
	}
}

// TestCadenceNIsReadFromConfig is the point of the whole type. "The DJ talks
// too much" is the most common complaint about every AI radio product that
// exists, so this number has to be tunable by listening, never a literal.
func TestCadenceNIsReadFromConfig(t *testing.T) {
	got := slots(NewCadence(2), 20, nil)
	want := []int{2, 4, 6, 8, 10, 12, 14, 16, 18, 20}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with N=2, slots = %v, want %v", got, want)
	}
	if n := NewCadence(0).EveryN(); n != DefaultBreakEveryNTracks {
		t.Errorf("unset cadence = %d, want the %d default rather than a division by zero", n, DefaultBreakEveryNTracks)
	}
}

func TestCadenceNoBreakAtSessionStart(t *testing.T) {
	c := NewCadence(4)
	// Boundary 0 is "before any track has played". The stream opens with music.
	if c.SlotAt(0, false) {
		t.Error("slot created before the first track played; the stream must open with music")
	}
	for b := 1; b <= 3; b++ {
		if c.SlotAt(b, false) {
			t.Errorf("slot at boundary %d with N=4", b)
		}
	}
}

// TestCadenceNoBreakOnAdjacentPair protects gapless material. Talking across a
// gapless album transition is worse than the crossfade the flag already refuses,
// so the slot MOVES rather than airing or vanishing.
func TestCadenceNoBreakOnAdjacentPair(t *testing.T) {
	c := NewCadence(4)
	got := slots(c, 20, map[int]bool{4: true, 12: true, 13: true})
	want := []int{5, 9, 14, 18}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("slots = %v, want %v: a gapless boundary must push the slot to the next eligible one", got, want)
	}
}

func TestCadenceGaplessDoesNotLoseTheSlot(t *testing.T) {
	c := NewCadence(4)
	for b := 1; b <= 3; b++ {
		c.SlotAt(b, false)
	}
	if c.SlotAt(4, true) {
		t.Fatal("slot created at a gapless boundary")
	}
	if !c.SlotAt(5, false) {
		t.Error("the slot refused at boundary 4 never reappeared; it was dropped, not moved")
	}
}

func TestCadenceSlotsAreDecidedAhead(t *testing.T) {
	c := NewCadence(4)
	// Two boundaries have passed; the generator needs to know what is coming
	// before the lookahead trigger fires for the first of them.
	c.SlotAt(1, false)
	c.SlotAt(2, false)

	got := c.UpcomingSlots(2, 3)
	want := []int{4, 8, 12}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UpcomingSlots(2, 3) = %v, want %v", got, want)
	}
	if n := len(c.UpcomingSlots(2, 0)); n != 0 {
		t.Errorf("UpcomingSlots asked for none returned %d", n)
	}
}

// TestCadenceDroppedBreakDoesNotShiftCadence guards a mistake that would be
// easy to add later and hard to notice: a dropped break is a NORMAL event
// (late generation, validator rejection, TTS failure), and retrying it sooner
// would compound every drop into the DJ talking more often.
func TestCadenceDroppedBreakDoesNotShiftCadence(t *testing.T) {
	c := NewCadence(4)
	for b := 1; b <= 8; b++ {
		c.SlotAt(b, false)
	}
	// The slot at 8 existed; the break never aired. The cadence is not told,
	// and must not be tellable -- that is what makes this structural.

	for b := 9; b <= 11; b++ {
		if c.SlotAt(b, false) {
			t.Errorf("slot at boundary %d after a drop at 8: a drop must not make the DJ talk sooner", b)
		}
	}
	if !c.SlotAt(12, false) {
		t.Error("no slot at boundary 12; the cadence lost its place after a drop")
	}
}

func TestCadenceRewindsNothingOnRepeatedCalls(t *testing.T) {
	// SlotAt advances state, so asking twice about the same boundary would
	// double-count. The scheduler calls it once per boundary; this pins that
	// contract rather than leaving it to be discovered.
	c := NewCadence(4)
	for b := 1; b <= 4; b++ {
		c.SlotAt(b, false)
	}
	if c.SlotAt(4, false) {
		t.Error("asking about boundary 4 twice produced two slots")
	}
}
