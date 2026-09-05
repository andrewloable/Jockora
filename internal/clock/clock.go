// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package clock provides an injectable clock so that time-dependent code can be
// tested without waiting. A thirty-minute pacing test has to run in
// milliseconds, or nobody ever runs it.
package clock

import "time"

// Clock is the only source of time for code that paces itself.
type Clock interface {
	Now() time.Time
	Sleep(time.Duration)
}

// Real is the production clock.
type Real struct{}

func (Real) Now() time.Time        { return time.Now() }
func (Real) Sleep(d time.Duration) { time.Sleep(d) }
