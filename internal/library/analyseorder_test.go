// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"testing"
)

// THE LIBRARY IS MEASURED IN ID ORDER AND THE STATION PLAYS A PLAYLIST.
//
// Loudness is what stops one record arriving 12 dB louder than the last, and an
// unmeasured track plays at its own level by design. On the deployed box that
// left 6301 tracks unmeasured at 2.2 tracks a minute -- 48 hours -- while only
// 55 of them were in any station's playlist. Everything audible could have been
// levelled in half an hour; instead the analyser was working alphabetically
// through records nobody was going to hear.
//
// Every test here is TestAnalyseOrder*, which is the -run pattern for this fix.

func TestAnalyseOrderPrefersWhatAStationPlays(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	db := s.DB()

	// Three unmeasured tracks. The one a station plays is the LAST by id, so
	// id order and playlist order disagree and the test can tell them apart.
	for i, path := range []string{"/a.mp3", "/b.mp3", "/c.mp3"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO tracks (id, path, playable) VALUES (?, ?, 1)`, i+1, path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO stations (id, name, genre, enabled, created_at) VALUES (1, 'Rock', 'rock', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO station_tracks (station_id, track_id) VALUES (1, 3)`); err != nil {
		t.Fatal(err)
	}

	a := &Analyser{Store: s}
	got, err := a.next(ctx)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if got != "/c.mp3" {
		t.Errorf("next = %q, want /c.mp3: the only track anyone can actually hear", got)
	}
}

func TestAnalyseOrderStillReachesTheRestOfTheLibrary(t *testing.T) {
	// Prioritising is not skipping. Once the playlist is measured the analyser
	// carries on through everything else, in id order as before.
	s := openStore(t)
	ctx := context.Background()
	db := s.DB()

	for i, path := range []string{"/a.mp3", "/b.mp3"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO tracks (id, path, playable) VALUES (?, ?, 1)`, i+1, path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO stations (id, name, genre, enabled, created_at) VALUES (1, 'Rock', 'rock', 1, 0)`); err != nil {
		t.Fatal(err)
	}
	// The playlist track is already measured, so nothing is prioritised.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO station_tracks (station_id, track_id) VALUES (1, 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE tracks SET loudness_lufs = -14 WHERE id = 2`); err != nil {
		t.Fatal(err)
	}

	got, err := (&Analyser{Store: s}).next(ctx)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if got != "/a.mp3" {
		t.Errorf("next = %q, want /a.mp3", got)
	}
}
