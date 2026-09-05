// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package clock

import (
	"testing"
	"time"
)

var start = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func TestFakeSleepAdvancesAndRecords(t *testing.T) {
	f := NewFake(start)

	f.Sleep(2 * time.Second)
	f.Sleep(500 * time.Millisecond)

	if got := f.Now().Sub(start); got != 2500*time.Millisecond {
		t.Errorf("clock advanced %v, want 2.5s", got)
	}
	calls := f.SleepCalls()
	if len(calls) != 2 || calls[0] != 2*time.Second || calls[1] != 500*time.Millisecond {
		t.Errorf("SleepCalls = %v", calls)
	}
}

// TestFakeSleepRecordsNonPositive is what lets a pacer test catch a negative
// sleep: the request is recorded, but time never runs backwards.
func TestFakeSleepRecordsNonPositive(t *testing.T) {
	f := NewFake(start)

	f.Sleep(-3 * time.Second)

	if got := f.Now(); !got.Equal(start) {
		t.Errorf("a negative sleep moved the clock to %v", got)
	}
	if calls := f.SleepCalls(); len(calls) != 1 || calls[0] != -3*time.Second {
		t.Errorf("SleepCalls = %v, want the negative request recorded", calls)
	}
}

func TestFakeAdvanceDoesNotRecordSleep(t *testing.T) {
	f := NewFake(start)

	f.Advance(time.Minute)

	if got := f.Now().Sub(start); got != time.Minute {
		t.Errorf("clock advanced %v, want 1m", got)
	}
	if calls := f.SleepCalls(); len(calls) != 0 {
		t.Errorf("Advance recorded a sleep: %v", calls)
	}
}

// TestFakeSleepCallsIsACopy stops a caller mutating the recorder's slice.
func TestFakeSleepCallsIsACopy(t *testing.T) {
	f := NewFake(start)
	f.Sleep(time.Second)

	got := f.SleepCalls()
	got[0] = 99 * time.Hour

	if again := f.SleepCalls(); again[0] != time.Second {
		t.Errorf("SleepCalls handed out its internal slice: %v", again)
	}
}

func TestRealClockMoves(t *testing.T) {
	var c Clock = Real{}

	before := c.Now()
	c.Sleep(time.Millisecond)

	if !c.Now().After(before) {
		t.Error("Real clock did not advance across a sleep")
	}
}
