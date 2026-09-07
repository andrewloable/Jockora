// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

// voiceList is the sidecar's answer, or its refusal.
type voiceList struct {
	names []string
	err   error
}

func (v voiceList) Voices(context.Context) ([]string, error) { return v.names, v.err }

// personaLog records the edits that reached the running stations.
type personaLog struct{ seen []store.Jock }

func (l *personaLog) PersonaChanged(j store.Jock) { l.seen = append(l.seen, j) }

const sampleCard = `{"id":"dutch_mahoney","name":"Dutch","voice_id":"am_fenrir",` +
	`"speech_style":"LOUD. Short bursts.","personality":"Shouts about rock.",` +
	`"good_for_genres":["rock"],"good_for_moods":["raw"],"forbidden":[]}`

func jocksServer(t *testing.T) (*Server, *store.Store, *personaLog) {
	t.Helper()
	s, st, _ := authServer(t)
	log := &personaLog{}
	s.SetJocks(st, voiceList{names: []string{"am_fenrir", "af_heart"}}, log)
	return s, st, log
}

func TestJocksAPICRUD(t *testing.T) {
	s, st, _ := jocksServer(t)
	admin := adminCookie(t, s)
	ctx := context.Background()

	if rec := as(t, s, http.MethodPost, "/admin/jocks", sampleCard, admin); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	j, err := st.GetJock(ctx, "dutch_mahoney")
	if err != nil {
		t.Fatal(err)
	}
	if j.Name != "Dutch" || j.VoiceID != "am_fenrir" || len(j.GoodForGenres) != 1 {
		t.Errorf("stored %+v", j)
	}

	rec := as(t, s, http.MethodGet, "/admin/jocks", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var list []store.Jock
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "dutch_mahoney" {
		t.Errorf("list = %+v", list)
	}

	// A card is written WHOLE: the console edits a form and submits it.
	edited := `{"name":"Dutch II","voice_id":"af_heart","speech_style":"quiet",` +
		`"personality":"Now whispers.","good_for_genres":["metal"],"forbidden":["no puns"]}`
	if rec := as(t, s, http.MethodPut, "/admin/jocks/dutch_mahoney", edited, admin); rec.Code != http.StatusNoContent {
		t.Fatalf("edit = %d: %s", rec.Code, rec.Body)
	}
	j, err = st.GetJock(ctx, "dutch_mahoney")
	if err != nil {
		t.Fatal(err)
	}
	if j.Name != "Dutch II" || j.VoiceID != "af_heart" || !reflect.DeepEqual(j.Forbidden, []string{"no puns"}) {
		t.Errorf("after the edit: %+v", j)
	}

	if rec := as(t, s, http.MethodDelete, "/admin/jocks/dutch_mahoney", "", admin); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body)
	}
	if _, err := st.GetJock(ctx, "dutch_mahoney"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the jock is still there: %v", err)
	}
	if rec := as(t, s, http.MethodDelete, "/admin/jocks/dutch_mahoney", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("deleting twice = %d, want 404", rec.Code)
	}
}

// TestJocksAPIVoiceMustExist: a voice nobody can produce fails as a SILENT
// BREAK, minutes later, on air. Refusing the form is the only place it can be
// caught while somebody is looking.
func TestJocksAPIVoiceMustExist(t *testing.T) {
	s, _, _ := jocksServer(t)
	admin := adminCookie(t, s)

	bad := `{"id":"x","name":"X","voice_id":"nobody","speech_style":"s","personality":"p"}`
	rec := as(t, s, http.MethodPost, "/admin/jocks", bad, admin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("= %d, want 400", rec.Code)
	}
	var e struct{ Field, Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Field != "voice_id" {
		t.Errorf("named field %q", e.Field)
	}
	// The refusal LISTS what is available, so the operator can pick one.
	if e.Error == "" || !containsString([]string{e.Error}, e.Error) {
		t.Errorf("the refusal says nothing useful: %q", e.Error)
	}

	// With no voice service at all -- the spike path, or a server whose
	// sidecar was never configured -- a jock is still editable.
	none, st0, _ := authServer(t)
	none.SetJocks(st0, nil, nil)
	if rec := as(t, none, http.MethodPost, "/admin/jocks", bad, adminCookie(t, none)); rec.Code != http.StatusCreated {
		t.Errorf("with no voice service = %d, want 201", rec.Code)
	}

	// With the sidecar down, jocks are still editable: refusing every one
	// because the voice service is unreachable would make the console useless
	// during an outage.
	down, st, _ := authServer(t)
	down.SetJocks(st, voiceList{err: errors.New("sidecar is down")}, nil)
	downAdmin := adminCookie(t, down)
	if rec := as(t, down, http.MethodPost, "/admin/jocks", bad, downAdmin); rec.Code != http.StatusCreated {
		t.Errorf("with the sidecar down = %d, want 201", rec.Code)
	}
}

func TestJocksAPIListVoices(t *testing.T) {
	s, _, _ := jocksServer(t)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodGet, "/admin/voices", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	var out struct{ Voices []string }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Voices, []string{"am_fenrir", "af_heart"}) {
		t.Errorf("voices = %v", out.Voices)
	}

	// A sidecar that will not answer is a 502: the failure is upstream, and
	// saying 500 would send an operator looking at Jockora.
	down, st, _ := authServer(t)
	down.SetJocks(st, voiceList{err: errors.New("sidecar is down")}, nil)
	if rec := as(t, down, http.MethodGet, "/admin/voices", "", adminCookie(t, down)); rec.Code != http.StatusBadGateway {
		t.Errorf("with the sidecar down = %d, want 502", rec.Code)
	}

	// And with no sidecar configured at all.
	bare, _, _ := authServer(t)
	if rec := as(t, bare, http.MethodGet, "/admin/voices", "", adminCookie(t, bare)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("with no voice service = %d, want 503", rec.Code)
	}
}

// TestJocksAPIEditReloadsWriter: the persona is read per break already, so an
// edit reaches a running station on its NEXT break -- the listener hears the
// change without hearing a gap.
func TestJocksAPIEditReloadsWriter(t *testing.T) {
	s, _, log := jocksServer(t)
	admin := adminCookie(t, s)

	if rec := as(t, s, http.MethodPost, "/admin/jocks", sampleCard, admin); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	edited := `{"name":"Dutch","voice_id":"am_fenrir","speech_style":"quiet",` +
		`"personality":"Now whispers about rock."}`
	if rec := as(t, s, http.MethodPut, "/admin/jocks/dutch_mahoney", edited, admin); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Body)
	}

	if len(log.seen) != 2 {
		t.Fatalf("the running stations were told %d times, want 2", len(log.seen))
	}
	last := log.seen[len(log.seen)-1]
	if last.ID != "dutch_mahoney" || last.Personality != "Now whispers about rock." {
		t.Errorf("the running station was told %+v", last)
	}
}

// TestJocksAPIDeleteUnassigns: the schema already sets the stations' jock to
// NULL. The API reports WHICH ones, because a station that went quiet without
// anyone saying so is the hardest kind of change to trace back.
func TestJocksAPIDeleteUnassigns(t *testing.T) {
	s, st, _ := jocksServer(t)
	admin := adminCookie(t, s)
	ctx := context.Background()

	if rec := as(t, s, http.MethodPost, "/admin/jocks", sampleCard, admin); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	var ids []int64
	for _, name := range []string{"ROCK", "METAL"} {
		id, err := st.CreateStation(ctx, store.Station{
			Name: name, Genre: "rock", JockID: "dutch_mahoney", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := st.CreateStation(ctx, store.Station{Name: "JAZZ", Genre: "jazz"}); err != nil {
		t.Fatal(err)
	}

	rec := as(t, s, http.MethodDelete, "/admin/jocks/dutch_mahoney", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var out struct{ Unassigned []int64 }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Unassigned, ids) {
		t.Errorf("unassigned = %v, want %v", out.Unassigned, ids)
	}
	for _, id := range ids {
		row, err := st.GetStation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if row.JockID != "" {
			t.Errorf("station %d still has jock %q", id, row.JockID)
		}
		// UNASSIGNED, not deleted: a station with no jock still plays music.
		if !row.Enabled {
			t.Errorf("station %d was switched off with its jock", id)
		}
	}
}

func TestJocksAPIValidation(t *testing.T) {
	s, _, _ := jocksServer(t)
	admin := adminCookie(t, s)

	for _, tc := range []struct{ body, field string }{
		{`{"name":"X","voice_id":"am_fenrir","speech_style":"s","personality":"p"}`, "id"},
		{`{"id":"x","voice_id":"am_fenrir","speech_style":"s","personality":"p"}`, "name"},
		{`{"id":"x","name":"X","speech_style":"s","personality":"p"}`, "voice_id"},
		{`{"id":"x","name":"X","voice_id":"am_fenrir","personality":"p"}`, "speech_style"},
		{`{"id":"x","name":"X","voice_id":"am_fenrir","speech_style":"s"}`, "personality"},
		{`{"id":"  ","name":"X","voice_id":"am_fenrir","speech_style":"s","personality":"p"}`, "id"},
	} {
		rec := as(t, s, http.MethodPost, "/admin/jocks", tc.body, admin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", tc.body, rec.Code)
			continue
		}
		var e struct{ Field string }
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e.Field != tc.field {
			t.Errorf("%s named %q, want %q", tc.body, e.Field, tc.field)
		}
	}

	// Forbidden may be empty: a jock with no prohibitions is one who has not
	// needed one yet.
	ok := `{"id":"x","name":"X","voice_id":"am_fenrir","speech_style":"s","personality":"p"}`
	if rec := as(t, s, http.MethodPost, "/admin/jocks", ok, admin); rec.Code != http.StatusCreated {
		t.Errorf("a card with no prohibitions = %d, want 201: %s", rec.Code, rec.Body)
	}
	if rec := as(t, s, http.MethodPost, "/admin/jocks", "not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
}

func TestJocksAPIRoleMatrix(t *testing.T) {
	s, _, _ := jocksServer(t)
	listener := listenerCookie(t, s)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/jocks"},
		{http.MethodPost, "/admin/jocks"},
		{http.MethodPut, "/admin/jocks/x"},
		{http.MethodDelete, "/admin/jocks/x"},
		{http.MethodGet, "/admin/voices"},
	} {
		if rec := as(t, s, tc.method, tc.path, "{}", listener); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a listener = %d, want 403", tc.method, tc.path, rec.Code)
		}
		if rec := as(t, s, tc.method, tc.path, "{}", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymously = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestJocksAPIBadTargets(t *testing.T) {
	s, _, _ := jocksServer(t)
	admin := adminCookie(t, s)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPut, "/admin/jocks/a/b", http.StatusNotFound},
		{http.MethodGet, "/admin/jocks/x", http.StatusMethodNotAllowed},
		{http.MethodPatch, "/admin/jocks", http.StatusMethodNotAllowed},
	} {
		if rec := as(t, s, tc.method, tc.path, "{}", admin); rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}

	bare, _, _ := authServer(t)
	if rec := as(t, bare, http.MethodGet, "/admin/jocks", "", adminCookie(t, bare)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("with no jocks to manage = %d, want 503", rec.Code)
	}
}

// brokenJocks fails every call.
type brokenJocks struct {
	err       error
	stationOK bool
}

func (b brokenJocks) ListJocks(context.Context) ([]store.Jock, error) { return nil, b.err }
func (b brokenJocks) GetJock(context.Context, string) (store.Jock, error) {
	return store.Jock{}, b.err
}
func (b brokenJocks) UpsertJock(context.Context, store.Jock) error { return b.err }
func (b brokenJocks) DeleteJock(context.Context, string) error     { return b.err }
func (b brokenJocks) ListStations(context.Context) ([]store.Station, error) {
	if b.stationOK {
		return nil, nil
	}
	return nil, b.err
}

func TestJocksAPISurfacesFailures(t *testing.T) {
	boom := errors.New("the database went away")
	s, _, _ := authServer(t)
	admin := adminCookie(t, s)
	s.SetJocks(brokenJocks{err: boom}, voiceList{names: []string{"am_fenrir"}}, nil)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/jocks", ""},
		{http.MethodPost, "/admin/jocks", sampleCard},
		{http.MethodPut, "/admin/jocks/x", sampleCard},
		{http.MethodDelete, "/admin/jocks/x", ""},
	} {
		if rec := as(t, s, tc.method, tc.path, tc.body, admin); rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s with a failing store = %d, want 500", tc.method, tc.path, rec.Code)
		}
	}

	// Listing the stations works, deleting the jock does not.
	s.SetJocks(brokenJocks{err: boom, stationOK: true}, voiceList{names: []string{"am_fenrir"}}, nil)
	if rec := as(t, s, http.MethodDelete, "/admin/jocks/x", "", admin); rec.Code != http.StatusInternalServerError {
		t.Errorf("= %d, want 500", rec.Code)
	}

	live, _, _ := jocksServer(t)
	live.listJocks(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/admin/jocks", nil))
	live.serveVoices(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/admin/voices", nil))
}
