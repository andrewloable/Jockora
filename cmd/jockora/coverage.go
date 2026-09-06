// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/library"
	"github.com/andrewloable/jockora/internal/store"
)

// LRCLibURL is the public API. Nothing else in Jockora talks to it.
const lrclibURL = "https://lrclib.net"

// lrclibPace is how often the coverage sweep may ask LRCLIB a question.
//
// LRCLIB publishes no rate limit. That is not permission to hammer a free
// community service two hundred times in four seconds, and the sweep is not on
// anyone's critical path, so it waits.
const lrclibPace = 250 * time.Millisecond

// runScan reads a music library into the database.
//
// Read-only against the library, always: Jockora layers on top of whatever
// already manages the music and never owns, retags or transcodes it.
func runScan(ctx context.Context, dbPath, root string, log *slog.Logger) error {
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close() //nolint:errcheck // nothing buffered

	stats, err := library.Scan(ctx, s, root)
	if err != nil {
		return err
	}
	log.Info("scan complete", "root", root,
		"found", stats.Found, "added", stats.Added, "updated", stats.Updated, "unplayable", stats.Unplayable)
	return nil
}

// runCoverage scans a library and measures synced-lyric coverage on a sample.
//
// This exists because the coverage number decides a SCOPE question -- whether
// vocal-onset detection is v0.1 work -- and until now BuildReport could be
// computed by nothing an operator could run. A decision that expensive should
// not rest on a figure nobody can reproduce.
//
// It samples rather than sweeping the whole library because the answer is a
// proportion: two hundred honest lookups settle it, and ten thousand would take
// forty minutes to say the same thing.
func runCoverage(ctx context.Context, dbPath, root string, sample int, log *slog.Logger) error {
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close() //nolint:errcheck // read-mostly; the report is already printed

	stats, err := library.Scan(ctx, s, root)
	if err != nil {
		return err
	}
	log.Info("scanned", "root", root, "found", stats.Found, "added", stats.Added, "updated", stats.Updated, "unplayable", stats.Unplayable)

	// Random, not the first N. An alphabetical prefix is one artist and one
	// language, and the whole point is to sample the material LRCLIB is WORST
	// at -- obscure and non-English releases -- rather than confirm that it
	// knows the same well-covered Western catalogue everyone measures with.
	rows, err := s.DB().QueryContext(ctx, `
		SELECT path, coalesce(artist, ''), coalesce(title, ''), coalesce(duration_s, 0)
		  FROM tracks
		 WHERE playable = 1 AND ramp_confidence IS NULL
		 ORDER BY random() LIMIT ?`, sample)
	if err != nil {
		return fmt.Errorf("sampling tracks: %w", err)
	}
	type track struct {
		path, artist, title string
		duration            float64
	}
	var picked []track
	for rows.Next() {
		var t track
		if err := rows.Scan(&t.path, &t.artist, &t.title, &t.duration); err != nil {
			return err
		}
		picked = append(picked, t)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()

	client := enrich.NewLRCLib(lrclibURL, nil)
	for i, t := range picked {
		if err := ctx.Err(); err != nil {
			return err
		}
		ramp, err := client.FetchRamp(ctx, t.artist, t.title, t.duration)
		if err != nil {
			// A lookup failure is not a coverage answer. Leaving the row NULL
			// keeps it out of the denominator rather than counting a network
			// problem as a track LRCLIB does not know.
			log.Warn("lrclib lookup failed", "artist", t.artist, "title", t.title, "err", err)
			continue
		}
		if err := enrich.StoreRamp(ctx, s, t.path, ramp); err != nil {
			return err
		}
		if (i+1)%25 == 0 {
			log.Info("coverage sweep", "done", i+1, "of", len(picked))
		}
		time.Sleep(lrclibPace)
	}

	r, err := enrich.BuildReport(ctx, s)
	if err != nil {
		return err
	}
	fmt.Print(r.String())
	return nil
}
