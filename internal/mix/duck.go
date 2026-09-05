// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import "math"

// The loudness contract. These numbers are fixed for the whole program: the
// mixer, the normaliser and the limiter all target them, and a component that
// invents its own makes every transition audible.
const (
	MusicLUFS           = -16.0 // music, integrated
	MusicDuckedLUFS     = -28.0 // music while the DJ is speaking
	SpeechLUFS          = -16.0 // speech, short-term
	TruePeakCeilingDBTP = -1.0  // enforced by the limiter, not here

	// DuckDepthDB is how far the music drops under speech: the gap between the
	// two music targets, never an independently chosen number.
	DuckDepthDB = MusicDuckedLUFS - MusicLUFS

	// Ramp times, as the time the ramp takes end to end.
	DuckAttack  = 0.080 // seconds
	DuckRelease = 0.400 // seconds
)

// Ducker lowers the music bus while the DJ speaks and brings it back afterwards.
//
// The ramp is a one-pole smoother rather than a gain switch: an abrupt gain
// change is an audible click, and a stepped ramp is zipper noise. Ducking is
// done here in Go, not with ffmpeg's sidechaincompress, because the encoder must
// see exactly one stream for the seamless splice to work.
//
// Only the music bus is ducked. Never run speech through this.
type Ducker struct {
	current float32 // applied gain, linear
	target  float32
	attack  float32 // one-pole coefficient
	release float32
}

// NewDucker returns an idle ducker at unity gain.
func NewDucker() *Ducker {
	return &Ducker{
		current: 1,
		target:  1,
		attack:  onePoleCoef(DuckAttack),
		release: onePoleCoef(DuckRelease),
	}
}

// Engage starts ducking towards the ducked music target.
func (d *Ducker) Engage() { d.target = float32(math.Pow(10, DuckDepthDB/20)) }

// Release brings the music back to unity.
func (d *Ducker) Release() { d.target = 1 }

// Gain reports the currently applied linear gain.
func (d *Ducker) Gain() float32 { return d.current }

// Process applies the ducking gain to buf in place, ramping one frame at a time.
func (d *Ducker) Process(buf []Frame) {
	coef := d.release
	if d.target < d.current {
		coef = d.attack
	}
	for i := range buf {
		d.current += (d.target - d.current) * coef
		buf[i].L *= d.current
		buf[i].R *= d.current
	}
}

// onePoleCoef converts a ramp time to a one-pole smoothing coefficient.
//
// rampSeconds is the time the ramp TAKES, not the exponential time constant:
// four time constants leaves 1.8% of the move outstanding, which is inaudible
// and lands the duck on target well inside the tolerance the mixer is graded
// against. Treating the stated 80 ms as the time constant instead leaves the
// duck 1.9 dB shy of target after 200 ms.
func onePoleCoef(rampSeconds float64) float32 {
	tau := rampSeconds / 4
	return float32(1 - math.Exp(-1/(tau*SampleRate)))
}
