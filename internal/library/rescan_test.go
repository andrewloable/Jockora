// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/store"
)

// settle waits for the background scan to finish. Polling rather than a channel
// because Progress is the only thing a console has, so it is the thing worth
// testing against.
func settle(t *testing.T, r *Rescan) RescanProgress {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := r.Progress(); !p.Running {
			return p
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the rescan never finished")
	return RescanProgress{}
}

func TestRescanSingleFlight(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	r := NewRescan(openStore(t), clock.Real{})
	r.scan = func(ctx context.Context, _ *store.Store, _ ScanProgress) (Stats, error) {
		close(entered)
		<-release
		return Stats{}, nil
	}

	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-entered

	// TWO WALKS AT ONCE is a race with no correct answer: each marks missing
	// what the other has not reached yet.
	if err := r.Start(context.Background()); !errors.Is(err, ErrScanRunning) {
		t.Errorf("second Start = %v, want ErrScanRunning", err)
	}
	if !r.Progress().Running {
		t.Error("Progress says nothing is running while a scan is blocked")
	}

	close(release)
	settle(t, r)

	// And it can be started again afterwards.
	r.scan = func(context.Context, *store.Store, ScanProgress) (Stats, error) {
		return Stats{}, nil
	}
	if err := r.Start(context.Background()); err != nil {
		t.Errorf("a second run after the first finished: %v", err)
	}
	settle(t, r)
}

func TestRescanProgressAdvances(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	r := NewRescan(openStore(t), clk)
	first := make(chan struct{})
	release := make(chan struct{})
	r.scan = func(_ context.Context, _ *store.Store, progress ScanProgress) (Stats, error) {
		progress(1, "/m/x.mp3")
		close(first)
		<-release
		for i := 2; i <= 5; i++ {
			progress(i, "/m/x.mp3")
		}
		return Stats{Found: 5, Skipped: 2, Marked: 1}, nil
	}

	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// DURING the walk, not after it. A library of 7,595 takes minutes, and a
	// bar that sits at zero until the end is indistinguishable from one that
	// has hung.
	<-first
	if mid := r.Progress(); mid.Found != 1 || !mid.Running {
		t.Errorf("mid-scan progress = %+v, want found 1 and still running", mid)
	}
	close(release)

	got := settle(t, r)
	if got.Found != 5 || got.Skipped != 2 || got.Missing != 1 {
		t.Errorf("progress = %+v, want found 5, skipped 2, missing 1", got)
	}
	if got.Running {
		t.Error("still running after it finished")
	}
	if got.StartedAt.IsZero() || got.FinishedAt.IsZero() {
		t.Errorf("times not recorded: %+v", got)
	}
	if got.Err != "" {
		t.Errorf("Err = %q on a clean run", got.Err)
	}
}

// TestRescanCancel: stopping a scan must not leave the library looking half
// gone. 11b already refuses to mark on a walk that failed; this proves the job
// reports the stop rather than a clean finish.
func TestRescanCancel(t *testing.T) {
	s := openStore(t)
	r := NewRescan(s, clock.Real{})
	started := make(chan struct{})
	r.scan = func(ctx context.Context, _ *store.Store, _ ScanProgress) (Stats, error) {
		close(started)
		<-ctx.Done()
		return Stats{Found: 2}, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()

	got := settle(t, r)
	if got.Running {
		t.Error("still running after cancellation")
	}
	if got.Missing != 0 {
		t.Errorf("Missing = %d after a cancelled scan, want 0", got.Missing)
	}
	if !strings.Contains(got.Err, "context") {
		t.Errorf("Err = %q, want it to name the cancellation", got.Err)
	}
	var marked int
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM tracks WHERE missing_at IS NOT NULL`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if marked != 0 {
		t.Errorf("%d tracks were marked missing by a cancelled scan", marked)
	}
}

func TestRescanErrorSurfaced(t *testing.T) {
	r := NewRescan(openStore(t), clock.Real{})
	r.scan = func(context.Context, *store.Store, ScanProgress) (Stats, error) {
		return Stats{Found: 3}, errors.New("folder /music: no such directory")
	}

	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := settle(t, r)
	if got.Err == "" {
		t.Fatal("a failed scan reported no error")
	}
	if !strings.Contains(got.Err, "no such directory") {
		t.Errorf("Err = %q, want the source's own message", got.Err)
	}
	if got.Running {
		t.Error("still running after failing")
	}
	// What it managed before failing is still worth showing.
	if got.Found != 3 {
		t.Errorf("Found = %d, want the 3 it reached", got.Found)
	}
}
