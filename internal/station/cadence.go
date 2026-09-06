// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

// DefaultBreakEveryNTracks is how many track boundaries pass between breaks
// when nothing says otherwise.
//
// It is a DEFAULT, never a constant to build against. "The DJ talks too much"
// is the loudest complaint aimed at every AI radio product that exists, and the
// only way to find the right number is to live with a station for a week and
// change it. Config owns the real value; this is what an unset one falls back
// to.
const DefaultBreakEveryNTracks = 4

// Cadence decides WHICH track boundaries get a break at all.
//
// It is the first link in the break pipeline: placement decides where inside a
// boundary the speech sits, lookahead decides when generation starts, and ad
// scheduling decides which slots become adverts -- but all three assume a slot
// already exists, and nothing else creates one.
//
// It knows nothing about whether a break it scheduled ever aired. That is
// deliberate and structural. Breaks drop often and by design -- late
// generation, a validator rejection, a TTS failure -- and a cadence that could
// be told about a drop is one somebody will eventually make retry sooner, which
// compounds every drop into the DJ talking more often.
type Cadence struct {
	everyN    int
	sinceLast int
	lastAsked int
}

// NewCadence returns a cadence firing every everyN boundaries. A value below 1
// falls back to the default rather than dividing by zero or talking constantly.
func NewCadence(everyN int) *Cadence {
	if everyN < 1 {
		everyN = DefaultBreakEveryNTracks
	}
	return &Cadence{everyN: everyN}
}

// EveryN is the configured cadence.
func (c *Cadence) EveryN() int { return c.everyN }

// SlotAt reports whether the boundary ending track boundaryIndex gets a break.
//
// Boundaries are numbered from 1. Zero is "before anything has played" and
// never gets a slot: the stream opens with music, not with a voice.
//
// It ADVANCES state, so it must be called once per boundary, in order. Asking
// about a boundary already passed returns false rather than double-counting.
//
// noCrossfadeNext refuses the slot WITHOUT consuming it. Talking across a
// gapless album transition is worse than the crossfade that flag already
// refuses -- a live record, a DJ mix, a concert segue, a classical movement --
// so the break moves to the next eligible boundary instead of airing there or
// vanishing.
func (c *Cadence) SlotAt(boundaryIndex int, noCrossfadeNext bool) bool {
	if boundaryIndex < 1 || boundaryIndex <= c.lastAsked {
		return false
	}
	c.lastAsked = boundaryIndex

	c.sinceLast++
	if c.sinceLast < c.everyN {
		return false
	}
	if noCrossfadeNext {
		// Not reset: the slot is owed, and the next ordinary boundary pays it.
		return false
	}
	c.sinceLast = 0
	return true
}

// UpcomingSlots returns the next n boundary indices expected to carry a break.
//
// This is the PLAN, not a promise. It cannot see which future boundaries are
// gapless, and a gapless one pushes its slot later -- so real slots land on or
// after these numbers, never before. That is the direction the generator can
// tolerate: it starts work early and waits, rather than being surprised.
func (c *Cadence) UpcomingSlots(from, n int) []int {
	if n < 1 {
		return nil
	}
	// A slot already owed (held back by a gapless boundary) lands at the very
	// next boundary, not at from itself, which has been asked already.
	step := c.everyN - c.sinceLast
	if step < 1 {
		step = 1
	}

	out := make([]int, 0, n)
	next := from + step
	for range n {
		out = append(out, next)
		next += c.everyN
	}
	return out
}
