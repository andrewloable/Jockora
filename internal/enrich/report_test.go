// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

// seedCoverage inserts tracks, giving the first n an 'lrc' ramp confidence.
func seedCoverage(t *testing.T, tracks, withLRC int) *store.Store {
	t.Helper()
	s := queueStore(t, tracks)
	for i := 1; i <= withLRC; i++ {
		if _, err := s.DB().Exec(
			`UPDATE tracks SET ramp_confidence = ?, ramp_s = 12.0, outro_s = 8.0 WHERE id = ?`,
			ConfidenceLRC, i); err != nil {
			t.Fatal(err)
		}
	}
	for i := withLRC + 1; i <= tracks; i++ {
		if _, err := s.DB().Exec(
			`UPDATE tracks SET ramp_confidence = ? WHERE id = ?`, ConfidenceNone, i); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestReportCoveragePercentage(t *testing.T) {
	s := seedCoverage(t, 200, 90)

	r, err := BuildReport(context.Background(), s)
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	if r.Tracks != 200 {
		t.Errorf("Tracks = %d, want 200", r.Tracks)
	}
	if r.WithSyncedLyrics != 90 {
		t.Errorf("WithSyncedLyrics = %d, want 90", r.WithSyncedLyrics)
	}
	if r.CoveragePct != 45.0 {
		t.Errorf("CoveragePct = %v, want 45.0", r.CoveragePct)
	}
}

func TestReportCostExtrapolation(t *testing.T) {
	s := seedCoverage(t, 200, 120)
	// 200 tracks averaging 900 tokens and 6.2 seconds each.
	for i := 1; i <= 200; i++ {
		if err := RecordEnrichmentCost(context.Background(), s, int64(i), 900, 6.2); err != nil {
			t.Fatal(err)
		}
	}

	r, err := BuildReport(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}

	if math.Abs(r.AvgTokens-900) > 0.5 {
		t.Errorf("AvgTokens = %v, want 900", r.AvgTokens)
	}
	if math.Abs(r.AvgWallSeconds-6.2) > 0.01 {
		t.Errorf("AvgWallSeconds = %v, want 6.2", r.AvgWallSeconds)
	}
	// 5000 x 900 = 4,500,000 tokens.
	if math.Abs(r.Projected5kTokens-4_500_000) > 1 {
		t.Errorf("Projected5kTokens = %v, want 4.5M", r.Projected5kTokens)
	}
	// 5000 x 6.2s = 31,000s = 8.611 hours.
	if math.Abs(r.Projected5kHours-8.611) > 0.01 {
		t.Errorf("Projected5kHours = %v, want about 8.61", r.Projected5kHours)
	}
}

// TestReportFlagsLowCoverage drives a real scope decision: below half, ramp
// placement is invisible in the room test and reads as a writing failure rather
// than a data gap.
func TestReportFlagsLowCoverage(t *testing.T) {
	for _, c := range []struct {
		tracks, withLRC int
		wantFallback    bool
	}{
		{200, 90, true},   // 45%
		{200, 99, true},   // 49.5%
		{200, 100, false}, // exactly 50%
		{200, 140, false}, // 70%
	} {
		s := seedCoverage(t, c.tracks, c.withLRC)
		r, err := BuildReport(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		if r.NeedsVocalOnsetFallback != c.wantFallback {
			t.Errorf("%d/%d (%.1f%%): NeedsVocalOnsetFallback = %v, want %v",
				c.withLRC, c.tracks, r.CoveragePct, r.NeedsVocalOnsetFallback, c.wantFallback)
		}
	}
}

// TestReportRefusesSmallSamples: extrapolating a library-wide cost from a
// handful of tracks produces a confident fiction.
func TestReportRefusesSmallSamples(t *testing.T) {
	s := seedCoverage(t, 50, 30)

	r, err := BuildReport(context.Background(), s)
	if err != nil {
		t.Fatalf("a small library is not an error: %v", err)
	}
	if r.SampleTooSmall != true {
		t.Error("SampleTooSmall is false for a 50-track sample, want true")
	}
	if r.Projected5kTokens != 0 || r.Projected5kHours != 0 {
		t.Errorf("projections were made from %d tracks: %v tokens, %v hours",
			r.Tracks, r.Projected5kTokens, r.Projected5kHours)
	}
	// The coverage number itself is still reported: it is a proportion, not an
	// extrapolation.
	if r.CoveragePct != 60 {
		t.Errorf("CoveragePct = %v, want 60", r.CoveragePct)
	}
}

func TestReportEmptyLibrary(t *testing.T) {
	s := queueStore(t, 0)

	r, err := BuildReport(context.Background(), s)
	if err != nil {
		t.Fatalf("an empty library is not an error: %v", err)
	}
	if r.CoveragePct != 0 || r.Tracks != 0 {
		t.Errorf("report = %+v, want zeros", r)
	}
	if r.NeedsVocalOnsetFallback {
		t.Error("an empty library flagged the fallback; there is no evidence either way")
	}
}

func TestReportStringIsReadable(t *testing.T) {
	s := seedCoverage(t, 200, 90)
	for i := 1; i <= 200; i++ {
		if err := RecordEnrichmentCost(context.Background(), s, int64(i), 900, 6.2); err != nil {
			t.Fatal(err)
		}
	}

	r, err := BuildReport(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	out := r.String()

	for _, want := range []string{"200", "45.0", "synced lyrics", "5000", "hours"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q:\n%s", want, out)
		}
	}
	// The decision it drives must be stated, not left to be inferred.
	if !strings.Contains(strings.ToLower(out), "vocal-onset") {
		t.Errorf("the report does not state the decision it drives:\n%s", out)
	}
	fmt.Println(out)
}
