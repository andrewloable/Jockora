// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"testing"
	"time"
)

// All named TestLookahead*, matching the task's own `-run TestLookahead`.

// TestLookaheadTriggersAsSoonAsTheSlotExists. It used to wait until
// insertionAt - now fell below T. That is a head start rather than a wait, but
// it is the SMALLEST head start that usually works, and against a hosted model
// whose latency is somebody else's queue it produced breaks that finished after
// their boundary and were deleted unheard.
func TestLookaheadTriggersAsSoonAsTheSlotExists(t *testing.T) {
	l := NewLookahead(150 * time.Second)

	if !l.ShouldTrigger(0, 300) {
		t.Error("did not trigger at t=0 for an insertion at 300s")
	}
	if !l.ShouldTrigger(149.9, 300) {
		t.Error("did not trigger inside T either")
	}
	if !l.ShouldTrigger(299, 300) {
		t.Error("did not trigger at t=299s; late is still worth attempting")
	}
	// Past is past.
	if l.ShouldTrigger(300, 300) {
		t.Error("triggered on a boundary the mixer has reached")
	}
}

// TestLookaheadSpansMultipleShortTracks kept its point and lost its threshold.
// The trigger reaching several tracks back is correct behaviour on short
// material rather than depth creep to cap -- and now it always does, because it
// fires the moment the slot exists. Depth still reports how far that is, which
// is the observability the number was for.
func TestLookaheadSpansMultipleShortTracks(t *testing.T) {
	l := NewLookahead(150 * time.Second)

	const trackLen = 60.0
	insertion := 5 * trackLen // after the fifth track

	// Find the track playing when the trigger first fires, by walking seconds
	// rather than only track starts.
	firedDuringTrack := 0
	for now := 0.0; now < insertion; now++ {
		if l.ShouldTrigger(now, insertion) {
			firedDuringTrack = int(now/trackLen) + 1
			break
		}
	}
	if firedDuringTrack != 1 {
		t.Errorf("trigger fired during track %d, want track 1: as soon as the slot exists", firedDuringTrack)
	}
	if d := l.Depth(0, insertion, trackLen); d != 3 {
		t.Errorf("Depth = %d tracks, want 3: a run of short tracks legitimately reaches past one", d)
	}
}

func TestLookaheadLateGenerationDropsBreak(t *testing.T) {
	l := NewLookahead(150 * time.Second)

	// Generation started at 160s and took 200s; the slot was at 300s.
	if l.InTime(160, 300, 200*time.Second) {
		t.Error("a break finishing at 360s was accepted for a slot at 300s")
	}
	if !l.InTime(160, 300, 100*time.Second) {
		t.Error("a break finishing at 260s was rejected for a slot at 300s")
	}
	// Finishing exactly on the insertion point is too late: the mixer needs the
	// file before it reaches the boundary, not as it crosses it.
	if l.InTime(160, 300, 140*time.Second) {
		t.Error("a break finishing exactly at the insertion point was accepted")
	}
}

// TestLookaheadMeasuresRetryInclusive: measuring only clean first passes
// understates T by a whole LLM call plus a TTS render, on precisely the breaks
// most likely to be late.
func TestLookaheadMeasuresRetryInclusive(t *testing.T) {
	l := NewLookahead(150 * time.Second)

	l.RecordTiming(20*time.Second, 1)
	l.RecordTiming(55*time.Second, 2) // one regeneration: both calls, both renders
	l.RecordTiming(25*time.Second, 1)

	if n := l.Count(); n != 3 {
		t.Fatalf("Count = %d, want 3", n)
	}
	if n := l.RetriedCount(); n != 1 {
		t.Errorf("RetriedCount = %d, want 1: retries must be visible in the sample, not averaged away", n)
	}
	if got := l.P95(); got < 55*time.Second {
		t.Errorf("P95 = %s, want at least the 55s retried generation: a p95 that excludes retries is the one that fails at the worst moment", got)
	}
	if got := l.P50(); got != 25*time.Second {
		t.Errorf("P50 = %s, want 25s", got)
	}
}

func TestLookaheadRecommendedTIsP95TimesOneAndAHalf(t *testing.T) {
	l := NewLookahead(150 * time.Second)
	for i := 0; i < 20; i++ {
		l.RecordTiming(40*time.Second, 1)
	}
	l.RecordTiming(80*time.Second, 2)

	want := time.Duration(float64(l.P95()) * LookaheadSafetyFactor)
	if got := l.RecommendedT(); got != want {
		t.Errorf("RecommendedT = %s, want p95 %s x %.1f = %s", got, l.P95(), LookaheadSafetyFactor, want)
	}
}

// TestLookaheadRecommendationWithheldOnASmallSample: setting T from three
// generations is how a lookahead that looks fine in testing fails in a room.
func TestLookaheadRecommendationWithheldOnASmallSample(t *testing.T) {
	l := NewLookahead(150 * time.Second)
	for i := 0; i < MinTimingSample-1; i++ {
		l.RecordTiming(40*time.Second, 1)
	}
	if got := l.RecommendedT(); got != 0 {
		t.Errorf("RecommendedT = %s from %d samples, want 0: the minimum is %d", got, l.Count(), MinTimingSample)
	}
	l.RecordTiming(40*time.Second, 1)
	if l.RecommendedT() == 0 {
		t.Errorf("RecommendedT still withheld at the %d-sample minimum", MinTimingSample)
	}
}

func TestLookaheadDefaultTIsTheStartingValue(t *testing.T) {
	if got := NewLookahead(0).T(); got != DefaultLookahead {
		t.Errorf("T = %s with no value given, want the %s default", got, DefaultLookahead)
	}
}
