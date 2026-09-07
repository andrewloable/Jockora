// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/store"
)

// ErrScanRunning refuses a second concurrent scan.
var ErrScanRunning = errors.New("library: a rescan is already running")

// RescanProgress is what the console shows while a scan runs.
//
// It rides on /now.json rather than an endpoint of its own: that poll already
// exists, and a second one is a second thing to break.
type RescanProgress struct {
	Running    bool      `json:"running"`
	Found      int       `json:"found"`
	Skipped    int       `json:"skipped"`
	Missing    int       `json:"missing"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	// Err is the failure as text, because this crosses JSON to a page. Empty
	// on a clean run.
	Err string `json:"err,omitempty"`
}

// Rescan runs library scans on demand, ONE AT A TIME.
//
// Single-flight is not politeness. Two walks running together each mark
// missing what the other has not reached yet, and there is no correct answer to
// that race -- only two wrong ones.
type Rescan struct {
	store *store.Store
	clk   clock.Clock

	// scan is ScanSources, replaceable so a test can block, fail or cancel on
	// demand. A real folder can do none of those three.
	scan func(context.Context, *store.Store, ScanProgress) (Stats, error)

	mu   sync.Mutex
	prog RescanProgress
}

// NewRescan builds the job.
func NewRescan(s *store.Store, clk clock.Clock) *Rescan {
	return &Rescan{store: s, clk: clk, scan: ScanSources}
}

// Start begins a scan in the background and returns immediately, so the request
// that asked for it is not held open for the length of a library walk.
func (r *Rescan) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.prog.Running {
		r.mu.Unlock()
		return ErrScanRunning
	}
	// Reset rather than update: the previous run's error and counts belong to
	// the previous run, and leaving them would show a failure that has been
	// fixed.
	r.prog = RescanProgress{Running: true, StartedAt: r.clk.Now()}
	r.mu.Unlock()

	go r.run(ctx)
	return nil
}

// Progress reports what the current or last scan did.
func (r *Rescan) Progress() RescanProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prog
}

func (r *Rescan) run(ctx context.Context) {
	stats, err := r.scan(ctx, r.store, func(found int, _ string) {
		// Live, so the console moves during the walk instead of sitting at
		// zero for the minutes a large library takes.
		r.mu.Lock()
		r.prog.Found = found
		r.mu.Unlock()
	})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.prog.Running = false
	r.prog.FinishedAt = r.clk.Now()
	// The counts a failed scan reached are still worth showing: "found 900 of
	// a library of 7,000, then the drive went" says more than a bare error.
	r.prog.Found, r.prog.Skipped, r.prog.Missing = stats.Found, stats.Skipped, stats.Marked
	if err != nil {
		r.prog.Err = err.Error()
	}
}
