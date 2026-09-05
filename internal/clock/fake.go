// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package clock

import (
	"sync"
	"time"
)

// Fake is a manually driven clock. Sleep does not sleep: it advances the fake
// time and records what it was asked for, so a test can assert on the pacing
// decisions themselves rather than on elapsed wall time.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

// NewFake returns a Fake reading start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Sleep records the request and advances the clock by it. Non-positive
// durations are recorded but do not move time backwards, so a test can catch a
// caller that asked to sleep a negative duration.
func (f *Fake) Sleep(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleeps = append(f.sleeps, d)
	if d > 0 {
		f.now = f.now.Add(d)
	}
}

// Advance moves the clock without recording a sleep. Use it to simulate time
// spent computing.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// SleepCalls returns every duration Sleep was asked for, in order.
func (f *Fake) SleepCalls() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.sleeps...)
}
