// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/sched"
)

// All named TestPipeline*.

// pacedRenderer takes a fixed wall-clock time per render, on a fake clock, so
// the retry-inclusive timing can be asserted exactly.
type pacedRenderer struct {
	*scriptedRenderer
	clk     *clock.Fake
	perCall time.Duration
}

func (p *pacedRenderer) Render(ctx context.Context, text, voice, outPath string) (float64, error) {
	p.clk.Advance(p.perCall)
	return p.scriptedRenderer.Render(ctx, text, voice, outPath)
}

func newPipeline(t *testing.T, clk *clock.Fake, perCall time.Duration, seconds ...float64) (*Pipeline, *scriptedRenderer, *sched.Queue) {
	t.Helper()
	s := &scriptedRenderer{seconds: seconds}
	r := &pacedRenderer{scriptedRenderer: s, clk: clk, perCall: perCall}
	q := &sched.Queue{}
	return &Pipeline{
		Cadence:     NewCadence(4),
		Lookahead:   NewLookahead(150 * time.Second),
		Length:      LengthCheck{Writer: s, Renderer: r, Dir: t.TempDir()},
		Queue:       q,
		FadeSeconds: 3,
		Clock:       clk,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, s, q
}

func lrcTrack(rampS, outroS float64) *Track {
	return &Track{RampS: rampS, OutroS: outroS, DurationS: 200, RampConfidence: enrich.ConfidenceLRC}
}

// TestPipelineEndToEnd walks the whole chain: cadence decides a slot, the
// lookahead holds it, generation fires, the length ladder accepts it and the
// scheduler receives an entry the mixer can splice.
func TestPipelineEndToEnd(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	p, s, q := newPipeline(t, clk, 20*time.Second, 6.0)

	// Four boundaries at 60-second intervals; only the fourth gets a slot.
	for i := 1; i <= 4; i++ {
		at := float64(i) * 60
		got := p.Announce(Boundary{
			Index: i, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4),
			InsertionAt: at, InsertionSample: int64(at * mix.SampleRate),
		})
		if got != (i == 4) {
			t.Fatalf("boundary %d slot = %v, want %v", i, got, i == 4)
		}
	}
	if p.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1", p.Pending())
	}

	// Too early: the insertion is at 240s and T is 150s.
	if n, err := p.Tick(context.Background(), 60); err != nil || n != 0 {
		t.Fatalf("Tick at t=60 enqueued %d (err %v), want 0", n, err)
	}
	if len(s.targets) != 0 {
		t.Errorf("wrote a break %d seconds before the lookahead fired", 240-60)
	}

	// Inside the window.
	n, err := p.Tick(context.Background(), 100)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 1 {
		t.Fatalf("enqueued %d breaks, want 1", n)
	}
	if q.Pending() != 1 {
		t.Fatalf("scheduler holds %d entries, want 1", q.Pending())
	}
	if p.Pending() != 0 {
		t.Errorf("pipeline still holds %d slots after generating them", p.Pending())
	}

	entries := q.DrainDue(int64(240 * mix.SampleRate))
	if len(entries) != 1 {
		t.Fatalf("drained %d entries, want 1", len(entries))
	}
	if entries[0].Placement != "ramp" {
		t.Errorf("placement = %q, want ramp: a 12s ramp holds a 6s break", entries[0].Placement)
	}
	if entries[0].Path == "" {
		t.Error("scheduled entry has no audio path")
	}
}

// TestPipelineTimingIsRetryInclusive is the measurement the whole task turns on.
func TestPipelineTimingIsRetryInclusive(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	// First render overruns the 12s ramp, second fits. 20s of wall clock each.
	p, _, _ := newPipeline(t, clk, 20*time.Second, 30.0, 6.0)

	for i := 1; i <= 4; i++ {
		p.Announce(Boundary{Index: i, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4),
			InsertionAt: 240, InsertionSample: int64(240 * mix.SampleRate)})
	}
	if _, err := p.Tick(context.Background(), 100); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := p.Lookahead.Count(); n != 1 {
		t.Fatalf("recorded %d timings, want 1", n)
	}
	if n := p.Lookahead.RetriedCount(); n != 1 {
		t.Errorf("RetriedCount = %d, want 1: the retry must be visible in the sample", n)
	}
	if got := p.Lookahead.P50(); got != 40*time.Second {
		t.Errorf("recorded %s, want 40s -- BOTH renders, not just the one that aired", got)
	}
}

// TestPipelineLateGenerationIsDroppedNotAired: a break that finishes after its
// slot must not be handed to the mixer, and must not stop the music.
func TestPipelineLateGenerationIsDroppedNotAired(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	p, _, q := newPipeline(t, clk, 200*time.Second, 6.0)

	for i := 1; i <= 4; i++ {
		p.Announce(Boundary{Index: i, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4),
			InsertionAt: 240, InsertionSample: int64(240 * mix.SampleRate)})
	}
	// Generation starts at t=100 and takes 200s, finishing at 300s for a slot
	// at 240s.
	n, err := p.Tick(context.Background(), 100)
	if err != nil {
		t.Fatalf("a late break is not an error: %v", err)
	}
	if n != 0 {
		t.Errorf("enqueued %d late breaks, want 0", n)
	}
	if q.Pending() != 0 {
		t.Errorf("scheduler holds %d entries after a late generation", q.Pending())
	}
	// And it was still measured: late generations are the ones T exists to price.
	if p.Lookahead.Count() != 1 {
		t.Error("a late generation was not recorded in the timing sample")
	}
}

func TestPipelineExpiredSlotIsDroppedNotGenerated(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	p, s, _ := newPipeline(t, clk, 20*time.Second, 6.0)

	for i := 1; i <= 4; i++ {
		p.Announce(Boundary{Index: i, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4),
			InsertionAt: 240, InsertionSample: int64(240 * mix.SampleRate)})
	}
	if n, err := p.Tick(context.Background(), 300); err != nil || n != 0 {
		t.Fatalf("Tick past the insertion point enqueued %d (err %v)", n, err)
	}
	if len(s.targets) != 0 {
		t.Error("generated a break for a boundary the stream had already passed")
	}
	if p.Pending() != 0 {
		t.Error("expired slot is still pending")
	}
}

func TestPipelinePreferredWindow(t *testing.T) {
	cases := []struct {
		name      string
		cur, next *Track
		want      mix.Placement
		wantSec   float64
	}{
		{"ramp when the intro is long enough", lrcTrack(4, 4), lrcTrack(12, 4), mix.PlacementRamp, 12},
		{"outro when the intro is not", lrcTrack(4, 14), lrcTrack(2, 4), mix.PlacementOutro, 14},
		{"span when neither alone is enough", lrcTrack(4, 2.5), lrcTrack(2.5, 4), mix.PlacementSpan, 8},
		{"between with no usable data", &Track{DurationS: 200}, &Track{DurationS: 200}, mix.PlacementBetween, BetweenWindowSeconds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, sec := PreferredWindow(tc.cur, tc.next, 3)
			if got != tc.want {
				t.Errorf("placement = %v, want %v", got, tc.want)
			}
			if sec != tc.wantSec {
				t.Errorf("window = %v, want %v", sec, tc.wantSec)
			}
		})
	}
}

// TestPipelineGaplessBoundaryNeverGetsASlot ties the two rules together: the
// cadence refuses the boundary, so the break moves to the next one rather than
// being repositioned inside a gapless pair.
func TestPipelineGaplessBoundaryNeverGetsASlot(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	p, _, _ := newPipeline(t, clk, 20*time.Second, 6.0)

	gapless := &Track{RampS: 4, OutroS: 4, DurationS: 200, RampConfidence: enrich.ConfidenceLRC, NoCrossfadeNext: true}
	for i := 1; i <= 4; i++ {
		cur := lrcTrack(4, 4)
		if i == 4 {
			cur = gapless
		}
		if p.Announce(Boundary{Index: i, Cur: cur, Next: lrcTrack(12, 4), InsertionAt: float64(i) * 60}) && i == 4 {
			t.Fatal("gapless boundary 4 was given a break slot")
		}
	}
	if !p.Announce(Boundary{Index: 5, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4), InsertionAt: 300}) {
		t.Error("the slot refused at the gapless boundary never reappeared")
	}
}
