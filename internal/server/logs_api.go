// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andrewloable/jockora/internal/obs"
)

// The operator's window onto what the server is doing.
//
// THE FIRST STREAMING ENDPOINT IN THIS CODEBASE THAT IS NOT HLS. There was no
// SSE anywhere before this, so none of it is copy-and-paste: the headers, the
// flush, the heartbeat and the unsubscribe are each a defect if omitted, and
// the symptom of most of them is a blank page with no error at all.

// DefaultLogLimit and MaxLogLimit bound what one request may ask for.
//
// A limit query is an operator-supplied number reaching a slice, so it is
// clamped rather than trusted. 200 is a screenful of scrollback; 1000 is as far
// back as anyone reads before they want a filter instead.
const (
	DefaultLogLimit = 200
	MaxLogLimit     = 1000
)

// LogHeartbeat is how often an idle stream says it is alive.
//
// A STATION CAN BE QUIET FOR A LONG TIME and that is the normal case, so an
// idle connection must not look dead to a proxy that closes what it thinks is
// a stalled request.
const LogHeartbeat = 20 * time.Second

// Logs is the operator's view of the log, implemented by the app.
type Logs interface {
	RecentLogs(ctx context.Context, minLevel string, limit int) ([]obs.Record, error)
	SubscribeLogs(buffer int) (<-chan obs.Record, func(), error)
	ClearLogs(ctx context.Context) error
	LogLevel() slog.Level
	SetLogLevel(ctx context.Context, l slog.Level) error
	DroppedLogs() int64
}

// SetLogs wires the log view. Without one every route reports 503: the routes
// exist, the capability does not.
func (s *Server) SetLogs(l Logs) { s.logs = l }

// serveLogs handles /admin/logs, /admin/logs/stream and /admin/logs/level.
func (s *Server) serveLogs(w http.ResponseWriter, r *http.Request, path string) {
	if s.logs == nil {
		s.writeJSON(w, http.StatusServiceUnavailable,
			map[string]any{"error": "no log view available"})
		return
	}
	// Matched by NAME before anything that could parse an id. There are no ids
	// here, and the split is written this way so adding one later does not
	// swallow these two.
	switch action := strings.Trim(strings.TrimPrefix(path, "/admin/logs"), "/"); {
	case action == "stream" && r.Method == http.MethodGet:
		s.streamLogs(w, r)
	case action == "level" && r.Method == http.MethodPost:
		s.setLogLevel(w, r)
	case action == "" && r.Method == http.MethodGet:
		s.recentLogs(w, r)
	case action == "" && r.Method == http.MethodDelete:
		s.clearLogs(w, r)
	default:
		s.writeJSON(w, http.StatusNotFound, map[string]any{"error": "no such log route"})
	}
}

// recentLogs is the ring merged with the persisted warnings, newest first.
func (s *Server) recentLogs(w http.ResponseWriter, r *http.Request) {
	level := r.URL.Query().Get("level")
	if level == "" {
		level = slog.LevelInfo.String()
	}
	recs, err := s.logs.RecentLogs(r.Context(), level, logLimit(r))
	if err != nil {
		s.writeFieldError(w, "level", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"records": recs,
		// THE DROPPED COUNT, so the console can say records were lost rather
		// than leaving a gap the operator has to notice for themselves.
		"dropped": s.logs.DroppedLogs(),
		"level":   s.logs.LogLevel().String(),
	})
}

// logLimit clamps an operator-supplied number before it reaches a slice.
func logLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return DefaultLogLimit
	}
	if n > MaxLogLimit {
		return MaxLogLimit
	}
	return n
}

// streamLogs is the live view.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	// REFUSED CLEARLY rather than streaming into a writer nobody flushes,
	// which delivers nothing until the request ends -- and for a live stream
	// that is never.
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeJSON(w, http.StatusInternalServerError,
			map[string]any{"error": "this connection cannot stream: the response writer does not flush"})
		return
	}

	ch, unsubscribe, err := s.logs.SubscribeLogs(64)
	if err != nil {
		// A 503 WITH WORDS, not a hang. The operator has too many tabs open
		// and that is a thing they can act on.
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
		return
	}
	// ALWAYS. A leaked subscriber is a leaked channel the sink drops into for
	// ever, counting every record as lost.
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// A BUFFERING REVERSE PROXY in front of this turns a live stream into
	// nothing at all, and the symptom is a blank page with no error.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Immediately, so a client knows the connection is alive before the first
	// record -- which on a healthy station may be minutes away.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	every := s.heartbeat
	if every <= 0 {
		every = LogHeartbeat
	}
	beat := time.NewTicker(every)
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case rec, open := <-ch:
			if !open {
				return
			}
			// A Record is a time, a level, two strings and a string map, so
			// this cannot fail -- and a branch no input can reach is a branch
			// no test can cover honestly. The same idiom as llmconfig.go.
			body, _ := json.Marshal(rec) //nolint:errcheck // cannot fail for a Record
			fmt.Fprintf(w, "data: %s\n\n", body)
			flusher.Flush()
		}
	}
}

// setLogLevel turns debug on and off without a restart.
func (s *Server) setLogLevel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Level string `json:"level"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	l, err := obs.ParseLevel(body.Level)
	if err != nil {
		s.writeFieldError(w, "level", err.Error())
		return
	}
	if err := s.logs.SetLogLevel(r.Context(), l); err != nil {
		s.writeFieldError(w, "level", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"level": l.String()})
}

// clearLogs empties the ring and the table, and then says so.
func (s *Server) clearLogs(w http.ResponseWriter, r *http.Request) {
	if err := s.logs.ClearLogs(r.Context()); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	// AFTER THE CLEAR, and naming who did it, so the record of who emptied the
	// log survives the emptying. One line, and it is the difference between an
	// audit trail and a hole.
	who := "an operator"
	if _, user, err := s.session(r); err == nil {
		who = user.Name
	}
	s.log.Warn("log records cleared", "by", who)
	s.writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}
