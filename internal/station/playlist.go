// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrewloable/jockora/internal/store"
)

// Diff is what one regeneration changed, for the console to show.
//
// Counts rather than lists: an operator wants to know that 40 tracks arrived
// and 3 left, and the list of 40 is the playlist they can already see.
type Diff struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Kept    int `json:"kept"`
}

// Regenerate materialises a station's filter into its playlist.
//
// MATERIALISED rather than evaluated live, because the operator EDITS the list
// and an edit needs a row to live on. Pins and excludes are flags on those
// rows, which makes regeneration a set operation rather than a merge heuristic
// that has to guess what a previous run meant.
//
// ONE TRANSACTION: a half-regenerated station is a station with a hole in it,
// and the hole would be discovered at play time.
func Regenerate(ctx context.Context, s *store.Store, stationID int64) (Diff, error) {
	st, err := s.GetStation(ctx, stationID)
	if err != nil {
		return Diff{}, err
	}
	filter := Filter{Genres: SplitList(st.Genre), Moods: SplitList(st.Mood)}
	wanted, err := filter.TrackIDs(ctx, s)
	if err != nil {
		return Diff{}, err
	}
	current, err := s.ListStationTracks(ctx, stationID)
	if err != nil {
		return Diff{}, err
	}

	want := make(map[int64]bool, len(wanted))
	for _, id := range wanted {
		want[id] = true
	}

	var diff Diff
	var remove []int64
	have := make(map[int64]bool, len(current))
	for _, row := range current {
		have[row.TrackID] = true
		// AN OPERATOR'S DECISION OUTLIVES THE FILTER. A pinned track stays
		// even when the filter no longer selects it -- that is what pinning
		// means -- and an excluded one keeps its row, or the next regeneration
		// would add it straight back and the ban would have to be repeated
		// forever.
		if row.Pinned || row.Excluded || want[row.TrackID] {
			diff.Kept++
			continue
		}
		remove = append(remove, row.TrackID)
		diff.Removed++
	}

	var add []int64
	for _, id := range wanted {
		if !have[id] {
			add = append(add, id)
			diff.Added++
		}
	}
	if len(add) == 0 && len(remove) == 0 {
		return diff, nil
	}

	// ReplaceStationTracks already IS this write: it deletes the unflagged
	// rows, keeps pinned and excluded ones, inserts what it is given, and does
	// all of it in one transaction. A second copy of that logic here would be
	// a second place for the flag rules to drift.
	if err := s.ReplaceStationTracks(ctx, stationID, wanted); err != nil {
		return Diff{}, err
	}
	return diff, nil
}

// ErrTooFewTracks refuses to put a station on air that would repeat itself.
var ErrTooFewTracks = errors.New("station: too few tracks to run")

// Enable puts a station on the dial, if it has enough music.
//
// Checked HERE rather than in the HTTP handler, because it is a rule about
// stations and not about requests: the same refusal has to hold whether the
// enable came from the console, a seed or a script.
func Enable(ctx context.Context, s *store.Store, id int64) error {
	st, err := s.GetStation(ctx, id)
	if err != nil {
		return err
	}
	pool, err := s.StationTrackIDs(ctx, id)
	if err != nil {
		return err
	}
	if ok, _ := CheckThreshold(len(pool)); !ok {
		// The COUNT is in the message. "Too few tracks" leaves an operator
		// guessing how many more they need; "9, needs 10" does not.
		return fmt.Errorf("%w: %d tracks, needs %d", ErrTooFewTracks, len(pool), MinStationTracks)
	}

	st.Enabled = true
	return s.UpdateStation(ctx, st)
}
