// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"
	"strings"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/web"
)

// Route is one entry in this server's whole surface.
//
// A TABLE rather than a switch, so a test can iterate it: the 401/403 matrix
// then cannot go stale, because a route added without an expectation fails the
// gate rather than quietly shipping unguarded.
type Route struct {
	Method string // "" matches GET and HEAD
	Path   string
	// Prefix makes Path a prefix match, for the families whose tails are data:
	// HLS segments and the admin collections.
	Prefix bool
	// AnyMethod hands every verb to the handler, which does its own dispatch.
	// Only the admin collections need it; HLS is a read surface and stays
	// GET-only, or a POST to a segment would be answered rather than refused.
	AnyMethod bool
	// Role is "" for public, or the role a caller must hold.
	Role string
	H    func(w http.ResponseWriter, r *http.Request)
}

// Routes is the entire surface, in the order it is matched.
//
// Exact paths come before prefixes, so /admin/vocab is not swallowed by a
// prefix that happens to share its start.
func (s *Server) Routes() []Route {
	adminWrite := func(w http.ResponseWriter, r *http.Request) {
		s.serveAdminWrite(w, r, r.URL.EscapedPath())
	}
	withPath := func(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, r.URL.EscapedPath()) }
	}

	return []Route{
		// Public: the pages themselves, and the two ends of a session. The
		// APIs behind them are guarded; the HTML is not worth hiding.
		{Method: http.MethodPost, Path: "/login", H: s.serveLogin},
		{Method: http.MethodPost, Path: "/logout", H: s.serveLogout},
		// /now.json stays public: it already reported health and metrics on an
		// unauthenticated service, and gating it would break the player page
		// while protecting nothing new.
		{Path: "/now.json", H: s.serveStatus},
		// HLS carries its own session check, because the identity it needs is
		// a presence key rather than a role -- and on the spike path, where
		// there are no accounts at all, it is an anonymous cookie.
		{Path: "/hls/", Prefix: true, H: func(w http.ResponseWriter, r *http.Request) {
			// serveHLS takes the part AFTER the prefix: the station and the
			// name are the whole of what it is given, so nothing else can be
			// smuggled into the path it builds.
			s.serveHLS(w, r, strings.TrimPrefix(r.URL.EscapedPath(), "/hls/"))
		}},

		// A listener's own controls.
		{Path: "/me", Role: auth.RoleListener, H: s.serveMe},
		{Path: "/stations.json", Role: auth.RoleListener, H: s.serveDial},
		{Method: http.MethodPost, Path: "/tune", Role: auth.RoleListener, H: s.serveTune},
		{Method: http.MethodPost, Path: "/feedback", Role: auth.RoleListener, H: s.serveFeedback},

		// The operator console.
		{Path: "/admin/overview.json", Role: auth.RoleAdmin, H: s.serveAdminOverview},
		{Path: "/admin/vocab", Role: auth.RoleAdmin, H: s.serveVocab},
		{Path: "/admin/voices", Role: auth.RoleAdmin, H: s.serveVoices},
		{Method: http.MethodPost, Path: "/admin/voices/preview", Role: auth.RoleAdmin, H: s.serveVoicePreview},
		{Method: http.MethodPost, Path: "/admin/cadence", Role: auth.RoleAdmin, H: adminWrite},
		{Method: http.MethodPost, Path: "/admin/enriching", Role: auth.RoleAdmin, H: adminWrite},
		{Method: http.MethodPost, Path: "/admin/rescan", Role: auth.RoleAdmin, H: s.serveRescan},
		{Path: "/admin/users", Prefix: true, AnyMethod: true, Role: auth.RoleAdmin, H: withPath(s.serveUsers)},
		{Path: "/admin/sources", Prefix: true, AnyMethod: true, Role: auth.RoleAdmin, H: withPath(s.serveSources)},
		{Path: "/admin/stations", Prefix: true, AnyMethod: true, Role: auth.RoleAdmin, H: withPath(s.serveStations)},
		{Path: "/admin/jocks", Prefix: true, AnyMethod: true, Role: auth.RoleAdmin, H: withPath(s.serveAdminJocks)},
		// TRACKS, not stations. A track's tags belong to the track, so the
		// path says so even though the console edits them from a playlist.
		{Path: "/admin/tracks", Prefix: true, AnyMethod: true, Role: auth.RoleAdmin, H: withPath(s.serveTrackTags)},
		// Enrichment is the most expensive thing this program makes, and it
		// lived in exactly one place. Admin only: it is the whole library's
		// work, and importing one is a write to every track.
		{Path: "/admin/enrichment", Prefix: true, AnyMethod: true, Role: auth.RoleAdmin, H: withPath(s.serveEnrichment)},

		// GET /login is the app's sign-in PAGE. POST /login above is the API
		// that page posts to; without this the page would answer 405, because
		// an exact path that claimed a method is not overridden by the
		// catch-all below.
		{Path: "/login", H: web.Handler().ServeHTTP},

		// LAST, AND A PREFIX: everything the API did not claim is the browser
		// app's. Routes are matched in order, so this catches only what nothing
		// above wanted.
		//
		// It has to be a prefix. The app's entry point is <script
		// src="main-<hash>.js">, a RELATIVE url that resolves to /main-<hash>.js
		// -- and while the four enumerated page paths that used to live here
		// served the shell perfectly, nothing served the script it asks for. The
		// page loaded, 404ed its own bundle, and rendered nothing. That is the
		// whole product, invisible to any test that only reads the shell.
		//
		// It also gives the Angular router its deep links: GET /login is this
		// route, not a 405 from the POST /login above. web.Handler still 404s a
		// missing ASSET rather than answering it with html, so a broken build
		// does not masquerade as a working one.
		{Path: "/", Prefix: true, H: web.Handler().ServeHTTP},
	}
}

// match finds the route for a request, and says whether some other method
// would have matched -- which is the difference between 404 and 405.
func (s *Server) match(method, path string) (Route, bool, bool) {
	var methodMismatch, exactClaim bool
	for _, rt := range s.Routes() {
		if !rt.matchesPath(path) {
			continue
		}
		if rt.matchesMethod(method) {
			// AN EXACT PATH IS A CLAIM; a prefix is a default. GET /tune is a
			// mistake worth a 405, not a page: answering it with the app's
			// html would tell an API caller nothing and look like success.
			// The browser-app catch-all is the only prefix this can affect,
			// and the pages it must still serve have exact routes of their own.
			if rt.Prefix && exactClaim {
				continue
			}
			return rt, true, false
		}
		methodMismatch = true
		if !rt.Prefix {
			exactClaim = true
		}
	}
	return Route{}, false, methodMismatch
}

func (rt Route) matchesPath(path string) bool {
	if rt.Prefix {
		return strings.HasPrefix(path, rt.Path)
	}
	return path == rt.Path
}

// matchesMethod. An empty Method means the two read-only verbs: HEAD is a GET
// whose body the client discards, and refusing it breaks proxies and probes.
func (rt Route) matchesMethod(method string) bool {
	// A PREFIX ROUTE OWNS EVERY METHOD under it, and this has to be asked
	// first: the handler behind it does its own dispatch, and listing each
	// verb here would be two places to keep in step.
	if rt.AnyMethod {
		return true
	}
	// An empty Method means the two read-only verbs. HEAD is a GET whose body
	// the client discards, and refusing it breaks proxies and probes.
	if rt.Method == "" {
		return method == http.MethodGet || method == http.MethodHead
	}
	return method == rt.Method
}
