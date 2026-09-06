// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Tuner changes which station is playing and records what a listener thought
// of a break.
//
// An interface so the server keeps knowing nothing about the library, the
// selector or the database, exactly as it knows nothing about the mixer.
type Tuner interface {
	// Tune switches to a station by tag. The current track is not interrupted.
	Tune(tag string) (int, error)
	// Feedback records a verdict on the break that just aired.
	Feedback(verdict string) error
}

// SetTuner wires the dial's controls. Without one both endpoints report 503:
// the routes exist, the capability does not.
func (s *Server) SetTuner(t Tuner) { s.tuner = t }

// serveTune handles POST /tune.
//
// A POST because it CHANGES what is playing. A GET that mutated the station
// would be followed by any link prefetcher on the network and retuned by a
// browser reloading a page.
func (s *Server) serveTune(w http.ResponseWriter, r *http.Request) {
	if s.tuner == nil {
		http.Error(w, "tuning unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, "expected {\"tag\": \"...\"}", http.StatusBadRequest)
		return
	}

	tracks, err := s.tuner.Tune(strings.TrimSpace(body.Tag))
	if err != nil {
		// A station with nothing in it is the listener's mistake, not a server
		// fault, and they stay on whatever they were already hearing.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tag": body.Tag, "tracks": tracks})
}

// serveFeedback handles POST /feedback.
//
// The thumbs-down is the ONLY signal the writing ever gets from a real ear.
// Every other judgement of break quality in this project is a machine checking
// rules it was handed; this is a person saying that one was bad.
func (s *Server) serveFeedback(w http.ResponseWriter, r *http.Request) {
	if s.tuner == nil {
		http.Error(w, "feedback unavailable", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Verdict string `json:"verdict"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, "expected {\"verdict\": \"down\"}", http.StatusBadRequest)
		return
	}
	verdict := strings.TrimSpace(strings.ToLower(body.Verdict))
	if verdict != "up" && verdict != "down" {
		http.Error(w, "verdict must be \"up\" or \"down\"", http.StatusBadRequest)
		return
	}
	if err := s.tuner.Feedback(verdict); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
