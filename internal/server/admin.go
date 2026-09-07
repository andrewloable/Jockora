// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Admin is the operator surface: what the station knows about itself, and the
// few things worth changing without a restart.
//
// READS ARE OPEN AND WRITES ARE NOT. /now.json already exposes now-playing,
// health and metrics on an unauthenticated service, so gating reads would break
// the existing page and protect nothing new. Writes are different in kind: they
// change what a listener hears.
type Admin interface {
	// Overview is everything the operator page renders.
	Overview() any
	// SetCadence changes how many tracks pass between breaks.
	SetCadence(n int) error
	// SetEnriching pauses or resumes the background enrichment worker.
	SetEnriching(on bool) error
}

// SetAdmin wires the operator surface. Without one every /admin route reports
// 503: the routes exist, the capability does not.
func (s *Server) SetAdmin(a Admin) { s.admin = a }

func (s *Server) serveAdminOverview(w http.ResponseWriter, r *http.Request) {
	if s.admin == nil {
		http.Error(w, "no admin surface", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.admin.Overview()); err != nil {
		s.log.Warn("writing admin overview", "err", err)
	}
}

// serveAdminWrite handles the two settings worth changing without a restart.
//
// Behind an OPERATOR ACCOUNT. The shared token this used to accept was deleted
// in 10g rather than kept as a fallback -- two authorities is how one of them
// gets forgotten, and the forgotten one is the one still accepting writes -- and
// the door stayed shut until there were accounts to open it with.
func (s *Server) serveAdminWrite(w http.ResponseWriter, r *http.Request, path string) {
	if s.admin == nil {
		http.Error(w, "no admin surface", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		Cadence   *int  `json:"cadence"`
		Enriching *bool `json:"enriching"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	switch path {
	case "/admin/cadence":
		if body.Cadence == nil {
			http.Error(w, `expected {"cadence": N}`, http.StatusBadRequest)
			return
		}
		if err := s.admin.SetCadence(*body.Cadence); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case "/admin/enriching":
		if body.Enriching == nil {
			http.Error(w, `expected {"enriching": true|false}`, http.StatusBadRequest)
			return
		}
		if err := s.admin.SetEnriching(*body.Enriching); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isAdminWrite reports whether a path is one of the admin write routes.
func isAdminWrite(p string) bool {
	return strings.HasPrefix(p, "/admin/") && p != "/admin/overview.json"
}
