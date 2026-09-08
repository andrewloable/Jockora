// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

// serveTrackTags handles PUT and DELETE on /admin/tracks/{id}/tags.
//
// TRACK-SCOPED, not station-scoped, even though the console edits it from a
// station's playlist. A track's tags are what it IS; the same track on another
// station would otherwise show something different, and one of the two rows
// would be lying.
func (s *Server) serveTrackTags(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.Trim(strings.TrimPrefix(path, "/admin/tracks"), "/")
	id, action, ok := userTarget("/" + rest)
	if !ok || action != "tags" {
		http.NotFound(w, r)
		return
	}
	// AFTER the shape check: a path this server does not have is a 404 whatever
	// is wired behind it, and answering 503 would claim a route that does not
	// exist.
	if s.playlists == nil {
		http.Error(w, "no library to manage", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodPut:
		s.setTrackTags(w, r, id)
	case http.MethodDelete:
		s.revertTrackTags(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// setTrackTags records an operator's own genres and moods for one track.
func (s *Server) setTrackTags(w http.ResponseWriter, r *http.Request, trackID int64) {
	var body struct {
		Genres []string `json:"genres"`
		Moods  []string `json:"moods"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	// DEDUPED FIRST, so the cap counts TAGS rather than copies and a row cannot
	// read back "rock, rock". The console sends checkboxes and cannot repeat
	// one, but the API is the API, and the dossier path has always deduped.
	genres, moods := once(body.Genres), once(body.Moods)
	if field, msg := validateTrackTags(genres, moods); field != "" {
		s.writeFieldError(w, field, msg)
		return
	}
	if err := s.playlists.SetTrackTags(r.Context(), trackID, genres, moods); err != nil {
		// A track the library does not have fails the foreign key, which is a
		// bad request rather than a broken server: nothing here could have
		// made it succeed.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeTrackTags(w, r, trackID)
}

// revertTrackTags drops the edit, putting the enrichment's answer back.
func (s *Server) revertTrackTags(w http.ResponseWriter, r *http.Request, trackID int64) {
	err := s.playlists.ClearTrackTags(r.Context(), trackID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "that track has no edit to undo", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeTrackTags(w, r, trackID)
}

// writeTrackTags answers with the EFFECTIVE state rather than an echo of the
// request, so the console redraws the row from the reply instead of re-fetching
// the page -- and a revert can say what the track went back to.
func (s *Server) writeTrackTags(w http.ResponseWriter, r *http.Request, trackID int64) {
	tags, err := s.playlists.TrackTags(r.Context(), trackID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, tags)
}

// once keeps the first of each value, in the order it arrived.
func once(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// validateTrackTags refuses anything a dossier itself could not hold.
//
// The vocabularies are closed so that grouping is exact. A tag outside them
// would not error at play time -- it would quietly make the track unreachable
// from every station, which is the failure an operator is least likely to spot.
func validateTrackTags(genres, moods []string) (field, msg string) {
	for _, g := range genres {
		if !slices.Contains(enrich.StationTags, g) {
			return "genres", strconv.Quote(g) + " is not a genre the enrichment uses"
		}
	}
	for _, m := range moods {
		if !slices.Contains(enrich.Moods, m) {
			return "moods", strconv.Quote(m) + " is not a mood the enrichment uses"
		}
	}
	// The same cap a dossier has. More than this is not curation: it is a
	// track that surfaces on every station in the dial.
	if len(genres) > enrich.MaxTags {
		return "genres", "at most " + strconv.Itoa(enrich.MaxTags) + " genres"
	}
	if len(moods) > enrich.MaxTags {
		return "moods", "at most " + strconv.Itoa(enrich.MaxTags) + " moods"
	}
	return "", ""
}
