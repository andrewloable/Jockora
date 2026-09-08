// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"strings"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/store"
)

// SeedStations turns the library's own proposal into rows, ONCE.
//
// AN EMPTY TABLE IS THE ONLY TRIGGER. §22A's promise is that first listen needs
// no setup, so the dial has to exist before anyone configures anything -- but
// the moment one station row exists the dial is the operator's, and a restart
// that re-proposed over it would undo their work with nothing to say it had.
// Emptying the table is therefore the documented way to ask for a fresh
// proposal.
//
// ProposeDial stays the source of the proposal and is not replaced: this is the
// seed, 13g serves the table.
//
// Jocks must already be seeded. The stations table references them, so calling
// this first fails loudly on the foreign key rather than quietly producing a
// dial on which nobody is on air.
func SeedStations(ctx context.Context, s *store.Store, personas []*dj.Persona) (int, error) {
	existing, err := s.ListStations(ctx)
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		return 0, nil
	}

	d, err := ProposeDial(ctx, s, personas)
	if err != nil {
		return 0, err
	}

	created := 0
	for _, st := range d.Stations {
		pool, err := PoolForTag(ctx, s, st.Tag)
		if err != nil {
			return created, err
		}
		// NO MOOD. The dial's moods describe what a bucket sounds like;
		// promoting one to a filter would cut the station down to a fraction of
		// the tracks that made it worth proposing. Narrowing by mood is an
		// operator decision, taken in the console with the counts visible.
		id, err := s.CreateStation(ctx, store.Station{
			Name:   strings.ToUpper(st.Tag),
			Genre:  st.Tag,
			JockID: st.Jock,
			// THE SAME SENTENCE THE MIGRATION WRITES. This runs on a FRESH
			// install, where the back-fill has nothing to back-fill -- so
			// without it the very first dial an operator ever sees is the one
			// dial with empty briefs, which is exactly the two-shapes problem
			// the back-fill was decided on to remove. Jockora-g1t.1.
			Brief:   store.BriefFor(st.Tag, ""),
			Enabled: true,
		})
		if err != nil {
			return created, err
		}
		if err := s.ReplaceStationTracks(ctx, id, pool); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
