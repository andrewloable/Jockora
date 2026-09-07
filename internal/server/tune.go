// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/store"
)

// DialStation is one position on the listener's dial.
//
// The dial is the listener's WHOLE UI, so it carries what a person picks a
// station by: what it is called, what it sounds like, and who is on air.
type DialStation struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Genre    string `json:"genre"`
	Mood     string `json:"mood,omitempty"`
	JockName string `json:"jock_name,omitempty"`
	Tracks   int    `json:"tracks"`
	// Listeners is how many people are on it now. A station with somebody on
	// it is already playing, which is the difference between joining a stream
	// and waiting for one to start.
	Listeners int `json:"listeners"`
}

// Tuner is the listener's side of the product.
//
// An interface so the server keeps knowing nothing about the library, the
// selector or the database, exactly as it knows nothing about the mixer.
type Tuner interface {
	// Dial lists the stations a listener may choose from.
	Dial(ctx context.Context) ([]DialStation, error)
	// Tune puts a session on a station and returns where to listen. It is the
	// FIRST HEARTBEAT: the station starts because somebody tuned to it.
	Tune(ctx context.Context, session string, stationID int64) (string, error)
	// Feedback records what a listener thought of the break that just aired.
	Feedback(ctx context.Context, userID, stationID int64, verdict string) error
}

// SetTuner wires the dial's controls. Without one the endpoints report 503:
// the routes exist, the capability does not.
func (s *Server) SetTuner(t Tuner) { s.tuner = t }

// serveDial lists the stations. Behind a login, because the dial is the
// product: an anonymous caller would be reading the operator's library.
func (s *Server) serveDial(w http.ResponseWriter, r *http.Request) {
	if s.tuner == nil {
		http.Error(w, "no dial", http.StatusServiceUnavailable)
		return
	}
	stations, err := s.tuner.Dial(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"stations": stations})
}

// serveTune handles POST /tune.
//
// A POST because it CHANGES what the listener is hearing. A GET that mutated
// the station would be followed by any link prefetcher on the network and
// retuned by a browser reloading a page.
func (s *Server) serveTune(w http.ResponseWriter, r *http.Request) {
	if s.tuner == nil {
		http.Error(w, "tuning unavailable", http.StatusServiceUnavailable)
		return
	}
	sess, _, err := s.session(r)
	if err != nil {
		http.Error(w, "sign in to listen", http.StatusUnauthorized)
		return
	}
	var body struct {
		StationID int64 `json:"station_id"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	hls, err := s.tuner.Tune(r.Context(), sess.SID, body.StationID)
	if errors.Is(err, store.ErrNotFound) {
		// A station that is not there, or is off the dial, is the listener's
		// mistake rather than a server fault -- and they stay on whatever they
		// were already hearing.
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"hls": hls, "station_id": body.StationID})
}

// serveFeedback records a thumbs-down on the break that just aired.
//
// ATTRIBUTED, because a household is several people with different taste and
// "somebody disliked this" is much less useful than knowing who.
func (s *Server) serveFeedback(w http.ResponseWriter, r *http.Request) {
	if s.tuner == nil {
		http.Error(w, "feedback unavailable", http.StatusServiceUnavailable)
		return
	}
	_, user, err := s.session(r)
	if err != nil {
		http.Error(w, "sign in", http.StatusUnauthorized)
		return
	}
	var body struct {
		StationID int64  `json:"station_id"`
		Verdict   string `json:"verdict"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	if err := s.tuner.Feedback(r.Context(), user.ID, body.StationID, body.Verdict); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listenerRoutes are the paths a signed-in listener may use. Everything else
// under /admin needs the operator role.
func (s *Server) requireListener(h http.HandlerFunc) http.HandlerFunc {
	return s.require(auth.RoleListener, h)
}

// stationParam reads ?station=N, reporting whether one was asked for.
func stationParam(r *http.Request) (int64, bool) {
	raw := r.URL.Query().Get("station")
	if raw == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
