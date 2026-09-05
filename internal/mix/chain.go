// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

// State describes what the scheduler wants the chain to do for one block.
//
// FadeTotal of zero means no crossfade is in progress and musicB is ignored,
// which is the common case; during a fade, FadePos is the position of out[0]
// within it.
type State struct {
	FadeTotal int  // frames in the crossfade, 0 when not fading
	FadePos   int  // position of out[0] within that fade
	Speaking  bool // true while a speech item is on air

	// GainA and GainB normalise each music item to the bus loudness target,
	// from GainFor(track.loudness_lufs). Zero means unset and plays at unity,
	// so a State built without them still makes sound.
	GainA float32
	GainB float32
}

// Chain is the whole signal path, in the one order that is correct:
//
//  0. track gain  normalise each music item to the bus target
//  1. crossfade   music item A into music item B
//  2. duck        attenuate the music bus while speech is on air
//  3. mix speech  add speech on top of the ducked music
//  4. limiter     last, so nothing downstream can push past the ceiling
//
// Per-track gain goes first because that is the only place it corrects the fade
// itself: two differently-mastered tracks blended before correction carry the
// level jump through the crossfade, which is exactly where it is most audible.
//
// Each stage changes what the next one sees. Ducking after limiting wastes
// headroom on music that was about to be turned down anyway, and limiting
// before the fade misses the peak two crossfaded tracks make together: an
// equal-power fade of two uncorrelated sources sums close to full scale even
// when neither input is near it.
//
// Chain is stateful for the life of the stream. The limiter's delay line and
// the ducker's gain ramp carry across blocks and must never be reset between
// them.
type Chain struct {
	duck *Ducker
	lim  *Limiter
}

// NewChain returns an idle chain at unity gain.
func NewChain() *Chain {
	return &Chain{duck: NewDucker(), lim: NewLimiter()}
}

// LatencyFrames is the delay the chain adds, which is the limiter's alone:
// crossfading and ducking are sample-aligned and add none.
//
// Anything that schedules by sample index must add this, or speech lands 5 ms
// away from where the scheduler put it.
func (c *Chain) LatencyFrames() int { return c.lim.LatencyFrames() }

// LimiterGain reports the gain reduction the limiter is currently applying, for
// metering. 1.0 means it is not working.
func (c *Chain) LimiterGain() float32 { return c.lim.Gain() }

// Process fills out with one block of finished audio.
//
// musicA is the item playing now; musicB is the one being faded in and may be
// nil outside a fade; speech may be nil. Any of them may be shorter than out,
// in which case the missing frames are silence.
func (c *Chain) Process(out []Frame, musicA, musicB, speech []Frame, st State) {
	// 0 and 1. Normalise each source, then crossfade or pass through.
	nA, nB := unitIfZero(st.GainA), unitIfZero(st.GainB)
	if st.FadeTotal > 0 {
		for i := range out {
			gA, gB := equalPowerGains(st.FadePos+i, st.FadeTotal)
			a, b := frameAt(musicA, i), frameAt(musicB, i)
			out[i] = Frame{
				L: a.L*nA*gA + b.L*nB*gB,
				R: a.R*nA*gA + b.R*nB*gB,
			}
		}
	} else {
		for i := range out {
			a := frameAt(musicA, i)
			out[i] = Frame{L: a.L * nA, R: a.R * nA}
		}
	}

	// 2. Duck the music bus. Only the music: running speech through the ducker
	// would have the DJ duck himself.
	if st.Speaking {
		c.duck.Engage()
	} else {
		c.duck.Release()
	}
	c.duck.Process(out)

	// 3. Add speech on top of the ducked music.
	for i := range out {
		s := frameAt(speech, i)
		out[i].L += s.L
		out[i].R += s.R
	}

	// 4. Limit, last.
	c.lim.Process(out)
}

// unitIfZero keeps State's zero value playable: an unset gain is unity, never
// silence.
func unitIfZero(g float32) float32 {
	if g == 0 {
		return 1
	}
	return g
}

// frameAt reads from a possibly-nil or short buffer, treating anything missing
// as silence.
func frameAt(buf []Frame, i int) Frame {
	if i < len(buf) {
		return buf[i]
	}
	return Frame{}
}
