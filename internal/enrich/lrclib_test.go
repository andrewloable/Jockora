// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

// lrcServer serves one canned LRCLIB response and records the request.
func lrcServer(t *testing.T, body any) (*httptest.Server, *http.Request) {
	t.Helper()
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// fetch runs a lookup against a canned body.
func fetch(t *testing.T, body any, duration float64) (Ramp, error) {
	t.Helper()
	srv, _ := lrcServer(t, body)
	c := NewLRCLib(srv.URL, srv.Client())
	return c.FetchRamp(context.Background(), "Artist", "Title", duration)
}

func TestLRCParseSyncedLyricsDerivesRampAndOutro(t *testing.T) {
	got, err := fetch(t, map[string]any{
		"syncedLyrics": "[00:14.20] first line\n[01:30.00] middle\n[03:05.00] last line\n",
	}, 200)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}

	// 14.20 minus the 1.5s safety margin.
	if got.RampS != 12.7 {
		t.Errorf("RampS = %v, want 12.7", got.RampS)
	}
	// 200 - 185 (03:05.00).
	if got.OutroS != 15.0 {
		t.Errorf("OutroS = %v, want 15.0", got.OutroS)
	}
	if got.Confidence != ConfidenceLRC {
		t.Errorf("Confidence = %q, want %q", got.Confidence, ConfidenceLRC)
	}
}

func TestLRCClampsNegativeRamp(t *testing.T) {
	got, err := fetch(t, map[string]any{
		"syncedLyrics": "[00:00.50] straight in\n[02:00.00] later\n",
	}, 200)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}

	if got.RampS != 0 {
		t.Errorf("RampS = %v, want 0: a vocal 0.5s in leaves no room to talk", got.RampS)
	}
	if got.Confidence != ConfidenceNone {
		t.Errorf("Confidence = %q, want %q", got.Confidence, ConfidenceNone)
	}
}

func TestLRCClampsRampBeyondDuration(t *testing.T) {
	got, err := fetch(t, map[string]any{
		"syncedLyrics": "[10:00.00] way past the end\n[11:00.00] later still\n",
	}, 200)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}

	if got.RampS >= 200 {
		t.Errorf("RampS = %v, want it clamped below the 200s duration", got.RampS)
	}
	if got.Confidence != ConfidenceNone {
		t.Errorf("Confidence = %q, want %q", got.Confidence, ConfidenceNone)
	}
}

func TestLRCRejectsNonMonotonicTimestamps(t *testing.T) {
	got, err := fetch(t, map[string]any{
		"syncedLyrics": "[00:14.20] first\n[00:05.00] backwards\n[03:05.00] last\n",
	}, 200)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	if got.Confidence != ConfidenceNone {
		t.Errorf("Confidence = %q, want %q for timestamps that go backwards", got.Confidence, ConfidenceNone)
	}
}

func TestLRCNoSyncedLyricsReturnsNone(t *testing.T) {
	got, err := fetch(t, map[string]any{
		"plainLyrics":  "just words, no timings at all",
		"syncedLyrics": "",
	}, 200)
	if err != nil {
		t.Fatalf("unsynced lyrics should not be an error: %v", err)
	}
	if got.Confidence != ConfidenceNone {
		t.Errorf("Confidence = %q, want %q", got.Confidence, ConfidenceNone)
	}
	if got.RampS != 0 || got.OutroS != 0 {
		t.Errorf("got ramp %v outro %v, want zeros", got.RampS, got.OutroS)
	}
}

func TestLRCNotFoundIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := NewLRCLib(srv.URL, srv.Client()).FetchRamp(context.Background(), "A", "T", 200)
	if err != nil {
		t.Fatalf("a track with no lyrics entry is normal, not an error: %v", err)
	}
	if got.Confidence != ConfidenceNone {
		t.Errorf("Confidence = %q, want %q", got.Confidence, ConfidenceNone)
	}
}

// TestLRCLyricsAreNotStored is checked structurally: the type this package
// returns must have nowhere to put lyric text. Lyrics are copyrighted; they are
// read, reduced to two numbers, and discarded.
func TestLRCLyricsAreNotStored(t *testing.T) {
	rt := reflect.TypeOf(Ramp{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name := strings.ToLower(f.Name)
		if strings.Contains(name, "lyric") || strings.Contains(name, "text") || strings.Contains(name, "words") {
			t.Errorf("Ramp has a field %q that could hold lyric text", f.Name)
		}
		// Confidence is the only string, and it is a controlled vocabulary.
		if f.Type.Kind() == reflect.String && f.Name != "Confidence" {
			t.Errorf("Ramp has an uncontrolled string field %q; lyrics must have nowhere to live", f.Name)
		}
	}

	got, err := fetch(t, map[string]any{
		"syncedLyrics": "[00:14.20] a very distinctive lyric phrase\n[03:05.00] another one\n",
		"plainLyrics":  "a very distinctive lyric phrase",
	}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Confidence; s != ConfidenceLRC && s != ConfidenceNone {
		t.Errorf("Confidence = %q, outside the controlled vocabulary", s)
	}
}

func TestLRCSendsIdentifyingUserAgent(t *testing.T) {
	var ua, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		query = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"syncedLyrics": ""}) //nolint:errcheck
	}))
	defer srv.Close()

	if _, err := NewLRCLib(srv.URL, srv.Client()).FetchRamp(context.Background(), "The Artist", "The Track", 200); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(strings.ToLower(ua), "jockora") {
		t.Errorf("User-Agent = %q, want it to identify Jockora", ua)
	}
	for _, want := range []string{"artist_name=", "track_name=", "duration="} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %s", query, want)
		}
	}
	if !strings.Contains(query, "The+Artist") && !strings.Contains(query, "The%20Artist") {
		t.Errorf("query %q did not encode the artist name", query)
	}
}

func TestLRCMalformedTimestampsYieldNone(t *testing.T) {
	for _, lrc := range []string{
		"[not a timestamp] line\n",
		"[99:99.99] nonsense\n[00:10.00] more\n",
		"no brackets at all\n",
		"[00:14.20]\n",
	} {
		got, err := fetch(t, map[string]any{"syncedLyrics": lrc}, 200)
		if err != nil {
			t.Errorf("%q returned an error: %v", lrc, err)
			continue
		}
		if got.Confidence == ConfidenceLRC && (got.RampS < 0 || got.RampS >= 200 || got.OutroS < 0 || got.OutroS >= 200) {
			t.Errorf("%q produced out-of-range values with confidence %q: ramp=%v outro=%v",
				lrc, got.Confidence, got.RampS, got.OutroS)
		}
	}
}

func TestLRCServerErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := NewLRCLib(srv.URL, srv.Client()).FetchRamp(context.Background(), "A", "T", 200); err == nil {
		t.Error("a 500 from the API was not reported as an error")
	}
}

func TestLRCHonoursContextCancel(t *testing.T) {
	srv, _ := lrcServer(t, map[string]any{"syncedLyrics": ""})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewLRCLib(srv.URL, srv.Client()).FetchRamp(ctx, "A", "T", 200); err == nil {
		t.Error("a cancelled context was ignored")
	}
}

// TestLRCSingleTimestampIsRejected: one timestamp cannot establish an outro.
func TestLRCSingleTimestampIsRejected(t *testing.T) {
	got, err := fetch(t, map[string]any{"syncedLyrics": "[00:14.20] the only line\n"}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != ConfidenceNone {
		t.Errorf("Confidence = %q for a single timestamp, want %q", got.Confidence, ConfidenceNone)
	}
}

func TestLRCStoreRampWritesOnlyThreeColumns(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`INSERT INTO tracks (path, artist, title) VALUES (?, ?, ?)`,
		"/music/a.mp3", "Artist", "Title"); err != nil {
		t.Fatal(err)
	}

	if err := StoreRamp(context.Background(), s, "/music/a.mp3",
		Ramp{RampS: 12.7, OutroS: 15.0, Confidence: ConfidenceLRC}); err != nil {
		t.Fatal(err)
	}

	var ramp, outro float64
	var conf, artist string
	if err := s.DB().QueryRow(
		`SELECT ramp_s, outro_s, ramp_confidence, artist FROM tracks WHERE path = ?`,
		"/music/a.mp3").Scan(&ramp, &outro, &conf, &artist); err != nil {
		t.Fatal(err)
	}
	if ramp != 12.7 || outro != 15.0 || conf != ConfidenceLRC {
		t.Errorf("stored ramp=%v outro=%v conf=%q", ramp, outro, conf)
	}
	if artist != "Artist" {
		t.Errorf("StoreRamp overwrote an unrelated column: artist = %q", artist)
	}

	// Nothing lyric-shaped may exist anywhere in the database.
	var hits int
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM tracks WHERE artist LIKE ? OR title LIKE ? OR album LIKE ?`,
		"%lyric%", "%lyric%", "%lyric%").Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Errorf("%d rows contain lyric-shaped text", hits)
	}
}

// TestLRCFetchLyricsReturnsTransientText covers the one path that may carry
// lyric text: straight into a prompt, never into anything stored.
func TestLRCFetchLyricsReturnsTransientText(t *testing.T) {
	srv, _ := lrcServer(t, map[string]any{
		"syncedLyrics": "[00:14.20] first line here\n[03:05.00] last line here\n",
		"plainLyrics":  "first line here\nlast line here",
	})

	text, ramp, err := NewLRCLib(srv.URL, srv.Client()).
		FetchLyrics(context.Background(), "Artist", "Title", 200)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "first line here") {
		t.Errorf("lyric text = %q, want the plain lyrics", text)
	}
	if ramp.RampS != 12.7 || ramp.Confidence != ConfidenceLRC {
		t.Errorf("ramp = %+v, want the same derivation FetchRamp makes", ramp)
	}
}

// TestLRCFetchLyricsStripsTimestamps: an LLM does not need [00:14.20] markers,
// and they crowd the token budget.
func TestLRCFetchLyricsStripsTimestamps(t *testing.T) {
	srv, _ := lrcServer(t, map[string]any{
		"syncedLyrics": "[00:14.20] first line\n[03:05.00] last line\n",
		"plainLyrics":  "",
	})

	text, _, err := NewLRCLib(srv.URL, srv.Client()).
		FetchLyrics(context.Background(), "Artist", "Title", 200)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "[00:") {
		t.Errorf("timestamps survived into the text handed to the model: %q", text)
	}
	if !strings.Contains(text, "first line") {
		t.Errorf("text = %q, want the words kept", text)
	}
}

func TestLRCFetchLyricsNotFoundIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	text, ramp, err := NewLRCLib(srv.URL, srv.Client()).
		FetchLyrics(context.Background(), "A", "T", 200)
	if err != nil {
		t.Fatalf("a missing entry is not an error: %v", err)
	}
	if text != "" || ramp.Confidence != ConfidenceNone {
		t.Errorf("text=%q ramp=%+v, want empty", text, ramp)
	}
}
