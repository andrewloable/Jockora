// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// AdminTokenHeader carries the operator token on a write.
const AdminTokenHeader = "X-Jockora-Admin"

// Admin is the operator surface: what the station knows about itself, and the
// few things worth changing without a restart.
//
// READS ARE OPEN AND WRITES ARE NOT. /now.json already exposes now-playing,
// health and metrics on an unauthenticated service, so gating reads would break
// the existing page and protect nothing new. Writes are different in kind: they
// change what a listener hears, and Jockora ships no authentication at all.
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

// SetAdminToken sets the shared secret required for admin WRITES.
//
// AN EMPTY TOKEN REFUSES EVERY WRITE, and that is the important half. Treating
// "unset" as "allow anyone" would make the default configuration -- the one
// most people run, published on the LAN by the supplied compose file -- the
// dangerous one. An operator who wants writes opts in by setting a token.
func (s *Server) SetAdminToken(t string) { s.adminToken = t }

// authorised reports whether a write may proceed, and writes the refusal if not.
func (s *Server) authorised(w http.ResponseWriter, r *http.Request) bool {
	if s.adminToken == "" {
		http.Error(w, "admin writes are disabled: set JOCKORA_ADMIN_TOKEN and send it as "+
			AdminTokenHeader, http.StatusForbidden)
		return false
	}
	got := r.Header.Get(AdminTokenHeader)
	// Constant time, so a wrong token cannot be found a character at a time by
	// measuring how long the comparison takes.
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.adminToken)) != 1 {
		http.Error(w, "bad admin token", http.StatusForbidden)
		return false
	}
	return true
}

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
func (s *Server) serveAdminWrite(w http.ResponseWriter, r *http.Request, path string) {
	if s.admin == nil {
		http.Error(w, "no admin surface", http.StatusServiceUnavailable)
		return
	}
	if !s.authorised(w, r) {
		return
	}

	var body struct {
		Cadence   *int  `json:"cadence"`
		Enriching *bool `json:"enriching"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, "expected a JSON object", http.StatusBadRequest)
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
