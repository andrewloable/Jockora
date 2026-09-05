// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import "testing"

func TestDeadAirDroppedBreakExtendsCrossfade(t *testing.T) {
	const def = 2 * SampleRate // a two-second default fade

	tr := TransitionFor(false, def).WithDroppedBreak()

	want := def * 3 / 2
	if tr.FadeFrames != want {
		t.Errorf("FadeFrames = %d, want %d (1.5x the %d-frame default)", tr.FadeFrames, want, def)
	}
}

func TestDeadAirNoDropUsesDefaultFade(t *testing.T) {
	const def = 2 * SampleRate

	if tr := TransitionFor(false, def); tr.FadeFrames != def {
		t.Errorf("FadeFrames = %d, want the default %d when no break was dropped", tr.FadeFrames, def)
	}
}

// TestDeadAirExtensionRespectsNoCrossfadeNext: the gapless flag outranks this.
// A flagged pair stays sample-adjacent even when a break drops.
func TestDeadAirExtensionRespectsNoCrossfadeNext(t *testing.T) {
	tr := TransitionFor(true, 2*SampleRate).WithDroppedBreak()

	if tr.FadeFrames != 0 {
		t.Errorf("FadeFrames = %d, want 0: a gapless pair must not gain a fade because a break dropped", tr.FadeFrames)
	}
	if !tr.Adjacent() {
		t.Error("the transition stopped being adjacent")
	}
}

func TestDeadAirExtensionCappedAtSixSeconds(t *testing.T) {
	const def = 5 * SampleRate // 1.5x would be 7.5 seconds

	tr := TransitionFor(false, def).WithDroppedBreak()

	if tr.FadeFrames != MaxFadeFrames {
		t.Errorf("FadeFrames = %d, want the %d-frame cap (6 seconds)", tr.FadeFrames, MaxFadeFrames)
	}
	if got := FramesToDuration(tr.FadeFrames).Seconds(); got != 6 {
		t.Errorf("cap is %v seconds, want 6", got)
	}
}

// TestDeadAirAlreadyOverCapIsNotLengthened: a default fade already past the cap
// must not grow.
func TestDeadAirAlreadyOverCapIsNotLengthened(t *testing.T) {
	const def = 8 * SampleRate

	tr := TransitionFor(false, def).WithDroppedBreak()

	if tr.FadeFrames > MaxFadeFrames {
		t.Errorf("FadeFrames = %d, want no more than the %d-frame cap", tr.FadeFrames, MaxFadeFrames)
	}
}

// TestDeadAirInsertsNoSilence: the extension is a longer fade, not a gap. The
// music must be continuous across the whole extended window.
func TestDeadAirInsertsNoSilence(t *testing.T) {
	const def = 2048
	tr := TransitionFor(false, def).WithDroppedBreak()

	a, b := constFrames(tr.FadeFrames, 0.5), constFrames(tr.FadeFrames, 0.5)
	out := make([]Frame, tr.FadeFrames)
	c := NewChain()
	c.Process(out, a, b, nil, State{FadeTotal: tr.FadeFrames})

	for i := c.LatencyFrames(); i < len(out); i++ {
		if out[i].L == 0 {
			t.Fatalf("frame %d is silent: a gap was inserted instead of a longer fade", i)
		}
	}
}

// TestDeadAirIsNotAnError: a dropped break is a designed outcome, not a fault.
// Extending the fade must not be able to fail.
func TestDeadAirIsNotAnError(t *testing.T) {
	for _, def := range []int{0, 1, SampleRate, 100 * SampleRate} {
		for _, gapless := range []bool{false, true} {
			tr := TransitionFor(gapless, def).WithDroppedBreak()
			if tr.FadeFrames < 0 {
				t.Errorf("TransitionFor(%v, %d) gave a negative fade %d", gapless, def, tr.FadeFrames)
			}
			if gapless && tr.FadeFrames != 0 {
				t.Errorf("TransitionFor(true, %d) gained a fade of %d", def, tr.FadeFrames)
			}
		}
	}
}
