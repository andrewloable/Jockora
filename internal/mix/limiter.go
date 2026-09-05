// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import "math"

const (
	// LimiterLookahead is how far ahead the limiter sees. It is also exactly
	// the latency it adds, which every downstream timing calculation must
	// account for.
	LimiterLookahead = 5 * SampleRate / 1000 // 5 ms, 240 frames at 48 kHz

	limiterAttack  = 0.001 // seconds, ramp down onto a peak
	limiterRelease = 0.100 // seconds, ramp back up afterwards
)

// Limiter holds the mix under the true-peak ceiling without audible distortion.
//
// It is a lookahead limiter, not a clipper: a delay line lets it start reducing
// gain BEFORE a peak arrives, so the waveform keeps its shape. Clipping the same
// material distorts loudest at exactly the moment the product is being judged,
// speech over ducked music.
//
// Gain reduction is stereo-linked. Independent per-channel gain would swing the
// stereo image every time one side peaked.
type Limiter struct {
	delay   []Frame // circular, holds the lookahead window
	pos     int     // index of the oldest frame, the next one out
	gain    float32
	attack  float32
	release float32
	ceiling float32
}

// NewLimiter returns a limiter with a 5 ms lookahead, holding TruePeakCeilingDBTP.
func NewLimiter() *Limiter {
	return &Limiter{
		delay:   make([]Frame, LimiterLookahead),
		gain:    1,
		attack:  onePoleCoef(limiterAttack),
		release: onePoleCoef(limiterRelease),
		ceiling: float32(math.Pow(10, TruePeakCeilingDBTP/20)),
	}
}

// LatencyFrames reports the delay the limiter adds. Callers MUST account for it:
// the scheduler places speech by sample index, and an unaccounted 240 frames
// shifts every placement by 5 ms.
func (l *Limiter) LatencyFrames() int { return len(l.delay) }

// Gain reports the gain reduction currently applied, for metering and for
// proving the chain order. 1.0 means the limiter is not working.
func (l *Limiter) Gain() float32 { return l.gain }

// Process limits buf in place. The output is the input delayed by
// LatencyFrames(), with gain reduction applied.
func (l *Limiter) Process(buf []Frame) {
	for i := range buf {
		in := buf[i]

		// Swap the incoming frame for the oldest one. After this the delay line
		// holds the frames that come after `out`, which is its lookahead.
		out := l.delay[l.pos]
		l.delay[l.pos] = in
		l.pos++
		if l.pos == len(l.delay) {
			l.pos = 0
		}

		// The lowest gain any frame in the window will need. Including `out`
		// itself is what makes the guarantee hold at the window edge.
		target := l.requiredGain(out)
		// ponytail: O(lookahead) scan per frame, about 1% of a core at 48 kHz.
		// Swap in a monotonic deque if profiling ever says it matters.
		for _, f := range l.delay {
			if g := l.requiredGain(f); g < target {
				target = g
			}
		}

		coef := l.release
		if target < l.gain {
			coef = l.attack
		}
		l.gain += (target - l.gain) * coef

		// The smoother approaches its target asymptotically, so it can sit a
		// few parts per billion above what this frame needs. Clamping to that
		// makes the ceiling a guarantee rather than a very good approximation.
		// It is not a clipper: by the time a peak reaches the output the
		// smoother has had the whole lookahead to converge, so this binds only
		// on the single sample that defines the window peak.
		g := min(l.gain, l.requiredGain(out))

		buf[i].L = out.L * g
		buf[i].R = out.R * g
	}
}

// requiredGain is the gain that would put this frame exactly on the ceiling,
// capped at unity: the limiter only ever turns things down.
func (l *Limiter) requiredGain(f Frame) float32 {
	peak := max(abs32(f.L), abs32(f.R)) // stereo-linked
	if peak <= l.ceiling {
		return 1
	}
	return l.ceiling / peak
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
