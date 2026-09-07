// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/store"
)

// seedFixture is dialStore plus the jocks the dial will reference. The stations
// table has a foreign key to jocks, so a seeder test that skips this proves
// nothing about the ordering that first run actually needs.
func seedFixture(t *testing.T, tagged map[string][]string, unenriched int) (*store.Store, []*dj.Persona) {
	t.Helper()
	s := dialStore(t, tagged, unenriched)
	personas, err := dj.LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dj.SeedJocks(context.Background(), s, "../../personas"); err != nil {
		t.Fatal(err)
	}
	return s, personas
}

func TestSeedStationsWhenEmpty(t *testing.T) {
	s, personas := seedFixture(t, map[string][]string{
		"rock":      rep("aggressive", 20),
		"classical": rep("calm", 15),
		"opm":       rep("wistful", 10),
	}, 0)
	ctx := context.Background()

	n, err := SeedStations(ctx, s, personas)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("SeedStations created %d stations, want 3", n)
	}

	list, err := s.ListStations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byGenre := map[string]store.Station{}
	for _, st := range list {
		byGenre[st.Genre] = st
	}
	rock, ok := byGenre["rock"]
	if !ok {
		t.Fatalf("no rock station: %+v", list)
	}
	// §22A renders the dial as ROCK 412 - SYNTHWAVE 208 - OPM 173, so the name
	// is the tag upper-cased. One rule, so the catch-all needs no special case.
	if rock.Name != "ROCK" {
		t.Errorf("rock station named %q, want ROCK", rock.Name)
	}
	if rock.JockID != "dutch_mahoney" {
		t.Errorf("rock station drew jock %q, want dutch_mahoney", rock.JockID)
	}
	if !rock.Enabled {
		t.Error("a seeded station arrived disabled")
	}
	// A proposed station is a GENRE, never a genre narrowed to one mood: the
	// dial's moods describe what the bucket sounds like, and picking one as a
	// filter would drop most of the tracks that made the bucket worth showing.
	if rock.Mood != "" {
		t.Errorf("seeded station carries mood %q, want none", rock.Mood)
	}

	// The playlist is materialised, not computed at play time.
	want, err := PoolForTag(ctx, s, "rock")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.StationTrackIDs(ctx, rock.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 20 || !reflect.DeepEqual(got, want) {
		t.Errorf("rock playlist = %v (%d), want PoolForTag's %d ids", got, len(got), len(want))
	}
}

func TestSeedStationsSkipsWhenNotEmpty(t *testing.T) {
	s, personas := seedFixture(t, map[string][]string{"rock": rep("aggressive", 20)}, 0)
	ctx := context.Background()

	// DISABLED, and not one the dial would propose. An operator who cut the
	// dial down to one station they then switched off must not find the whole
	// proposal back after a restart.
	if _, err := s.CreateStation(ctx, store.Station{Name: "MINE", Genre: "shoegaze"}); err != nil {
		t.Fatal(err)
	}
	n, err := SeedStations(ctx, s, personas)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("SeedStations created %d stations over an existing dial, want 0", n)
	}
	list, _ := s.ListStations(ctx)
	if len(list) != 1 || list[0].Name != "MINE" {
		t.Errorf("the operator's dial was disturbed: %+v", list)
	}
}

func TestSeedStationsMatchesProposeDial(t *testing.T) {
	s, personas := seedFixture(t, map[string][]string{
		"rock":       rep("aggressive", 20),
		"classical":  rep("calm", 15),
		"electronic": append(rep("hypnotic", 8), rep("nocturnal", 6)...),
	}, 12)
	ctx := context.Background()

	d, err := ProposeDial(ctx, s, personas)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SeedStations(ctx, s, personas); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListStations(ctx)
	if err != nil {
		t.Fatal(err)
	}

	pairs := func(in []string) []string { sort.Strings(in); return in }
	var proposed, seeded []string
	for _, st := range d.Stations {
		proposed = append(proposed, st.Tag+"/"+st.Jock)
	}
	for _, st := range list {
		seeded = append(seeded, st.Genre+"/"+st.JockID)
	}
	if !reflect.DeepEqual(pairs(proposed), pairs(seeded)) {
		t.Errorf("seeded %v, proposed %v", pairs(seeded), pairs(proposed))
	}
}

func TestSeedStationsUnsortedBucket(t *testing.T) {
	s, personas := seedFixture(t, map[string][]string{"rock": rep("aggressive", 20)}, 30)
	ctx := context.Background()

	if _, err := SeedStations(ctx, s, personas); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListStations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var un *store.Station
	for i := range list {
		if list[i].Genre == UnsortedTag {
			un = &list[i]
		}
	}
	if un == nil {
		t.Fatalf("30 tracks with no dossier produced no catch-all station: %+v", list)
	}
	if un.Name != "UNSORTED" {
		t.Errorf("catch-all named %q, want UNSORTED", un.Name)
	}
	// Assigning a persona from the literal word "unsorted" would present an
	// accident as a deliberate pairing; ProposeDial refuses and so must this.
	if un.JockID != "" {
		t.Errorf("the catch-all was given jock %q", un.JockID)
	}
	ids, err := s.StationTrackIDs(ctx, un.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 30 {
		t.Errorf("catch-all holds %d tracks, want the 30 with no dossier", len(ids))
	}
}

// TestSeedStationsNeedsItsJocksFirst pins the ORDER of first run. The stations
// table references jocks, so seeding stations before jocks is a foreign key
// failure -- and it must be a loud one, not a dial that quietly comes up with
// every station jockless.
func TestSeedStationsNeedsItsJocksFirst(t *testing.T) {
	s := dialStore(t, map[string][]string{"rock": rep("aggressive", 20)}, 0)
	personas, err := dj.LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SeedStations(context.Background(), s, personas); err == nil {
		t.Fatal("SeedStations succeeded with no jocks in the table")
	}
}

// TestSeedStationsSurfacesFailures: every step can fail, and a half-seeded dial
// that reports success is worse than one that reports the error.
func TestSeedStationsSurfacesFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("closed store", func(t *testing.T) {
		s, personas := seedFixture(t, map[string][]string{"rock": rep("aggressive", 20)}, 0)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := SeedStations(ctx, s, personas); err == nil {
			t.Error("SeedStations succeeded on a closed store")
		}
	})

	t.Run("no dossiers table", func(t *testing.T) {
		s, personas := seedFixture(t, map[string][]string{"rock": rep("aggressive", 20)}, 0)
		if _, err := s.DB().Exec(`DROP TABLE dossiers`); err != nil {
			t.Fatal(err)
		}
		if _, err := SeedStations(ctx, s, personas); err == nil {
			t.Error("SeedStations proposed a dial with no dossiers table")
		}
	})

	t.Run("corrupt dossier json", func(t *testing.T) {
		s, personas := seedFixture(t, map[string][]string{"rock": rep("aggressive", 20)}, 0)
		// ProposeDial skips a row it cannot parse, so the dial still has a rock
		// station; PoolForTag runs json_extract over every dossier and cannot.
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (id, path, playable) VALUES (900, '/m/900.mp3', 1);
			 INSERT INTO dossiers (track_id, json, confidence) VALUES (900, 'not json', 'high')`,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := SeedStations(ctx, s, personas); err == nil {
			t.Error("SeedStations built a pool from a corrupt dossier")
		}
	})

	t.Run("no station_tracks table", func(t *testing.T) {
		s, personas := seedFixture(t, map[string][]string{"rock": rep("aggressive", 20)}, 0)
		if _, err := s.DB().Exec(`DROP TABLE station_tracks`); err != nil {
			t.Fatal(err)
		}
		n, err := SeedStations(ctx, s, personas)
		if err == nil {
			t.Error("SeedStations materialised a playlist with no table to put it in")
		}
		if n != 0 {
			t.Errorf("reported %d stations created after failing to fill the first", n)
		}
	})
}
