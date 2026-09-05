// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"time"

	"github.com/andrewloable/jockora/internal/clock"
)

// Pacer makes the mixer produce audio at exactly real-time speed.
//
// Without it the mixer writes an hour of PCM in seconds, the segmenter emits
// everything at once, old segments are pruned before anyone fetches them, and
// the stream is not live at all while every other test still passes.
//
// Pacing is against an absolute target computed from the total frames written,
// never a per-block ticker: a ticker's per-block rounding and the cost of the
// work between ticks accumulate, and over an hour that is minutes of drift.
type Pacer struct {
	clk           clock.Clock
	start         time.Time
	framesWritten int64
}

// NewPacer starts a pacer whose schedule begins now, by the given clock.
func NewPacer(clk clock.Clock) *Pacer {
	return &Pacer{clk: clk, start: clk.Now()}
}

// Wait accounts for frames just produced and sleeps until they should have
// finished playing. If the caller is already behind schedule it does not sleep
// at all; it never sleeps a negative duration.
func (p *Pacer) Wait(frames int) {
	p.framesWritten += int64(frames)

	target := p.start.Add(framesToDuration64(p.framesWritten))
	if d := target.Sub(p.clk.Now()); d > 0 {
		p.clk.Sleep(d)
	}
}

// Drift reports how far behind schedule the mixer is: elapsed wall time minus
// the audio produced. Positive means late, negative means running ahead.
func (p *Pacer) Drift() time.Duration {
	return p.clk.Now().Sub(p.start) - framesToDuration64(p.framesWritten)
}

// FramesWritten reports the total frames accounted for so far.
func (p *Pacer) FramesWritten() int64 { return p.framesWritten }

// framesToDuration64 converts a frame count to wall-clock time without ever
// forming the full nanosecond product, which would overflow int64 after about
// 53 hours of continuous streaming. Seconds and remainder are converted
// separately, so a server that stays up for months still keeps exact time.
func framesToDuration64(n int64) time.Duration {
	sec, rem := n/SampleRate, n%SampleRate
	return time.Duration(sec)*time.Second + time.Duration(rem*int64(time.Second)/SampleRate)
}

// Clock returns the clock this pacer runs on, so that anything else in the mixer
// loop measures time the same way it does.
func (p *Pacer) Clock() clock.Clock { return p.clk }
