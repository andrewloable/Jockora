// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

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
	// Genres is what the station plays. EMPTY MEANS ANY GENRE, the same way an
	// empty Mood has always meant any mood: an operator who wants "everything
	// nocturnal" should not have to name every tag in the vocabulary to get it.
	//
	// A LIST, because a station is a place on a dial and "rock and punk" is one
	// place. Two stations that a listener flips between is not the same thing,
	// and the dial is the listener's whole UI.
	Genres []string `json:"genres,omitempty"`

	// Moods narrows by feeling. Empty means any.
	Moods []string `json:"moods,omitempty"`
}

// SplitList reads the comma-joined form these are stored in.
//
// The columns already existed holding ONE value, and the vocabularies are
// closed and contain no commas -- so a station written before stations could
// hold several reads back as a one-element list, and no migration is needed.
// Blanks and stray spaces are tolerated on the way in, because editing the
// database by hand is a thing that happens.
func SplitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// JoinList writes the comma-joined form.
func JoinList(values []string) string { return strings.Join(values, ",") }

// catchAll reports whether this filter is the catch-all, which is a different
// question from what it plays: "other" and "unsorted" mean everything the
// enrichment could not place, including tracks with no dossier at all.
func (f Filter) catchAll() bool {
	return len(f.Genres) == 1 && isCatchAll(f.Genres[0])
}

// Validate refuses a filter that could never match anything.
//
// Checked BEFORE a station is created, because a station whose genre is a typo
// is not an empty station -- it is a station that will never fill, and the
// operator has nothing on screen to tell them why.
func (f Filter) Validate() error {
	// isCatchAll as well as the vocabulary: there are TWO catch-all tags and
	// only one of them is in enrich.StationTags. The seeded dial station uses
	// "unsorted", which is not in the vocabulary, so regenerating it failed --
	// on the live box POST /admin/stations/1/regenerate answered 500, and the
	// one station that works on a fresh library was the one the operator could
	// not rebuild.
	// EVERY value, not the first: a filter that silently dropped one it did not
	// recognise would select more music than the operator asked for, which is
	// the failure they are least likely to notice.
	for _, g := range f.Genres {
		if !isCatchAll(g) && !slices.Contains(enrich.StationTags, g) {
			return fmt.Errorf("%w: genre %q", ErrBadVocabulary, g)
		}
	}
	// An empty mood means ANY mood, which is what most stations want: a rock
	// station narrowed to one feeling is a fraction of the rock in a library.
	for _, m := range f.Moods {
		if !slices.Contains(enrich.Moods, m) {
			return fmt.Errorf("%w: mood %q", ErrBadVocabulary, m)
		}
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
	if f.catchAll() {
		return f.unplacedTrackIDs(ctx, s)
	}

	// NO GENRE AND NO MOOD IS THE WHOLE LIBRARY, and it must not be expressed
	// as a join through dossiers: that would silently drop every track
	// enrichment has not reached yet, which on a fresh library is most of them.
	if len(f.Genres) == 0 && len(f.Moods) == 0 {
		rows, err := s.DB().QueryContext(ctx, `
			SELECT t.id FROM tracks t
			 WHERE t.playable = 1 AND t.missing_at IS NULL
			 ORDER BY t.id`)
		if err != nil {
			return nil, fmt.Errorf("station: selecting every track: %w", err)
		}
		return scanIDs(rows)
	}

	// The lists travel as JSON arrays and are matched with json_each, so one
	// query shape serves any number of values and nothing is built by string
	// concatenation.
	genres, err := json.Marshal(f.Genres)
	if err != nil {
		return nil, fmt.Errorf("station: encoding genres: %w", err)
	}
	moods, err := json.Marshal(f.Moods)
	if err != nil {
		return nil, fmt.Errorf("station: encoding moods: %w", err)
	}

	// THROUGH effective_tags, NEVER dossiers. An operator's own tags outrank
	// the enrichment's, and the view is the one place the two are combined --
	// so a station selects on exactly what the playlist shows.
	// A MOOD IMPLIES A TEMPO, for the five where tempo means anything. A NULL
	// bpm never excludes: analysis is a slow background pass, and on a fresh
	// library dropping the unmeasured would make every mood station empty.
	lo, hi := tempoBounds(f.Moods)

	rows, err := s.DB().QueryContext(ctx, `
		SELECT DISTINCT t.id FROM tracks t
		  JOIN effective_tags e ON e.track_id = t.id
		 WHERE t.playable = 1 AND t.missing_at IS NULL
		   AND (t.bpm IS NULL OR t.bpm BETWEEN ? AND ?)
		   AND (json_array_length(?) = 0 OR EXISTS (
		         SELECT 1 FROM json_each(e.station_tags) tag
		          WHERE tag.value IN (SELECT value FROM json_each(?))))
		   AND (json_array_length(?) = 0 OR EXISTS (
		         SELECT 1 FROM json_each(e.mood) m
		          WHERE m.value IN (SELECT value FROM json_each(?))))
		 ORDER BY t.id`, lo, hi, genres, genres, moods, moods)
	if err != nil {
		return nil, fmt.Errorf("station: selecting %v tracks: %w", f.Genres, err)
	}
	return scanIDs(rows)
}

// unplacedTrackIDs is the catch-all: tagged "other", or never enriched, or
// enriched into nothing.
func (f Filter) unplacedTrackIDs(ctx context.Context, s *store.Store) ([]int64, error) {
	moods, err := json.Marshal(f.Moods)
	if err != nil {
		return nil, fmt.Errorf("station: encoding moods: %w", err)
	}
	// "No tags at all" replaces the old "no dossier row": through the view a
	// track nobody has enriched and a track somebody emptied by hand read the
	// same, which is right -- neither is anything in particular.
	//
	// AN OVERRIDE TAKES A TRACK OUT OF HERE even when its dossier still says
	// nothing, because confidence is about what the enrichment managed, and an
	// operator who placed the track has already answered the question.
	// The catch-all takes a mood like any other station, so it answers the same
	// question about tempo.
	lo, hi := tempoBounds(f.Moods)

	rows, err := s.DB().QueryContext(ctx, `
		SELECT t.id FROM tracks t
		  JOIN effective_tags e ON e.track_id = t.id
		 WHERE t.playable = 1 AND t.missing_at IS NULL
		   AND (t.bpm IS NULL OR t.bpm BETWEEN ? AND ?)
		   AND (json_array_length(e.station_tags) = 0
		        OR (e.overridden = 0 AND e.confidence = ?)
		        OR EXISTS (
		             SELECT 1 FROM json_each(e.station_tags) tag
		              WHERE tag.value = ?))
		   AND (json_array_length(?) = 0 OR EXISTS (
		         SELECT 1 FROM json_each(e.mood) m
		          WHERE m.value IN (SELECT value FROM json_each(?))))
		 ORDER BY t.id`,
		lo, hi, ConfidenceNone, enrich.FallbackStationTag, moods, moods)
	if err != nil {
		return nil, fmt.Errorf("station: selecting unplaced tracks: %w", err)
	}
	return scanIDs(rows)
}

// ConfidenceNone marks a dossier that could assert nothing. Named here because
// it is the value the catch-all query tests for.
const ConfidenceNone = "none"
