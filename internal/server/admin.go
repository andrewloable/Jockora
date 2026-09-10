// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
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
	// SetBreakOverlap changes how far into the outgoing track's instrumental
	// tail the DJ starts talking. Capped by the measured outro at use, so a
	// track that ends on a vocal is never talked over whatever this is set to.
	SetBreakOverlap(seconds float64) error
	// SetEnriching pauses or resumes the background enrichment worker.
	// SetEnriching pauses or resumes, and SAYS WHICH THING IT DID: resuming a
	// worker that gave up restarts it, resuming a paused one only unpauses it,
	// and an operator pressing one button deserves to know which they got.
	SetEnriching(on bool) (string, error)
	// PublicListener reports whether the listener half of the product is open
	// to anyone. Read on EVERY request that a guest could make, so the answer
	// is the live one and switching it off logs guests out on their next fetch
	// rather than whenever their guest cookie happens to lapse.
	PublicListener() bool
	// SetPublicListener opens or closes it. Never touches the console: an
	// operator account is required there whatever this is set to.
	SetPublicListener(on bool) error
	// SetEnrichRecall lets the enricher ask the model what a song is about
	// when nothing could be looked up. Future enrichment only -- a dossier is
	// written once, so this changes nothing already stored.
	SetEnrichRecall(on bool) error
	// RedoUnknownDossiers clears the dossiers with no meaning in them so the
	// enricher writes them again, and reports how many. It is what makes the
	// switch above visible on a library that is already enriched.
	RedoUnknownDossiers(ctx context.Context) (int64, error)
}

// ErrNoLibrary means the operation needs a music library and there is none.
//
// Defined HERE rather than in package app, for the reason ErrStationPreparing
// gives: the HTTP status is the only thing that turns on it, and package app
// already imports this one. It separates "this server cannot do that" (503)
// from "that went wrong" (500), which is the split serveRescan beside it makes.
var ErrNoLibrary = errors.New("no library")

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
		Cadence        *int     `json:"cadence"`
		Overlap        *float64 `json:"overlap"`
		Enriching      *bool    `json:"enriching"`
		PublicListener *bool    `json:"public_listener"`
		Recall         *bool    `json:"recall"`
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
	case "/admin/overlap":
		if body.Overlap == nil {
			http.Error(w, `expected {"overlap": seconds}`, http.StatusBadRequest)
			return
		}
		if err := s.admin.SetBreakOverlap(*body.Overlap); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case "/admin/public-listener":
		if body.PublicListener == nil {
			http.Error(w, `expected {"public_listener": true|false}`, http.StatusBadRequest)
			return
		}
		if err := s.admin.SetPublicListener(*body.PublicListener); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case "/admin/recall":
		if body.Recall == nil {
			http.Error(w, `expected {"recall": true|false}`, http.StatusBadRequest)
			return
		}
		if err := s.admin.SetEnrichRecall(*body.Recall); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	case "/admin/redo-dossiers":
		// STATUSES MATCHED TO serveRescan, the other route that sets a long
		// library job going: 503 when the capability is absent, 500 when it
		// failed. Both were 400 here, which says the operator sent something
		// wrong when they did not.
		// THE ONE ADMIN WRITE THAT DESTROYS SOMETHING, so it answers with the
		// count rather than a bare 204: an operator who clears four thousand
		// dossiers and one who clears none see the same success otherwise.
		n, err := s.admin.RedoUnknownDossiers(r.Context())
		if errors.Is(err, ErrNoLibrary) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
		return
	case "/admin/enriching":
		if body.Enriching == nil {
			http.Error(w, `expected {"enriching": true|false}`, http.StatusBadRequest)
			return
		}
		said, err := s.admin.SetEnriching(*body.Enriching)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The one admin write that answers with a body. A restart that finds
		// nothing to do and one that brings a dead worker back look identical
		// from here, and the console has to be able to tell the operator which
		// it was rather than reporting a bare success.
		s.writeJSON(w, http.StatusOK, map[string]any{"said": said})
		return
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
