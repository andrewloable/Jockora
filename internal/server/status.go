// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"net/http"
	"sync"
)

// StatusSource supplies the numbers /now.json reports.
//
// It is an interface so the endpoint reads CACHED COUNTERS rather than querying
// anything: the page polls this every few seconds, and a status endpoint that
// costs a library scan is a status endpoint nobody can afford to look at.
type StatusSource interface {
	Status() Status
}

// Track is what a listener may be told about a track.
//
// Artist and title only. No path: the endpoint is a debugging surface, and a
// filesystem layout is not something to hand out even on loopback.
type Track struct {
	Artist   string  `json:"artist"`
	Title    string  `json:"title"`
	ElapsedS float64 `json:"elapsed_s,omitempty"`
}

// LastBreak describes the most recent break that actually aired.
type LastBreak struct {
	Text      string `json:"text"`
	Placement string `json:"placement"`
	AiredAt   int64  `json:"aired_at"`
}

// Health reports each operator-supplied dependency.
type Health struct {
	LLM    string `json:"llm"`
	TTS    string `json:"tts"`
	FFmpeg string `json:"ffmpeg"`
}

// Metrics are the counters that make quiet degradation visible.
type Metrics struct {
	BreakDropRate   float64 `json:"break_drop_rate"`
	RingOccupancyS  float64 `json:"ring_occupancy_s"`
	Underruns       uint64  `json:"underruns"`
	EncoderRestarts int     `json:"encoder_restarts"`

	// The pacing figures a long unattended run is judged on. They are here so a
	// soak harness can WATCH them rather than only read them in the autopsy at
	// shutdown: a run that degrades at minute 12 and recovers by minute 30 looks
	// identical to a healthy one if you only see the final numbers.
	P99GapMs          float64 `json:"p99_inter_write_gap_ms"`
	MaxGapMs          float64 `json:"max_inter_write_gap_ms"`
	MinRingOccupancyS float64 `json:"min_ring_occupancy_s"`
	DriftMs           float64 `json:"drift_ms"`
}

// Enrichment reports background progress.
//
// Stream-first onboarding runs enrichment behind the stream, so "how far along
// is it, and is it still moving?" is a live question during normal use. Without
// it a stalled queue and a finished one look identical from outside, which is
// the same silent-degradation problem the rest of this endpoint exists to solve.
type Enrichment struct {
	Done             int            `json:"done"`
	Total            int            `json:"total"`
	Pct              float64        `json:"pct"`
	Running          bool           `json:"running"`
	LastCompletedAt  int64          `json:"last_completed_at"`
	ConfidenceCounts map[string]int `json:"confidence_counts"`
}

// Status is the whole /now.json payload.
type Status struct {
	Now        *Track      `json:"now"`
	Next       *Track      `json:"next"`
	LastBreak  *LastBreak  `json:"last_break"`
	Health     Health      `json:"health"`
	Metrics    Metrics     `json:"metrics"`
	Enrichment *Enrichment `json:"enrichment,omitempty"`
}

// StaticStatus is a StatusSource backed by a value, for wiring and tests.
type StaticStatus struct {
	mu sync.RWMutex
	s  Status
}

func (s *StaticStatus) Set(v Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s = v
}

func (s *StaticStatus) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.s
}

// serveStatus writes the status payload.
//
// It lives at /now.json, not under /hls/: it is not a media route, and putting
// it there would drag it through the segment allowlist.
func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	if s.status == nil {
		http.Error(w, "status unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// The whole point is that it changes; a cached one is useless.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.status.Status()); err != nil {
		s.log.Warn("writing status", "err", err)
	}
}
