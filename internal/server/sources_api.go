// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/andrewloable/jockora/internal/library"
	"github.com/andrewloable/jockora/internal/store"
)

// Sources is the library-source management a console needs.
type Sources interface {
	ListSources(ctx context.Context) ([]store.Source, error)
	CreateSource(ctx context.Context, src store.Source) (int64, error)
	DeleteSource(ctx context.Context, id int64) error
	SetSourceEnabled(ctx context.Context, id int64, enabled bool) error
	RetireSourceTracks(ctx context.Context, sourceID int64) (int, error)
}

// Rescanner triggers and reports a library scan.
type Rescanner interface {
	Start(ctx context.Context) error
	Progress() library.RescanProgress
}

// SetSources wires source management and the rescan trigger.
func (s *Server) SetSources(src Sources, rescan Rescanner) { s.sources, s.rescan = src, rescan }

// sourceView is a source as the console sees it. NO PASSWORD: a Subsonic
// credential has to be replayable, so it cannot be hashed, which makes keeping
// it out of every response the only thing standing between it and a log.
type sourceView struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Locator  string `json:"locator"`
	Username string `json:"username,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// serveSources handles every /admin/sources route.
func (s *Server) serveSources(w http.ResponseWriter, r *http.Request, path string) {
	if s.sources == nil {
		http.Error(w, "no library to manage", http.StatusServiceUnavailable)
		return
	}
	rest := strings.TrimPrefix(path, "/admin/sources")

	if rest == "" || rest == "/" {
		switch r.Method {
		case http.MethodGet:
			s.listSources(w, r)
		case http.MethodPost:
			s.createSource(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	id, action, ok := userTarget(rest)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodDelete && action == "":
		s.deleteSource(w, r, id)
	case r.Method == http.MethodPost && action == "enable":
		s.setSourceEnabled(w, r, id, true)
	case r.Method == http.MethodPost && action == "disable":
		s.setSourceEnabled(w, r, id, false)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.sources.ListSources(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]sourceView, 0, len(rows))
	for _, src := range rows {
		out = append(out, sourceView{ID: src.ID, Kind: src.Kind, Locator: src.Locator,
			Username: src.Username, Enabled: src.Enabled})
	}
	s.writeJSON(w, http.StatusOK, out)
}

// createSource checks the source WORKS before storing it.
//
// A folder that is not there, or a server that does not answer, fails silently
// in a background scan: the operator sees a source in the list, no tracks
// appearing, and nothing anywhere saying why. Better to refuse the form.
func (s *Server) createSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind     string `json:"kind"`
		Locator  string `json:"locator"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	switch body.Kind {
	case store.SourceFolder:
		fi, err := os.Stat(body.Locator)
		if err != nil {
			s.writeFieldError(w, "locator", "that folder cannot be read: "+err.Error())
			return
		}
		if !fi.IsDir() {
			s.writeFieldError(w, "locator", "that path is a file, not a folder")
			return
		}
	case store.SourceSubsonic:
		sub := library.Subsonic{BaseURL: body.Locator, User: body.Username, Password: body.Password}
		if err := sub.Ping(r.Context()); err != nil {
			s.writeFieldError(w, "locator", "that server did not answer: "+err.Error())
			return
		}
	default:
		s.writeFieldError(w, "kind", "a source is either "+store.SourceFolder+" or "+store.SourceSubsonic)
		return
	}

	id, err := s.sources.CreateSource(r.Context(), store.Source{
		Kind: body.Kind, Locator: body.Locator, Username: body.Username,
		Password: body.Password, Enabled: true,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// deleteSource removes a source and RETIRES its tracks rather than deleting
// them: the rows carry dossiers, and a source added back should find them
// waiting instead of enriching a library all over again.
func (s *Server) deleteSource(w http.ResponseWriter, r *http.Request, id int64) {
	if _, err := s.sources.RetireSourceTracks(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err := s.sources.DeleteSource(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such source", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setSourceEnabled(w http.ResponseWriter, r *http.Request, id int64, enabled bool) {
	err := s.sources.SetSourceEnabled(r.Context(), id, enabled)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such source", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveRescan starts a library scan and returns immediately.
//
// 202, not 200: a scan of a real library takes minutes, and holding the request
// open for it would time out in every browser. Progress rides on /now.json.
func (s *Server) serveRescan(w http.ResponseWriter, r *http.Request) {
	if s.rescan == nil {
		http.Error(w, "no library to scan", http.StatusServiceUnavailable)
		return
	}
	// The context deliberately OUTLIVES the request: the scan continues after
	// the browser has been answered, and tying it to the request would cancel
	// it the moment the response was written.
	err := s.rescan.Start(context.WithoutCancel(r.Context()))
	if errors.Is(err, library.ErrScanRunning) {
		http.Error(w, "a scan is already running", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusAccepted, s.rescan.Progress())
}
