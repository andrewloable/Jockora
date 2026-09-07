// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

// ErrBadVocabulary refuses a filter naming something no dossier can hold.
var ErrBadVocabulary = errors.New("station: not in the vocabulary")

// Filter is a station's music: one genre, and optionally one mood.
//
// EXACT MATCHES against the closed vocabularies, never fuzzy and never
// lower-cased at query time. The vocabularies exist precisely so that grouping
// is exact; matching loosely here would quietly reintroduce the near-duplicate
// buckets they were built to prevent.
type Filter struct {
	Genre string `json:"genre"`
	Mood  string `json:"mood,omitempty"`
}

// Validate refuses a filter that could never match anything.
//
// Checked BEFORE a station is created, because a station whose genre is a typo
// is not an empty station -- it is a station that will never fill, and the
// operator has nothing on screen to tell them why.
func (f Filter) Validate() error {
	if !slices.Contains(enrich.StationTags, f.Genre) {
		return fmt.Errorf("%w: genre %q", ErrBadVocabulary, f.Genre)
	}
	// An empty mood means ANY mood, which is what most stations want: a rock
	// station narrowed to one feeling is a fraction of the rock in a library.
	if f.Mood != "" && !slices.Contains(enrich.Moods, f.Mood) {
		return fmt.Errorf("%w: mood %q", ErrBadVocabulary, f.Mood)
	}
	return nil
}

// TrackIDs is the music this filter selects, in a stable order.
//
// Ordered by id because SQLite promises nothing otherwise, and an unstable pool
// makes a seeded selector produce a different sequence on every run of the same
// library.
func (f Filter) TrackIDs(ctx context.Context, s *store.Store) ([]int64, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}

	// THE CATCH-ALL IS A DIFFERENT QUESTION. "other" is not only the tracks
	// tagged "other": it is everything the enrichment could not place, which
	// includes tracks with no dossier at all and tracks whose dossier says it
	// could assert nothing. On a fresh library that is most of them, and they
	// have to be listenable.
	if f.Genre == enrich.FallbackStationTag {
		return f.unplacedTrackIDs(ctx, s)
	}

	rows, err := s.DB().QueryContext(ctx, `
		SELECT DISTINCT t.id FROM tracks t
		  JOIN dossiers d ON d.track_id = t.id
		  JOIN json_each(json_extract(d.json, '$.station_tags')) tag
		 WHERE t.playable = 1 AND t.missing_at IS NULL
		   AND tag.value = ?
		   AND (? = '' OR EXISTS (
		         SELECT 1 FROM json_each(json_extract(d.json, '$.mood')) m
		          WHERE m.value = ?))
		 ORDER BY t.id`, f.Genre, f.Mood, f.Mood)
	if err != nil {
		return nil, fmt.Errorf("station: selecting %s tracks: %w", f.Genre, err)
	}
	return scanIDs(rows)
}

// unplacedTrackIDs is the catch-all: tagged "other", or never enriched, or
// enriched into nothing.
func (f Filter) unplacedTrackIDs(ctx context.Context, s *store.Store) ([]int64, error) {
	rows, err := s.DB().QueryContext(ctx, `
		SELECT t.id FROM tracks t
		  LEFT JOIN dossiers d ON d.track_id = t.id
		 WHERE t.playable = 1 AND t.missing_at IS NULL
		   AND (d.track_id IS NULL
		        OR d.confidence = ?
		        OR EXISTS (
		             SELECT 1 FROM json_each(json_extract(d.json, '$.station_tags')) tag
		              WHERE tag.value = ?))
		   AND (? = '' OR EXISTS (
		         SELECT 1 FROM json_each(json_extract(d.json, '$.mood')) m
		          WHERE m.value = ?))
		 ORDER BY t.id`,
		ConfidenceNone, enrich.FallbackStationTag, f.Mood, f.Mood)
	if err != nil {
		return nil, fmt.Errorf("station: selecting unplaced tracks: %w", err)
	}
	return scanIDs(rows)
}

// ConfidenceNone marks a dossier that could assert nothing. Named here because
// it is the value the catch-all query tests for.
const ConfidenceNone = "none"
