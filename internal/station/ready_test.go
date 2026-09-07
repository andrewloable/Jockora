// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import "testing"

func TestReadyCatchAllIsAlwaysTunable(t *testing.T) {
	// It holds everything enrichment could not place, which on a fresh library
	// is nearly the whole library. Gating it would leave a new install with
	// nothing to play at all.
	for _, tag := range []string{UnsortedTag, "other"} {
		if ok, _ := Ready(tag, 1, false); !ok {
			t.Errorf("%q with one track and no enrichment was gated", tag)
		}
	}
}

func TestReadyThinStationWaitsWhileEnrichmentRuns(t *testing.T) {
	// Twelve tracks on a loop is a worse first impression than the station not
	// being there yet -- and it WILL grow, because enrichment is still working.
	ok, why := Ready("rock", WarnStationTracks-1, false)
	if ok {
		t.Errorf("a station below %d tracks was offered mid-enrichment", WarnStationTracks)
	}
	if why == "" {
		t.Error("a gated station must say why")
	}
}

func TestReadyComfortableStationIsTunable(t *testing.T) {
	if ok, _ := Ready("rock", WarnStationTracks, false); !ok {
		t.Errorf("a station at %d tracks was gated", WarnStationTracks)
	}
}

func TestReadyGateLiftsWhenEnrichmentIsDone(t *testing.T) {
	// A small station is then small because that is all the music there is.
	// No amount of waiting will add to it, so waiting is just a closed door.
	if ok, why := Ready("rock", 3, true); !ok {
		t.Errorf("a three-track station stayed gated after enrichment finished: %q", why)
	}
}

// The catch-all station's genre is "unsorted", which is NOT in the dossier
// vocabulary -- so the filter rejected it and the one station that works on a
// fresh library was the one nobody could rebuild. Measured on the live box:
// POST /admin/stations/1/regenerate answered 500.

func TestReadyCatchAllFilterValidates(t *testing.T) {
	for _, genre := range []string{UnsortedTag, "other"} {
		if err := (Filter{Genres: SplitList(genre)}).Validate(); err != nil {
			t.Errorf("Filter{Genres: SplitList(%q)}.Validate() = %v, want nil", genre, err)
		}
	}
	if err := (Filter{Genres: SplitList("nonsense")}).Validate(); err == nil {
		t.Error("a genre outside the vocabulary was accepted")
	}
}
