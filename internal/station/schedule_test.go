// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"testing"

	"github.com/andrewloable/jockora/internal/mix"
)

// All named TestSchedule*, matching the task's own `-run TestSchedule`.

const (
	testPersonaVoice   = "kokoro:am_michael"
	testAnnouncerVoice = "kokoro:af_bella"
)

func TestScheduleAdSlotRatio(t *testing.T) {
	s := NewAdScheduler(AdSlotInterval, testPersonaVoice, testAnnouncerVoice)

	ads := 0
	for i := 0; i < 100; i++ {
		if s.Next(mix.PlacementRamp).Kind == SlotAd {
			ads++
		}
	}
	if ads < 20 || ads > 30 {
		t.Errorf("%d of 100 slots were adverts, want 20-30: below that they never become familiar, above it the station feels like advertisements", ads)
	}
}

// TestScheduleAdsUseAnnouncerVoice: the voice change does the work of a jingle
// without needing one. Nobody has to be told that this bit is an advert.
func TestScheduleAdsUseAnnouncerVoice(t *testing.T) {
	s := NewAdScheduler(AdSlotInterval, testPersonaVoice, testAnnouncerVoice)

	var sawAd, sawBreak bool
	for i := 0; i < 20; i++ {
		slot := s.Next(mix.PlacementRamp)
		switch slot.Kind {
		case SlotAd:
			sawAd = true
			if slot.Voice != testAnnouncerVoice {
				t.Errorf("advert used %q, want the announcer voice %q", slot.Voice, testAnnouncerVoice)
			}
		case SlotBreak:
			sawBreak = true
			if slot.Voice != testPersonaVoice {
				t.Errorf("break used %q, want the persona voice %q", slot.Voice, testPersonaVoice)
			}
		}
	}
	if !sawAd || !sawBreak {
		t.Fatalf("20 slots produced ad=%v break=%v; the test proved nothing", sawAd, sawBreak)
	}
}

func TestScheduleNoBackToBackAds(t *testing.T) {
	// Interval 1 asks for an advert every single slot. The guard is what stops
	// that becoming an advert block, and at interval 4 it would never be
	// exercised at all.
	s := NewAdScheduler(1, testPersonaVoice, testAnnouncerVoice)

	prevWasAd := false
	ads := 0
	for i := 0; i < 50; i++ {
		isAd := s.Next(mix.PlacementBetween).Kind == SlotAd
		if isAd && prevWasAd {
			t.Fatalf("adverts at slots %d and %d; ad blocks are not a thing here", i, i+1)
		}
		if isAd {
			ads++
		}
		prevWasAd = isAd
	}
	if ads == 0 {
		t.Fatal("no adverts at all at interval 1; the guard swallowed everything")
	}
}

// TestScheduleAdsUseBetweenPlacement: an advert is a production, not a segue.
// Talking over an intro is a DJ move and it would blur exactly the line the
// announcer voice is there to draw.
func TestScheduleAdsUseBetweenPlacement(t *testing.T) {
	s := NewAdScheduler(AdSlotInterval, testPersonaVoice, testAnnouncerVoice)

	for _, want := range []mix.Placement{mix.PlacementRamp, mix.PlacementOutro, mix.PlacementSpan, mix.PlacementBetween} {
		s2 := NewAdScheduler(1, testPersonaVoice, testAnnouncerVoice)
		slot := s2.Next(want)
		if slot.Kind != SlotAd {
			t.Fatalf("interval 1 did not produce an advert on the first slot")
		}
		if slot.Placement != mix.PlacementBetween {
			t.Errorf("advert placed %v when the boundary offered %v, want between", slot.Placement, want)
		}
	}

	// A normal break keeps whatever placement it was offered.
	if got := s.Next(mix.PlacementRamp); got.Kind == SlotBreak && got.Placement != mix.PlacementRamp {
		t.Errorf("break placement = %v, want the offered ramp", got.Placement)
	}
}

func TestScheduleAdsUseTheBetweenWindow(t *testing.T) {
	s := NewAdScheduler(1, testPersonaVoice, testAnnouncerVoice)
	slot := s.Next(mix.PlacementRamp)
	if slot.WindowSeconds != BetweenWindowSeconds {
		t.Errorf("advert window = %vs, want the between window %vs: sizing an advert to a ramp it will not use would make every one of them too short",
			slot.WindowSeconds, BetweenWindowSeconds)
	}
}

func TestScheduleWithoutAnAnnouncerVoiceRunsNoAds(t *testing.T) {
	// A station with one voice must not read its own adverts. Losing adverts is
	// a missing feature; a DJ reading them is a broken illusion.
	s := NewAdScheduler(AdSlotInterval, testPersonaVoice, "")
	for i := 0; i < 20; i++ {
		if s.Next(mix.PlacementRamp).Kind == SlotAd {
			t.Fatal("scheduled an advert with no announcer voice configured")
		}
	}
}
