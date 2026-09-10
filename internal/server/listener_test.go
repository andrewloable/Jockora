// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

// listenTuner is the listener side of the app, faked.
//
// GUARDED, because the real one is reached from many request goroutines at
// once and this stands in for it. Unguarded, every concurrent test of the
// listener routes reported a race in the DOUBLE and hid whatever the
// production code was or was not doing -- which is why there were no
// concurrent tests of them at all.
type listenTuner struct {
	mu       sync.Mutex
	stations []DialStation
	err      error

	tunedTo      int64
	tunedSession string
	feedback     []string
	fbUser       int64
	fbStation    int64
}

// tuned reports what the last Tune was told, safely.
func (l *listenTuner) tuned() (int64, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tunedTo, l.tunedSession
}

func (l *listenTuner) Dial(context.Context) ([]DialStation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stations, l.err
}

func (l *listenTuner) Tune(_ context.Context, session string, id int64) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, st := range l.stations {
		if st.ID == id {
			l.tunedTo, l.tunedSession = id, session
			return "/hls/" + strconv.FormatInt(id, 10) + "/stream.m3u8", nil
		}
	}
	return "", store.ErrNotFound
}

func (l *listenTuner) Feedback(_ context.Context, userID, stationID int64, verdict string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	l.feedback = append(l.feedback, verdict)
	l.fbUser, l.fbStation = userID, stationID
	return nil
}

func listenServer(t *testing.T) (*Server, *listenTuner) {
	t.Helper()
	s, _, _ := authServer(t)
	tuner := &listenTuner{stations: []DialStation{
		{ID: 1, Name: "ROCK", Genre: "rock", JockName: "Dutch", Tracks: 412, Listeners: 2},
		{ID: 3, Name: "AMBIENT", Genre: "ambient", Mood: "calm", Tracks: 96},
	}}
	s.SetTuner(tuner)
	return s, tuner
}

// TestListenerDialListsEnabledStations: the dial is the listener's WHOLE UI, so
// it carries what a person picks a station by.
func TestListenerDialListsEnabledStations(t *testing.T) {
	s, _ := listenServer(t)
	listener := listenerCookie(t, s)

	rec := as(t, s, http.MethodGet, "/stations.json", "", listener)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var out struct{ Stations []DialStation }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Stations) != 2 {
		t.Fatalf("%d stations", len(out.Stations))
	}
	first := out.Stations[0]
	if first.Name != "ROCK" || first.Genre != "rock" || first.JockName != "Dutch" {
		t.Errorf("station = %+v", first)
	}
	// Listeners, because a station with somebody on it is already playing --
	// the difference between joining a stream and waiting for one to start.
	if first.Listeners != 2 || first.Tracks != 412 {
		t.Errorf("station = %+v, want 2 listeners and 412 tracks", first)
	}
	if out.Stations[1].Mood != "calm" {
		t.Errorf("mood missing: %+v", out.Stations[1])
	}
}

// TestListenerDialRequiresLogin: an anonymous caller reading the dial is
// reading the operator's library.
func TestListenerDialRequiresLogin(t *testing.T) {
	s, _ := listenServer(t)

	if rec := as(t, s, http.MethodGet, "/stations.json", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("= %d, want 401", rec.Code)
	}
	// An operator may listen too: admin is a superset of listener.
	if rec := as(t, s, http.MethodGet, "/stations.json", "", adminCookie(t, s)); rec.Code != http.StatusOK {
		t.Errorf("an operator = %d, want 200", rec.Code)
	}
}

// TestListenerTuneReturnsURL: tuning IS the first heartbeat. The station starts
// because somebody tuned to it, and the client cannot fetch a playlist that
// does not exist yet.
func TestListenerTuneReturnsURL(t *testing.T) {
	s, tuner := listenServer(t)
	listener := listenerCookie(t, s)

	rec := as(t, s, http.MethodPost, "/tune", `{"station_id":3}`, listener)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		HLS       string `json:"hls"`
		StationID int64  `json:"station_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.HLS != "/hls/3/stream.m3u8" || out.StationID != 3 {
		t.Errorf("= %+v", out)
	}
	if tuner.tunedTo != 3 {
		t.Errorf("tuned to %d, want 3", tuner.tunedTo)
	}
	if tuner.tunedSession == "" {
		t.Error("the heartbeat carried no session, so nobody is counted as listening")
	}
}

func TestListenerTuneDisabledStation404(t *testing.T) {
	s, _ := listenServer(t)
	listener := listenerCookie(t, s)

	// Station 2 is off the dial. The same answer as one that does not exist:
	// a listener has no business knowing which stations were switched off.
	if rec := as(t, s, http.MethodPost, "/tune", `{"station_id":2}`, listener); rec.Code != http.StatusNotFound {
		t.Errorf("a disabled station = %d, want 404", rec.Code)
	}
}

func TestListenerTuneUnknownStation404(t *testing.T) {
	s, _ := listenServer(t)
	listener := listenerCookie(t, s)

	if rec := as(t, s, http.MethodPost, "/tune", `{"station_id":9999}`, listener); rec.Code != http.StatusNotFound {
		t.Errorf("= %d, want 404", rec.Code)
	}
	if rec := as(t, s, http.MethodPost, "/tune", "not json", listener); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
}

func TestListenerTuneAnonymous401(t *testing.T) {
	s, tuner := listenServer(t)

	if rec := as(t, s, http.MethodPost, "/tune", `{"station_id":1}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("= %d, want 401", rec.Code)
	}
	if tuner.tunedTo != 0 {
		t.Error("an anonymous caller started a station")
	}
}

// TestListenerFeedbackAttributed: a household is several people with different
// taste, and "somebody disliked this" is much less useful than knowing who.
func TestListenerFeedbackAttributed(t *testing.T) {
	s, tuner := listenServer(t)
	listener := listenerCookie(t, s)

	rec := as(t, s, http.MethodPost, "/feedback", `{"station_id":3,"verdict":"down"}`, listener)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if len(tuner.feedback) != 1 || tuner.feedback[0] != "down" {
		t.Errorf("feedback = %v", tuner.feedback)
	}
	if tuner.fbUser == 0 || tuner.fbStation != 3 {
		t.Errorf("recorded against user %d station %d", tuner.fbUser, tuner.fbStation)
	}

	if rec := as(t, s, http.MethodPost, "/feedback", `{"verdict":"down"}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous feedback = %d, want 401", rec.Code)
	}
	if rec := as(t, s, http.MethodPost, "/feedback", "not json", listener); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
}

// TestListenerJockRoutesGone: listeners pick STATIONS, never jocks. The dial is
// the whole UI, and a jock picker made the jock a listener-facing choice it was
// never meant to be.
func TestListenerJockRoutesGone(t *testing.T) {
	s, _ := listenServer(t)
	listener := listenerCookie(t, s)

	if rec := as(t, s, http.MethodPost, "/jock", `{"id":"dutch_mahoney"}`, listener); rec.Code == http.StatusOK ||
		rec.Code == http.StatusNoContent {
		t.Errorf("POST /jock = %d; the route should be gone", rec.Code)
	}
	if rec := as(t, s, http.MethodGet, "/jocks.json", "", listener); rec.Code != http.StatusNotFound {
		t.Errorf("GET /jocks.json = %d, want 404", rec.Code)
	}
}

// TestListenerCannotReachAdmin: a listener's controls and an operator's are
// different doors.
func TestListenerCannotReachAdmin(t *testing.T) {
	s, _ := listenServer(t)
	listener := listenerCookie(t, s)

	for _, path := range []string{"/admin/users", "/admin/sources", "/admin/stations", "/admin/jocks"} {
		if rec := as(t, s, http.MethodGet, path, "", listener); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s as a listener = %d, want 403", path, rec.Code)
		}
	}
}

// nowPerStation reports one station's now-playing and nothing for the rest.
type nowPerStation struct{ byStation map[int64]string }

// Status carries a now-playing DELIBERATELY. The endpoint has to strip it when
// no station was named, and a fake with nothing to strip proves nothing.
func (n nowPerStation) Status() Status {
	return Status{
		Health: Health{FFmpeg: "ok"},
		Now:    &Track{Artist: "Somebody", Title: "Somebody Else's Track"},
		Next:   &Track{Artist: "Somebody", Title: "And Another"},
	}
}

func (n nowPerStation) StatusForStation(id int64) Status {
	st := n.Status()
	st.Now, st.Next = nil, nil
	if title, ok := n.byStation[id]; ok {
		st.Now = &Track{Artist: "A", Title: title}
	}
	return st
}

// TestListenerNowPerStation: there is no global now-playing once stations are
// per-listener. Four running stations are four different tracks.
func TestListenerNowPerStation(t *testing.T) {
	s, _ := listenServer(t)
	s.SetStatusSource(nowPerStation{byStation: map[int64]string{3: "Blue Monday"}})

	rec := get(t, s, "/now.json?station=3")
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	now, ok := out["now"].(map[string]any)
	if !ok || now["title"] != "Blue Monday" {
		t.Fatalf("station 3 = %v", out)
	}

	// A station that is not playing says so by omission, not by claiming
	// somebody else's track.
	rec = get(t, s, "/now.json?station=1")
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["now"] != nil {
		t.Errorf("station 1 reports %v", out["now"])
	}

	// A source that cannot answer per station falls back to its global one
	// rather than failing: an older status source is still a status source.
	plain, _ := listenServer(t)
	static := &StaticStatus{}
	static.Set(Status{Health: Health{FFmpeg: "ok"},
		Now: &Track{Artist: "A", Title: "Fallback"}})
	plain.SetStatusSource(static)
	rec = get(t, plain, "/now.json?station=3")
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if now, ok := out["now"].(map[string]any); !ok || now["title"] != "Fallback" {
		t.Errorf("a source with no per-station answer gave %v", out["now"])
	}

	// And with no station named there is no now-playing at all.
	for _, path := range []string{"/now.json", "/now.json?station=abc", "/now.json?station=0"} {
		rec = get(t, s, path)
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out["now"] != nil {
			t.Errorf("GET %s carries a now-playing: %v", path, out["now"])
		}
		if out["next"] != nil {
			t.Errorf("GET %s carries a next track: %v", path, out["next"])
		}
		if out["health"] == nil {
			t.Errorf("GET %s lost the global fields", path)
		}
	}
}

func TestListenerWithoutATuner(t *testing.T) {
	s, _, _ := authServer(t)
	listener := listenerCookie(t, s)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/stations.json"},
		{http.MethodPost, "/tune"},
		{http.MethodPost, "/feedback"},
	} {
		if rec := as(t, s, tc.method, tc.path, "{}", listener); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s with no tuner = %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}

func TestListenerSurfacesFailures(t *testing.T) {
	s, tuner := listenServer(t)
	listener := listenerCookie(t, s)
	tuner.err = errors.New("the database went away")

	if rec := as(t, s, http.MethodGet, "/stations.json", "", listener); rec.Code != http.StatusInternalServerError {
		t.Errorf("a dial that could not be read = %d, want 500", rec.Code)
	}
	// A tune that failed for a reason other than "no such station" is a server
	// fault, and must not be reported as the listener asking for the wrong
	// thing.
	broken := &listenTuner{stations: []DialStation{{ID: 1}}, err: errors.New("boom")}
	s.SetTuner(brokenTune{broken})
	if rec := as(t, s, http.MethodPost, "/tune", `{"station_id":1}`, listener); rec.Code != http.StatusInternalServerError {
		t.Errorf("a tune that failed = %d, want 500", rec.Code)
	}
	s.SetTuner(tuner)

	// Feedback that cannot be recorded is the caller's problem to retry, not a
	// server fault worth alarming about.
	if rec := as(t, s, http.MethodPost, "/feedback", `{"verdict":"down"}`, listener); rec.Code != http.StatusBadRequest {
		t.Errorf("= %d, want 400", rec.Code)
	}

	s.serveDial(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/stations.json", nil))
	s.SetStatusSource(nowPerStation{})
	s.serveStatus(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/now.json", nil))

	// The handlers refuse an unidentified caller on their own, not only
	// because the router wrapped them. A route rearranged later must not
	// silently open them.
	for _, h := range []http.HandlerFunc{s.serveTune, s.serveFeedback} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("an unwrapped handler answered %d, want 401", rec.Code)
		}
	}
}

// brokenTune answers every tune with a fault rather than a refusal.
type brokenTune struct{ *listenTuner }

func (b brokenTune) Tune(context.Context, string, int64) (string, error) {
	return "", b.listenTuner.err
}
