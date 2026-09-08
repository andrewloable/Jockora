// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"strings"
	"testing"
)

// Jockora-e9a.52. The console has had a Resume button since the beginning and
// it set a boolean nothing was reading once the worker had exited. During the
// outage of 2026-09-08 it sat on the page looking like the fix, did nothing,
// and said nothing -- which is worse than not being there.

// TestEnrichmentRestartWakesAWorkerThatGaveUp: this is the case the button was
// always wrong about.
func TestEnrichmentRestartWakesAWorkerThatGaveUp(t *testing.T) {
	a := &App{log: quietLogger()}
	a.enrichWake = make(chan struct{}, 1)
	a.enriching.Store(false)
	a.enrichAlive.Store(false)
	a.enrichGaveUp.Store("rejected the API key")

	said, err := a.SetEnriching(true)
	if err != nil {
		t.Fatalf("SetEnriching: %v", err)
	}
	select {
	case <-a.enrichWake:
	default:
		t.Fatal("resuming a worker that gave up did not restart it")
	}
	if !strings.Contains(said, "restart") {
		t.Errorf("said %q, want it to say the worker was restarted rather than merely resumed", said)
	}
	// The old reason is cleared by the attempt, so the console does not keep
	// showing the failure the operator has just acted on.
	if why := a.enrichmentTrouble(); why != "" {
		t.Errorf("the old give-up reason survived the restart: %q", why)
	}
}

// TestEnrichmentRestartDoesNotWakeALiveWorker: a worker that is merely paused
// needs the flag flipped and nothing else. Waking it would be a second Run
// against a live one, which is what the enrichment lock exists to stop.
func TestEnrichmentRestartDoesNotWakeALiveWorker(t *testing.T) {
	a := &App{log: quietLogger()}
	a.enrichWake = make(chan struct{}, 1)
	a.enriching.Store(false)
	a.enrichAlive.Store(true)

	said, err := a.SetEnriching(true)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.enrichWake:
		t.Fatal("resuming a live worker tried to start a second one")
	default:
	}
	if strings.Contains(said, "restart") {
		t.Errorf("said %q; a paused worker is resumed, not restarted", said)
	}
	if !a.enrichmentRunning() {
		t.Error("resuming did not actually resume")
	}
}

// TestEnrichmentRestartPausingNeverWakesAnything: the wake channel is for
// starting work, and pausing must not queue a start that fires later.
func TestEnrichmentRestartPausingNeverWakesAnything(t *testing.T) {
	a := &App{log: quietLogger()}
	a.enrichWake = make(chan struct{}, 1)
	a.enriching.Store(true)
	a.enrichAlive.Store(false)

	if _, err := a.SetEnriching(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.enrichWake:
		t.Fatal("pausing queued a restart")
	default:
	}
	if a.enriching.Load() {
		t.Error("pausing did not pause")
	}
}

// TestEnrichmentRestartNamesWhatTheButtonWillDo: "Resume" over a dead worker
// and "Resume" over a paused one are different promises, and the operator
// cannot tell which they are about to get.
func TestEnrichmentRestartNamesWhatTheButtonWillDo(t *testing.T) {
	a := &App{log: quietLogger()}
	a.enriching.Store(true)
	a.enrichAlive.Store(true)
	if a.EnrichmentStopped() {
		t.Error("a live worker reports as stopped")
	}
	a.enrichAlive.Store(false)
	if !a.EnrichmentStopped() {
		t.Error("a worker that gave up does not report as stopped")
	}
	// PAUSED IS NOT STOPPED. The operator put it there.
	a.enriching.Store(false)
	if a.EnrichmentStopped() {
		t.Error("a worker paused on purpose reports as stopped")
	}
}
