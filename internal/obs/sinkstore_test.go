// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package obs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Jockora-69n.2, the sink half: WARN and above go to a durable store, written
// on a goroutine that the logging call never waits for.

// recordingStore counts batches and can be made to block or to fail.
type recordingStore struct {
	mu      sync.Mutex
	got     []Record
	batches int

	release chan struct{} // when non-nil, Append waits on it
	err     error
	calls   atomic.Int64
}

func (r *recordingStore) AppendLogs(_ context.Context, recs []Record) error {
	r.calls.Add(1)
	if r.release != nil {
		<-r.release
	}
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	r.got = append(r.got, recs...)
	r.batches++
	r.mu.Unlock()
	return nil
}

func (r *recordingStore) saved() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Record{}, r.got...)
}

// syncBuffer is a bytes.Buffer a test may read while the writer goroutine is
// still writing to it. The wrapped handler is where a persistence failure is
// reported, and that happens on the writer's goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func storeSink(t *testing.T, st LogStore, buffer int) (*Sink, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	s := NewSink(h, 100)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.Persist(ctx, st, buffer)
	return s, buf
}

// waitFor polls until cond, so a test never sleeps a fixed time for an
// asynchronous write.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestLogStoreOnlyWarnAndAbove: info is chatter and a station logs a great deal
// of it. Persisting all of it would turn a music library into a log database.
func TestLogStoreOnlyWarnAndAbove(t *testing.T) {
	st := &recordingStore{}
	s, _ := storeSink(t, st, 16)

	log := slog.New(s)
	log.Debug("a detail")
	log.Info("on air")
	log.Warn("break dropped", "reason", "llm_error")
	log.Error("enrichment stopped")

	waitFor(t, "both records to land", func() bool { return len(st.saved()) == 2 })
	for _, r := range st.saved() {
		if r.Level < slog.LevelWarn {
			t.Errorf("%v was persisted; the floor is WARN", r.Level)
		}
	}
	// And the ring still holds ALL FOUR: the floor is about what is durable,
	// not about what the live view shows.
	if n := len(s.Recent(slog.LevelDebug, 10)); n != 4 {
		t.Errorf("the ring holds %d records, want all 4", n)
	}
}

// TestLogStoreAsync: the logging call must not wait on SQLite. The mixer and
// the encoder log, and a slow disk must not reach them.
func TestLogStoreAsync(t *testing.T) {
	st := &recordingStore{release: make(chan struct{})}
	s, _ := storeSink(t, st, 16)

	done := make(chan struct{})
	go func() {
		defer close(done)
		slog.New(s).Warn("into a blocked writer")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle waited for the database")
	}
	// The write has not landed yet, which is the point.
	if len(st.saved()) != 0 {
		t.Error("the write happened synchronously")
	}
	// CLOSED, not nil-ed. A closed channel returns immediately, so the writer
	// stops waiting without the test writing a field the writer is reading --
	// which is what the race detector caught the first time.
	close(st.release)
	waitFor(t, "the write to land", func() bool { return len(st.saved()) == 1 })
}

// TestLogStoreDropsWhenFull: a blocked writer must cost records, never the
// audio path -- the same rule the live fan-out follows.
func TestLogStoreDropsWhenFull(t *testing.T) {
	st := &recordingStore{release: make(chan struct{})}
	s, _ := storeSink(t, st, 1)

	before := s.Dropped()
	done := make(chan struct{})
	go func() {
		defer close(done)
		log := slog.New(s)
		for i := 0; i < 50; i++ {
			log.Warn("into a full queue")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle blocked on a full persistence queue")
	}
	if s.Dropped() <= before {
		t.Error("records were lost without being counted")
	}
	close(st.release)
	// And the ring kept every one of them: a slow disk costs the durable copy,
	// not the live view.
	if n := len(s.Recent(slog.LevelDebug, 100)); n != 50 {
		t.Errorf("the ring holds %d records, want all 50", n)
	}
}

// TestLogStoreWriteErrorDoesNotLoop is the one that could take the process
// down: a failed write reported THROUGH THE SINK would be a new record, which
// would be queued, which would fail. It goes to the wrapped handler only.
func TestLogStoreWriteErrorDoesNotLoop(t *testing.T) {
	st := &recordingStore{err: errors.New("disk is full")}
	s, buf := storeSink(t, st, 16)

	slog.New(s).Warn("the first failure")
	waitFor(t, "the failing write", func() bool { return st.calls.Load() >= 1 })

	// Give any recursion a chance to run away before measuring.
	time.Sleep(50 * time.Millisecond)
	if n := st.calls.Load(); n > 4 {
		t.Errorf("%d write attempts from one record; the failure is feeding itself", n)
	}
	// The ring must NOT contain the failure notice, or the console would show
	// the log complaining about the log.
	for _, r := range s.Recent(slog.LevelDebug, 100) {
		if strings.Contains(r.Message, "disk is full") ||
			strings.Contains(r.Attrs["err"], "disk is full") {
			t.Errorf("the write failure was logged back through the sink: %+v", r)
		}
	}
	// It reaches stderr, which is where a person would look for it.
	if !strings.Contains(buf.String(), "disk is full") {
		t.Errorf("the write failure was swallowed entirely: %s", buf.String())
	}
}

// TestLogStoreBatches: SQLite must not be asked for a write per line, and a
// station under load produces them in bursts.
func TestLogStoreBatches(t *testing.T) {
	st := &recordingStore{release: make(chan struct{})}
	s, _ := storeSink(t, st, 64)

	log := slog.New(s)
	log.Warn("first")
	// The writer is now inside AppendLogs holding the release channel, so
	// everything after this queues behind it.
	waitFor(t, "the writer to take the first record", func() bool { return st.calls.Load() == 1 })
	for i := 0; i < 20; i++ {
		log.Warn("queued")
	}
	close(st.release)

	waitFor(t, "everything to land", func() bool { return len(st.saved()) == 21 })
	st.mu.Lock()
	batches := st.batches
	st.mu.Unlock()
	if batches >= 21 {
		t.Errorf("%d batches for 21 records; nothing was batched", batches)
	}
}

// TestLogStoreStopsWithItsContext: the writer is a goroutine and must not
// outlive the process it belongs to.
func TestLogStoreStopsWithItsContext(t *testing.T) {
	st := &recordingStore{}
	s := NewSink(slog.NewJSONHandler(&syncBuffer{}, nil), 10)
	ctx, cancel := context.WithCancel(context.Background())
	s.Persist(ctx, st, 4)

	slog.New(s).Warn("before")
	waitFor(t, "the first write", func() bool { return len(st.saved()) == 1 })

	cancel()
	waitFor(t, "the writer to stop", func() bool { return !s.persisting() })

	// Logging after the writer has gone is not an error and does not block.
	slog.New(s).Warn("after")
	if n := len(s.Recent(slog.LevelDebug, 10)); n != 2 {
		t.Errorf("the ring holds %d records after the writer stopped, want 2", n)
	}
}

// TestLogStorePersistIsIdempotent: calling it twice must not start two writers
// racing for the same queue.
func TestLogStorePersistIsIdempotent(t *testing.T) {
	st := &recordingStore{}
	s, _ := storeSink(t, st, 8)
	s.Persist(context.Background(), st, 8)
	// A nil store starts nothing, and a zero buffer is a caller who meant one
	// rather than a queue that drops every record.
	s.Persist(context.Background(), nil, 8)
	s.Persist(context.Background(), st, 0)

	slog.New(s).Warn("once")
	waitFor(t, "the write", func() bool { return len(st.saved()) >= 1 })
	time.Sleep(50 * time.Millisecond)
	if n := len(st.saved()); n != 1 {
		t.Errorf("one record produced %d writes; there is more than one writer", n)
	}
}
