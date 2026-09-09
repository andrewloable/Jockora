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

// playlistServer is an admin server over one station with n tracks on it.
func playlistServer(t *testing.T, n int) (*Server, *store.Store, int64) {
	t.Helper()
	s, st, _ := authServer(t)
	ctx := context.Background()

	ids := make([]int64, 0, n)
	for i := 1; i <= n; i++ {
		if _, err := st.DB().Exec(
			`INSERT INTO tracks (id, path, artist, title, playable) VALUES (?, ?, ?, ?, 1)`,
			i, fmt.Sprintf("/m/%d.mp3", i), fmt.Sprintf("Artist %d", i),
			fmt.Sprintf("Title %d", i)); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{
			"station_tags": []string{"rock"}, "mood": []string{"raw"}})
		if _, err := st.DB().Exec(
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
			i, string(raw), enrich.ConfidenceHigh); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, int64(i))
	}
	id, err := st.CreateStation(ctx, store.Station{Name: "ROCK", Genre: "rock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceStationTracks(ctx, id, ids); err != nil {
		t.Fatal(err)
	}

	s.SetPlaylists(st)
	s.SetEnrichmentPort(enrich.NewPort(st))
	s.SetStations(st, func(ctx context.Context, sid int64) (station.Diff, error) {
		return station.Regenerate(ctx, st, sid)
	}, nil)
	return s, st, id
}

func tracksPath(id int64) string {
	return "/admin/stations/" + strconv.FormatInt(id, 10) + "/tracks"
}

// TestPlaylistAPIListPaged: a station can hold thousands of tracks, so the
// console needs a page and a total -- "showing 50 of 1,200" rather than
// guessing when to stop.
func TestPlaylistAPIListPaged(t *testing.T) {
	s, _, id := playlistServer(t, 120)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodGet, tracksPath(id)+"?limit=50&offset=100", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var page struct {
		Tracks []store.StationTrackDetail
		Total  int
		Limit  int
		Offset int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 120 || len(page.Tracks) != 20 {
		t.Fatalf("total %d with %d rows, want 120 and 20", page.Total, len(page.Tracks))
	}
	if page.Limit != 50 || page.Offset != 100 {
		t.Errorf("limit/offset echoed as %d/%d", page.Limit, page.Offset)
	}
	// Enough about each track for an operator to RECOGNISE it. A list of ids
	// is not a playlist editor.
	first := page.Tracks[0]
	if first.TrackID != 101 || first.Artist != "Artist 101" || first.Title != "Title 101" {
		t.Errorf("first row = %+v", first)
	}

	// A nonsense page is answered, not refused: a bad query should not look
	// like a broken server.
	if rec := as(t, s, http.MethodGet, tracksPath(id)+"?limit=abc&offset=-5", "", admin); rec.Code != http.StatusOK {
		t.Errorf("a nonsense page = %d, want 200", rec.Code)
	}
}

// TestPlaylistAPISorted: Jockora-22s. The playlist is paged here, so the sort
// belongs here too -- a console ordering the fifty rows it was handed would
// answer a question about the page and look like it answered one about the
// library.
func TestPlaylistAPISorted(t *testing.T) {
	s, _, id := playlistServer(t, 12)
	admin := adminCookie(t, s)

	ids := func(query string) []int64 {
		t.Helper()
		rec := as(t, s, http.MethodGet, tracksPath(id)+query, "", admin)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", query, rec.Code, rec.Body)
		}
		var page struct{ Tracks []store.StationTrackDetail }
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		out := make([]int64, len(page.Tracks))
		for i, tr := range page.Tracks {
			out[i] = tr.TrackID
		}
		return out
	}

	// "Artist 10" sorts before "Artist 2" as text, which is exactly what makes
	// this distinguishable from the id order the playlist has by default.
	if got := ids("?sort=artist")[:3]; !reflect.DeepEqual(got, []int64{1, 10, 11}) {
		t.Errorf("sorted by artist = %v, want it to start 1, 10, 11", got)
	}
	if got := ids("?sort=artist&dir=desc")[:3]; !reflect.DeepEqual(got, []int64{9, 8, 7}) {
		t.Errorf("reversed = %v, want it to start 9, 8, 7", got)
	}
	// ANYTHING ELSE IS THE DEFAULT ORDER, not a 400: the column name comes off
	// a query string, and a console asking for one this server does not sort by
	// should still get its playlist.
	for _, q := range []string{"", "?sort=nonsense", "?dir=desc"} {
		if got := ids(q)[:3]; !reflect.DeepEqual(got, []int64{1, 2, 3}) {
			t.Errorf("%q = %v, want the default order", q, got)
		}
	}
}

// TestPlaylistAPILimitCapped: five thousand rows is not an answer a browser can
// render, and refusing would make a bad query look like a broken server.
func TestPlaylistAPILimitCapped(t *testing.T) {
	s, _, id := playlistServer(t, 120)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodGet, tracksPath(id)+"?limit=5000", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	var page struct{ Limit, Total int }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Limit != MaxPlaylistPage {
		t.Errorf("limit = %d, want it capped at %d", page.Limit, MaxPlaylistPage)
	}
	if page.Total != 120 {
		t.Errorf("total = %d", page.Total)
	}
}

func TestPlaylistAPIPinUnpin(t *testing.T) {
	s, st, id := playlistServer(t, 12)
	admin := adminCookie(t, s)
	ctx := context.Background()

	if rec := as(t, s, http.MethodPost, tracksPath(id)+"/3/pin", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("pin = %d: %s", rec.Code, rec.Body)
	}
	rows, err := st.ListStationTracks(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var pinned bool
	for _, row := range rows {
		if row.TrackID == 3 {
			pinned = row.Pinned
		}
	}
	if !pinned {
		t.Error("the track was not pinned")
	}

	if rec := as(t, s, http.MethodPost, tracksPath(id)+"/3/unpin", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("unpin = %d", rec.Code)
	}
	rows, _ = st.ListStationTracks(ctx, id)
	for _, row := range rows {
		if row.TrackID == 3 && row.Pinned {
			t.Error("the pin was not removed")
		}
	}
}

func TestPlaylistAPIExcludeUnexclude(t *testing.T) {
	s, st, id := playlistServer(t, 12)
	admin := adminCookie(t, s)
	ctx := context.Background()

	if rec := as(t, s, http.MethodPost, tracksPath(id)+"/4/exclude", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("exclude = %d: %s", rec.Code, rec.Body)
	}
	ids, err := st.StationTrackIDs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, tid := range ids {
		if tid == 4 {
			t.Error("the excluded track is still playable")
		}
	}
	// Still LISTED, so the operator can find it to put it back.
	rec := as(t, s, http.MethodGet, tracksPath(id), "", admin)
	var page struct{ Tracks []store.StationTrackDetail }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var listed bool
	for _, row := range page.Tracks {
		if row.TrackID == 4 {
			listed = row.Excluded
		}
	}
	if !listed {
		t.Error("the excluded track vanished from the list")
	}

	if rec := as(t, s, http.MethodPost, tracksPath(id)+"/4/unexclude", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("unexclude = %d", rec.Code)
	}
	if ids, _ := st.StationTrackIDs(ctx, id); len(ids) != 12 {
		t.Errorf("%d playable tracks after un-excluding, want 12", len(ids))
	}
}

func TestPlaylistAPIRegenerate(t *testing.T) {
	s, st, id := playlistServer(t, 12)
	admin := adminCookie(t, s)
	ctx := context.Background()

	// A pin on a track the filter will not select, and an exclusion on one it
	// will: the operator's decisions have to survive.
	if _, err := st.DB().Exec(
		`INSERT INTO tracks (id, path, artist, title, playable) VALUES (99, '/m/99.mp3', 'Jazz', 'Outsider', 1)`); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"station_tags": []string{"jazz"}, "mood": []string{"warm"}})
	if _, err := st.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence) VALUES (99, ?, ?)`,
		string(raw), enrich.ConfidenceHigh); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceStationTracks(ctx, id, append(idsTo(12), 99)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPinned(ctx, id, 99, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetExcluded(ctx, id, 5, true); err != nil {
		t.Fatal(err)
	}

	rec := as(t, s, http.MethodPost, "/admin/stations/"+strconv.FormatInt(id, 10)+"/regenerate", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var diff station.Diff
	if err := json.Unmarshal(rec.Body.Bytes(), &diff); err != nil {
		t.Fatal(err)
	}
	if diff.Kept != 13 || diff.Added != 0 || diff.Removed != 0 {
		t.Errorf("Diff = %+v, want everything kept", diff)
	}

	rows, _ := st.ListStationTracks(ctx, id)
	var pinned, excluded bool
	for _, row := range rows {
		if row.TrackID == 99 {
			pinned = row.Pinned
		}
		if row.TrackID == 5 {
			excluded = row.Excluded
		}
	}
	if !pinned {
		t.Error("the pinned outsider was dropped")
	}
	if !excluded {
		t.Error("the exclusion was cleared")
	}
	ids, _ := st.StationTrackIDs(ctx, id)
	for _, tid := range ids {
		if tid == 5 {
			t.Error("the excluded track is playable again")
		}
	}
}

func TestPlaylistAPIPinUnknownTrack404(t *testing.T) {
	s, _, id := playlistServer(t, 12)
	admin := adminCookie(t, s)

	for _, tc := range []struct {
		path string
		want int
	}{
		{tracksPath(id) + "/9999/pin", http.StatusNotFound},
		{tracksPath(id) + "/9999/exclude", http.StatusNotFound},
		{tracksPath(id) + "/abc/pin", http.StatusNotFound},
		{tracksPath(id) + "/3/nonsense", http.StatusNotFound},
	} {
		if rec := as(t, s, http.MethodPost, tc.path, "", admin); rec.Code != tc.want {
			t.Errorf("POST %s = %d, want %d", tc.path, rec.Code, tc.want)
		}
	}
}

// TestPlaylistAPIPinMissingTrack409: a pin means "keep this one whatever the
// filter says". Keeping a file the library has lost puts a hole in the station
// that only shows up at play time.
func TestPlaylistAPIPinMissingTrack409(t *testing.T) {
	s, st, id := playlistServer(t, 12)
	admin := adminCookie(t, s)

	if _, err := st.DB().Exec(`UPDATE tracks SET missing_at = 1 WHERE id = 6`); err != nil {
		t.Fatal(err)
	}
	if rec := as(t, s, http.MethodPost, tracksPath(id)+"/6/pin", "", admin); rec.Code != http.StatusConflict {
		t.Errorf("pinning a missing track = %d, want 409", rec.Code)
	}
	// Excluding one is fine: a track that is gone can still be banned so it
	// does not come back when the drive does.
	if rec := as(t, s, http.MethodPost, tracksPath(id)+"/6/exclude", "", admin); rec.Code != http.StatusNoContent {
		t.Errorf("excluding a missing track = %d, want 204", rec.Code)
	}
	// And unpinning one is fine too: it removes a flag, it does not add one.
	if rec := as(t, s, http.MethodPost, tracksPath(id)+"/6/unpin", "", admin); rec.Code != http.StatusNoContent {
		t.Errorf("unpinning a missing track = %d, want 204", rec.Code)
	}
}

func TestPlaylistAPIRoleMatrix(t *testing.T) {
	s, _, id := playlistServer(t, 12)
	listener := listenerCookie(t, s)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, tracksPath(id)},
		{http.MethodPost, tracksPath(id) + "/1/pin"},
		{http.MethodPost, tracksPath(id) + "/1/exclude"},
		{http.MethodPost, "/admin/stations/" + strconv.FormatInt(id, 10) + "/regenerate"},
	} {
		if rec := as(t, s, tc.method, tc.path, "", listener); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a listener = %d, want 403", tc.method, tc.path, rec.Code)
		}
		if rec := as(t, s, tc.method, tc.path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymously = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

// brokenPlaylists fails every call.
type brokenPlaylists struct {
	err        error
	pageOK     bool
	tagWriteOK bool
}

func (b brokenPlaylists) StationTrackPage(context.Context, int64, int, int, string, bool) (
	[]store.StationTrackDetail, int, error) {
	if b.pageOK {
		return []store.StationTrackDetail{{TrackID: 1}}, 1, nil
	}
	return nil, 0, b.err
}
func (b brokenPlaylists) SetPinned(context.Context, int64, int64, bool) error   { return b.err }
func (b brokenPlaylists) SetExcluded(context.Context, int64, int64, bool) error { return b.err }
func (b brokenPlaylists) SetTrackTags(context.Context, int64, []string, []string) error {
	// tagWriteOK separates "the write failed" from "the write landed and
	// reading it back failed", which are different answers to the operator.
	if b.tagWriteOK {
		return nil
	}
	return b.err
}
func (b brokenPlaylists) ClearTrackTags(context.Context, int64) error {
	if b.tagWriteOK {
		return nil
	}
	return b.err
}
func (b brokenPlaylists) TrackTags(context.Context, int64) (store.TrackTags, error) {
	return store.TrackTags{}, b.err
}

func TestPlaylistAPISurfacesFailures(t *testing.T) {
	boom := errors.New("the database went away")
	s, _, id := playlistServer(t, 12)
	admin := adminCookie(t, s)

	s.SetPlaylists(brokenPlaylists{err: boom})
	for _, path := range []string{tracksPath(id), tracksPath(id) + "/1/pin"} {
		method := http.MethodGet
		if path != tracksPath(id) {
			method = http.MethodPost
		}
		if rec := as(t, s, method, path, "", admin); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s = %d, want 500", method, path, rec.Code)
		}
	}
	// The page works, the write does not.
	s.SetPlaylists(brokenPlaylists{err: boom, pageOK: true})
	if rec := as(t, s, http.MethodPost, tracksPath(id)+"/1/exclude", "", admin); rec.Code != http.StatusInternalServerError {
		t.Errorf("= %d, want 500", rec.Code)
	}

	// A regeneration that fails, with the station store still there so the
	// request reaches it.
	failing, st, failID := playlistServer(t, 2)
	failing.SetStations(st, func(context.Context, int64) (station.Diff, error) {
		return station.Diff{}, boom
	}, nil)
	failAdmin := adminCookie(t, failing)
	if rec := as(t, failing, http.MethodPost,
		"/admin/stations/"+strconv.FormatInt(failID, 10)+"/regenerate", "", failAdmin); rec.Code != http.StatusInternalServerError {
		t.Errorf("a failed regeneration = %d, want 500", rec.Code)
	}
	// A station store but no playlist editing: the route exists and says the
	// capability does not.
	noEdit, st3, id3 := playlistServer(t, 2)
	noEdit.SetPlaylists(nil)
	noEditAdmin := adminCookie(t, noEdit)
	_ = st3
	if rec := as(t, noEdit, http.MethodGet, tracksPath(id3), "", noEditAdmin); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("= %d, want 503", rec.Code)
	}
	// And a path this server does not have is a 404 whatever is behind it:
	// answering 503 would say a route exists when it does not.
	if rec := as(t, noEdit, http.MethodGet,
		"/admin/stations/"+strconv.FormatInt(id3, 10)+"/nonsense/x/y", "", noEditAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown path = %d, want 404", rec.Code)
	}
	// Playlists wired, stations wired, but nothing to regenerate FROM.
	withPlaylists, st2, id2 := playlistServer(t, 2)
	withPlaylists.SetStations(st2, nil, nil)
	if rec := as(t, withPlaylists, http.MethodPost,
		"/admin/stations/"+strconv.FormatInt(id2, 10)+"/regenerate", "",
		adminCookie(t, withPlaylists)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("regenerate with no library = %d, want 503", rec.Code)
	}

	live, _, liveID := playlistServer(t, 2)
	live.listPlaylist(&failingWriter{}, httptest.NewRequest(http.MethodGet, tracksPath(liveID), nil), liveID)
}

func TestPlaylistAPIBadTargets(t *testing.T) {
	s, _, id := playlistServer(t, 12)
	admin := adminCookie(t, s)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodDelete, tracksPath(id), http.StatusMethodNotAllowed},
		{http.MethodGet, tracksPath(id) + "/1/pin", http.StatusMethodNotAllowed},
		{http.MethodPost, "/admin/stations/abc/tracks/1/pin", http.StatusNotFound},
		{http.MethodPost, "/admin/stations/0/tracks/1/pin", http.StatusNotFound},
		{http.MethodGet, "/admin/stations/" + strconv.FormatInt(id, 10) + "/regenerate", http.StatusMethodNotAllowed},
	} {
		if rec := as(t, s, tc.method, tc.path, "", admin); rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

func idsTo(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}
