// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import "github.com/andrewloable/jockora/internal/mix"

// AdSlotInterval is how many break slots pass between adverts.
//
// One in four keeps adverts present enough to become familiar -- which is the
// entire point, since an advert nobody recognises is just an interruption --
// without crossing into the "this feels like advertisements" complaint that
// sinks other AI radio.
const AdSlotInterval = 4

// SlotKind is what a break slot has been assigned to.
type SlotKind int

const (
	// SlotBreak is the DJ talking.
	SlotBreak SlotKind = iota
	// SlotAd is an advert, read by the announcer.
	SlotAd
)

// Slot is one scheduled break, resolved to a voice, a placement and a window.
type Slot struct {
	Kind          SlotKind
	Voice         string
	Placement     mix.Placement
	WindowSeconds float64
}

// AdScheduler turns break slots into adverts, one in every interval.
type AdScheduler struct {
	interval  int
	persona   string
	announcer string

	since     int
	lastWasAd bool
}

// NewAdScheduler returns a scheduler.
//
// An empty announcer voice disables adverts entirely. A station with one voice
// must not read its own adverts: losing them is a missing feature, and a DJ
// reading them is a broken illusion.
func NewAdScheduler(interval int, personaVoice, announcerVoice string) *AdScheduler {
	if interval < 1 {
		interval = AdSlotInterval
	}
	return &AdScheduler{interval: interval, persona: personaVoice, announcer: announcerVoice}
}

// Next assigns the coming break slot, given the placement the boundary offers.
//
// Call it once per slot, in order.
func (s *AdScheduler) Next(offered mix.Placement) Slot {
	s.since++

	isAd := s.announcer != "" && s.since >= s.interval && !s.lastWasAd
	s.lastWasAd = isAd
	if !isAd {
		return Slot{Kind: SlotBreak, Voice: s.persona, Placement: offered}
	}
	s.since = 0

	// An advert is a PRODUCTION, not a segue. Talking over an intro is a DJ
	// move, and using it for an advert blurs exactly the line the announcer
	// voice exists to draw. The window follows the placement: sizing an advert
	// to a ramp it will never use would make every one of them too short.
	return Slot{
		Kind:          SlotAd,
		Voice:         s.announcer,
		Placement:     mix.PlacementBetween,
		WindowSeconds: BetweenWindowSeconds,
	}
}
