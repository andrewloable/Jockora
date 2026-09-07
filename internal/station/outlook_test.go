// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import "testing"

// WHETHER THE DJ IS ABOUT TO SPEAK IS INVISIBLE FROM OUTSIDE. A break that is
// coming, one still being written, and one that was never scheduled all look
// identical to a listener -- and to an operator, who then cannot tell a quiet
// station from a broken one.
//
// Every test here is TestOutlook*, which is the -run pattern for this change.

func TestOutlookSaysNothingIsComing(t *testing.T) {
	p := &Pipeline{Cadence: NewCadence(4)}
	if got := p.Outlook(); got != OutlookNone {
		t.Errorf("Outlook = %q, want %q", got, OutlookNone)
	}
}

func TestOutlookSaysABreakIsBeingWritten(t *testing.T) {
	p := &Pipeline{Cadence: NewCadence(1)}
	if !p.Announce(Boundary{Index: 1, InsertionAt: 200}) {
		t.Fatal("the cadence refused a slot at every boundary")
	}
	if got := p.Outlook(); got != OutlookWriting {
		t.Errorf("Outlook = %q, want %q while a slot waits on the model", got, OutlookWriting)
	}
}

func TestOutlookSaysABreakIsReady(t *testing.T) {
	// Nothing pending and audio already queued: it is going to air.
	p := &Pipeline{Cadence: NewCadence(4)}
	p.SetScheduled(1)
	if got := p.Outlook(); got != OutlookReady {
		t.Errorf("Outlook = %q, want %q", got, OutlookReady)
	}
}

func TestOutlookPrefersWritingOverReady(t *testing.T) {
	// One queued and another still being written is honestly "writing": the
	// interesting state is the one that can still fail.
	p := &Pipeline{Cadence: NewCadence(1)}
	p.SetScheduled(1)
	p.Announce(Boundary{Index: 1, InsertionAt: 200})
	if got := p.Outlook(); got != OutlookWriting {
		t.Errorf("Outlook = %q, want %q", got, OutlookWriting)
	}
}
