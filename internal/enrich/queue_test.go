// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/store"
)

// countingLLM answers every call successfully and counts them.
type countingLLM struct {
	mu     sync.Mutex
	calls  int
	stopAt int // when >0, cancel the context after this many calls
	cancel context.CancelFunc
}

func (c *countingLLM) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()
	if c.stopAt > 0 && n >= c.stopAt && c.cancel != nil {
		c.cancel()
	}
	return ok(goodJSON()), nil
}

func (c *countingLLM) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func queueStore(t *testing.T, tracks int) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for i := 1; i <= tracks; i++ {
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (id, path, artist, title, duration_s) VALUES (?, ?, ?, ?, ?)`,
			i, fmt.Sprintf("/music/%d.mp3", i), fmt.Sprintf("Artist %d", i),
			fmt.Sprintf("Title %d", i), 200.0); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func newQueue(s *store.Store, llm Completer) *Queue {
	return &Queue{
		Store: s, LLM: llm, Owner: "test",
		Log:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		Clock: clock.NewFake(time.Unix(1_700_000_000, 0)),
	}
}

func dossierCount(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM dossiers`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestQueueProcessesAllPendingTracks(t *testing.T) {
	s := queueStore(t, 10)
	llm := &countingLLM{}

	if err := newQueue(s, llm).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := dossierCount(t, s); got != 10 {
		t.Errorf("%d dossiers, want 10", got)
	}
	if llm.count() != 10 {
		t.Errorf("%d LLM calls, want 10", llm.count())
	}
}

// TestQueueResumesAfterInterruption is what makes the most expensive work in the
// system survivable: one track per commit, so a kill loses at most one.
func TestQueueResumesAfterInterruption(t *testing.T) {
	s := queueStore(t, 10)

	ctx, cancel := context.WithCancel(context.Background())
	first := &countingLLM{stopAt: 5, cancel: cancel}
	if err := newQueue(s, first).Run(ctx); err == nil {
		t.Fatal("Run returned nil after cancellation")
	}

	partial := dossierCount(t, s)
	if partial < 4 || partial > 6 {
		t.Fatalf("%d dossiers after interrupting at 5, want about 5", partial)
	}

	second := &countingLLM{}
	if err := newQueue(s, second).Run(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}

	if got := dossierCount(t, s); got != 10 {
		t.Errorf("%d dossiers after resuming, want 10", got)
	}
	if second.count() != 10-partial {
		t.Errorf("the resumed run made %d LLM calls, want %d: it re-enriched tracks that were already done",
			second.count(), 10-partial)
	}
}

func TestQueueSkipsTracksWithDossiers(t *testing.T) {
	s := queueStore(t, 6)

	if err := newQueue(s, &countingLLM{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := &countingLLM{}
	if err := newQueue(s, second).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if second.count() != 0 {
		t.Errorf("the second run made %d LLM calls, want 0: dossiers are cached forever", second.count())
	}
}

func TestQueueSecondInstanceIsRejected(t *testing.T) {
	s := queueStore(t, 3)
	fake := clock.NewFake(time.Unix(1_700_000_000, 0))

	held := &Queue{Store: s, LLM: &countingLLM{}, Owner: "worker-a", Clock: fake,
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if err := held.acquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer held.releaseLock()

	other := &Queue{Store: s, LLM: &countingLLM{}, Owner: "worker-b", Clock: fake,
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	err := other.Run(context.Background())
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
	if !contains(err.Error(), "worker-a") {
		t.Errorf("error %q should name the holder", err)
	}
}

// TestQueueStealsAStaleLock: a worker killed with -9 leaves its lock behind, and
// without stealing, enrichment would never restart.
func TestQueueStealsAStaleLock(t *testing.T) {
	s := queueStore(t, 3)
	fake := clock.NewFake(time.Unix(1_700_000_000, 0))

	dead := &Queue{Store: s, LLM: &countingLLM{}, Owner: "dead-worker", Clock: fake,
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if err := dead.acquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	// No release: the process died.

	fake.Advance(StaleLockAfter + time.Second)

	revived := &Queue{Store: s, LLM: &countingLLM{}, Owner: "new-worker", Clock: fake,
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if err := revived.Run(context.Background()); err != nil {
		t.Fatalf("a stale lock was not stolen: %v", err)
	}
	if got := dossierCount(t, s); got != 3 {
		t.Errorf("%d dossiers, want 3", got)
	}
}

// TestQueueFreshLockIsNotStolen is the other half: a healthy worker's lock must
// survive right up to the staleness boundary.
func TestQueueFreshLockIsNotStolen(t *testing.T) {
	s := queueStore(t, 3)
	fake := clock.NewFake(time.Unix(1_700_000_000, 0))

	held := &Queue{Store: s, LLM: &countingLLM{}, Owner: "alive", Clock: fake,
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if err := held.acquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer held.releaseLock()

	fake.Advance(StaleLockAfter - time.Second)

	other := &Queue{Store: s, LLM: &countingLLM{}, Owner: "thief", Clock: fake,
		Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if err := other.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("a lock one second inside the window was stolen: %v", err)
	}
}

func TestQueueReleasesLockOnCompletion(t *testing.T) {
	s := queueStore(t, 2)

	if err := newQueue(s, &countingLLM{}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM enrich_lock`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the lock survived a clean run; the next run would wait out the stale timeout")
	}
}

func TestQueueRecordsProgress(t *testing.T) {
	s := queueStore(t, 8)
	q := newQueue(s, &countingLLM{})

	if err := q.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	done, total := q.Progress()
	if total != 8 {
		t.Errorf("total = %d, want 8", total)
	}
	if done != 8 {
		t.Errorf("done = %d, want 8", done)
	}
}

// TestQueueUnplayableTracksAreNotEnriched: enriching a file that cannot be
// played is pure waste, and it is the most expensive work in the system.
func TestQueueUnplayableTracksAreNotEnriched(t *testing.T) {
	s := queueStore(t, 5)
	if _, err := s.DB().Exec(`UPDATE tracks SET playable = 0 WHERE id IN (2, 4)`); err != nil {
		t.Fatal(err)
	}

	llm := &countingLLM{}
	if err := newQueue(s, llm).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if llm.count() != 3 {
		t.Errorf("%d LLM calls, want 3: unplayable tracks must be skipped", llm.count())
	}
}

// TestQueueFailedTrackStillGetsARow: a missing row is indistinguishable from
// unfinished work, so the queue would pick the same track forever.
func TestQueueFailedTrackStillGetsARow(t *testing.T) {
	s := queueStore(t, 3)

	llm := &refusingLLM{}
	if err := newQueue(s, llm).Run(context.Background()); err != nil {
		t.Fatalf("a refusing model should not end the run: %v", err)
	}

	if got := dossierCount(t, s); got != 3 {
		t.Errorf("%d dossiers, want 3: every attempted track needs a row", got)
	}
	var none int
	if err := s.DB().QueryRow(`SELECT count(*) FROM dossiers WHERE confidence = ?`, ConfidenceNone).Scan(&none); err != nil {
		t.Fatal(err)
	}
	if none != 3 {
		t.Errorf("%d dossiers with confidence none, want 3", none)
	}

	// And a second run does not retry them.
	second := &refusingLLM{}
	if err := newQueue(s, second).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if second.calls != 0 {
		t.Errorf("the second run retried %d failed tracks; they would be retried forever", second.calls)
	}
}

type refusingLLM struct{ calls int }

func (r *refusingLLM) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	r.calls++
	return ok("I cannot help with that."), nil
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && bytes.Contains([]byte(s), []byte(sub))
}
