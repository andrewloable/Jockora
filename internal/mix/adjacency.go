// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

// Placement is where a break sits relative to a track transition.
type Placement int

const (
	// PlacementBetween puts the break in the gap between two tracks.
	PlacementBetween Placement = iota
	// PlacementRamp talks over the instrumental intro of the incoming track.
	PlacementRamp
	// PlacementOutro talks over the instrumental tail of the outgoing one.
	PlacementOutro
	// PlacementSpan runs from the outgoing track's tail, through the crossfade,
	// into the incoming track's intro. The classic segue, and the longest
	// window available: its budget is outro + fade + ramp.
	PlacementSpan
)

func (p Placement) String() string {
	switch p {
	case PlacementRamp:
		return "ramp"
	case PlacementOutro:
		return "outro"
	case PlacementSpan:
		return "span"
	default:
		return "between"
	}
}

// MaxFadeFrames caps any crossfade at six seconds. Past that a transition stops
// reading as a transition and starts sounding like a mistake.
const MaxFadeFrames = 6 * SampleRate

// droppedBreakFadeNumerator and denominator stretch a fade to 1.5x when the
// break that was going to sit in the transition never arrived.
const droppedBreakFadeNumerator, droppedBreakFadeDenominator = 3, 2

// Transition describes how one music item gives way to the next.
type Transition struct {
	// FadeFrames is the crossfade length, and zero means a sample-adjacent
	// splice: A's final sample followed immediately by B's first.
	FadeFrames int
}

// Adjacent reports whether this transition is a gapless splice.
func (t Transition) Adjacent() bool { return t.FadeFrames == 0 }

// TransitionFor turns the scanner's no_crossfade_next flag into a fade length.
//
// The flag exists because on live records, DJ mixes, concept albums and
// classical movements a forced crossfade is worse than the gap it prevents, and
// those are exactly the albums a music person notices. Zero means zero: a short
// compromise fade is audible on gapless material and defeats the flag.
func TransitionFor(noCrossfadeNext bool, defaultFadeFrames int) Transition {
	if noCrossfadeNext {
		return Transition{FadeFrames: 0}
	}
	return Transition{FadeFrames: defaultFadeFrames}
}

// WithDroppedBreak lengthens the fade to cover a break that never aired.
//
// Breaks drop often and by design: late generation, a validator rejection, a TTS
// failure. Each drop would otherwise leave an ordinary hard transition exactly
// where a beat of speech was planned. Stretching the fade makes the absence read
// as deliberate. No silence is inserted; a gap is the thing being avoided, and a
// dropped break is never an error.
//
// A gapless pair is left alone. The no_crossfade_next flag outranks this.
func (t Transition) WithDroppedBreak() Transition {
	if t.Adjacent() {
		return t
	}
	t.FadeFrames = min(
		t.FadeFrames*droppedBreakFadeNumerator/droppedBreakFadeDenominator,
		MaxFadeFrames,
	)
	return t
}

// PlaceBreak resolves where a break can actually go, given the transition it
// was scheduled against.
//
// A gapless splice has no fade window to talk over, so ramp, outro and span are
// not available there. The break is relocated rather than dropped: breaks are
// optional, but silently losing one is not the same as moving it.
//
// In practice station.Cadence refuses to create a slot at a gapless boundary at
// all, so this branch should never be reached for a slot the scheduler made --
// moving the break to a different BOUNDARY is a better answer than moving it
// within one, because "between" at a gapless boundary would insert exactly the
// gap the flag exists to prevent. This stays as a second line of defence.
func PlaceBreak(want Placement, t Transition) Placement {
	if t.Adjacent() {
		return PlacementBetween
	}
	return want
}
