// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/station"
)

// THE DJ ANNOUNCED ONE RECORD AND THE STATION PLAYED ANOTHER.
//
// Reported live on 2026-09-08: the break said "Now Green Day 21 Guns" over the
// last seconds of Nine Inch Nails, Hurt. The station log shows why:
//
//	boundary 1  now_s=0    track_s=479  insertion_at_s=479
//	boundary 2  now_s=469  track_s=374  insertion_at_s=843
//
// Boundary 2 was announced at 469 -- TEN SECONDS BEFORE the track of boundary 1
// ends at 479 -- because announceBoundary samples the MIXER's position, and the
// decoder has already fed the rest of the current track into the ring. The ring
// is station.RingSeconds deep, which is ten. So `now` is not when the next
// track starts; the next track starts when that buffered audio has played out.
//
// insertion_at then lands a ring's worth EARLY, inside the tail of the track it
// was meant to follow. A ramp break introduces the track AFTER that one, so a
// listener hears the next record announced over the end of the current one.

// boundaryLog captures the structured record announceBoundary writes.
type boundaryLog struct{ records []map[string]any }

func (b *boundaryLog) Enabled(context.Context, slog.Level) bool { return true }
func (b *boundaryLog) WithAttrs([]slog.Attr) slog.Handler       { return b }
func (b *boundaryLog) WithGroup(string) slog.Handler            { return b }
func (b *boundaryLog) Handle(_ context.Context, r slog.Record) error {
	m := map[string]any{"msg": r.Message}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	b.records = append(b.records, m)
	return nil
}

func (b *boundaryLog) offer(t *testing.T) map[string]any {
	t.Helper()
	for _, r := range b.records {
		if r["msg"] == "break slot offered" {
			return r
		}
	}
	t.Fatalf("no boundary was offered: %v", b.records)
	return nil
}

func TestBoundaryInsertionAccountsForBufferedAudio(t *testing.T) {
	const durationS = 374 // Hurt, the track that was still playing

	for _, tc := range []struct {
		name      string
		bufferedS float64
	}{
		// The first boundary of a stream: nothing decoded yet, so the mixer's
		// position IS when the track starts. This is the case that already
		// worked, and it must keep working.
		{"an empty ring adds nothing", 0},
		// Every boundary after the first, with the decoder keeping up.
		{"a full ring is a whole RingSeconds", station.RingSeconds},
		// AND A PARTIAL ONE. The ring is only full while the decoder keeps up;
		// on a slow disk, a short track or a cold start it holds less. Reading
		// it is the difference between a fix and a constant that happens to be
		// right on a healthy box.
		{"a partly full ring counts what is actually in it", 3.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, st := llmApp(t)
			ctx := context.Background()
			if _, err := st.DB().ExecContext(ctx,
				`INSERT INTO tracks (id, path, playable, duration_s) VALUES (7, '/m/hurt.mp3', 1, ?)`,
				float64(durationS)); err != nil {
				t.Fatal(err)
			}

			captured := &boundaryLog{}
			a.log = slog.New(captured)
			a.opts.Breaks = &station.Pipeline{Cadence: station.NewCadence(1)}

			rt := station.NewRuntime(station.Deps{}, 1, t.TempDir())
			if frames := int(tc.bufferedS * mix.SampleRate); frames > 0 {
				if n := rt.Ring().Write(make([]mix.Frame, frames)); n != frames {
					t.Fatalf("wrote %d of %d frames into the ring", n, frames)
				}
			}

			a.announceBoundary(ctx, rt, 2,
				track{id: 6, artist: "Weezer", title: "Say It Aint So"},
				track{id: 7, artist: "Nine Inch Nails", title: "Hurt"},
				track{id: 8, artist: "Green Day", title: "21 Guns"})

			got := captured.offer(t)
			// The mixer has not moved, so `now` is 0 -- but the track being
			// announced does not START until the buffered audio has played.
			want := tc.bufferedS + durationS
			at, _ := got["insertion_at_s"].(float64)
			if at != math.Round(want) {
				t.Errorf("insertion_at_s = %v, want %v\n"+
					"the break lands %v seconds out, inside the track it was "+
					"meant to follow -- which is a listener hearing the next "+
					"record announced over the current one", at, want, want-at)
			}
			if b, _ := got["buffered_s"].(float64); b != math.Round(tc.bufferedS) {
				t.Errorf("buffered_s = %v, want %v -- the log has to show the "+
					"term, or the next person cannot see it is counted", b, tc.bufferedS)
			}
			// AND THE BOUNDARY WAS ACTUALLY TAKEN, so the number above is one
			// the pipeline received rather than one only the log saw.
			if n := a.opts.Breaks.Pending(); n != 1 {
				t.Fatalf("the boundary was not taken (pending = %d)", n)
			}
		})
	}
}

// TestBoundaryBufferedSecondsWithoutARing: several tests build a Runtime
// directly, and a zero-valued one has no ring. Reporting nothing is cheaper
// than the crash, and the crash would be on the mixer goroutine.
func TestBoundaryBufferedSecondsWithoutARing(t *testing.T) {
	if got := bufferedSeconds(&station.Runtime{}); got != 0 {
		t.Errorf("bufferedSeconds with no ring = %v, want 0", got)
	}
}

// TestHoldForBreakMakesTheHole: Jockora-ey4. The break is spliced to start
// before the outgoing track runs out, so by the time the music stops the DJ is
// already talking. Holding the next track back for the rest of the break is
// what puts the incoming record under the DJ's LAST words rather than under all
// of them.
func TestHoldForBreakMakesTheHole(t *testing.T) {
	ctx := context.Background()

	t.Run("silence lands in the ring", func(t *testing.T) {
		rt := station.NewRuntime(station.Deps{}, 1, t.TempDir())
		feedSilence(ctx, rt.Ring(), 0.5)
		if got := rt.Ring().Occupancy(); got != 500*time.Millisecond {
			t.Errorf("the hole is %v of audio, want 500ms", got)
		}
	})

	t.Run("no break, no hole", func(t *testing.T) {
		// Most boundaries carry no break at all, and holding the music for one
		// of those is dead air with nothing over it.
		rt := station.NewRuntime(station.Deps{}, 1, t.TempDir())
		for _, seconds := range []float64{0, -1} {
			feedSilence(ctx, rt.Ring(), seconds)
		}
		if got := rt.Ring().Occupancy(); got != 0 {
			t.Errorf("an empty gap still wrote %v", got)
		}
	})

	t.Run("a cancelled feed stops part way", func(t *testing.T) {
		// The station is going off air. Filling the whole hole first would
		// block the shutdown for the length of a break.
		stopped, cancel := context.WithCancel(ctx)
		cancel()
		rt := station.NewRuntime(station.Deps{}, 1, t.TempDir())
		feedSilence(stopped, rt.Ring(), 5)
		if got := rt.Ring().Occupancy(); got != 0 {
			t.Errorf("a cancelled feed wrote %v", got)
		}
	})

	t.Run("a full ring that never drains gives up rather than spinning", func(t *testing.T) {
		// THE HOLE CANNOT ALWAYS BE FILLED. feedRing waits for space when the
		// ring is full, so a station shutting down mid-gap has to come back out
		// of that wait -- otherwise the feed goroutine outlives the station by
		// the length of a break, holding the ring it was told to stop writing.
		//
		// Deterministic rather than timing-dependent: the ring is filled to
		// capacity here and nothing is draining it, so the write CANNOT
		// proceed and the deadline is the only way out.
		rt := station.NewRuntime(station.Deps{}, 1, t.TempDir())
		full := make([]mix.Frame, station.RingSeconds*mix.SampleRate)
		if n := rt.Ring().Write(full); n != len(full) {
			t.Fatalf("filled %d of %d frames", n, len(full))
		}
		deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()

		done := make(chan struct{})
		go func() { defer close(done); feedSilence(deadline, rt.Ring(), 5) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("feedSilence never returned; the feed goroutine is stuck on a full ring")
		}
	})

	t.Run("a runtime with no ring is not a crash", func(t *testing.T) {
		// Several tests build a Runtime directly and a zero-valued one has no
		// ring. This runs on the feed goroutine, where a panic ends the feed.
		feedSilence(ctx, (&station.Runtime{}).Ring(), 3)
	})

	t.Run("the pipeline is asked, and only once", func(t *testing.T) {
		a := &App{}
		rt := station.NewRuntime(station.Deps{}, 1, t.TempDir())

		// No break machinery at all is a shuffle, and it holds nothing.
		a.holdForBreak(ctx, rt, 2)
		if got := rt.Ring().Occupancy(); got != 0 {
			t.Errorf("a station with no pipeline held the music for %v", got)
		}

		a.opts.Breaks = &station.Pipeline{Cadence: station.NewCadence(1)}
		a.holdForBreak(ctx, rt, 2)
		if got := rt.Ring().Occupancy(); got != 0 {
			t.Errorf("a boundary with no scheduled break held %v", got)
		}
	})
}
