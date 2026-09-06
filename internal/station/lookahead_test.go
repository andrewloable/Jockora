// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"testing"
	"time"
)

// All named TestLookahead*, matching the task's own `-run TestLookahead`.

func TestLookaheadTriggersAtTSecondsBefore(t *testing.T) {
	l := NewLookahead(150 * time.Second)

	if l.ShouldTrigger(149.9, 300) {
		t.Error("triggered at t=149.9s for an insertion at 300s; T is 150s")
	}
	if !l.ShouldTrigger(150, 300) {
		t.Error("did not trigger at t=150s for an insertion at 300s")
	}
	if !l.ShouldTrigger(299, 300) {
		t.Error("did not trigger at t=299s; late is still worth attempting")
	}
}

// TestLookaheadSpansMultipleShortTracks is the reason the trigger is a TIME
// threshold and not "one track ahead". Five 60-second tracks put the insertion
// point 300 seconds out, so a 150-second lookahead fires in the MIDDLE of the
// run. That is correct behaviour on short material, not depth creep to cap.
//
// The task said this fires during track 2. It does not, and the task's own
// numbers say so: with T=150 the trigger is at t=150s, which is inside track 3
// (120-180s). Track 2 would need T between 180 and 240. The number is wrong;
// the point it was making -- that the trigger reaches past one track and this
// is not an error -- is right and is what is asserted here.
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
	if firedDuringTrack != 3 {
		t.Errorf("trigger fired during track %d, want track 3 (t=150s falls in 120-180s)", firedDuringTrack)
	}
	if firedDuringTrack <= 1 {
		t.Error("trigger did not reach past the current track; a time threshold must")
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
