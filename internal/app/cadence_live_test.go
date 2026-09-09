// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"testing"

	"github.com/andrewloable/jockora/internal/station"
)

// THE CONSOLE MUST REPORT THE CADENCE THE STATION IS USING, not the one it was
// started with. The overview served a config field that only SetCadence wrote,
// which made the number correct by coincidence and racy by construction: an
// admin HTTP handler wrote it while another read it.
//
// Every test here is TestLiveCadence*, which is the -run pattern for this fix.

func TestLiveCadenceIsWhatTheOverviewReports(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	pipe := &station.Pipeline{Cadence: station.NewCadence(4)}
	a := &App{
		opts: Options{Library: &Library{Store: s}, Breaks: pipe},
		cfg:  testConfig(t),
		log:  quietLogger(),
	}
	a.cfg.BreakEveryNTracks = 4

	if got := a.Overview().(map[string]any)["cadence"]; got != 4 {
		t.Fatalf("cadence = %v, want the starting 4", got)
	}
	if err := a.SetCadence(1); err != nil {
		t.Fatalf("SetCadence: %v", err)
	}
	if got := a.Overview().(map[string]any)["cadence"]; got != 1 {
		t.Errorf("cadence = %v, want the operator's 1", got)
	}

	// AND WHATEVER ELSE SETS IT. Going through App.SetCadence is not the only
	// way the live cadence changes, and a console reading a field that only
	// that one method writes is correct by coincidence: it reports the number
	// the last caller happened to announce, not the number the DJ is using.
	pipe.SetCadence(station.NewCadence(6))
	if got := a.Overview().(map[string]any)["cadence"]; got != 6 {
		t.Errorf("cadence = %v, want the pipeline's 6", got)
	}
}

func TestLiveCadenceFallsBackToTheConfiguredOne(t *testing.T) {
	// No DJ is a real state -- the station plays music with nothing to say --
	// and the console still has to render a number.
	s := openUpgraded(t, v01Fixture(t))
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}
	a.cfg.BreakEveryNTracks = 7
	if got := a.Overview().(map[string]any)["cadence"]; got != 7 {
		t.Errorf("cadence = %v, want the configured 7", got)
	}

	// A pipeline that has no cadence at all reads the same way.
	a.opts.Breaks = &station.Pipeline{}
	if got := a.Overview().(map[string]any)["cadence"]; got != 7 {
		t.Errorf("cadence = %v, want the configured 7", got)
	}
}

// TestLiveOverlapIsWhatTheOverviewReports: the lead-in, on the cadence's own
// terms. Found reviewing Jockora-ugw -- the configured default was applied only
// inside the branch that runs when a store exists, so a deployment without one
// reported and used zero however the flag was set.
func TestLiveOverlapIsWhatTheOverviewReports(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	pipe := &station.Pipeline{Cadence: station.NewCadence(4)}
	cfg := testConfig(t)
	cfg.BreakOverlapS = 2.5
	a := &App{
		opts:       Options{Library: &Library{Store: s}, Breaks: pipe},
		cfg:        cfg,
		log:        quietLogger(),
		overlap:    cfg.BreakOverlapS,
		overlapSet: true,
	}

	if got := a.Overview().(map[string]any)["break_overlap_s"]; got != 2.5 {
		t.Fatalf("break_overlap_s = %v, want the configured 2.5", got)
	}
	if err := a.SetBreakOverlap(1); err != nil {
		t.Fatalf("SetBreakOverlap: %v", err)
	}
	if got := a.Overview().(map[string]any)["break_overlap_s"]; got != 1.0 {
		t.Errorf("break_overlap_s = %v, want the operator's 1", got)
	}
	// AND THE PIPELINE ACTUALLY HAS IT, not just the number the console shows.
	if got := pipe.Overlap(); got != 1 {
		t.Errorf("the pipeline is on %v, so the console and the DJ disagree", got)
	}

	// ZERO IS A CHOICE, not an absent setting: it means never start before the
	// boundary. A non-zero test for "has anybody chosen" would make it the one
	// value an operator cannot pick.
	if err := a.SetBreakOverlap(0); err != nil {
		t.Fatalf("SetBreakOverlap(0): %v", err)
	}
	if got := a.Overview().(map[string]any)["break_overlap_s"]; got != 0.0 {
		t.Errorf("break_overlap_s = %v, want the operator's 0", got)
	}

	// REFUSED, and the range said out loud rather than silently clamped: an
	// operator who types 20 has misunderstood the setting, and a clamp would
	// hide that behind a number they did not choose.
	if err := a.SetBreakOverlap(MaxBreakOverlapS + 1); err == nil {
		t.Error("an overlap past the ceiling was accepted")
	}
	if err := a.SetBreakOverlap(-1); err == nil {
		t.Error("a negative overlap was accepted")
	}
}
