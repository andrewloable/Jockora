// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// Stations is the station management a console needs.
//
// The regeneration and threshold rules live in package station, not here: they
// are rules about stations, and the same ones have to hold whether the change
// came from the console, a seed or a script.
type Stations interface {
	ListStations(ctx context.Context) ([]store.Station, error)
	GetStation(ctx context.Context, id int64) (store.Station, error)
	CreateStation(ctx context.Context, st store.Station) (int64, error)
	UpdateStation(ctx context.Context, st store.Station) error
	DeleteStation(ctx context.Context, id int64) error
	StationTrackIDs(ctx context.Context, id int64) ([]int64, error)
}

// Runtimes is the part of the station manager this API needs: taking a station
// off air when it is deleted or switched off.
type Runtimes interface {
	Stop(id int64) error
}

// Regenerator materialises a station's playlist.
type Regenerator func(ctx context.Context, id int64) (station.Diff, error)

// SetStations wires station management.
func (s *Server) SetStations(st Stations, regen Regenerator, rt Runtimes) {
	s.stations, s.regenerate, s.runtimes = st, regen, rt
}

// stationBody is what the console sends when it makes or edits a station.
//
// LISTS, with the single-value fields still accepted. A station is a place on a
// dial and "rock and punk" is one place; two stations a listener flips between
// is a different thing, and the dial is the listener's whole UI. The old fields
// stay because they cost one line each and something else may still send them.
type stationBody struct {
	Name   string   `json:"name"`
	Genre  string   `json:"genre"`
	Mood   string   `json:"mood"`
	Genres []string `json:"genres"`
	Moods  []string `json:"moods"`
}

func (b stationBody) genres() []string {
	if len(b.Genres) > 0 {
		return b.Genres
	}
	return station.SplitList(b.Genre)
}

func (b stationBody) moods() []string {
	if len(b.Moods) > 0 {
		return b.Moods
	}
	return station.SplitList(b.Mood)
}

// stationView is a station as the console sees it, with the numbers that decide
// whether it can run.
type stationView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Genre and Mood are the STORED form, comma-joined, and Genres and Moods
	// are the same values as lists. Both are sent: the lists are what the
	// console edits, and the strings are what every existing reader already
	// understands.
	Genre   string   `json:"genre"`
	Mood    string   `json:"mood,omitempty"`
	Genres  []string `json:"genres"`
	Moods   []string `json:"moods"`
	JockID  string   `json:"jock_id,omitempty"`
	Enabled bool     `json:"enabled"`
	Tracks  int      `json:"tracks"`
	// Warning is set when a station is legal but thinner than most people
	// want, so the console can say so without refusing.
	Warning string `json:"warning,omitempty"`
}

// serveStations handles every /admin/stations route.
func (s *Server) serveStations(w http.ResponseWriter, r *http.Request, path string) {
	if s.stations == nil {
		http.Error(w, "no library to manage", http.StatusServiceUnavailable)
		return
	}
	rest := strings.TrimPrefix(path, "/admin/stations")

	if rest == "" || rest == "/" {
		switch r.Method {
		case http.MethodGet:
			s.listStations(w, r)
		case http.MethodPost:
			s.createStation(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// The playlist routes go deeper than a station's own: /{id}/tracks/{track}
	// /{action}. Split here rather than teaching userTarget a fourth shape.
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if len(parts) > 2 {
		stationID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || stationID <= 0 {
			http.NotFound(w, r)
			return
		}
		s.servePlaylist(w, r, stationID, parts[1:])
		return
	}

	id, action, ok := userTarget(rest)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case r.Method == http.MethodPost && (action == "tracks" || action == "regenerate"):
		s.servePlaylist(w, r, id, []string{action})
	case r.Method == http.MethodGet && action == "tracks":
		s.servePlaylist(w, r, id, []string{action})
	case r.Method == http.MethodPut && action == "":
		s.updateStation(w, r, id)
	case r.Method == http.MethodDelete && action == "":
		s.deleteStation(w, r, id)
	case r.Method == http.MethodPost && action == "enable":
		s.enableStation(w, r, id)
	case r.Method == http.MethodPost && action == "disable":
		s.disableStation(w, r, id)
	case r.Method == http.MethodPut && action == "jock":
		s.assignJock(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveVocab is the closed vocabularies, so the console offers choices rather
// than a text box a typo can defeat.
func (s *Server) serveVocab(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"genres": enrich.StationTags,
		"moods":  enrich.Moods,
	})
}

func (s *Server) listStations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.stations.ListStations(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]stationView, 0, len(rows))
	for _, st := range rows {
		view, err := s.viewOfStation(r.Context(), st)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, view)
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) viewOfStation(ctx context.Context, st store.Station) (stationView, error) {
	ids, err := s.stations.StationTrackIDs(ctx, st.ID)
	if err != nil {
		return stationView{}, err
	}
	view := stationView{ID: st.ID, Name: st.Name, Genre: st.Genre, Mood: st.Mood,
		Genres: station.SplitList(st.Genre), Moods: station.SplitList(st.Mood),
		JockID: st.JockID, Enabled: st.Enabled, Tracks: len(ids)}
	if ok, warn := station.CheckThreshold(len(ids)); !ok {
		view.Warning = "too few tracks to run"
	} else if warn {
		view.Warning = "fewer tracks than most stations"
	}
	return view, nil
}

// createStation builds the playlist IMMEDIATELY, so the operator sees a track
// count rather than a promise -- and finds out at once if their genre and mood
// together select nothing.
func (s *Server) createStation(w http.ResponseWriter, r *http.Request) {
	var body stationBody
	if !s.decode(w, r, &body) {
		return
	}
	if field, msg := validateStation(body.Name, body.genres(), body.moods()); field != "" {
		s.writeFieldError(w, field, msg)
		return
	}

	id, err := s.stations.CreateStation(r.Context(), store.Station{
		Name:  body.Name,
		Genre: station.JoinList(body.genres()),
		Mood:  station.JoinList(body.moods()),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	diff, err := s.regenerate(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"id": id, "tracks": diff.Added})
}

// updateStation rewrites the station and REGENERATES, because changing the
// genre changes what the station is: leaving the old playlist would keep airing
// music the operator has just said they do not want.
func (s *Server) updateStation(w http.ResponseWriter, r *http.Request, id int64) {
	var body stationBody
	if !s.decode(w, r, &body) {
		return
	}
	if field, msg := validateStation(body.Name, body.genres(), body.moods()); field != "" {
		s.writeFieldError(w, field, msg)
		return
	}

	st, err := s.stations.GetStation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	st.Name = body.Name
	st.Genre = station.JoinList(body.genres())
	st.Mood = station.JoinList(body.moods())
	if err := s.stations.UpdateStation(r.Context(), st); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	diff, err := s.regenerate(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, diff)
}

// deleteStation takes the station off air FIRST. Removing the rows under a
// running pipeline leaves it drawing from a playlist that no longer exists,
// which a listener hears as the stream stopping.
func (s *Server) deleteStation(w http.ResponseWriter, r *http.Request, id int64) {
	if s.runtimes != nil {
		if err := s.runtimes.Stop(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	err := s.stations.DeleteStation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// enableStation puts a station on the dial if it has enough music.
func (s *Server) enableStation(w http.ResponseWriter, r *http.Request, id int64) {
	st, err := s.stations.GetStation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ids, err := s.stations.StationTrackIDs(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ok, warn := station.CheckThreshold(len(ids))
	if !ok {
		// 409 with the NUMBERS: "too few tracks" leaves an operator guessing
		// how many more they need.
		s.writeJSON(w, http.StatusConflict, map[string]any{
			"error": "too few tracks to run", "tracks": len(ids),
			"minimum": station.MinStationTracks,
		})
		return
	}

	st.Enabled = true
	if err := s.stations.UpdateStation(r.Context(), st); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := map[string]any{"tracks": len(ids)}
	if warn {
		// A WARNING, not a refusal: an operator who wants a station of twelve
		// deep cuts is entitled to one.
		out["warning"] = "fewer tracks than most stations"
	}
	s.writeJSON(w, http.StatusOK, out)
}

// disableStation takes a station off the dial and off the air.
func (s *Server) disableStation(w http.ResponseWriter, r *http.Request, id int64) {
	st, err := s.stations.GetStation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.runtimes != nil {
		if err := s.runtimes.Stop(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	st.Enabled = false
	if err := s.stations.UpdateStation(r.Context(), st); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// assignJock puts a jock on a station, or takes one off.
func (s *Server) assignJock(w http.ResponseWriter, r *http.Request, id int64) {
	var body struct {
		JockID *string `json:"jock_id"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	st, err := s.stations.GetStation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// null UNASSIGNS. A station with no jock still plays music -- that is what
	// the catch-all does -- so this is a normal state, not an error.
	st.JockID = ""
	if body.JockID != nil {
		st.JockID = *body.JockID
	}
	if err := s.stations.UpdateStation(r.Context(), st); err != nil {
		// A jock id that is not in the jocks table fails the foreign key, which
		// is the database refusing a station nobody could present.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.jockToAir(r.Context(), st.JockID)
	w.WriteHeader(http.StatusNoContent)
}

// jockToAir pushes a newly assigned jock to the station already airing.
//
// Saving the row was the whole of it before: the station kept speaking as the
// previous jock until it next started, which on a station somebody is listening
// to is never. PersonaChanged does the matching -- it looks for a running
// station holding this jock, which is the one just saved.
//
// UNASSIGNING pushes nothing: there is no persona to swap to, and silence is
// worse than a jock nobody chose.
func (s *Server) jockToAir(ctx context.Context, jockID string) {
	if jockID == "" || s.personas == nil || s.jocks == nil {
		return
	}
	j, err := s.jocks.GetJock(ctx, jockID)
	if err != nil {
		// The foreign key already refused an id with no row behind it, so
		// this is a database fault rather than a bad request -- and the
		// assignment IS saved. Leaving the old jock on air until the station
		// restarts is the honest outcome.
		return
	}
	s.personas.PersonaChanged(j)
}

// validateStation refuses a station the filter could never satisfy.
func validateStation(name string, genres, moods []string) (field, msg string) {
	if strings.TrimSpace(name) == "" {
		return "name", "a name is required"
	}
	// Against the CLOSED vocabularies, so a station cannot be made
	// unsatisfiable by a typo -- which produces not an empty station but one
	// that will never fill, with nothing on screen to say why.
	if err := (station.Filter{Genres: genres, Moods: moods}).Validate(); err != nil {
		// WHICH BOX TO HIGHLIGHT. The filter reports one error for two fields,
		// so the offending value decides: a genre the vocabulary does not hold
		// blames the genre picker, and anything else is the mood.
		for _, g := range genres {
			if !containsString(enrich.StationTags, g) {
				return "genre", err.Error()
			}
		}
		return "mood", err.Error()
	}
	return "", ""
}

func containsString(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}
