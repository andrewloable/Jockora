// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/andrewloable/jockora/internal/store"
)

// Jocks is the persona management a console needs.
type Jocks interface {
	ListJocks(ctx context.Context) ([]store.Jock, error)
	GetJock(ctx context.Context, id string) (store.Jock, error)
	UpsertJock(ctx context.Context, j store.Jock) error
	DeleteJock(ctx context.Context, id string) error
	ListStations(ctx context.Context) ([]store.Station, error)
}

// Voices lists the voices the TTS sidecar can actually produce.
//
// Asked rather than assumed, so an operator cannot save a jock nobody can
// voice -- a failure that would otherwise appear as a silent break, minutes
// later, on air.
type Voices interface {
	Voices(ctx context.Context) ([]string, error)
	// Preview speaks one line in a voice and returns the audio, so an operator
	// can HEAR a jock before putting them on air rather than reading a name
	// like "am_fenrir" and guessing.
	Preview(ctx context.Context, voice, text string) ([]byte, error)
}

// Personas is the running writers an edit has to reach.
type Personas interface {
	// PersonaChanged tells the running stations that a jock was edited.
	PersonaChanged(j store.Jock)
}

// SetJocks wires persona management.
func (s *Server) SetJocks(j Jocks, v Voices, p Personas) { s.jocks, s.voices, s.personas = j, v, p }

// serveJocks handles every /admin/jocks route.
func (s *Server) serveAdminJocks(w http.ResponseWriter, r *http.Request, path string) {
	if s.jocks == nil {
		http.Error(w, "no jocks to manage", http.StatusServiceUnavailable)
		return
	}
	rest := strings.TrimPrefix(path, "/admin/jocks")

	if rest == "" || rest == "/" {
		switch r.Method {
		case http.MethodGet:
			s.listJocks(w, r)
		case http.MethodPost:
			s.saveJock(w, r, "", http.StatusCreated)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	id := strings.TrimPrefix(rest, "/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.saveJock(w, r, id, http.StatusNoContent)
	case http.MethodDelete:
		s.deleteJock(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveVoices is the sidecar's own list.
func (s *Server) serveVoices(w http.ResponseWriter, r *http.Request) {
	if s.voices == nil {
		http.Error(w, "no voice service", http.StatusServiceUnavailable)
		return
	}
	names, err := s.voices.Voices(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"voices": names})
}

// previewLine is what a voice says when nobody says what to say.
//
// It is written to EXERCISE a voice rather than to be pleasant: a couple of
// sentences with different rhythms, a station ident, and some numbers, which is
// most of what a DJ actually has to get through.
const previewLine = "You're listening to Jockora. That was a good one — " +
	"stay with me, there's more coming up after this."

// serveVoicePreview renders a line in one voice and returns the audio.
func (s *Server) serveVoicePreview(w http.ResponseWriter, r *http.Request) {
	if s.voices == nil {
		http.Error(w, "no voice service", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Voice string `json:"voice"`
		Text  string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.writeFieldError(w, "voice", "that request is not JSON")
		return
	}
	if body.Voice == "" {
		s.writeFieldError(w, "voice", "which voice?")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		text = previewLine
	}
	// Bounded, because this is a synthesis request an operator can repeat as
	// fast as they can click, and the sidecar is the same one the DJ uses.
	if len(text) > 300 {
		text = text[:300]
	}

	audio, err := s.voices.Preview(r.Context(), body.Voice, text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	// A write failure here means the operator's connection went away, which is
	// not something this handler can do anything about.
	_, _ = w.Write(audio) //nolint:errcheck // client hung up
}

func (s *Server) listJocks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.jocks.ListJocks(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, rows)
}

// saveJock creates or replaces a persona card.
//
// One handler for both, because a persona card is written WHOLE: the console
// edits a form and submits it, and a partial update would need the caller to
// know which fields were touched.
func (s *Server) saveJock(w http.ResponseWriter, r *http.Request, id string, ok int) {
	var body store.Jock
	if !s.decode(w, r, &body) {
		return
	}
	if id != "" {
		body.ID = id
	}
	if field, msg := validateJock(body); field != "" {
		s.writeFieldError(w, field, msg)
		return
	}
	if err := s.checkVoice(r.Context(), body.VoiceID); err != nil {
		s.writeFieldError(w, "voice_id", err.Error())
		return
	}

	if err := s.jocks.UpsertJock(r.Context(), body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The edit reaches a RUNNING station on its next break, rather than
	// restarting it: the persona is already read per break, so a listener
	// hears the change without hearing a gap.
	if s.personas != nil {
		s.personas.PersonaChanged(body)
	}
	if ok == http.StatusCreated {
		s.writeJSON(w, ok, map[string]any{"id": body.ID})
		return
	}
	w.WriteHeader(ok)
}

// checkVoice refuses a voice the sidecar cannot produce.
func (s *Server) checkVoice(ctx context.Context, voice string) error {
	if s.voices == nil {
		// No sidecar to ask. Refusing every jock because the voice service is
		// down would make the console useless during an outage.
		return nil
	}
	names, err := s.voices.Voices(ctx)
	if err != nil {
		return nil
	}
	if !slices.Contains(names, voice) {
		return errors.New("no voice by that name: " + strings.Join(names, ", "))
	}
	return nil
}

// deleteJock removes a persona and UNASSIGNS it from every station.
//
// The schema's ON DELETE SET NULL already does this; the API reports WHICH
// stations lost their jock, because a station that went quiet without anyone
// saying so is the hardest kind of change to trace back.
func (s *Server) deleteJock(w http.ResponseWriter, r *http.Request, id string) {
	stations, err := s.jocks.ListStations(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	unassigned := []int64{}
	for _, st := range stations {
		if st.JockID == id {
			unassigned = append(unassigned, st.ID)
		}
	}

	err = s.jocks.DeleteJock(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such jock", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"unassigned": unassigned})
}

// validateJock refuses a card the break writer could not use.
func validateJock(j store.Jock) (field, msg string) {
	switch {
	case strings.TrimSpace(j.ID) == "":
		return "id", "an id is required"
	case strings.TrimSpace(j.Name) == "":
		return "name", "a name is required"
	case strings.TrimSpace(j.VoiceID) == "":
		return "voice_id", "a voice is required"
	case strings.TrimSpace(j.SpeechStyle) == "":
		// Without these two the prompt has nothing to characterise, and the
		// jock comes out as a generic announcer -- which is exactly the thing
		// this product exists not to be.
		return "speech_style", "a speech style is required"
	case strings.TrimSpace(j.Personality) == "":
		return "personality", "a personality is required"
	}
	// Forbidden may be empty: a jock with no prohibitions is a jock who has
	// not needed one yet.
	return "", ""
}
