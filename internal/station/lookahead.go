// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"math"
	"sort"
	"sync"
	"time"
)

// DefaultLookahead is where T starts, before anything has been measured.
//
// 150 seconds is a guess with a reason: generation is an LLM call plus a TTS
// render, sometimes twice, and the buffer exists precisely so that none of it
// happens on the critical path. It is replaced by RecommendedT once enough real
// generations have been timed.
var DefaultLookahead = 150 * time.Second

// LookaheadSafetyFactor multiplies the measured p95 to get T.
//
// The p95 is the value that fails one break in twenty; the margin is what turns
// that into a value that fails almost none. A dropped break is cheap but it is
// not free, and it is invisible -- nobody reports the joke they did not hear.
const LookaheadSafetyFactor = 1.5

// MinTimingSample is how many generations must be timed before a recommendation
// is worth acting on. Setting T from three samples is how a lookahead that
// looked fine in testing fails in a room.
const MinTimingSample = 20

// Lookahead decides when to start generating a break, and measures how long
// generation actually takes.
//
// The trigger is a TIME threshold, not "one track ahead". Track length varies
// enormously in a real library -- a 90-second punk track and a 12-minute live
// version sit in the same rotation -- and a count-based trigger would be far
// too early for one and far too late for the other. A run of short tracks
// legitimately pushes the trigger two or three tracks back, and that is the
// design working rather than depth creep to cap.
type Lookahead struct {
	mu      sync.Mutex
	t       time.Duration
	timings []time.Duration
	retried int
}

// NewLookahead returns a Lookahead with the given trigger threshold; zero or
// less takes the default.
func NewLookahead(t time.Duration) *Lookahead {
	if t <= 0 {
		t = DefaultLookahead
	}
	return &Lookahead{t: t}
}

// T is the current trigger threshold.
func (l *Lookahead) T() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.t
}

// SetT replaces the threshold, so a measured recommendation can be applied
// without restarting.
func (l *Lookahead) SetT(t time.Duration) {
	if t <= 0 {
		return
	}
	l.mu.Lock()
	l.t = t
	l.mu.Unlock()
}

// ShouldTrigger reports whether generation for a break at insertionAt should
// start now. Both are seconds on the same timeline as the mixer's clock.
//
// It stays true after the threshold rather than firing on one exact instant: a
// caller that polls every few seconds must not be able to step over the moment
// and never generate at all.
func (l *Lookahead) ShouldTrigger(now, insertionAt float64) bool {
	// AS SOON AS THE SLOT EXISTS. This used to wait until insertionAt - now
	// fell below T, which is a head start rather than a wait -- but it is the
	// SMALLEST head start that usually works, and against a hosted model whose
	// latency is somebody else's queue, "usually" is what produced breaks that
	// finished after the boundary they were written for and were deleted
	// unheard.
	//
	// Starting immediately costs nothing extra: the work is identical and the
	// result is a file on disk either way. The only thing that changes is how
	// much slack remains when the model is slow.
	//
	// T is NOT dead. It moved to where it was always doing the load-bearing
	// work: InTime, which decides whether what came back is still worth airing.
	return now < insertionAt
}

// Depth is how many track boundaries the trigger reaches across, given an
// average track length. Reported for observability, never enforced: a value
// above one is correct on short material.
func (l *Lookahead) Depth(now, insertionAt, trackSeconds float64) int {
	if trackSeconds <= 0 {
		return 0
	}
	return int(math.Ceil((insertionAt - now - l.T().Seconds()) / trackSeconds))
}

// InTime reports whether a generation that started at startedAt and took
// elapsed will be ready before the insertion point.
//
// Finishing exactly ON the insertion point is late. The mixer needs the file
// before it reaches the boundary, not as it crosses it.
func (l *Lookahead) InTime(startedAt, insertionAt float64, elapsed time.Duration) bool {
	return startedAt+elapsed.Seconds() < insertionAt
}

// RecordTiming records one generation's TOTAL elapsed time.
//
// attempts is how many writer passes it took. Both matter: measuring only clean
// first passes understates T by a whole LLM call plus a TTS render, on exactly
// the breaks most likely to be late. A retried generation belongs in the
// sample, not in a separate bucket that nobody looks at.
func (l *Lookahead) RecordTiming(elapsed time.Duration, attempts int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.timings = append(l.timings, elapsed)
	if attempts > 1 {
		l.retried++
	}
}

// Count is how many generations have been timed.
func (l *Lookahead) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.timings)
}

// RetriedCount is how many of them needed more than one writer pass.
func (l *Lookahead) RetriedCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.retried
}

// P50 and P95 are over every recorded generation, retries included.
func (l *Lookahead) P50() time.Duration { return l.percentile(0.50) }

// P95 is the value T is set from.
func (l *Lookahead) P95() time.Duration { return l.percentile(0.95) }

func (l *Lookahead) percentile(p float64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.timings) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), l.timings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	i := int(p * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// RecommendedT is p95 x the safety factor, or zero while the sample is too
// small to act on.
func (l *Lookahead) RecommendedT() time.Duration {
	if l.Count() < MinTimingSample {
		return 0
	}
	return time.Duration(float64(l.P95()) * LookaheadSafetyFactor)
}
