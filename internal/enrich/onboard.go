// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/andrewloable/jockora/internal/store"
)

// Stream-first onboarding.
//
// A freshly scanned library is playable IMMEDIATELY. Nothing here waits for a
// dossier or a loudness measurement: the tag scan is fast and gates serving, and
// everything expensive runs behind the stream.
//
// The mechanism already existed as graceful degradation -- a library with no
// dossiers still broadcasts, every DJ simply runs personality-only. Making it
// the first-run path means the operator hears a DJ on day one instead of after a
// pass that can take hours.

// PlayableTrackIDs returns every track that can be aired right now.
//
// It deliberately does NOT require a dossier or a loudness measurement. A track
// is playable the moment the scanner has seen it.
func PlayableTrackIDs(ctx context.Context, s *store.Store) ([]int64, error) {
	rows, err := s.DB().QueryContext(ctx, `SELECT id FROM tracks WHERE playable = 1 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("enrich: listing playable tracks: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("enrich: listing playable tracks: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AssertableFacts returns the facts the DJ is allowed to state about a track.
//
// This is a LIVE read, so a track enriched while the stream is running is used
// on its next airing with no restart.
//
// No dossier returns no facts and no error. An empty result means
// personality-only talk, which is a designed and supported outcome, and it is
// what makes a library playable before enrichment has run at all.
func AssertableFacts(ctx context.Context, s *store.Store, trackID int64) ([]string, error) {
	var raw, confidence string
	err := s.DB().QueryRowContext(ctx,
		`SELECT json, confidence FROM dossiers WHERE track_id = ?`, trackID).Scan(&raw, &confidence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // not enriched yet: personality-only
	}
	if err != nil {
		return nil, fmt.Errorf("enrich: reading dossier for track %d: %w", trackID, err)
	}

	// Only a high-confidence dossier may be spoken as fact. Low means the model
	// was not adequately grounded, and the DJ asserting it anyway is exactly the
	// failure the dossier design exists to prevent.
	if confidence != ConfidenceHigh {
		return nil, nil
	}

	var d Dossier
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		// A corrupt dossier is treated as no dossier rather than as an outage.
		return nil, nil
	}
	if len(d.Sources) == 0 {
		// Nothing was actually looked up, so nothing may be asserted.
		return nil, nil
	}
	return d.ArtistFacts, nil
}

// TrackLoudness returns a track's measured loudness, or 0 when it has not been
// measured yet.
//
// Zero is the unmeasured signal all the way through: SQLite NULL arrives in Go
// as 0, and mix.GainFor treats 0 as unmeasured and returns unity. So a track
// that is playing before the loudness pass reaches it sounds like itself rather
// than being slammed 16 dB down.
func TrackLoudness(ctx context.Context, s *store.Store, trackID int64) (float64, error) {
	var lufs sql.NullFloat64
	err := s.DB().QueryRowContext(ctx,
		`SELECT loudness_lufs FROM tracks WHERE id = ?`, trackID).Scan(&lufs)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("enrich: reading loudness for track %d: %w", trackID, err)
	}
	if !lufs.Valid {
		return 0, nil
	}
	return lufs.Float64, nil
}
