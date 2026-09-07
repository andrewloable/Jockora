// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TrackTags is what a track counts as, after an operator's edit is laid over
// the dossier.
//
// Overridden says WHICH of the two the caller is looking at. A console needs
// it to offer "revert to what enrichment decided", and the catch-all needs it
// to tell a track nobody has placed from one somebody has.
type TrackTags struct {
	Genres     []string `json:"genres"`
	Moods      []string `json:"moods"`
	Overridden bool     `json:"overridden"`
}

// SetTrackTags records an operator's own tags for a track.
//
// The WHOLE list each time, not an add or a remove: the console edits a set of
// checkboxes and submits the result, and a partial update would need the caller
// to know which of two lists moved.
//
// It writes an OVERRIDE, never the dossier. A dossier is rewritten whenever it
// is missing -- which the requeue path does to a whole library at once -- so an
// edit written there would survive until the next model change and then vanish.
func (s *Store) SetTrackTags(ctx context.Context, trackID int64, genres, moods []string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO track_tags (track_id, station_tags, mood, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(track_id) DO UPDATE SET
			station_tags = excluded.station_tags,
			mood         = excluded.mood,
			updated_at   = excluded.updated_at`,
		trackID, encodeList(genres), encodeList(moods), time.Now().Unix())
	if err != nil {
		// The foreign key refuses a track the library does not have, which is
		// the database declining to tag something nobody could play.
		return fmt.Errorf("store: tagging track %d: %w", trackID, err)
	}
	return nil
}

// ClearTrackTags drops the override, putting the dossier back in charge.
//
// ErrNotFound when there was nothing to drop, so a console can tell "reverted"
// from "there was never an edit here".
func (s *Store) ClearTrackTags(ctx context.Context, trackID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM track_tags WHERE track_id = ?`, trackID)
	if err != nil {
		return fmt.Errorf("store: clearing tags on track %d: %w", trackID, err)
	}
	return requireOneRow(res, trackID)
}

// TrackTags reads what a track counts as, through the effective_tags view --
// the one place the override and the dossier are combined.
func (s *Store) TrackTags(ctx context.Context, trackID int64) (TrackTags, error) {
	var out TrackTags
	var genres, moods string
	err := s.db.QueryRowContext(ctx,
		`SELECT station_tags, mood, overridden FROM effective_tags WHERE track_id = ?`, trackID).
		Scan(&genres, &moods, &out.Overridden)
	if errors.Is(err, sql.ErrNoRows) {
		return TrackTags{}, ErrNotFound
	}
	if err != nil {
		return TrackTags{}, fmt.Errorf("store: reading tags of track %d: %w", trackID, err)
	}
	if err := decodeTagLists(&out, genres, moods); err != nil {
		return TrackTags{}, fmt.Errorf("store: track %d: %w", trackID, err)
	}
	return out, nil
}

// decodeTagLists turns the two JSON columns into slices.
//
// A corrupt list is an ERROR rather than a track with no tags: emptying it
// silently would move the track into the catch-all and look like enrichment
// having failed.
func decodeTagLists(into *TrackTags, genres, moods string) error {
	for _, f := range []struct {
		raw  string
		list *[]string
		name string
	}{{genres, &into.Genres, "station_tags"}, {moods, &into.Moods, "mood"}} {
		if err := json.Unmarshal([]byte(f.raw), f.list); err != nil {
			return fmt.Errorf("decoding %s: %w", f.name, err)
		}
		if *f.list == nil {
			*f.list = []string{}
		}
	}
	return nil
}
