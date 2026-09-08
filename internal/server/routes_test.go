// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/auth"
)

// expectation is what one route must do for each of the three callers.
//
// The SAMPLE is here rather than on Route because it is test data: a concrete
// path for the prefix families, whose tails are ids the registry knows nothing
// about.
type expectation struct {
	method string
	path   string
	sample string // for prefix routes
	role   string // "", listener, admin
	body   string
}

// matrix is the whole surface, written out by hand.
//
// BY HAND ON PURPOSE. The point of the gate is that a route cannot exist
// without somebody having decided who may call it, so this table is the
// decision and TestRouteMatrixEveryRouteHasExpectation is the enforcement.
var matrix = []expectation{
	// Real credentials: a wrong password answers 401 too, and the matrix is
	// about the ROLE gate rather than about what each handler decides.
	{method: http.MethodPost, path: "/login",
		body: `{"name":"andrew","password":"correct horse battery"}`},
	{method: http.MethodPost, path: "/logout"},
	// The app's sign-in PAGE, which shares its path with the POST above.
	{path: "/login"},
	// The browser app: everything the API did not claim, assets included. The
	// sample is the entry point the shell asks for by name -- when this was
	// four exact page paths, the shell was served and its own bundle 404ed.
	{path: "/", sample: "/main.js"},
	{path: "/now.json"},
	{path: "/hls/", sample: "/hls/1/stream.m3u8"},

	{path: "/me", role: auth.RoleListener},
	{path: "/stations.json", role: auth.RoleListener},
	{method: http.MethodPost, path: "/tune", role: auth.RoleListener, body: `{"station_id":1}`},
	{method: http.MethodPost, path: "/feedback", role: auth.RoleListener, body: `{"verdict":"down"}`},

	{path: "/admin/overview.json", role: auth.RoleAdmin},
	{path: "/admin/vocab", role: auth.RoleAdmin},
	{method: http.MethodPost, path: "/admin/voices/preview", role: auth.RoleAdmin,
		body: `{"voice":"am_fenrir"}`},
	{path: "/admin/voices", role: auth.RoleAdmin},
	{method: http.MethodPost, path: "/admin/cadence", role: auth.RoleAdmin, body: `{"cadence":8}`},
	{method: http.MethodPost, path: "/admin/enriching", role: auth.RoleAdmin, body: `{"enriching":true}`},
	{method: http.MethodPost, path: "/admin/rescan", role: auth.RoleAdmin},
	{path: "/admin/users", sample: "/admin/users", role: auth.RoleAdmin},
	{path: "/admin/sources", sample: "/admin/sources", role: auth.RoleAdmin},
	{path: "/admin/stations", sample: "/admin/stations", role: auth.RoleAdmin},
	{path: "/admin/jocks", sample: "/admin/jocks", role: auth.RoleAdmin},
	// Re-filing a track is an admin act: it changes what every station
	// containing it plays, not only the playlist it was edited from.
	{path: "/admin/tracks", sample: "/admin/tracks/1/tags", role: auth.RoleAdmin},
	// The library's accumulated enrichment, out and in.
	{path: "/admin/enrichment", sample: "/admin/enrichment/export", role: auth.RoleAdmin},
	// Which language model the station speaks through.
	{path: "/admin/llm", sample: "/admin/llm", role: auth.RoleAdmin},
}

func (e expectation) requestPath() string {
	if e.sample != "" {
		return e.sample
	}
	return e.path
}

func (e expectation) verb() string {
	if e.method == "" {
		return http.MethodGet
	}
	return e.method
}

// matrixServer has every capability wired, so a refusal is about the ROLE and
// never about something being unconfigured.
func matrixServer(t *testing.T) *Server {
	t.Helper()
	s, st, _ := authServer(t)
	s.SetAdmin(&fakeAdmin{cadence: 4})
	s.SetSources(st, &fakeRescan{})
	s.SetStations(st, nil, nil)
	s.SetJocks(st, &voiceList{names: []string{"am_fenrir"}}, nil)
	s.SetPlaylists(st)
	s.SetTuner(&listenTuner{stations: []DialStation{{ID: 1, Name: "ROCK"}}})
	s.SetStatusSource(&StaticStatus{})
	return s
}

// TestRouteMatrixEveryRouteHasExpectation is the gate itself: a route added
// without an expectation fails here, naming itself, rather than shipping with
// nobody having decided who may call it.
func TestRouteMatrixEveryRouteHasExpectation(t *testing.T) {
	s := matrixServer(t)

	want := map[string]expectation{}
	for _, e := range matrix {
		want[e.verb()+" "+e.path] = e
	}
	for _, rt := range s.Routes() {
		method := rt.Method
		if method == "" {
			method = http.MethodGet
		}
		key := method + " " + rt.Path
		e, ok := want[key]
		if !ok {
			t.Fatalf("route %s has no expectation; add it to the matrix and decide who may call it", key)
		}
		if e.role != rt.Role {
			t.Errorf("route %s is guarded as %q, the matrix says %q", key, rt.Role, e.role)
		}
		if rt.Prefix && e.sample == "" {
			t.Errorf("prefix route %s has no sample path in the matrix", key)
		}
		delete(want, key)
	}
	for key := range want {
		t.Errorf("the matrix expects %s, which is not a route", key)
	}
}

// TestRouteMatrixAnonymous: every guarded route refuses an unidentified caller,
// and every public one answers.
func TestRouteMatrixAnonymous(t *testing.T) {
	s := matrixServer(t)

	for _, e := range matrix {
		rec := as(t, s, e.verb(), e.requestPath(), e.body, nil)
		if e.role == "" {
			// /hls carries its own check and refuses without a session, which
			// is the one public route that may answer 401.
			if rec.Code == http.StatusUnauthorized && !strings.HasPrefix(e.path, "/hls/") {
				t.Errorf("%s %s is public but answered 401", e.verb(), e.requestPath())
			}
			continue
		}
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymously = %d, want 401", e.verb(), e.requestPath(), rec.Code)
		}
	}
}

func TestRouteMatrixListener(t *testing.T) {
	s := matrixServer(t)
	listener := listenerCookie(t, s)

	for _, e := range matrix {
		rec := as(t, s, e.verb(), e.requestPath(), e.body, listener)
		switch e.role {
		case auth.RoleAdmin:
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s as a listener = %d, want 403", e.verb(), e.requestPath(), rec.Code)
			}
		default:
			// A listener's own routes never refuse them. What they answer
			// beyond that is each route's business.
			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
				t.Errorf("%s %s as a listener = %d", e.verb(), e.requestPath(), rec.Code)
			}
		}
	}
}

func TestRouteMatrixAdmin(t *testing.T) {
	s := matrixServer(t)
	admin := adminCookie(t, s)

	for _, e := range matrix {
		rec := as(t, s, e.verb(), e.requestPath(), e.body, admin)
		// An operator may do anything a listener may. The reverse is the whole
		// point of having roles.
		if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
			t.Errorf("%s %s as an operator = %d: %s", e.verb(), e.requestPath(), rec.Code, rec.Body)
		}
	}
}

// TestRouteMatrixNoLegacyRoutes: the routes v0.1 had that v0.2 deliberately
// does not. A registry makes this checkable rather than hopeful.
func TestRouteMatrixNoLegacyRoutes(t *testing.T) {
	s := matrixServer(t)
	admin := adminCookie(t, s)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/jock"},
		{http.MethodGet, "/jocks.json"},
		{http.MethodGet, "/hls/stream.m3u8"},
		{http.MethodGet, "/hls/seg0.ts"},
		{http.MethodPost, "/register"},
		{http.MethodPost, "/signup"},
	} {
		rec := as(t, s, tc.method, tc.path, "{}", admin)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d; the route should be gone", tc.method, tc.path, rec.Code)
		}
	}

	// And the shared token that used to open the admin writes changes nothing.
	if rec := as(t, s, http.MethodPost, "/admin/cadence", `{"cadence":8}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("= %d, want 401", rec.Code)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/cadence", strings.NewReader(`{"cadence":8}`))
	req.Header.Set("X-Jockora-Admin", "correct-horse")
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("the old token header = %d, want 401", rec.Code)
	}
}

func TestRouteMatrixMethodNotAllowed(t *testing.T) {
	s := matrixServer(t)
	admin := adminCookie(t, s)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/now.json"},
		{http.MethodPost, "/"},
		{http.MethodDelete, "/stations.json"},
		{http.MethodGet, "/tune"},
		{http.MethodPost, "/hls/1/stream.m3u8"},
	} {
		if rec := as(t, s, tc.method, tc.path, "{}", admin); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", tc.method, tc.path, rec.Code)
		}
	}

	// HEAD is a GET whose body the client discards. Refusing it breaks proxies
	// and health probes.
	if rec := as(t, s, http.MethodHead, "/now.json", "", nil); rec.Code == http.StatusMethodNotAllowed {
		t.Error("HEAD /now.json was refused")
	}
}
