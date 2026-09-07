// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

// Enrichment order decides how long a new station is empty.
//
// A station's tracks are SELECTED BY DOSSIER, so nothing can join it until its
// tracks are enriched -- and enrichment is one pass per track at tens of
// seconds, which is days for a real library. Working in id order meant an
// operator made a rock station and watched it sit at twelve tracks. The file's
// own genre tag is the only thing known before enrichment, so it is used to
// enrich likely candidates first.

func tagged(t *testing.T, s *store.Store, id int64, genre string) {
	t.Helper()
	if _, err := s.DB().Exec(`UPDATE tracks SET genre = ? WHERE id = ?`, genre, id); err != nil {
		t.Fatal(err)
	}
}

func station(t *testing.T, s *store.Store, id int64, genre string) {
	t.Helper()
	if _, err := s.DB().Exec(
		`INSERT INTO stations (id, name, genre, created_at) VALUES (?, ?, ?, 1)`,
		id, "S"+genre, genre); err != nil {
		t.Fatal(err)
	}
}

func TestPriorityPlainIDOrderWithNoStations(t *testing.T) {
	s := queueStore(t, 3)
	tagged(t, s, 3, "Rock")

	w, err := newQueue(s, nil).nextTrack(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.id != 1 {
		t.Errorf("next = %d, want 1: with no stations there is nothing to prioritise for", w.id)
	}
}

func TestPriorityStationGenreJumpsTheQueue(t *testing.T) {
	s := queueStore(t, 5)
	tagged(t, s, 4, "Rock")
	station(t, s, 1, "rock")

	w, err := newQueue(s, nil).nextTrack(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.id != 4 {
		t.Errorf("next = %d, want 4: the operator's rock station needs rock tracks first", w.id)
	}
}

func TestPriorityMatchesLooselyAndIgnoresCase(t *testing.T) {
	// Real tags are "Hard Rock", "Alt. Rock", "Rock/Pop". An exact match would
	// prioritise almost nothing.
	for _, tag := range []string{"Rock", "HARD ROCK", "Alt. Rock", "rock/pop"} {
		s := queueStore(t, 4)
		tagged(t, s, 3, tag)
		station(t, s, 1, "rock")

		w, err := newQueue(s, nil).nextTrack(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if w.id != 3 {
			t.Errorf("tag %q: next = %d, want 3", tag, w.id)
		}
	}
}

func TestPriorityUnmatchedTagsWaitTheirTurn(t *testing.T) {
	s := queueStore(t, 4)
	tagged(t, s, 2, "Polka")
	station(t, s, 1, "rock")

	w, err := newQueue(s, nil).nextTrack(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.id != 1 {
		t.Errorf("next = %d, want 1: a tag matching no station buys no priority", w.id)
	}
}

func TestPriorityUntaggedTracksAreStillEnriched(t *testing.T) {
	// A HINT ABOUT ORDER, NEVER ABOUT MEMBERSHIP. Everything gets a dossier
	// eventually, or the catch-all station never improves and the library is
	// permanently half-known.
	s := queueStore(t, 2)
	tagged(t, s, 2, "Rock")
	station(t, s, 1, "rock")

	q := newQueue(s, nil)
	first, err := q.nextTrack(context.Background())
	if err != nil || first.id != 2 {
		t.Fatalf("first = %d (%v), want the rock track", first.id, err)
	}
	// Give it a dossier, as enrichOne would.
	if _, err := s.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence, created_at) VALUES (2, '{}', 'low', 1)`); err != nil {
		t.Fatal(err)
	}
	second, err := q.nextTrack(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.id != 1 {
		t.Errorf("second = %d, want the untagged track: nothing is skipped forever", second.id)
	}
}

func TestPriorityEveryStationCounts(t *testing.T) {
	s := queueStore(t, 6)
	tagged(t, s, 5, "Jazz")
	station(t, s, 1, "rock")
	station(t, s, 2, "jazz")

	w, err := newQueue(s, nil).nextTrack(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.id != 5 {
		t.Errorf("next = %d, want 5: a second station's genre counts too", w.id)
	}
}
