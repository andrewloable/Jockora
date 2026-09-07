// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// stopLog records which stations were taken off air.
type stopLog struct {
	stopped []int64
	err     error
}

func (l *stopLog) Stop(id int64) error {
	l.stopped = append(l.stopped, id)
	return l.err
}

// stationsServer is an admin server over a library with `rock` tracks.
func stationsServer(t *testing.T, rock, jazz int) (*Server, *store.Store, *stopLog) {
	t.Helper()
	s, st, _ := authServer(t)

	id := 0
	add := func(tag string) {
		id++
		if _, err := st.DB().Exec(
			`INSERT INTO tracks (id, path, playable) VALUES (?, ?, 1)`,
			id, fmt.Sprintf("/m/%d.mp3", id)); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{
			"station_tags": []string{tag}, "mood": []string{"raw"}})
		if _, err := st.DB().Exec(
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
			id, string(raw), enrich.ConfidenceHigh); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < rock; i++ {
		add("rock")
	}
	for i := 0; i < jazz; i++ {
		add("jazz")
	}

	stops := &stopLog{}
	s.SetStations(st, func(ctx context.Context, sid int64) (station.Diff, error) {
		return station.Regenerate(ctx, st, sid)
	}, stops)
	return s, st, stops
}

func TestStationsAPICreateBuildsPlaylist(t *testing.T) {
	s, st, _ := stationsServer(t, 12, 5)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodPost, "/admin/stations",
		`{"name":"ROCK","genre":"rock","mood":"raw"}`, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var made struct {
		ID     int64
		Tracks int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	// A COUNT, not a promise: the operator finds out at once if their genre
	// and mood together select nothing.
	if made.Tracks != 12 {
		t.Errorf("tracks = %d, want 12", made.Tracks)
	}

	ids, err := st.StationTrackIDs(context.Background(), made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 12 {
		t.Errorf("the playlist holds %d tracks", len(ids))
	}
	// It arrives OFF the dial: a station is enabled deliberately, after its
	// track count has been seen.
	row, err := st.GetStation(context.Background(), made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Enabled {
		t.Error("a new station went straight on the dial")
	}
}

func TestStationsAPIGenreMustBeInVocabulary(t *testing.T) {
	s, _, _ := stationsServer(t, 12, 0)
	admin := adminCookie(t, s)

	for _, genre := range []string{"vaporwave", "Rock", "", "rock and roll"} {
		rec := as(t, s, http.MethodPost, "/admin/stations",
			`{"name":"S","genre":"`+genre+`"}`, admin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("genre %q = %d, want 400", genre, rec.Code)
			continue
		}
		var e struct{ Field string }
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e.Field != "genre" {
			t.Errorf("genre %q named field %q", genre, e.Field)
		}
	}
	if rec := as(t, s, http.MethodPost, "/admin/stations",
		`{"name":"","genre":"rock"}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("an unnamed station = %d, want 400", rec.Code)
	}
	if rec := as(t, s, http.MethodPost, "/admin/stations", "not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
}

func TestStationsAPIMoodOptionalAndValidated(t *testing.T) {
	s, _, _ := stationsServer(t, 12, 0)
	admin := adminCookie(t, s)

	// Omitted means ANY mood, which is what most stations want.
	if rec := as(t, s, http.MethodPost, "/admin/stations",
		`{"name":"ROCK","genre":"rock"}`, admin); rec.Code != http.StatusCreated {
		t.Errorf("a station with no mood = %d, want 201: %s", rec.Code, rec.Body)
	}
	rec := as(t, s, http.MethodPost, "/admin/stations",
		`{"name":"SAD","genre":"rock","mood":"sad"}`, admin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mood sad = %d, want 400", rec.Code)
	}
	var e struct{ Field string }
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Field != "mood" {
		t.Errorf("named field %q, want mood", e.Field)
	}
}

func TestStationsAPIAssignUnassignJock(t *testing.T) {
	s, st, _ := stationsServer(t, 12, 0)
	admin := adminCookie(t, s)
	ctx := context.Background()
	if err := st.UpsertJock(ctx, store.Jock{ID: "dutch_mahoney", Name: "Dutch",
		VoiceID: "kokoro:am_fenrir", SpeechStyle: "loud", Personality: "loud"}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateStation(ctx, store.Station{Name: "ROCK", Genre: "rock"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/stations/" + strconv.FormatInt(id, 10) + "/jock"

	if rec := as(t, s, http.MethodPut, path, `{"jock_id":"dutch_mahoney"}`, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("assign = %d: %s", rec.Code, rec.Body)
	}
	if row, _ := st.GetStation(ctx, id); row.JockID != "dutch_mahoney" {
		t.Errorf("jock = %q", row.JockID)
	}

	// null UNASSIGNS, and a station with no jock still plays music.
	if rec := as(t, s, http.MethodPut, path, `{"jock_id":null}`, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("unassign = %d: %s", rec.Code, rec.Body)
	}
	if row, _ := st.GetStation(ctx, id); row.JockID != "" {
		t.Errorf("jock = %q after unassigning", row.JockID)
	}

	// A jock nobody has heard of is refused by the foreign key, not stored.
	if rec := as(t, s, http.MethodPut, path, `{"jock_id":"nobody"}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown jock = %d, want 400", rec.Code)
	}
	if rec := as(t, s, http.MethodPut, "/admin/stations/9999/jock", `{"jock_id":null}`, admin); rec.Code != http.StatusNotFound {
		t.Errorf("a station that is gone = %d, want 404", rec.Code)
	}
	if rec := as(t, s, http.MethodPut, path, "not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
}

// TestStationsAPIEnableBelowMinimum409: the refusal carries the numbers, or an
// operator is left guessing how many more tracks they need.
func TestStationsAPIEnableBelowMinimum409(t *testing.T) {
	s, st, _ := stationsServer(t, 9, 0)
	admin := adminCookie(t, s)
	ctx := context.Background()
	id, err := st.CreateStation(ctx, store.Station{Name: "ROCK", Genre: "rock"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := station.Regenerate(ctx, st, id); err != nil {
		t.Fatal(err)
	}

	rec := as(t, s, http.MethodPost, "/admin/stations/"+strconv.FormatInt(id, 10)+"/enable", "", admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("= %d, want 409: %s", rec.Code, rec.Body)
	}
	var e struct {
		Tracks, Minimum int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Tracks != 9 || e.Minimum != station.MinStationTracks {
		t.Errorf("= %+v, want 9 of %d", e, station.MinStationTracks)
	}
	if row, _ := st.GetStation(ctx, id); row.Enabled {
		t.Error("the station was enabled anyway")
	}
}

// TestStationsAPIEnableWarnsBelow50: a warning, not a refusal. An operator who
// wants a station of twelve deep cuts is entitled to one.
func TestStationsAPIEnableWarnsBelow50(t *testing.T) {
	s, st, _ := stationsServer(t, 12, 0)
	admin := adminCookie(t, s)
	ctx := context.Background()
	id, err := st.CreateStation(ctx, store.Station{Name: "ROCK", Genre: "rock"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := station.Regenerate(ctx, st, id); err != nil {
		t.Fatal(err)
	}

	rec := as(t, s, http.MethodPost, "/admin/stations/"+strconv.FormatInt(id, 10)+"/enable", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Tracks  int
		Warning string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Tracks != 12 || out.Warning == "" {
		t.Errorf("= %+v, want 12 tracks and a warning", out)
	}
	if row, _ := st.GetStation(ctx, id); !row.Enabled {
		t.Error("the station is not on the dial")
	}

	// And a comfortable station says nothing.
	big, _, _ := stationsServer(t, 60, 0)
	bigAdmin := adminCookie(t, big)
	if rec := as(t, big, http.MethodPost, "/admin/stations",
		`{"name":"ROCK","genre":"rock"}`, bigAdmin); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	rec = as(t, big, http.MethodPost, "/admin/stations/1/enable", "", bigAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var quiet struct{ Warning string }
	if err := json.Unmarshal(rec.Body.Bytes(), &quiet); err != nil {
		t.Fatal(err)
	}
	if quiet.Warning != "" {
		t.Errorf("a sixty-track station warned: %q", quiet.Warning)
	}
}

// TestStationsAPIDeleteStopsRuntime: removing the rows under a running pipeline
// leaves it drawing from a playlist that no longer exists, which a listener
// hears as the stream stopping.
func TestStationsAPIDeleteStopsRuntime(t *testing.T) {
	s, st, stops := stationsServer(t, 12, 0)
	admin := adminCookie(t, s)
	ctx := context.Background()
	id, err := st.CreateStation(ctx, store.Station{Name: "ROCK", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := station.Regenerate(ctx, st, id); err != nil {
		t.Fatal(err)
	}

	if rec := as(t, s, http.MethodDelete, "/admin/stations/"+strconv.FormatInt(id, 10), "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if !reflect.DeepEqual(stops.stopped, []int64{id}) {
		t.Errorf("stopped %v, want the station taken off air first", stops.stopped)
	}
	if _, err := st.GetStation(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the station is still there: %v", err)
	}
	var rows int
	if err := st.DB().QueryRow(`SELECT count(*) FROM station_tracks`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d playlist rows survived the station", rows)
	}

	// Disabling also takes it off air.
	other, err := st.CreateStation(ctx, store.Station{Name: "J", Genre: "jazz", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec := as(t, s, http.MethodPost, "/admin/stations/"+strconv.FormatInt(other, 10)+"/disable", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("disable = %d", rec.Code)
	}
	if row, _ := st.GetStation(ctx, other); row.Enabled {
		t.Error("the station is still on the dial")
	}
	if len(stops.stopped) != 2 {
		t.Errorf("stopped %v, want the disabled station taken off air too", stops.stopped)
	}

	if rec := as(t, s, http.MethodDelete, "/admin/stations/9999", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("deleting a station that is gone = %d, want 404", rec.Code)
	}
	if rec := as(t, s, http.MethodPost, "/admin/stations/9999/disable", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("disabling a station that is gone = %d, want 404", rec.Code)
	}
	if rec := as(t, s, http.MethodPost, "/admin/stations/9999/enable", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("enabling a station that is gone = %d, want 404", rec.Code)
	}
}

// TestStationsAPIUpdateGenreRegenerates: changing the genre changes what the
// station IS. Leaving the old playlist would keep airing music the operator
// has just said they do not want.
func TestStationsAPIUpdateGenreRegenerates(t *testing.T) {
	s, st, _ := stationsServer(t, 12, 5)
	admin := adminCookie(t, s)
	ctx := context.Background()
	id, err := st.CreateStation(ctx, store.Station{Name: "ROCK", Genre: "rock"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := station.Regenerate(ctx, st, id); err != nil {
		t.Fatal(err)
	}

	rec := as(t, s, http.MethodPut, "/admin/stations/"+strconv.FormatInt(id, 10),
		`{"name":"JAZZ","genre":"jazz"}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var diff station.Diff
	if err := json.Unmarshal(rec.Body.Bytes(), &diff); err != nil {
		t.Fatal(err)
	}
	if diff.Added != 5 || diff.Removed != 12 {
		t.Errorf("Diff = %+v, want 5 added and 12 removed", diff)
	}
	ids, _ := st.StationTrackIDs(ctx, id)
	if len(ids) != 5 {
		t.Errorf("the playlist holds %d tracks, want the 5 jazz", len(ids))
	}
	row, _ := st.GetStation(ctx, id)
	if row.Name != "JAZZ" || row.Genre != "jazz" {
		t.Errorf("station = %+v", row)
	}

	if rec := as(t, s, http.MethodPut, "/admin/stations/9999", `{"name":"X","genre":"rock"}`, admin); rec.Code != http.StatusNotFound {
		t.Errorf("a station that is gone = %d, want 404", rec.Code)
	}
	if rec := as(t, s, http.MethodPut, "/admin/stations/"+strconv.FormatInt(id, 10),
		`{"name":"X","genre":"vaporwave"}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("an update to a genre outside the vocabulary = %d, want 400", rec.Code)
	}
	if rec := as(t, s, http.MethodPut, "/admin/stations/"+strconv.FormatInt(id, 10), "not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
}

// TestStationsAPIVocab: the console offers choices rather than a text box a
// typo can defeat.
func TestStationsAPIVocab(t *testing.T) {
	s, _, _ := stationsServer(t, 1, 0)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodGet, "/admin/vocab", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	var out struct {
		Genres []string
		Moods  []string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Genres, enrich.StationTags) {
		t.Errorf("genres = %v", out.Genres)
	}
	if !reflect.DeepEqual(out.Moods, enrich.Moods) {
		t.Errorf("moods = %v", out.Moods)
	}
}

func TestStationsAPIList(t *testing.T) {
	s, st, _ := stationsServer(t, 12, 5)
	admin := adminCookie(t, s)
	ctx := context.Background()
	for _, g := range []string{"rock", "jazz"} {
		id, err := st.CreateStation(ctx, store.Station{Name: g, Genre: g})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := station.Regenerate(ctx, st, id); err != nil {
			t.Fatal(err)
		}
	}

	rec := as(t, s, http.MethodGet, "/admin/stations", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	var out []stationView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("%d stations", len(out))
	}
	if out[0].Tracks != 12 || out[1].Tracks != 5 {
		t.Errorf("track counts = %d and %d, want 12 and 5", out[0].Tracks, out[1].Tracks)
	}
	// The console needs to know which stations cannot run BEFORE trying.
	if out[0].Warning == "" || out[1].Warning == "" {
		t.Errorf("thin stations carry no warning: %+v", out)
	}
	if out[1].Warning == out[0].Warning {
		t.Errorf("a five-track station and a twelve-track one warn identically: %q", out[0].Warning)
	}
}

func TestStationsAPIRoleMatrix(t *testing.T) {
	s, _, _ := stationsServer(t, 12, 0)
	listener := listenerCookie(t, s)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/stations"},
		{http.MethodPost, "/admin/stations"},
		{http.MethodPut, "/admin/stations/1"},
		{http.MethodDelete, "/admin/stations/1"},
		{http.MethodPost, "/admin/stations/1/enable"},
		{http.MethodPost, "/admin/stations/1/disable"},
		{http.MethodPut, "/admin/stations/1/jock"},
		{http.MethodGet, "/admin/vocab"},
	} {
		if rec := as(t, s, tc.method, tc.path, "{}", listener); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a listener = %d, want 403", tc.method, tc.path, rec.Code)
		}
		if rec := as(t, s, tc.method, tc.path, "{}", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymously = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestStationsAPIBadTargets(t *testing.T) {
	s, _, _ := stationsServer(t, 12, 0)
	admin := adminCookie(t, s)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodDelete, "/admin/stations/abc", http.StatusNotFound},
		{http.MethodPost, "/admin/stations/1/a/b", http.StatusNotFound},
		{http.MethodPost, "/admin/stations/1/nonsense", http.StatusMethodNotAllowed},
		{http.MethodGet, "/admin/stations/1", http.StatusMethodNotAllowed},
		{http.MethodPatch, "/admin/stations", http.StatusMethodNotAllowed},
	} {
		if rec := as(t, s, tc.method, tc.path, "", admin); rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}

	// Without a library there is nothing to manage.
	bare, _, _ := authServer(t)
	bareAdmin := adminCookie(t, bare)
	if rec := as(t, bare, http.MethodGet, "/admin/stations", "", bareAdmin); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("= %d, want 503", rec.Code)
	}
}

// brokenStations fails every call, which is what a store that has gone away
// looks like from up here.
type brokenStations struct {
	err error
	// getOK and listOK let a test walk further into a handler before the
	// failure, so each error branch can be reached on its own.
	getOK, tracksOK, listOK bool
}

func (b *brokenStations) ListStations(context.Context) ([]store.Station, error) {
	if b.listOK {
		return []store.Station{{ID: 1, Name: "S", Genre: "rock"}}, nil
	}
	return nil, b.err
}
func (b *brokenStations) GetStation(context.Context, int64) (store.Station, error) {
	if b.getOK {
		return store.Station{ID: 1, Name: "S", Genre: "rock"}, nil
	}
	return store.Station{}, b.err
}
func (b *brokenStations) CreateStation(context.Context, store.Station) (int64, error) {
	return 0, b.err
}
func (b *brokenStations) UpdateStation(context.Context, store.Station) error { return b.err }
func (b *brokenStations) DeleteStation(context.Context, int64) error         { return b.err }
func (b *brokenStations) StationTrackIDs(context.Context, int64) ([]int64, error) {
	if b.tracksOK {
		// Comfortably over the threshold, so a handler reaches the write it
		// is being tested for rather than stopping at the refusal.
		out := make([]int64, 60)
		for i := range out {
			out[i] = int64(i + 1)
		}
		return out, nil
	}
	return nil, b.err
}

// TestStationsAPISurfacesFailures: a swallowed failure looks exactly like an
// empty station list or a station that was created, and an operator acts on
// both.
func TestStationsAPISurfacesFailures(t *testing.T) {
	boom := errors.New("the database went away")
	okDiff := func(context.Context, int64) (station.Diff, error) { return station.Diff{}, nil }
	badDiff := func(context.Context, int64) (station.Diff, error) { return station.Diff{}, boom }

	newBroken := func(t *testing.T, b *brokenStations, regen Regenerator, rt Runtimes) (*Server, *http.Cookie) {
		t.Helper()
		s, _, _ := authServer(t)
		admin := adminCookie(t, s)
		s.SetStations(b, regen, rt)
		return s, admin
	}

	t.Run("everything fails", func(t *testing.T) {
		s, admin := newBroken(t, &brokenStations{err: boom}, okDiff, nil)
		for _, tc := range []struct{ method, path, body string }{
			{http.MethodGet, "/admin/stations", ""},
			{http.MethodPost, "/admin/stations", `{"name":"S","genre":"rock"}`},
			{http.MethodPut, "/admin/stations/1", `{"name":"S","genre":"rock"}`},
			{http.MethodDelete, "/admin/stations/1", ""},
			{http.MethodPost, "/admin/stations/1/enable", ""},
			{http.MethodPost, "/admin/stations/1/disable", ""},
			{http.MethodPut, "/admin/stations/1/jock", `{"jock_id":null}`},
		} {
			// The jock route reports a bad request, because a store that
			// refuses the update is usually a foreign key refusing the jock.
			want := http.StatusInternalServerError
			if tc.path == "/admin/stations/1/jock" {
				want = http.StatusInternalServerError
			}
			rec := as(t, s, tc.method, tc.path, tc.body, admin)
			if rec.Code < 400 {
				t.Errorf("%s %s with a failing store = %d, want a failure (%d)",
					tc.method, tc.path, rec.Code, want)
			}
		}
	})

	t.Run("the count fails", func(t *testing.T) {
		// Listing works, counting its tracks does not.
		s, admin := newBroken(t, &brokenStations{err: boom, listOK: true, getOK: true}, okDiff, nil)
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/admin/stations"},
			{http.MethodPost, "/admin/stations/1/enable"},
		} {
			if rec := as(t, s, tc.method, tc.path, "", admin); rec.Code != http.StatusInternalServerError {
				t.Errorf("%s %s = %d, want 500", tc.method, tc.path, rec.Code)
			}
		}
	})

	t.Run("the update fails", func(t *testing.T) {
		s, admin := newBroken(t, &brokenStations{err: boom, getOK: true, tracksOK: true}, okDiff, nil)
		for _, tc := range []struct{ method, path, body string }{
			{http.MethodPut, "/admin/stations/1", `{"name":"S","genre":"rock"}`},
			{http.MethodPost, "/admin/stations/1/enable", ""},
			{http.MethodPost, "/admin/stations/1/disable", ""},
		} {
			if rec := as(t, s, tc.method, tc.path, tc.body, admin); rec.Code != http.StatusInternalServerError {
				t.Errorf("%s %s = %d, want 500", tc.method, tc.path, rec.Code)
			}
		}
	})

	t.Run("the regeneration fails", func(t *testing.T) {
		// The station is created, the playlist cannot be built: the operator
		// must be told, not handed a station that will never fill.
		s, admin := newBroken(t, &brokenStations{getOK: true, tracksOK: true}, badDiff, nil)
		if rec := as(t, s, http.MethodPost, "/admin/stations",
			`{"name":"S","genre":"rock"}`, admin); rec.Code != http.StatusInternalServerError {
			t.Errorf("create = %d, want 500", rec.Code)
		}
		if rec := as(t, s, http.MethodPut, "/admin/stations/1",
			`{"name":"S","genre":"rock"}`, admin); rec.Code != http.StatusInternalServerError {
			t.Errorf("update = %d, want 500", rec.Code)
		}
	})

	t.Run("the station will not stop", func(t *testing.T) {
		// A station that cannot be taken off air must NOT have its rows
		// removed underneath it.
		s, admin := newBroken(t, &brokenStations{getOK: true, tracksOK: true}, okDiff,
			&stopLog{err: errors.New("ffmpeg would not die")})
		for _, tc := range []struct{ method, path string }{
			{http.MethodDelete, "/admin/stations/1"},
			{http.MethodPost, "/admin/stations/1/disable"},
		} {
			if rec := as(t, s, tc.method, tc.path, "", admin); rec.Code != http.StatusInternalServerError {
				t.Errorf("%s %s = %d, want 500", tc.method, tc.path, rec.Code)
			}
		}
	})

	t.Run("client hangs up", func(t *testing.T) {
		s, _, _ := stationsServer(t, 1, 0)
		s.listStations(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/admin/stations", nil))
		s.serveVocab(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/admin/vocab", nil))
	})
}
