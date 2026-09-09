// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/andrewloable/jockora/internal/store"
)

// DefaultPlaylistPage and MaxPlaylistPage bound a listing.
//
// Capped rather than refused: a console asking for five thousand rows gets five
// hundred and a total, which is a usable answer. Refusing would make a bad
// query look like a broken server.
const (
	DefaultPlaylistPage = 50
	MaxPlaylistPage     = 500
)

// Playlists is the playlist editing a console needs.
//
// The tag methods are here rather than in an interface of their own because
// they are edited from the same screen, by the same operator, in the same
// breath as pinning -- even though what they change is the TRACK and not the
// station's copy of it.
type Playlists interface {
	StationTrackPage(ctx context.Context, id int64, limit, offset int, sort string, desc bool) ([]store.StationTrackDetail, int, error)
	SetPinned(ctx context.Context, stationID, trackID int64, pinned bool) error
	SetExcluded(ctx context.Context, stationID, trackID int64, excluded bool) error
	SetTrackTags(ctx context.Context, trackID int64, genres, moods []string) error
	ClearTrackTags(ctx context.Context, trackID int64) error
	TrackTags(ctx context.Context, trackID int64) (store.TrackTags, error)
}

// SetPlaylists wires playlist editing.
func (s *Server) SetPlaylists(p Playlists) { s.playlists = p }

// servePlaylist handles /admin/stations/{id}/tracks... and .../regenerate.
func (s *Server) servePlaylist(w http.ResponseWriter, r *http.Request, stationID int64, parts []string) {
	// SHAPE FIRST, then wiring, then method. A path this server does not have
	// is a 404 whatever is configured behind it; answering 503 would tell an
	// operator a route exists when it does not.
	known := (len(parts) == 1 && (parts[0] == "regenerate" || parts[0] == "tracks")) ||
		(len(parts) == 3 && parts[0] == "tracks")
	if !known {
		http.NotFound(w, r)
		return
	}
	if s.playlists == nil {
		http.Error(w, "no playlists to manage", http.StatusServiceUnavailable)
		return
	}

	switch {
	case len(parts) == 1 && parts[0] == "regenerate" && r.Method == http.MethodPost:
		s.regeneratePlaylist(w, r, stationID)
	case len(parts) == 1 && parts[0] == "tracks" && r.Method == http.MethodGet:
		s.listPlaylist(w, r, stationID)
	case len(parts) == 3 && parts[0] == "tracks" && r.Method == http.MethodPost:
		s.flagTrack(w, r, stationID, parts[1], parts[2])
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listPlaylist(w http.ResponseWriter, r *http.Request, stationID int64) {
	limit := pageSize(r.URL.Query().Get("limit"))
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || offset < 0 {
		offset = 0
	}

	// UNVALIDATED ON PURPOSE. The store owns the list of columns it can sort
	// by, and a name it does not know is the default order -- the same way an
	// unreadable limit is the default page size rather than a 400.
	sort := r.URL.Query().Get("sort")
	desc := r.URL.Query().Get("dir") == "desc"

	rows, total, err := s.playlists.StationTrackPage(r.Context(), stationID, limit, offset, sort, desc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"tracks": rows, "total": total, "limit": limit, "offset": offset,
	})
}

// pageSize keeps a listing within something a browser can render.
func pageSize(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return DefaultPlaylistPage
	}
	if n > MaxPlaylistPage {
		return MaxPlaylistPage
	}
	return n
}

// flagTrack pins, unpins, excludes or unexcludes one track.
func (s *Server) flagTrack(w http.ResponseWriter, r *http.Request, stationID int64, track, action string) {
	trackID, err := strconv.ParseInt(track, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var flag func(context.Context, int64, int64, bool) error
	var on bool
	switch action {
	case "pin":
		flag, on = s.playlists.SetPinned, true
	case "unpin":
		flag, on = s.playlists.SetPinned, false
	case "exclude":
		flag, on = s.playlists.SetExcluded, true
	case "unexclude":
		flag, on = s.playlists.SetExcluded, false
	default:
		http.NotFound(w, r)
		return
	}

	// PINNING A TRACK THE LIBRARY NO LONGER HAS is refused: a pin means "keep
	// this one whatever the filter says", and keeping a file that is not there
	// puts a hole in the station that only shows up at play time.
	if on && action == "pin" {
		missing, err := s.trackIsMissing(r.Context(), stationID, trackID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if missing {
			http.Error(w, "that track is missing from the library", http.StatusConflict)
			return
		}
	}

	err = flag(r.Context(), stationID, trackID, on)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "that track is not on this station", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// trackIsMissing reports whether one playlist row names a file the library has
// lost. Read through the same paged listing rather than a second query, so
// there is one definition of what "missing" means.
func (s *Server) trackIsMissing(ctx context.Context, stationID, trackID int64) (bool, error) {
	rows, _, err := s.playlists.StationTrackPage(ctx, stationID, MaxPlaylistPage, 0, "", false)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.TrackID == trackID {
			return row.Missing, nil
		}
	}
	return false, nil
}

func (s *Server) regeneratePlaylist(w http.ResponseWriter, r *http.Request, stationID int64) {
	if s.regenerate == nil {
		http.Error(w, "no library to regenerate from", http.StatusServiceUnavailable)
		return
	}
	diff, err := s.regenerate(r.Context(), stationID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The DIFF, so the console can say what changed rather than redrawing a
	// list and leaving the operator to spot the difference.
	s.writeJSON(w, http.StatusOK, diff)
}
