// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/mix"
)

type countingWriter struct{ n int }

func (c *countingWriter) Write(context.Context, int, mix.Placement) (string, error) {
	c.n++
	return "the DJ talking", nil
}

func adPool(n int) *dj.AdRotation {
	ads := make([]dj.Ad, n)
	for i := range ads {
		ads[i] = dj.Ad{
			ID:     string(rune('a' + i)),
			Brand:  "Brindlewax",
			Script: "advert number " + string(rune('a'+i)),
		}
	}
	return dj.NewAdRotation(ads)
}

// TestAdWriterAirsOneSlotInFour is the whole feature: before this, adverts
// could be written and could never be heard -- GenerateAdPool, AdRotation and
// the ads table had no callers anywhere.
func TestAdWriterAirsOneSlotInFour(t *testing.T) {
	inner := &countingWriter{}
	clk := clock.NewFake(time.Unix(0, 0))
	w := NewAdWriter(inner, adPool(8), clk)

	var ads int
	for i := 0; i < 12; i++ {
		// Well past the floor, so only the ratio is under test.
		clk.Advance(2 * time.Hour)
		got, err := w.Write(context.Background(), 40, mix.PlacementRamp)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(got, "advert") {
			ads++
		}
	}
	if ads != 3 {
		t.Errorf("%d adverts in 12 slots, want 3 (one in %d)", ads, AdEveryNBreaks)
	}
	if inner.n != 9 {
		t.Errorf("the DJ wrote %d breaks, want 9", inner.n)
	}
}

// TestAdWriterHonoursTheMinimumInterval: the ratio governs the long run, this
// governs a short session that happens to hit several boundaries quickly.
func TestAdWriterHonoursTheMinimumInterval(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	w := NewAdWriter(&countingWriter{}, adPool(8), clk)

	var ads int
	for i := 0; i < 40; i++ {
		clk.Advance(time.Minute) // 40 minutes total, inside MinAdInterval
		got, _ := w.Write(context.Background(), 40, mix.PlacementRamp)
		if strings.HasPrefix(got, "advert") {
			ads++
		}
	}
	if ads > 1 {
		t.Errorf("%d adverts inside %s; the interval floor is not holding", ads, MinAdInterval)
	}
}

// TestAdWriterFallsBackToTheDJ. An empty or fully-cooled pool is not a failure:
// the slot goes back to the DJ. Adverts are optional in exactly the way breaks
// are optional and music is not.
func TestAdWriterFallsBackToTheDJ(t *testing.T) {
	inner := &countingWriter{}
	clk := clock.NewFake(time.Unix(0, 0))
	w := NewAdWriter(inner, dj.NewAdRotation(nil), clk)

	for i := 0; i < 8; i++ {
		clk.Advance(2 * time.Hour)
		got, err := w.Write(context.Background(), 40, mix.PlacementRamp)
		if err != nil {
			t.Fatalf("an empty ad pool produced an error: %v", err)
		}
		if got != "the DJ talking" {
			t.Fatalf("got %q, want the DJ's own break", got)
		}
	}
	if inner.n != 8 {
		t.Errorf("the DJ wrote %d of 8 slots", inner.n)
	}
}

// TestAdWriterWithoutARotationIsTheWriterItself: a station with no ad pool must
// behave exactly as it did before this existed.
func TestAdWriterWithoutARotationIsTheWriterItself(t *testing.T) {
	inner := &countingWriter{}
	if got := NewAdWriter(inner, nil, nil); got != Writer(inner) {
		t.Error("a nil rotation wrapped the writer anyway")
	}
}
