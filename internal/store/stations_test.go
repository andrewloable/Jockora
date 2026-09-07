// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func stationStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// tracks inserts playable library rows. station_tracks has a foreign key to
// tracks, so a playlist test that skips this is testing nothing.
func tracks(t *testing.T, s *Store, n int) []int64 {
	t.Helper()
	var ids []int64
	for i := 1; i <= n; i++ {
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (id, path, playable) VALUES (?, ?, 1)`,
			i, fmt.Sprintf("/m/%d.mp3", i)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, int64(i))
	}
	return ids
}

func TestStationsCreateGetList(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()

	if err := s.UpsertJock(ctx, sampleJock()); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateStation(ctx, Station{
		Name: "ROCK", Genre: "rock", Mood: "aggressive", JockID: "dutch_mahoney", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.GetStation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.Name != "ROCK" || got.Genre != "rock" {
		t.Errorf("identity did not round-trip: %+v", got)
	}
	if got.Mood != "aggressive" || got.JockID != "dutch_mahoney" || !got.Enabled {
		t.Errorf("fields did not round-trip: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	// A station with no mood and no jock is legal -- the catch-all is exactly
	// that -- and both must come back as empty strings, not as a scan error on
	// a NULL column.
	if _, err := s.CreateStation(ctx, Station{Name: "UNSORTED", Genre: "unsorted"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListStations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListStations returned %d stations, want 2", len(list))
	}
	if list[0].Name != "ROCK" || list[1].Name != "UNSORTED" {
		t.Errorf("wrong order: %q then %q", list[0].Name, list[1].Name)
	}
	if list[1].Mood != "" || list[1].JockID != "" {
		t.Errorf("NULL mood/jock came back as %q/%q, want empty", list[1].Mood, list[1].JockID)
	}
	if list[1].Enabled {
		t.Error("Enabled was not carried through as false")
	}
}

func TestStationsUpdate(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	if err := s.UpsertJock(ctx, sampleJock()); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateStation(ctx, Station{Name: "ROCK", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	want := Station{ID: id, Name: "HARD ROCK", Genre: "metal", Mood: "raw",
		JockID: "dutch_mahoney", Enabled: false}
	if err := s.UpdateStation(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetStation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != want.Name || got.Genre != want.Genre || got.Mood != want.Mood {
		t.Errorf("update did not stick: %+v", got)
	}
	if got.JockID != want.JockID || got.Enabled {
		t.Errorf("jock/enabled did not stick: %+v", got)
	}

	// Clearing a jock must write NULL, not the string "": the schema's
	// ON DELETE SET NULL and this path have to agree or an unassigned station
	// reads back differently depending on how it got that way.
	want.JockID, want.Mood = "", ""
	if err := s.UpdateStation(ctx, want); err != nil {
		t.Fatal(err)
	}
	var jock, mood any
	if err := s.DB().QueryRow(`SELECT jock_id, mood FROM stations WHERE id = ?`, id).
		Scan(&jock, &mood); err != nil {
		t.Fatal(err)
	}
	if jock != nil || mood != nil {
		t.Errorf("cleared fields stored as %v/%v, want NULL/NULL", jock, mood)
	}

	if err := s.UpdateStation(ctx, Station{ID: 9999, Name: "GHOST"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("updating a missing station: %v, want ErrNotFound", err)
	}
}

func TestStationsDeleteCascades(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	ids := tracks(t, s, 3)
	id, err := s.CreateStation(ctx, Station{Name: "ROCK", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStationTracks(ctx, id, ids); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteStation(ctx, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM station_tracks WHERE station_id = ?`, id).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d playlist rows survived the station", n)
	}
	// The cascade must stop at the playlist. A delete that reached tracks would
	// delete the library, which Jockora never owns.
	if err := s.DB().QueryRow(`SELECT count(*) FROM tracks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("deleting a station destroyed library rows: %d tracks left, want 3", n)
	}
	if _, err := s.GetStation(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetStation after delete: %v, want ErrNotFound", err)
	}
	if err := s.DeleteStation(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v, want ErrNotFound", err)
	}
}

func TestStationsSelectorState(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	id, err := s.CreateStation(ctx, Station{Name: "ROCK", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// Never played is not the same as played from position zero: a fresh
	// station must draw a new seed rather than resume someone else's.
	if _, ok, err := s.GetSelectorState(ctx, id); err != nil || ok {
		t.Fatalf("fresh station: ok = %v, err = %v, want false, nil", ok, err)
	}

	want := SelectorState{Seed: 424242, Cursor: 17}
	if err := s.SetSelectorState(ctx, id, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetSelectorState(ctx, id)
	if err != nil || !ok {
		t.Fatalf("after set: ok = %v, err = %v", ok, err)
	}
	if got != want {
		t.Errorf("selector state = %+v, want %+v", got, want)
	}

	// Cursor zero with a seed set still counts as saved, or the first track of
	// every restart replays.
	if err := s.SetSelectorState(ctx, id, SelectorState{Seed: 7, Cursor: 0}); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.GetSelectorState(ctx, id); !ok || got.Cursor != 0 || got.Seed != 7 {
		t.Errorf("cursor 0 did not survive: %+v ok=%v", got, ok)
	}

	if err := s.SetSelectorState(ctx, 9999, want); !errors.Is(err, ErrNotFound) {
		t.Errorf("setting state on a missing station: %v, want ErrNotFound", err)
	}
	if _, _, err := s.GetSelectorState(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("reading state of a missing station: %v, want ErrNotFound", err)
	}
}

func TestStationsTracksReplace(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	ids := tracks(t, s, 6)
	id, err := s.CreateStation(ctx, Station{Name: "ROCK", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.ReplaceStationTracks(ctx, id, []int64{ids[0], ids[1], ids[2]}); err != nil {
		t.Fatal(err)
	}
	got, err := s.StationTrackIDs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Fatalf("StationTrackIDs = %v, want [1 2 3]", got)
	}

	// The playlist is a SET the selector shuffles, not a running order, so the
	// order it comes back in is by track id whatever order it went in -- which
	// is the same order PoolForTag produces, so a rescan is a no-op.
	if err := s.ReplaceStationTracks(ctx, id, []int64{ids[5], ids[3], ids[4]}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.StationTrackIDs(ctx, id); !reflect.DeepEqual(got, []int64{4, 5, 6}) {
		t.Fatalf("after replace: %v, want [4 5 6]", got)
	}

	// A pin is an operator decision about THIS station and a rescan is a fact
	// about the library. The rescan must not quietly undo the decision.
	if err := s.SetPinned(ctx, id, ids[3], true); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStationTracks(ctx, id, []int64{ids[0]}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.StationTrackIDs(ctx, id); !reflect.DeepEqual(got, []int64{1, 4}) {
		t.Errorf("after replacing around a pin: %v, want [1 4] (the pin survives)", got)
	}

	// An exclusion is an operator decision too. If a rescan re-added an
	// excluded track the nightly scan would silently un-ban it, and nothing in
	// the console would say so.
	if err := s.SetExcluded(ctx, id, ids[0], true); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStationTracks(ctx, id, []int64{ids[0], ids[1]}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.StationTrackIDs(ctx, id); !reflect.DeepEqual(got, []int64{2, 4}) {
		t.Errorf("after a rescan re-offered an excluded track: %v, want [2 4]", got)
	}
	if err := s.SetExcluded(ctx, id, ids[0], false); err != nil {
		t.Fatal(err)
	}

	// Replacing with nothing is how a station is emptied, and must not be
	// mistaken for "leave it alone".
	if err := s.SetPinned(ctx, id, ids[3], false); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStationTracks(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.StationTrackIDs(ctx, id); len(got) != 0 {
		t.Errorf("emptying left %v", got)
	}
}

func TestStationsPinExclude(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	ids := tracks(t, s, 3)
	id, err := s.CreateStation(ctx, Station{Name: "ROCK", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStationTracks(ctx, id, ids); err != nil {
		t.Fatal(err)
	}

	if err := s.SetExcluded(ctx, id, ids[1], true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.StationTrackIDs(ctx, id); !reflect.DeepEqual(got, []int64{1, 3}) {
		t.Errorf("playable ids = %v, want [1 3] with 2 excluded", got)
	}

	// The console still has to SHOW the excluded track, or an operator cannot
	// find it to put it back.
	if err := s.SetPinned(ctx, id, ids[0], true); err != nil {
		t.Fatal(err)
	}
	full, err := s.ListStationTracks(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []StationTrack{
		{TrackID: 1, Pinned: true}, {TrackID: 2, Excluded: true}, {TrackID: 3},
	}
	if !reflect.DeepEqual(full, want) {
		t.Errorf("ListStationTracks = %+v, want %+v", full, want)
	}

	if err := s.SetExcluded(ctx, id, ids[1], false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.StationTrackIDs(ctx, id); len(got) != 3 {
		t.Errorf("un-excluding did not restore the track: %v", got)
	}

	// Flagging a track that is not on this station is a caller bug, not a
	// silent no-op that leaves the operator staring at an unchanged screen.
	if err := s.SetPinned(ctx, id, 9999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("pinning a track not on the station: %v, want ErrNotFound", err)
	}
	if err := s.SetExcluded(ctx, id, 9999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("excluding a track not on the station: %v, want ErrNotFound", err)
	}
}

// TestStationsErrorsSurface covers the paths a working database never takes.
func TestStationsErrorsSurface(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	ids := tracks(t, s, 2)
	id, err := s.CreateStation(ctx, Station{Name: "ROCK", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStationTracks(ctx, id, ids); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetStation(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetStation of a missing id: %v, want ErrNotFound", err)
	}

	// A playlist naming a track the library no longer has is a stale pool, and
	// the foreign key must say so. Accepting it would leave a station holding a
	// row that resolves to no file and fails only at play time.
	if err := s.ReplaceStationTracks(ctx, id, []int64{9999}); err == nil {
		t.Error("ReplaceStationTracks accepted a track that is not in the library")
	}
	// The whole call is one transaction, so the rejected batch must leave the
	// old playlist standing rather than half-cleared.
	if got, _ := s.StationTrackIDs(ctx, id); !reflect.DeepEqual(got, ids) {
		t.Errorf("a rejected replace left the playlist as %v, want %v", got, ids)
	}

	// A corrupt flag column must be an error rather than a track that is
	// neither pinned nor excluded. SQLite has type affinity, not types, so a
	// bad writer really can put text in an integer column.
	if _, err := s.DB().Exec(
		`UPDATE station_tracks SET pinned = 'yes' WHERE track_id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListStationTracks(ctx, id); err == nil {
		t.Error("ListStationTracks accepted a corrupt pinned flag")
	}
	if _, err := s.DB().Exec(`UPDATE station_tracks SET pinned = 0 WHERE track_id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE stations SET enabled = 'yes'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListStations(ctx); err == nil {
		t.Error("ListStations accepted a corrupt enabled flag")
	}
	if _, err := s.DB().Exec(`UPDATE stations SET enabled = 1`); err != nil {
		t.Fatal(err)
	}

	readOnly(t, s)
	if _, err := s.CreateStation(ctx, Station{Name: "X", Genre: "x"}); err == nil {
		t.Error("CreateStation succeeded against a read-only database")
	}
	if err := s.UpdateStation(ctx, Station{ID: id, Name: "X"}); err == nil {
		t.Error("UpdateStation succeeded against a read-only database")
	}
	if err := s.DeleteStation(ctx, id); err == nil {
		t.Error("DeleteStation succeeded against a read-only database")
	}
	if err := s.SetSelectorState(ctx, id, SelectorState{Seed: 1}); err == nil {
		t.Error("SetSelectorState succeeded against a read-only database")
	}
	if err := s.ReplaceStationTracks(ctx, id, ids); err == nil {
		t.Error("ReplaceStationTracks succeeded against a read-only database")
	}
	if err := s.SetPinned(ctx, id, ids[0], true); err == nil {
		t.Error("SetPinned succeeded against a read-only database")
	}
	if err := s.SetExcluded(ctx, id, ids[0], true); err == nil {
		t.Error("SetExcluded succeeded against a read-only database")
	}
}

// TestStationsClosedStore reaches the read paths, which a read-only database
// leaves working.
func TestStationsClosedStore(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetStation(ctx, 1); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("GetStation on a closed store: %v, want a real error", err)
	}
	if _, err := s.ListStations(ctx); err == nil {
		t.Error("ListStations succeeded on a closed store")
	}
	if _, _, err := s.GetSelectorState(ctx, 1); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("GetSelectorState on a closed store: %v, want a real error", err)
	}
	if _, err := s.StationTrackIDs(ctx, 1); err == nil {
		t.Error("StationTrackIDs succeeded on a closed store")
	}
	if err := s.ReplaceStationTracks(ctx, 1, []int64{1}); err == nil {
		t.Error("ReplaceStationTracks opened a transaction on a closed store")
	}
	if _, err := s.ListStationTracks(ctx, 1); err == nil {
		t.Error("ListStationTracks succeeded on a closed store")
	}
}

// TestStationsTrackPage: a station can hold thousands of tracks, so the console
// asks for a page and a total rather than the whole list.
func TestStationsTrackPage(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (id, path, artist, title, playable) VALUES (?, ?, ?, ?, 1)`,
			i, fmt.Sprintf("/m/%d.mp3", i), fmt.Sprintf("Artist %d", i),
			fmt.Sprintf("Title %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	id, err := s.CreateStation(ctx, store2Station())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStationTracks(ctx, id, []int64{1, 2, 3, 4, 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPinned(ctx, id, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExcluded(ctx, id, 3, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE tracks SET missing_at = 1 WHERE id = 4`); err != nil {
		t.Fatal(err)
	}

	rows, total, err := s.StationTrackPage(ctx, id, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	// The TOTAL is of the whole playlist, not of the page: a console showing
	// "2 of 2" for a station of five is lying.
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(rows) != 2 || rows[0].TrackID != 2 || rows[1].TrackID != 3 {
		t.Fatalf("page = %+v", rows)
	}
	if !rows[0].Pinned || rows[0].Artist != "Artist 2" || rows[0].Title != "Title 2" {
		t.Errorf("row = %+v; a list of ids is not a playlist editor", rows[0])
	}
	if !rows[1].Excluded {
		t.Errorf("the excluded track is not flagged: %+v", rows[1])
	}

	all, _, err := s.StationTrackPage(ctx, id, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("%d rows", len(all))
	}
	// Missing tracks are LISTED and flagged, so an operator can see why a
	// station shrank.
	if !all[3].Missing {
		t.Errorf("the missing track is not flagged: %+v", all[3])
	}

	// Past the end is an empty page, not an error.
	if rows, _, err := s.StationTrackPage(ctx, id, 10, 500); err != nil || len(rows) != 0 {
		t.Errorf("past the end = %v rows, %v", len(rows), err)
	}

	// The count works and the page does not: dropping a column only the page
	// reads separates the two queries, which a closed store cannot.
	if _, err := s.DB().Exec(`ALTER TABLE tracks DROP COLUMN artist`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StationTrackPage(ctx, id, 10, 0); err == nil {
		t.Error("a page that could not be read was reported as empty")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StationTrackPage(ctx, id, 10, 0); err == nil {
		t.Error("StationTrackPage succeeded on a closed store")
	}
}

func store2Station() Station {
	return Station{Name: "ROCK", Genre: "rock", Enabled: true}
}
