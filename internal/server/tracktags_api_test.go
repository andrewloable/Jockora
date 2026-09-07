// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// TAGS ARE A PROPERTY OF THE TRACK, NOT OF THE STATION IT WAS EDITED FROM. The
// route says so: a station-scoped path would promise an edit that stops at the
// station boundary, and the same track on another station would disagree.

func tagPath(track int64) string {
	return "/admin/tracks/" + strconv.FormatInt(track, 10) + "/tags"
}

func TestTrackTagsAPIEditAndRevert(t *testing.T) {
	s, st, _ := playlistServer(t, 3)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodPut, tagPath(1), `{"genres":["synthwave"],"moods":["nocturnal"]}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Genres     []string `json:"genres"`
		Moods      []string `json:"moods"`
		Overridden bool     `json:"overridden"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Genres) != 1 || got.Genres[0] != "synthwave" || !got.Overridden {
		t.Errorf("edit answered %+v, want the new tags back", got)
	}
	// The answer is the EFFECTIVE state, not an echo: the console redraws the
	// row from it rather than re-fetching the page.
	if stored, _ := st.TrackTags(t.Context(), 1); stored.Moods[0] != "nocturnal" {
		t.Errorf("stored = %+v", stored)
	}

	rec = as(t, s, http.MethodDelete, tagPath(1), "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("revert = %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Overridden || got.Genres[0] != "rock" {
		t.Errorf("revert answered %+v, want the enrichment's own tags", got)
	}
	// Reverting twice is 404: there was nothing to put back.
	if rec := as(t, s, http.MethodDelete, tagPath(1), "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("second revert = %d, want 404", rec.Code)
	}
}

func TestTrackTagsAPIRefusesWhatNoDossierCouldHold(t *testing.T) {
	// The vocabularies are closed on purpose: a typo would make a track
	// unreachable from every station rather than showing up as an error.
	s, _, _ := playlistServer(t, 3)
	admin := adminCookie(t, s)

	for _, body := range []string{
		`{"genres":["rocc"],"moods":[]}`,
		`{"genres":[],"moods":["moody"]}`,
	} {
		rec := as(t, s, http.MethodPut, tagPath(1), body, admin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, rec.Code)
		}
	}

	// More than a dossier itself may carry. Six tags is not curation, it is a
	// track that would surface on every station in the dial.
	many := `{"genres":["rock","pop","jazz","folk","metal","punk"],"moods":[]}`
	if rec := as(t, s, http.MethodPut, tagPath(1), many, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("six genres = %d, want 400", rec.Code)
	}

	// EMPTY IS ALLOWED. "This is not anything" is a real answer and puts the
	// track in the catch-all, where a listener can still find it.
	if rec := as(t, s, http.MethodPut, tagPath(1), `{"genres":[],"moods":[]}`, admin); rec.Code != http.StatusOK {
		t.Errorf("clearing every tag = %d, want 200", rec.Code)
	}
}

func TestTrackTagsAPIRejectsBadRequests(t *testing.T) {
	s, _, _ := playlistServer(t, 3)
	admin := adminCookie(t, s)

	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPut, tagPath(1), "not json", http.StatusBadRequest},
		{http.MethodPut, "/admin/tracks/nope/tags", `{"genres":[]}`, http.StatusNotFound},
		{http.MethodPut, "/admin/tracks/1", `{"genres":[]}`, http.StatusNotFound},
		{http.MethodPut, "/admin/tracks/1/nonsense", `{"genres":[]}`, http.StatusNotFound},
		{http.MethodPost, tagPath(1), `{"genres":[]}`, http.StatusMethodNotAllowed},
		// A track the library does not have. The foreign key refuses it, and
		// so does reading it back.
		{http.MethodPut, tagPath(4040), `{"genres":["rock"],"moods":[]}`, http.StatusBadRequest},
		{http.MethodDelete, tagPath(4040), "", http.StatusNotFound},
	} {
		if rec := as(t, s, tc.method, tc.path, tc.body, admin); rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body)
		}
	}
}

func TestTrackTagsAPINeedsAnAdminAndAStore(t *testing.T) {
	s, _, _ := playlistServer(t, 3)
	// A listener cannot re-file the library.
	if rec := as(t, s, http.MethodPut, tagPath(1), `{"genres":[]}`, listenerCookie(t, s)); rec.Code != http.StatusForbidden {
		t.Errorf("as a listener = %d, want 403", rec.Code)
	}
	if rec := as(t, s, http.MethodPut, tagPath(1), `{"genres":[]}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("signed out = %d, want 401", rec.Code)
	}

	bare, _, _ := authServer(t)
	rec := as(t, bare, http.MethodPut, tagPath(1), `{"genres":[]}`, adminCookie(t, bare))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("with no library = %d, want 503", rec.Code)
	}
}

// A failure on the way in must not be reported as a saved edit.
func TestTrackTagsAPISurfacesFailures(t *testing.T) {
	s, st, _ := playlistServer(t, 3)
	admin := adminCookie(t, s)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	rec := as(t, s, http.MethodPut, tagPath(1), `{"genres":["rock"],"moods":[]}`, admin)
	if rec.Code == http.StatusOK {
		t.Error("an edit against a closed database answered 200")
	}
	if !strings.Contains(rec.Body.String(), "closed") {
		t.Logf("body = %s", rec.Body.String())
	}
}

// TestTrackTagsAPISurfacesStoreFailures: an edit that did not land must never
// answer like one that did, and neither must an edit that landed and could not
// be read back -- the console redraws the row from that reply.
func TestTrackTagsAPISurfacesStoreFailures(t *testing.T) {
	boom := errors.New("the database went away")
	s, _, _ := playlistServer(t, 3)
	admin := adminCookie(t, s)

	s.SetPlaylists(brokenPlaylists{err: boom})
	if rec := as(t, s, http.MethodDelete, tagPath(1), "", admin); rec.Code != http.StatusInternalServerError {
		t.Errorf("a revert that failed = %d, want 500", rec.Code)
	}

	// The write lands, reading it back does not.
	s.SetPlaylists(brokenPlaylists{err: boom, tagWriteOK: true})
	for _, tc := range []struct{ method, body string }{
		{http.MethodPut, `{"genres":["rock"],"moods":[]}`},
		{http.MethodDelete, ""},
	} {
		if rec := as(t, s, tc.method, tagPath(1), tc.body, admin); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s with an unreadable row = %d, want 500", tc.method, rec.Code)
		}
	}
}

// The mood cap is the same as the genre cap and needs its own case: a bug in
// one of the two limbs is invisible while the other is checked first.
func TestTrackTagsAPICapsMoodsToo(t *testing.T) {
	s, _, _ := playlistServer(t, 3)
	body := `{"genres":[],"moods":["raw","calm","tense","playful","lonely","hypnotic"]}`
	if rec := as(t, s, http.MethodPut, tagPath(1), body, adminCookie(t, s)); rec.Code != http.StatusBadRequest {
		t.Errorf("six moods = %d, want 400", rec.Code)
	}
}

// TestMoodTempoReachesTheConsole: the tempo is measured, stored and now on the
// row -- and it is only useful if it survives the wire.
func TestMoodTempoReachesTheConsole(t *testing.T) {
	s, st, id := playlistServer(t, 2)
	if _, err := st.DB().Exec(`UPDATE tracks SET bpm = 128.5 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	rec := as(t, s, http.MethodGet, tracksPath(id), "", adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Tracks []struct {
			BPM float64 `json:"bpm"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Tracks[0].BPM != 128.5 || body.Tracks[1].BPM != 0 {
		t.Errorf("tempos = %v, want the measured one and zero for unmeasured", body.Tracks)
	}
}
