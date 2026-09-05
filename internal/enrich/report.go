// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/andrewloable/jockora/internal/store"
)

// MinSampleForProjection is the smallest sample worth extrapolating from.
//
// Below it the projection is a confident fiction: a handful of tracks says
// nothing about a library, and the sample must include the obscure and
// non-English material or the coverage figure is meaningless.
const MinSampleForProjection = 200

// VocalOnsetThresholdPct is the coverage below which vocal-onset detection
// becomes v0.1 scope rather than deferred work.
//
// Under half, most tracks fall back to "between" placement, the ramp rule is
// invisible in the room test, and the result reads as a writing failure rather
// than a data gap.
const VocalOnsetThresholdPct = 50.0

// ProjectionLibrarySize is the library size costs are projected to.
const ProjectionLibrarySize = 5000

// Report is the measured basis for a scope decision.
type Report struct {
	Tracks           int
	WithSyncedLyrics int
	CoveragePct      float64

	Enriched       int
	AvgTokens      float64
	AvgWallSeconds float64

	Projected5kTokens float64
	Projected5kHours  float64

	SampleTooSmall          bool
	NeedsVocalOnsetFallback bool
}

// BuildReport measures synced-lyric coverage and enrichment cost.
func BuildReport(ctx context.Context, s *store.Store) (Report, error) {
	var r Report

	if err := s.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM tracks WHERE playable = 1`).Scan(&r.Tracks); err != nil {
		return r, fmt.Errorf("enrich: counting tracks: %w", err)
	}
	if err := s.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM tracks WHERE playable = 1 AND ramp_confidence = ?`,
		ConfidenceLRC).Scan(&r.WithSyncedLyrics); err != nil {
		return r, fmt.Errorf("enrich: counting synced lyrics: %w", err)
	}

	if r.Tracks > 0 {
		r.CoveragePct = float64(r.WithSyncedLyrics) * 100 / float64(r.Tracks)
		// An empty library is no evidence either way, so the flag stays off.
		r.NeedsVocalOnsetFallback = r.CoveragePct < VocalOnsetThresholdPct
	}

	var tokens, seconds sql.NullFloat64
	if err := s.DB().QueryRowContext(ctx,
		`SELECT count(*), avg(tokens), avg(wall_seconds) FROM enrich_cost`).
		Scan(&r.Enriched, &tokens, &seconds); err != nil {
		return r, fmt.Errorf("enrich: reading enrichment cost: %w", err)
	}
	r.AvgTokens = tokens.Float64
	r.AvgWallSeconds = seconds.Float64

	r.SampleTooSmall = r.Tracks < MinSampleForProjection
	if !r.SampleTooSmall && r.Enriched > 0 {
		r.Projected5kTokens = r.AvgTokens * ProjectionLibrarySize
		r.Projected5kHours = r.AvgWallSeconds * ProjectionLibrarySize / 3600
	}

	return r, nil
}

// RecordEnrichmentCost stores what one track's enrichment cost.
func RecordEnrichmentCost(ctx context.Context, s *store.Store, trackID int64, tokens int, wallSeconds float64) error {
	_, err := s.DB().ExecContext(ctx, `
		INSERT INTO enrich_cost (track_id, tokens, wall_seconds) VALUES (?, ?, ?)
		ON CONFLICT(track_id) DO UPDATE SET tokens = excluded.tokens, wall_seconds = excluded.wall_seconds`,
		trackID, tokens, wallSeconds)
	if err != nil {
		return fmt.Errorf("enrich: recording cost for track %d: %w", trackID, err)
	}
	return nil
}

// String renders the report as a plain table, stating the decision it drives
// rather than leaving it to be inferred.
func (r Report) String() string {
	var b strings.Builder

	b.WriteString("ENRICHMENT REPORT\n")
	fmt.Fprintf(&b, "  playable tracks         %d\n", r.Tracks)
	fmt.Fprintf(&b, "  with synced lyrics      %d\n", r.WithSyncedLyrics)
	fmt.Fprintf(&b, "  coverage                %.1f%%\n", r.CoveragePct)
	b.WriteString("\n")

	if r.Enriched == 0 {
		b.WriteString("  cost                    not measured yet\n")
	} else {
		fmt.Fprintf(&b, "  enriched                %d\n", r.Enriched)
		fmt.Fprintf(&b, "  avg tokens/track        %.0f\n", r.AvgTokens)
		fmt.Fprintf(&b, "  avg wall seconds/track  %.1f\n", r.AvgWallSeconds)
	}

	switch {
	case r.SampleTooSmall:
		fmt.Fprintf(&b, "\n  projection for %d      WITHHELD: %d tracks is under the %d-track\n",
			ProjectionLibrarySize, r.Tracks, MinSampleForProjection)
		b.WriteString("                          minimum, and extrapolating from fewer is a\n")
		b.WriteString("                          confident fiction.\n")
	case r.Enriched > 0:
		fmt.Fprintf(&b, "\n  projected for %d      %.1fM tokens, %.1f hours\n",
			ProjectionLibrarySize, r.Projected5kTokens/1_000_000, r.Projected5kHours)
	}

	b.WriteString("\nDECISION\n")
	switch {
	case r.Tracks == 0:
		b.WriteString("  No tracks scanned. No evidence either way.\n")
	case r.NeedsVocalOnsetFallback:
		fmt.Fprintf(&b, "  Coverage %.1f%% is under %.0f%%. VOCAL-ONSET DETECTION BECOMES v0.1\n",
			r.CoveragePct, VocalOnsetThresholdPct)
		b.WriteString("  SCOPE, before the placement task. Without it most tracks fall back\n")
		b.WriteString("  to \"between\" placement, the ramp rule is invisible in the room\n")
		b.WriteString("  test, and the result reads as a writing failure, not a data gap.\n")
	default:
		fmt.Fprintf(&b, "  Coverage %.1f%% is at or above %.0f%%. Ramp placement works from\n",
			r.CoveragePct, VocalOnsetThresholdPct)
		b.WriteString("  LRC data alone. Vocal-onset detection stays deferred.\n")
	}

	return b.String()
}
