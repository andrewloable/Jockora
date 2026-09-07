// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import "testing"

// GENERATE AS SOON AS THE SLOT EXISTS, not when the deadline gets close.
//
// The trigger fired when insertionAt - now fell below T, a measured threshold
// of about 150 seconds. That is a HEAD START rather than a wait, but it is the
// SMALLEST head start that usually works -- and against a hosted model whose
// latency is somebody else's queue, "usually" is what produced breaks that
// finished after the boundary they were written for and were thrown away.
//
// Starting immediately spends nothing extra. The work is identical, the result
// is a file on disk, and the only difference is how much of the slack is left
// when the model is slow. T survives as the LATENESS check, which is where it
// was always doing the load-bearing work.
//
// Every test here is TestASAP*, which is the -run pattern for this change.

func TestASAPTriggersImmediately(t *testing.T) {
	l := NewLookahead(150e9) // T is irrelevant to the trigger now

	// A boundary eleven minutes out, which the old rule would have made wait
	// nine of them.
	if !l.ShouldTrigger(0, 660) {
		t.Error("a slot 660s away did not trigger; the whole point is to start now")
	}
	if !l.ShouldTrigger(0, 1) {
		t.Error("a slot one second away did not trigger")
	}
}

func TestASAPStillRefusesABoundaryAlreadyPast(t *testing.T) {
	// Nothing to aim at. The mixer has played through it.
	l := NewLookahead(150e9)
	if l.ShouldTrigger(300, 300) {
		t.Error("triggered on a boundary that is exactly now")
	}
	if l.ShouldTrigger(301, 300) {
		t.Error("triggered on a boundary that has gone past")
	}
}

func TestASAPKeepsTheLatenessCheck(t *testing.T) {
	// T stops gating the START and still governs whether what came back is
	// worth airing: a break that finished after its slot is thrown away.
	l := NewLookahead(150e9)
	if !l.InTime(0, 100, 10e9) {
		t.Error("a break that took 10s for a slot 100s away was called late")
	}
	if l.InTime(0, 100, 200e9) {
		t.Error("a break that took 200s for a slot 100s away was called on time")
	}
}
