// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every test here is named TestOnset*, because the task's VERIFY line is
// `go test ./internal/enrich/ -run TestOnset` and four of the five names it
// proposed -- TestInstrumentalYieldsFullIntroCapped, TestConfidenceIsAnalysis,
// TestClampingStillApplies, TestLRCWinsWhenBothAvailable -- do not match it.
// Seventh instance of that pattern in this plan.

func f64(v float64) *float64 { return &v }

// onsetServer answers /onset with fixed bounds, and fails the test if asked
// about anything else.
func onsetServer(t *testing.T, start, end *float64) *OnsetClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/onset" {
			t.Errorf("unexpected request to %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"vocal_start": start, "vocal_end": end})
	}))
	t.Cleanup(srv.Close)
	return NewOnsetClient(srv.URL, srv.Client())
}

// TestOnsetReturnsRampAndOutro uses the task's worked example -- a vocal from
// 14.2s to 185.0s of a 200s file -- but NOT the task's expected answer.
//
// The task wanted ramp 12.7 and outro 15.0, which is the arithmetic before any
// cap. The cap was added afterwards on measured evidence (see MaxAnalysisRamp)
// and it applies here: both numbers land on 10.0. The task's example predates
// the measurement, and the measurement is what keeps the DJ off the vocal.
func TestOnsetReturnsRampAndOutro(t *testing.T) {
	c := onsetServer(t, f64(14.2), f64(185.0))
	got, err := c.FetchRamp(context.Background(), "/music/a.flac", 200)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	// 14.2 - 1.5 = 12.7, capped to 10.0.
	if got.RampS != MaxAnalysisRamp {
		t.Errorf("RampS = %v, want %v (12.7 before the cap)", got.RampS, MaxAnalysisRamp)
	}
	// 200 - 185.0 = 15.0, capped to 10.0.
	if got.OutroS != MaxAnalysisRamp {
		t.Errorf("OutroS = %v, want %v (15.0 before the cap)", got.OutroS, MaxAnalysisRamp)
	}

	// Under the cap the arithmetic is exactly the task's, margin and all.
	c2 := onsetServer(t, f64(8.0), f64(194.0))
	got2, err := c2.FetchRamp(context.Background(), "/music/b.flac", 200)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	if got2.RampS != 8.0-SafetyMargin {
		t.Errorf("RampS = %v, want %v (8.0 onset minus the %.1fs safety margin)", got2.RampS, 8.0-SafetyMargin, SafetyMargin)
	}
	if got2.OutroS != 6.0 {
		t.Errorf("OutroS = %v, want 6.0 (200 duration minus the 194.0 last vocal)", got2.OutroS)
	}
}

// TestOnsetInstrumentalYieldsFullIntroCapped: with no vocal anywhere the DJ
// could in principle talk over the entire track, which is not a feature.
func TestOnsetInstrumentalYieldsFullIntroCapped(t *testing.T) {
	c := onsetServer(t, nil, nil)
	got, err := c.FetchRamp(context.Background(), "/music/instrumental.flac", 200)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	if got.RampS != MaxInstrumentalRamp {
		t.Errorf("RampS = %v on an instrumental, want the %v cap", got.RampS, MaxInstrumentalRamp)
	}
	if got.Confidence != ConfidenceAnalysis {
		t.Errorf("Confidence = %q, want %q", got.Confidence, ConfidenceAnalysis)
	}
	if got.RampS+got.OutroS >= 200 {
		t.Errorf("ramp %v + outro %v covers the whole %v-second track", got.RampS, got.OutroS, 200.0)
	}

	// A very short instrumental must not be talked over end to end either.
	short, err := c.FetchRamp(context.Background(), "/music/interlude.flac", 12)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	if short.RampS+short.OutroS >= 12 {
		t.Errorf("ramp %v + outro %v covers the whole 12-second interlude", short.RampS, short.OutroS)
	}
}

// TestOnsetConfidenceIsAnalysis keeps provenance visible. A number derived from
// a spectral guess must never be indistinguishable from one a human typed into
// an LRC file.
func TestOnsetConfidenceIsAnalysis(t *testing.T) {
	c := onsetServer(t, f64(14.2), f64(185.0))
	got, err := c.FetchRamp(context.Background(), "/music/a.flac", 200)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	if got.Confidence != ConfidenceAnalysis {
		t.Errorf("Confidence = %q, want %q", got.Confidence, ConfidenceAnalysis)
	}
	if got.Confidence == ConfidenceLRC {
		t.Error("analysis output claimed to be LRC data")
	}
}

// TestOnsetClampingStillApplies runs the SAME bad inputs through both paths.
// Analysis output needs the same validation community-submitted data gets, for
// a different reason and to the same effect, and the two must not drift.
func TestOnsetClampingStillApplies(t *testing.T) {
	cases := []struct {
		name       string
		start, end float64
		duration   float64
	}{
		{"vocal starts immediately, no room to talk", 0.5, 180, 200},
		{"vocal starts before the margin", 1.5, 180, 200},
		{"last vocal at the very end", 14.2, 200, 200},
		{"last vocal past the end", 14.2, 260, 200},
		{"zero duration", 14.2, 185, 0},
		{"negative duration", 14.2, 185, -5},
		{"vocal ends before it starts", 185, 14.2, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := onsetServer(t, f64(tc.start), f64(tc.end))
			got, err := c.FetchRamp(context.Background(), "/music/a.flac", tc.duration)
			if err != nil {
				t.Fatalf("FetchRamp: %v", err)
			}
			if got.Confidence != ConfidenceNone {
				t.Errorf("analysis path: confidence = %q (ramp %v outro %v), want %q",
					got.Confidence, got.RampS, got.OutroS, ConfidenceNone)
			}

			// The LRC path must reject the same shape, from equivalent input.
			lrc := DeriveRamp(lrcFor(tc.start, tc.end), tc.duration)
			if lrc.Confidence != ConfidenceNone {
				t.Errorf("LRC path accepted what the analysis path rejected: %+v", lrc)
			}
		})
	}
}

// lrcFor renders two vocal timestamps as synced-lyric text.
func lrcFor(start, end float64) string {
	return stamp(start) + "first line\n" + stamp(end) + "last line\n"
}

func stamp(s float64) string {
	if s < 0 {
		s = 0
	}
	m := int(s) / 60
	rem := s - float64(m*60)
	return "[" + itoa2(m) + ":" + fmt2(rem) + "]"
}

func itoa2(v int) string {
	if v < 10 {
		return "0" + string(rune('0'+v))
	}
	return string(rune('0'+v/10)) + string(rune('0'+v%10))
}

func fmt2(v float64) string {
	whole := int(v)
	frac := int((v - float64(whole)) * 100)
	return itoa2(whole) + "." + itoa2(frac)
}

// TestOnsetLRCWinsWhenBothAvailable: LRC is cheaper and more accurate, and the
// analysis server here fails the test if it is asked anything at all.
func TestOnsetLRCWinsWhenBothAvailable(t *testing.T) {
	analysis := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("ran audio analysis on a track that already had LRC data: %s", r.URL)
	}))
	defer analysis.Close()

	lrcRamp := Ramp{RampS: 12.7, OutroS: 15.0, Confidence: ConfidenceLRC}
	got, err := ResolveRamp(context.Background(), lrcRamp,
		NewOnsetClient(analysis.URL, analysis.Client()), "/music/a.flac", 200)
	if err != nil {
		t.Fatalf("ResolveRamp: %v", err)
	}
	if got != lrcRamp {
		t.Errorf("ResolveRamp = %+v, want the LRC ramp %+v untouched", got, lrcRamp)
	}
}

// TestOnsetRescuesTracksWithoutLRC is the population this whole task exists
// for: measured coverage on the real library is 41.5%, so most tracks arrive
// here with nothing.
func TestOnsetRescuesTracksWithoutLRC(t *testing.T) {
	c := onsetServer(t, f64(14.2), f64(185.0))
	got, err := ResolveRamp(context.Background(), Ramp{Confidence: ConfidenceNone}, c, "/music/untagged.flac", 200)
	if err != nil {
		t.Fatalf("ResolveRamp: %v", err)
	}
	if got.Confidence != ConfidenceAnalysis {
		t.Errorf("Confidence = %q, want %q: a track with no LRC entry is exactly what analysis is for", got.Confidence, ConfidenceAnalysis)
	}
	if got.RampS != MaxAnalysisRamp {
		t.Errorf("RampS = %v, want the %v cap", got.RampS, MaxAnalysisRamp)
	}
}

// TestOnsetSidecarFailureIsNotFatal: analysis is a nice-to-have. A dead sidecar
// means personality-only talk, never a stalled enrichment run.
func TestOnsetSidecarFailureIsNotFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	got, err := ResolveRamp(context.Background(), Ramp{Confidence: ConfidenceNone},
		NewOnsetClient(srv.URL, srv.Client()), "/music/a.flac", 200)
	if err != nil {
		t.Fatalf("ResolveRamp returned an error for an unavailable sidecar: %v", err)
	}
	if got.Confidence != ConfidenceNone {
		t.Errorf("Confidence = %q, want %q", got.Confidence, ConfidenceNone)
	}
}

func TestOnsetNilSourceIsAllowed(t *testing.T) {
	in := Ramp{Confidence: ConfidenceNone}
	got, err := ResolveRamp(context.Background(), in, nil, "/music/a.flac", 200)
	if err != nil {
		t.Fatalf("ResolveRamp with no analysis source: %v", err)
	}
	if got != in {
		t.Errorf("ResolveRamp = %+v, want the input unchanged", got)
	}
}

// TestOnsetQueueRescuesUntaggedTracks is the wiring test. Before analysis, a
// track with no artist or title tag skipped the ramp block entirely and got no
// ramp row at all -- and those are precisely the tracks LRCLIB can never answer
// for, so they are the ones analysis exists to rescue.
func TestOnsetQueueRescuesUntaggedTracks(t *testing.T) {
	s := queueStore(t, 1)
	if _, err := s.DB().Exec(`UPDATE tracks SET artist = '', title = '' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	q := newQueue(s, &countingLLM{})
	q.Onset = onsetServer(t, f64(14.2), f64(185.0))
	if err := q.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var confidence string
	var rampS float64
	if err := s.DB().QueryRow(
		`SELECT coalesce(ramp_confidence, ''), coalesce(ramp_s, 0) FROM tracks WHERE id = 1`).
		Scan(&confidence, &rampS); err != nil {
		t.Fatal(err)
	}
	if confidence != ConfidenceAnalysis {
		t.Errorf("ramp_confidence = %q, want %q for an untagged track", confidence, ConfidenceAnalysis)
	}
	if rampS != MaxAnalysisRamp {
		t.Errorf("ramp_s = %v, want the %v cap", rampS, MaxAnalysisRamp)
	}
}

// TestOnsetQueueStoresNothingWhenNeitherSourceLooked keeps a track that nobody
// asked about out of the coverage denominator. "No lookup happened" and
// "LRCLIB does not know this track" are different claims and the coverage
// figure depends on telling them apart.
func TestOnsetQueueStoresNothingWhenNeitherSourceLooked(t *testing.T) {
	s := queueStore(t, 1)
	q := newQueue(s, &countingLLM{}) // no Lyrics, no Onset
	if err := q.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var confidence *string
	if err := s.DB().QueryRow(`SELECT ramp_confidence FROM tracks WHERE id = 1`).Scan(&confidence); err != nil {
		t.Fatal(err)
	}
	if confidence != nil {
		t.Errorf("ramp_confidence = %q, want NULL: nothing looked, so there is no answer to record", *confidence)
	}
}

// TestOnsetCapsAnalysisRamp pins the measured safety mechanism. Against forty
// LRC-annotated tracks the detector places the first vocal too late often
// enough to matter, and the cap -- not the detection threshold, not a
// proportional shrink, both of which were tried -- is what bounds the damage:
// 17.7s of talking over singing at a 20s cap, 7.7s at 10s.
func TestOnsetCapsAnalysisRamp(t *testing.T) {
	// A detector that claims the vocal starts 90 seconds in. Uncapped this
	// would hand the DJ an 88.5-second ramp.
	c := onsetServer(t, f64(90.0), f64(100.0))
	got, err := c.FetchRamp(context.Background(), "/music/long-intro.flac", 300)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	if got.RampS != MaxAnalysisRamp {
		t.Errorf("RampS = %v, want the %v cap: an uncapped analysis ramp is the difference between 7.7s and 17.7s of talking over a vocal",
			got.RampS, MaxAnalysisRamp)
	}
	if got.OutroS != MaxAnalysisRamp {
		t.Errorf("OutroS = %v, want the %v cap", got.OutroS, MaxAnalysisRamp)
	}

	// A short, believable intro is left exactly as measured.
	c2 := onsetServer(t, f64(9.0), f64(190.0))
	got2, err := c2.FetchRamp(context.Background(), "/music/a.flac", 200)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	if got2.RampS != 7.5 {
		t.Errorf("RampS = %v, want 7.5: the cap must not touch a ramp already under it", got2.RampS)
	}
}

// TestOnsetUsesSidecarDurationWhenCallerHasNone closes a silent-failure path.
// Every ramp fails validation against a zero-length track, so a caller that did
// not know the duration got ConfidenceNone for a perfectly good detection --
// indistinguishable from "this track has no usable ramp".
func TestOnsetUsesSidecarDurationWhenCallerHasNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vocal_start": 8.0, "vocal_end": 194.0, "duration": 200.0,
		})
	}))
	defer srv.Close()

	got, err := NewOnsetClient(srv.URL, srv.Client()).FetchRamp(context.Background(), "/music/a.flac", 0)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	if got.Confidence != ConfidenceAnalysis {
		t.Fatalf("Confidence = %q with an unknown caller duration, want %q", got.Confidence, ConfidenceAnalysis)
	}
	if got.RampS != 6.5 {
		t.Errorf("RampS = %v, want 6.5", got.RampS)
	}
	if got.OutroS != 6.0 {
		t.Errorf("OutroS = %v, want 6.0 (the sidecar's 200s duration minus 194.0)", got.OutroS)
	}
}

// The caller's duration still wins when it has one: the database knows the
// track length from the scan, and ffprobe is more authoritative than a decode
// length rounded to an analysis frame.
func TestOnsetPrefersCallerDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vocal_start": 8.0, "vocal_end": 194.0, "duration": 999.0,
		})
	}))
	defer srv.Close()

	got, err := NewOnsetClient(srv.URL, srv.Client()).FetchRamp(context.Background(), "/music/a.flac", 200)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	if got.OutroS != 6.0 {
		t.Errorf("OutroS = %v, want 6.0 from the caller's 200s, not the sidecar's 999s", got.OutroS)
	}
}
