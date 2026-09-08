// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"testing"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/mix"
)

// Jockora-bpr. The flag says "0 disables adverts". It did not: applyTuning
// skipped the assignment when the value was 0, so the station kept the default
// of one slot in four, and the log line that would have said otherwise was in
// the branch never taken. An operator who read the flag, set it, and listened
// still heard an advert every fourth break.

type sayingWriter struct{ said string }

func (w sayingWriter) Write(context.Context, int, mix.Placement) (string, error) {
	return w.said, nil
}

// TestAdEveryNZeroDisables: no adverts, and no panic. adDue does
// breaks % everyN, so a zero that reached the writer would take the station off
// air on the first break slot rather than merely being ignored.
func TestAdEveryNZeroDisables(t *testing.T) {
	was := AdEveryNBreaks
	t.Cleanup(func() { AdEveryNBreaks = was })
	AdEveryNBreaks = 0

	rotation := dj.NewAdRotation([]dj.Ad{{
		ID: "stillwater", Brand: "Stillwater Ceramics",
		Script: "Stillwater Ceramics. One mug, still.",
	}})
	w := NewAdWriter(sayingWriter{said: "the DJ speaking"}, rotation, clock.Real{})

	// Enough slots that the default of one in four would have aired several.
	for i := 0; i < 20; i++ {
		got, err := w.Write(context.Background(), 40, mix.PlacementRamp)
		if err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
		if got != "the DJ speaking" {
			t.Fatalf("slot %d aired %q; 0 is supposed to disable adverts", i, got)
		}
	}
}

// TestAdEveryNZeroIsImpossibleByConstruction: the guard is at the point the
// writer is BUILT, not inside adDue, so there is no live AdWriter anywhere with
// a zero divisor to trip over later.
func TestAdEveryNZeroIsImpossibleByConstruction(t *testing.T) {
	was := AdEveryNBreaks
	t.Cleanup(func() { AdEveryNBreaks = was })

	inner := sayingWriter{said: "the DJ speaking"}
	rotation := dj.NewAdRotation([]dj.Ad{{ID: "a", Brand: "B", Script: "S"}})

	AdEveryNBreaks = 0
	if _, wrapped := NewAdWriter(inner, rotation, clock.Real{}).(*AdWriter); wrapped {
		t.Error("an AdWriter was built with a zero divisor; adDue would panic on the first slot")
	}
	// Negative is the same state: somebody typed it, nobody meant one advert
	// every minus two breaks.
	AdEveryNBreaks = -2
	if _, wrapped := NewAdWriter(inner, rotation, clock.Real{}).(*AdWriter); wrapped {
		t.Error("an AdWriter was built with a negative divisor")
	}

	AdEveryNBreaks = 4
	if _, wrapped := NewAdWriter(inner, rotation, clock.Real{}).(*AdWriter); !wrapped {
		t.Error("a normal frequency no longer wraps, so adverts never air at all")
	}
}
