// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

func dialStore(t *testing.T, tagged map[string][]string, unenriched int) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	id := int64(0)
	add := func(dossier *enrich.Dossier) {
		id++
		if _, err := s.DB().Exec(`INSERT INTO tracks (id, path, playable) VALUES (?, ?, 1)`,
			id, fmt.Sprintf("/m/%d.mp3", id)); err != nil {
			t.Fatal(err)
		}
		if dossier == nil {
			return
		}
		raw, _ := json.Marshal(dossier)
		if _, err := s.DB().Exec(
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
			id, string(raw), dossier.Confidence); err != nil {
			t.Fatal(err)
		}
	}

	for tag, moods := range tagged {
		n := 0
		for _, m := range moods {
			n++
			add(&enrich.Dossier{StationTags: []string{tag}, Mood: []string{m},
				Confidence: enrich.ConfidenceHigh})
			_ = n
		}
	}
	for i := 0; i < unenriched; i++ {
		add(nil)
	}
	return s
}

func rep(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// TestDialProposesBucketsBiggestFirst is §22A's whole promise: first run shows
// the library, not a blank form.
func TestDialProposesBucketsBiggestFirst(t *testing.T) {
	s := dialStore(t, map[string][]string{
		"rock":    rep("aggressive", 20),
		"ambient": rep("calm", 12),
		// Exactly MinStationTracks: the smallest bucket that is still a
		// station, so this test says something about ordering and not about
		// the threshold.
		"synthwave": rep("nocturnal", 10),
	}, 0)

	d, err := ProposeDial(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Stations) != 3 {
		t.Fatalf("got %d stations, want 3: %+v", len(d.Stations), d.Stations)
	}
	if d.Stations[0].Tag != "rock" || d.Stations[0].Tracks != 20 {
		t.Errorf("first station = %+v, want rock with 20 tracks", d.Stations[0])
	}
	if d.Stations[2].Tag != "synthwave" {
		t.Errorf("stations are not ordered by size: %+v", d.Stations)
	}
	if d.Stations[0].Moods == nil {
		t.Error("a station reports no moods, so the dial cannot say what it sounds like")
	}
}

// TestDialKeepsUnenrichedTracksVisible. On a large library most tracks have no
// dossier for the first few days. A dial that silently omits them looks exactly
// like a broken scan.
func TestDialKeepsUnenrichedTracksVisible(t *testing.T) {
	s := dialStore(t, map[string][]string{"rock": rep("aggressive", 10)}, 240)

	d, err := ProposeDial(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := d.Stations[len(d.Stations)-1]
	if last.Tag != UnsortedTag {
		t.Fatalf("last station = %q, want the catch-all last", last.Tag)
	}
	if last.Tracks != 240 {
		t.Errorf("catch-all holds %d, want the 240 unenriched tracks", last.Tracks)
	}
	if d.Enriched != 10 || d.Total != 250 {
		t.Errorf("enriched/total = %d/%d, want 10/250 so a client can say how provisional this is", d.Enriched, d.Total)
	}
}

// TestDialFoldsTinyBucketsIntoTheCatchAll: a two-track station repeats itself
// immediately. Those tracks are not discarded, they are folded in.
func TestDialFoldsTinyBucketsIntoTheCatchAll(t *testing.T) {
	s := dialStore(t, map[string][]string{
		"rock":   rep("aggressive", 30),
		"polka":  rep("playful", 3),
		"shanty": rep("warm", 2),
	}, 0)

	d, err := ProposeDial(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range d.Stations {
		if st.Tag == "polka" || st.Tag == "shanty" {
			t.Errorf("%q became a station with only a few tracks", st.Tag)
		}
	}
	last := d.Stations[len(d.Stations)-1]
	if last.Tag != UnsortedTag || last.Tracks != 5 {
		t.Errorf("catch-all = %+v, want the 5 folded tracks", last)
	}
}

// TestDialMatchesAJockPerStation is what makes the dial feel authored: the rock
// station is met by the rock jock, not by whoever sorts first.
func TestDialMatchesAJockPerStation(t *testing.T) {
	personas, err := dj.LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}
	s := dialStore(t, map[string][]string{
		"rock":      rep("aggressive", 20),
		"classical": rep("calm", 15),
		"opm":       rep("wistful", 10),
	}, 0)

	d, err := ProposeDial(context.Background(), s, personas)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"rock":      "dutch_mahoney",
		"classical": "wendell_pike",
		"opm":       "baby_concepcion",
	}
	for _, st := range d.Stations {
		if w, ok := want[st.Tag]; ok && st.Jock != w {
			t.Errorf("%s station drew %q, want %q", st.Tag, st.Jock, w)
		}
		if st.Jock != "" && st.JockName == "" {
			t.Errorf("%s has a jock id but no name to display", st.Tag)
		}
	}
}

// TestDialLeavesTheCatchAllWithoutAJock: "unsorted" is not a genre, and
// matching a persona against that word produces an arbitrary jock presented as
// a deliberate choice.
func TestDialLeavesTheCatchAllWithoutAJock(t *testing.T) {
	personas, err := dj.LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}
	s := dialStore(t, map[string][]string{"rock": rep("aggressive", 10)}, 50)

	d, err := ProposeDial(context.Background(), s, personas)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range d.Stations {
		if st.Tag == UnsortedTag && st.Jock != "" {
			t.Errorf("the catch-all was assigned jock %q from the word %q", st.Jock, UnsortedTag)
		}
	}
}

// TestDialUsesMoodToBreakAGenreTie.
//
// §22A says the match intersects declared genres AND moods. Every other test
// here is decided by genre alone, so none of them would notice if the mood half
// were dropped -- verified: removing it left them all passing. This one cannot
// be satisfied by genre, because midnight_vale and marlon_vex both declare
// "electronic" and only their moods tell them apart.
func TestDialUsesMoodToBreakAGenreTie(t *testing.T) {
	personas, err := dj.LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ mood, want string }{
		{"nocturnal", "midnight_vale"},
		{"ominous", "marlon_vex"},
	} {
		s := dialStore(t, map[string][]string{"electronic": rep(tc.mood, 20)}, 0)
		d, err := ProposeDial(context.Background(), s, personas)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Stations) == 0 {
			t.Fatal("no stations")
		}
		if got := d.Stations[0].Jock; got != tc.want {
			t.Errorf("an electronic/%s station drew %q, want %q -- mood is not affecting the match",
				tc.mood, got, tc.want)
		}
	}
}

// TestDialGivesNoJockToEitherCatchAll.
//
// "unsorted" holds tracks with no dossier; "other" holds tracks that were
// enriched and fit no genre in the closed vocabulary. Neither describes a
// SOUND, so neither has a character a persona can match. Measured on a real
// library before this guard: the "other" bucket drew Marcus "Midnight" Vale
// purely from the literal word "other", and the dial presented that accident as
// a deliberate pairing.
func TestDialGivesNoJockToEitherCatchAll(t *testing.T) {
	personas, err := dj.LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}
	s := dialStore(t, map[string][]string{
		"rock":  rep("aggressive", 30),
		"other": rep("wistful", 24),
	}, 40)

	d, err := ProposeDial(context.Background(), s, personas)
	if err != nil {
		t.Fatal(err)
	}

	// The tags are named LITERALLY here, not via isCatchAll. Asserting through
	// the same helper the code uses makes the test self-referential: narrowing
	// isCatchAll would narrow the assertion with it and the test would keep
	// passing. Verified -- it did exactly that until this was rewritten.
	seen := map[string]Station{}
	for _, st := range d.Stations {
		seen[st.Tag] = st
	}
	for _, tag := range []string{"unsorted", "other"} {
		if st, ok := seen[tag]; ok && st.Jock != "" {
			t.Errorf("catch-all %q was assigned jock %q", tag, st.Jock)
		}
	}
	if seen["rock"].Jock == "" {
		t.Error("a real genre station lost its jock; the guard is too wide")
	}
	if d.Stations[0].Tag == "unsorted" || d.Stations[0].Tag == "other" {
		t.Errorf("the dial opens on a catch-all: %+v", d.Stations)
	}
	if last := d.Stations[len(d.Stations)-1].Tag; last != "unsorted" && last != "other" {
		t.Errorf("a catch-all is not last: %+v", d.Stations)
	}
}

// TestDialCountsTheOperatorsOwnFiling: the dial is proposed from what the
// library sounds like, and a hand edit outranks the enrichment everywhere else.
// It can pre-exist the first station too -- an enrichment file imported into a
// fresh install carries overrides with it.
func TestDialCountsTheOperatorsOwnFiling(t *testing.T) {
	ctx := context.Background()
	// Twelve rock tracks, of which eleven are re-filed as folk by hand. Both
	// buckets have to clear MinStationTracks to appear on the dial.
	moods := make([]string, 12)
	for i := range moods {
		moods[i] = "raw"
	}
	s := dialStore(t, map[string][]string{"rock": moods}, 0)
	for id := int64(1); id <= 11; id++ {
		if err := s.SetTrackTags(ctx, id, []string{"folk"}, []string{"gentle"}); err != nil {
			t.Fatal(err)
		}
	}

	d, err := ProposeDial(ctx, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rock, folk int
	for _, st := range d.Stations {
		switch st.Tag {
		case "rock":
			rock = st.Tracks
		case "folk":
			folk = st.Tracks
		}
	}
	// Folk is a station; the one rock track left is below MinStationTracks and
	// correctly does not get one, which is the same rule as any thin bucket.
	if folk != 11 || rock != 0 {
		t.Errorf("dial proposed rock=%d folk=%d, want the re-filed tracks counted as folk", rock, folk)
	}
}
