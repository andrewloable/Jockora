// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"sync"
	"time"
)

// Ring is a bounded FIFO of frames sitting between the decoders and the mixer.
//
// Its whole purpose is that reading never stalls: when the ring runs dry the
// mixer gets silence and a counter goes up. A brief gap is recoverable, a
// stalled stream is not, and music never stopping is the invariant everything
// else in this program is arranged around.
type Ring struct {
	mu        sync.Mutex
	buf       []Frame
	head      int // next frame to read
	count     int // frames currently held
	underruns uint64
}

// NewRing returns a ring holding at most capacityFrames frames.
func NewRing(capacityFrames int) *Ring {
	if capacityFrames < 1 {
		capacityFrames = 1
	}
	return &Ring{buf: make([]Frame, capacityFrames)}
}

// Write appends as many frames as fit and reports how many were accepted. A
// short return means the ring is full; the caller decides whether to retry or
// drop. The ring never grows, because a ring that grew on demand would turn a
// slow encoder into unbounded memory growth.
func (r *Ring) Write(f []Frame) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := min(len(f), len(r.buf)-r.count)
	for i := 0; i < n; i++ {
		r.buf[(r.head+r.count+i)%len(r.buf)] = f[i]
	}
	r.count += n
	return n
}

// Read fills out completely, always, and reports how many of those frames were
// real audio. Any shortfall is padded with silence and counts as one underrun.
//
// Read never blocks and never returns short: blocking here would stall the
// stream, which is the exact failure this component exists to prevent.
func (r *Ring) Read(out []Frame) int {
	if len(out) == 0 {
		return 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	n := min(len(out), r.count)
	for i := 0; i < n; i++ {
		out[i] = r.buf[(r.head+i)%len(r.buf)]
	}
	r.head = (r.head + n) % len(r.buf)
	r.count -= n

	if n < len(out) {
		clear(out[n:])
		r.underruns++
	}
	return n
}

// Occupancy reports how much audio the ring is currently holding.
func (r *Ring) Occupancy() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return FramesToDuration(r.count)
}

// UnderrunCount reports how many reads have been padded with silence. Zero in a
// healthy run; non-zero means the decoders are not keeping up with the clock.
func (r *Ring) UnderrunCount() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.underruns
}
