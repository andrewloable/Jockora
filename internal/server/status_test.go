// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func statusServer(t *testing.T, st Status) *Server {
	t.Helper()
	s, _ := newServer(t, 20)
	src := &StaticStatus{}
	src.Set(st)
	s.SetStatusSource(src)
	return s
}

func fullStatus() Status {
	return Status{
		Now:  &Track{Artist: "New Order", Title: "Blue Monday", ElapsedS: 92.5},
		Next: &Track{Artist: "The Cure", Title: "A Forest"},
		LastBreak: &LastBreak{
			Text:      "Three in the morning and that one still holds up.",
			Placement: "outro", AiredAt: 1788600000,
		},
		Health: Health{LLM: "ok", TTS: "ok", FFmpeg: "ok"},
		Metrics: Metrics{
			BreakDropRate: 0.08, RingOccupancyS: 4.2, Underruns: 0, EncoderRestarts: 0,
		},
		Enrichment: &Enrichment{
			Done: 412, Total: 4197, Pct: 9.8, Running: true,
			LastCompletedAt:  1788600000,
			ConfidenceCounts: map[string]int{"high": 380, "low": 24, "none": 8},
		},
	}
}

// getStatus asks about ONE STATION. Since 13g /now.json without a station
// carries no now-playing at all: four running stations are four different
// tracks, and naming one of them "now" would tell three quarters of the
// listeners something false.
func getStatus(t *testing.T, s *Server) map[string]any {
	t.Helper()
	rec := get(t, s, "/now.json?station=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /now.json returned %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	return out
}

func TestStatusReturnsNowPlaying(t *testing.T) {
	got := getStatus(t, statusServer(t, fullStatus()))

	now, ok := got["now"].(map[string]any)
	if !ok {
		t.Fatalf("no now object: %v", got)
	}
	if now["artist"] != "New Order" || now["title"] != "Blue Monday" {
		t.Errorf("now = %v", now)
	}
	if now["elapsed_s"] != 92.5 {
		t.Errorf("elapsed_s = %v, want 92.5", now["elapsed_s"])
	}
}

func TestStatusReturnsNextTrack(t *testing.T) {
	got := getStatus(t, statusServer(t, fullStatus()))

	next, ok := got["next"].(map[string]any)
	if !ok {
		t.Fatalf("no next object: %v", got)
	}
	if next["artist"] != "The Cure" || next["title"] != "A Forest" {
		t.Errorf("next = %v", next)
	}
}

func TestStatusReturnsLastBreakText(t *testing.T) {
	got := getStatus(t, statusServer(t, fullStatus()))

	lb, ok := got["last_break"].(map[string]any)
	if !ok {
		t.Fatalf("no last_break object: %v", got)
	}
	if !strings.Contains(lb["text"].(string), "still holds up") {
		t.Errorf("last_break text = %v", lb["text"])
	}
	if lb["placement"] != "outro" {
		t.Errorf("placement = %v", lb["placement"])
	}
}

func TestStatusReturnsDropRate(t *testing.T) {
	got := getStatus(t, statusServer(t, fullStatus()))

	m, ok := got["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("no metrics object: %v", got)
	}
	if m["break_drop_rate"] != 0.08 {
		t.Errorf("break_drop_rate = %v, want 0.08", m["break_drop_rate"])
	}
	for _, k := range []string{"ring_occupancy_s", "underruns", "encoder_restarts"} {
		if _, ok := m[k]; !ok {
			t.Errorf("metrics is missing %s", k)
		}
	}
}

func TestStatusReturnsDependencyHealth(t *testing.T) {
	st := fullStatus()
	st.Health = Health{LLM: "ok", TTS: "sidecar not responding", FFmpeg: "ok"}
	got := getStatus(t, statusServer(t, st))

	h, ok := got["health"].(map[string]any)
	if !ok {
		t.Fatalf("no health object: %v", got)
	}
	for _, k := range []string{"llm", "tts", "ffmpeg"} {
		v, ok := h[k].(string)
		if !ok || v == "" {
			t.Errorf("health.%s = %v, want ok or a failure reason", k, h[k])
		}
	}
	if h["tts"] != "sidecar not responding" {
		t.Errorf("a failure reason was not reported: %v", h["tts"])
	}
}

// TestStatusNeverLeaksPaths: this is a debugging surface, and a filesystem
// layout is not something to hand out even on loopback.
func TestStatusNeverLeaksPaths(t *testing.T) {
	body := get(t, statusServer(t, fullStatus()), "/now.json").Body.String()

	for _, leak := range []string{"/music/", "/Users/", "/home/", "/var/", ".mp3", ".flac", ".ts"} {
		if strings.Contains(body, leak) {
			t.Errorf("the status payload leaks a filesystem path fragment %q:\n%s", leak, body)
		}
	}
	// And the Track type structurally has nowhere to put one.
	raw, err := json.Marshal(Track{Artist: "a", Title: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "path") {
		t.Errorf("Track carries a path field: %s", raw)
	}
}

// TestStatusReportsEnrichmentProgress: with enrichment running behind the
// stream, a stalled queue and a finished one otherwise look identical.
func TestStatusReportsEnrichmentProgress(t *testing.T) {
	got := getStatus(t, statusServer(t, fullStatus()))

	e, ok := got["enrichment"].(map[string]any)
	if !ok {
		t.Fatalf("no enrichment object: %v", got)
	}
	if e["done"] != float64(412) || e["total"] != float64(4197) {
		t.Errorf("enrichment = %v", e)
	}
	if e["pct"] != 9.8 {
		t.Errorf("pct = %v", e["pct"])
	}
	if e["running"] != true {
		t.Error("running is not reported")
	}
	if e["last_completed_at"] == nil {
		t.Error("last_completed_at is missing; a stalled queue would look like a finished one")
	}
	counts, ok := e["confidence_counts"].(map[string]any)
	if !ok {
		t.Fatalf("no confidence_counts: %v", e)
	}
	if counts["high"] != float64(380) || counts["none"] != float64(8) {
		t.Errorf("confidence_counts = %v", counts)
	}
}

func TestStatusIsNotCached(t *testing.T) {
	rec := get(t, statusServer(t, fullStatus()), "/now.json")
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") && !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q; a cached status endpoint is useless", cc)
	}
}

func TestStatusWithoutSourceIsUnavailable(t *testing.T) {
	s, _ := newServer(t, 20)
	rec := get(t, s, "/now.json")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d with no source, want 503 rather than a fabricated healthy payload", rec.Code)
	}
}

// TestStatusIsNotUnderHLS: it is not a media route and must not be dragged
// through the segment allowlist.
func TestStatusIsNotUnderHLS(t *testing.T) {
	s := statusServer(t, fullStatus())

	if rec := get(t, s, "/hls/now.json"); rec.Code == http.StatusOK {
		t.Error("the status endpoint is reachable under /hls/")
	}
}
