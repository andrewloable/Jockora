// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package obs

import (
	"context"
	"log/slog"
	"testing"
)

// Jockora-69n.9. Persist is called from App.Run, so everything logged during
// app.New -- migrations, seeding, the non-loopback warning, a source that would
// not open -- reached stderr and the ring and never the table.
//
// The durable half exists because a crash loop empties the ring of exactly the
// evidence somebody wanted, and a crash loop is mostly STARTUP. So the window
// this feature covered least was the window it was built for.

// backlogSink is a sink that is NOT yet persisting, so a test can log into it
// first and turn the durable half on afterwards -- which is the real order.
func backlogSink(t *testing.T) (*Sink, *slog.Logger) {
	t.Helper()
	h := slog.NewJSONHandler(&syncBuffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})
	s := NewSink(h, 100)
	return s, slog.New(s)
}

func persistNow(t *testing.T, s *Sink, st LogStore) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.Persist(ctx, st, 16)
}

func TestSinkPersistBacklogFlushesWhatCameBefore(t *testing.T) {
	s, log := backlogSink(t)
	log.Warn("serving without authentication on a non-loopback address")
	log.Error("a source would not open")

	st := &recordingStore{}
	persistNow(t, s, st)

	waitFor(t, "the backlog to reach the store", func() bool {
		return len(st.saved()) == 2
	})
	got := st.saved()
	// OLDEST FIRST. A durable log that reads back in a different order than it
	// happened is a log nobody can reason about.
	if got[0].Message != "serving without authentication on a non-loopback address" {
		t.Errorf("first persisted record is %q, want the one logged first", got[0].Message)
	}
	if got[1].Level != slog.LevelError {
		t.Errorf("second record is %v, want the error logged second", got[1].Level)
	}
}

// TestSinkPersistBacklogSkipsInfo: the floor is PersistLevel and the reasons
// are in its comment. Draining the ring must not smuggle info past it.
func TestSinkPersistBacklogSkipsInfo(t *testing.T) {
	s, log := backlogSink(t)
	log.Info("scanning elapsed=1s files=40")
	log.Warn("the one that matters")

	st := &recordingStore{}
	persistNow(t, s, st)
	waitFor(t, "the warning", func() bool { return len(st.saved()) >= 1 })

	// A BARRIER, not a sleep: a record logged now cannot reach the store before
	// the backlog did, so once it lands the backlog is finished arriving.
	log.Warn("a later warning")
	waitFor(t, "the later warning", func() bool { return len(st.saved()) >= 2 })

	got := st.saved()
	for _, r := range got {
		if r.Level < PersistLevel {
			t.Errorf("info reached the store: %q", r.Message)
		}
	}
	if len(got) != 2 {
		t.Errorf("persisted %d records, want the two warnings", len(got))
	}
}

func TestSinkPersistBacklogDoesNotDuplicate(t *testing.T) {
	s, log := backlogSink(t)
	log.Warn("logged before the store opened")

	st := &recordingStore{}
	persistNow(t, s, st)
	log.Warn("logged after the store opened")

	waitFor(t, "both warnings", func() bool { return len(st.saved()) >= 2 })
	counts := map[string]int{}
	for _, r := range st.saved() {
		counts[r.Message]++
	}
	if counts["logged before the store opened"] != 1 {
		t.Errorf("the startup warning was persisted %d times, want once",
			counts["logged before the store opened"])
	}
	if counts["logged after the store opened"] != 1 {
		t.Errorf("the later warning was persisted %d times, want once",
			counts["logged after the store opened"])
	}
}

func TestSinkPersistBacklogSurvivesAnEmptyRing(t *testing.T) {
	s, log := backlogSink(t)
	st := &recordingStore{}
	persistNow(t, s, st)

	// The barrier again: once a real record has been written, any empty batch
	// the backlog might have produced has already been written too.
	log.Warn("the first thing that ever happened")
	waitFor(t, "the warning", func() bool { return len(st.saved()) == 1 })
	if n := st.calls.Load(); n != 1 {
		t.Errorf("the store was called %d times, want only the one real batch", n)
	}
}

// TestSinkPersistBacklogAfterTheRingWrapped: a startup noisy enough to lap the
// ring is the crash-loop case itself. The backlog is whatever the ring still
// holds -- the newest capacity records -- and the index arithmetic has to keep
// working once n has passed it.
func TestSinkPersistBacklogAfterTheRingWrapped(t *testing.T) {
	h := slog.NewJSONHandler(&syncBuffer{}, &slog.HandlerOptions{Level: slog.LevelDebug})
	s := NewSink(h, 4)
	log := slog.New(s)
	for i := 0; i < 12; i++ {
		log.Warn("warning", "i", i)
	}

	st := &recordingStore{}
	persistNow(t, s, st)
	waitFor(t, "the backlog", func() bool { return len(st.saved()) == 4 })

	got := st.saved()
	for n, want := range []string{"8", "9", "10", "11"} {
		if got[n].Attrs["i"] != want {
			t.Errorf("record %d is i=%s, want the last four in order (i=%s)",
				n, got[n].Attrs["i"], want)
		}
	}
}
