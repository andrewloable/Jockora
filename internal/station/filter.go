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
	"time"

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

	// THE NUMERIC BOUNDS, all with ZERO MEANING UNBOUNDED on that side.
	//
	// These are what a brief becomes: "mostly 80s", "calm but driving",
	// "nothing over four minutes". They are the complete set of what the
	// database can actually select on -- year from the scanner, bpm from the
	// analyser, duration from the scanner -- and nothing else is selectable
	// because nothing else is a column with a closed meaning.
	YearMin int `json:"year_min,omitempty"`
	YearMax int `json:"year_max,omitempty"`

	// TempoMin and TempoMax REPLACE the range a mood implies, they do not
	// intersect with it. See tempoBounds.
	TempoMin float64 `json:"tempo_min,omitempty"`
	TempoMax float64 `json:"tempo_max,omitempty"`

	DurationMinS float64 `json:"duration_min_s,omitempty"`
	DurationMaxS float64 `json:"duration_max_s,omitempty"`
}

// The bounds a value is refused outside. REFUSED HERE, REPAIRED IN enrich:
// DeriveStationParams already fixes what a model returns, and this is the
// boundary that catches a hand-edited database or a direct API call.
// ONE DEFINITION, IN enrich. These were four numbers written out twice --
// once here and once as the range the derive clamps to -- and the year pair had
// already drifted into two constants with a comment on one saying it matched
// the other. The schema bounds the sampler, the clamps repair what a hosted
// model returns anyway, and Validate below refuses a hand-edited database:
// three checks over one pair of numbers, or the sampler starts producing values
// this then refuses. station imports enrich, so this is the direction that is
// not an import cycle.
const (
	earliestYear                  = enrich.EarliestBriefYear
	slowestBPM, fastestBPM        = enrich.SlowestBPM, enrich.FastestBPM
	shortestTrackS, longestTrackS = enrich.ShortestTrackS, enrich.LongestTrackS
)

// ErrBadRange refuses a bound nothing could satisfy.
var ErrBadRange = errors.New("station: impossible range")

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

	for _, r := range []struct {
		what     string
		min, max float64
		lo, hi   float64
	}{
		{"year", float64(f.YearMin), float64(f.YearMax), earliestYear, float64(time.Now().Year() + 1)},
		{"tempo", f.TempoMin, f.TempoMax, slowestBPM, fastestBPM},
		{"length", f.DurationMinS, f.DurationMaxS, shortestTrackS, longestTrackS},
	} {
		if err := checkRange(r.what, r.min, r.max, r.lo, r.hi); err != nil {
			return err
		}
	}
	return nil
}

// checkRange refuses a nonzero bound outside what could exist, and a pair the
// wrong way round. Zero is unbounded on that side and is always fine.
func checkRange(what string, min, max, lo, hi float64) error {
	for _, v := range []float64{min, max} {
		if v != 0 && (v < lo || v > hi) {
			return fmt.Errorf("%w: %s %g is outside %g to %g", ErrBadRange, what, v, lo, hi)
		}
	}
	if min != 0 && max != 0 && min > max {
		return fmt.Errorf("%w: %s %g to %g selects nothing", ErrBadRange, what, min, max)
	}
	return nil
}

// bounds widens an unset side to a sentinel, so the SQL is one shape rather
// than four -- the same trick the tempo clause has always used.
func bounds(min, max, lo, hi float64) (float64, float64) {
	if min == 0 {
		min = lo
	}
	if max == 0 {
		max = hi
	}
	return min, max
}

// yearBounds and lengthBounds are the two new clauses' arguments.
func (f Filter) yearBounds() (float64, float64) {
	return bounds(float64(f.YearMin), float64(f.YearMax), 0, 99999)
}

func (f Filter) lengthBounds() (float64, float64) {
	return bounds(f.DurationMinS, f.DurationMaxS, 0, 1e9)
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
		// THE NUMERIC BOUNDS APPLY HERE TOO. A years-only station -- no
		// genres, no moods, 1980 to 1989 -- is a real station, and forgetting
		// this path is how it silently becomes the whole library.
		lo, hi := tempoBounds(f)
		yLo, yHi := f.yearBounds()
		dLo, dHi := f.lengthBounds()
		rows, err := s.DB().QueryContext(ctx, `
			SELECT t.id FROM tracks t
			 WHERE t.playable = 1 AND t.missing_at IS NULL
			   AND (t.bpm IS NULL OR t.bpm BETWEEN ? AND ?)
			   AND (t.year IS NULL OR t.year BETWEEN ? AND ?)
			   AND (t.duration_s IS NULL OR t.duration_s BETWEEN ? AND ?)
			 ORDER BY t.id`, lo, hi, yLo, yHi, dLo, dHi)
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
	lo, hi := tempoBounds(f)
	yLo, yHi := f.yearBounds()
	dLo, dHi := f.lengthBounds()

	rows, err := s.DB().QueryContext(ctx, `
		SELECT DISTINCT t.id FROM tracks t
		  JOIN effective_tags e ON e.track_id = t.id
		 WHERE t.playable = 1 AND t.missing_at IS NULL
		   AND (t.bpm IS NULL OR t.bpm BETWEEN ? AND ?)
		   AND (t.year IS NULL OR t.year BETWEEN ? AND ?)
		   AND (t.duration_s IS NULL OR t.duration_s BETWEEN ? AND ?)
		   AND (json_array_length(?) = 0 OR EXISTS (
		         SELECT 1 FROM json_each(e.station_tags) tag
		          WHERE tag.value IN (SELECT value FROM json_each(?))))
		   AND (json_array_length(?) = 0 OR EXISTS (
		         SELECT 1 FROM json_each(e.mood) m
		          WHERE m.value IN (SELECT value FROM json_each(?))))
		 ORDER BY t.id`, lo, hi, yLo, yHi, dLo, dHi, genres, genres, moods, moods)
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
	lo, hi := tempoBounds(f)
	yLo, yHi := f.yearBounds()
	dLo, dHi := f.lengthBounds()

	rows, err := s.DB().QueryContext(ctx, `
		SELECT t.id FROM tracks t
		  JOIN effective_tags e ON e.track_id = t.id
		 WHERE t.playable = 1 AND t.missing_at IS NULL
		   AND (t.bpm IS NULL OR t.bpm BETWEEN ? AND ?)
		   AND (t.year IS NULL OR t.year BETWEEN ? AND ?)
		   AND (t.duration_s IS NULL OR t.duration_s BETWEEN ? AND ?)
		   AND (json_array_length(e.station_tags) = 0
		        OR (e.overridden = 0 AND e.confidence = ?)
		        OR EXISTS (
		             SELECT 1 FROM json_each(e.station_tags) tag
		              WHERE tag.value = ?))
		   AND (json_array_length(?) = 0 OR EXISTS (
		         SELECT 1 FROM json_each(e.mood) m
		          WHERE m.value IN (SELECT value FROM json_each(?))))
		 ORDER BY t.id`,
		lo, hi, yLo, yHi, dLo, dHi, ConfidenceNone, enrich.FallbackStationTag, moods, moods)
	if err != nil {
		return nil, fmt.Errorf("station: selecting unplaced tracks: %w", err)
	}
	return scanIDs(rows)
}

// ConfidenceNone marks a dossier that could assert nothing. Named here because
// it is the value the catch-all query tests for.
const ConfidenceNone = "none"
