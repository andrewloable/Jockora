// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
)

var mbEpoch = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

// mbArtist is a minimal MusicBrainz artist search reply.
func mbArtist(name, country, typ, begin string) map[string]any {
	return map[string]any{
		"artists": []map[string]any{{
			"id":             "abc-123",
			"name":           name,
			"country":        country,
			"type":           typ,
			"disambiguation": "the one from Manchester",
			"life-span":      map[string]any{"begin": begin},
		}},
	}
}

func TestMusicBrainzThrottleEnforcesOnePerSecond(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		json.NewEncoder(w).Encode(mbArtist("A", "GB", "Group", "1985")) //nolint:errcheck
	}))
	defer srv.Close()

	fake := clock.NewFake(mbEpoch)
	mb := NewMusicBrainzWithThrottle(srv.URL, srv.Client(), fake, &Throttle{})

	for i := 0; i < 5; i++ {
		if _, err := mb.ArtistFacts(context.Background(), "Artist"); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	if hits != 5 {
		t.Fatalf("server saw %d requests, want 5", hits)
	}
	elapsed := fake.Now().Sub(mbEpoch)
	if elapsed < 4*time.Second {
		t.Errorf("five requests consumed %v of simulated time, want at least 4s: "+
			"MusicBrainz allows one request per second and will block the IP otherwise", elapsed)
	}
	t.Logf("five requests took %v of simulated time", elapsed)
}

// TestMusicBrainzThrottleCostIsStated turns the rate limit into the number that
// actually matters: how long enriching a real library takes.
func TestMusicBrainzThrottleCostIsStated(t *testing.T) {
	if ThrottleInterval != time.Second {
		t.Fatalf("ThrottleInterval = %v, want 1s", ThrottleInterval)
	}
	// 10,000 tracks at one request per second.
	floor := time.Duration(10000) * ThrottleInterval
	if floor < 2*time.Hour {
		t.Errorf("a 10k-track library floors at %v, which contradicts the 2.8 hour estimate", floor)
	}
}

func TestMusicBrainzRetriesOn503WithBackoff(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(mbArtist("Recovered", "US", "Person", "1970")) //nolint:errcheck
	}))
	defer srv.Close()

	fake := clock.NewFake(mbEpoch)
	mb := NewMusicBrainzWithThrottle(srv.URL, srv.Client(), fake, &Throttle{})

	got, err := mb.ArtistFacts(context.Background(), "Artist")
	if err != nil {
		t.Fatalf("ArtistFacts: %v", err)
	}
	if got.Name != "Recovered" {
		t.Errorf("Name = %q, want Recovered", got.Name)
	}
	if hits != 3 {
		t.Errorf("server saw %d requests, want 3 (two failures then success)", hits)
	}

	// Backoff sleeps are distinguishable from throttle sleeps: the throttle
	// never waits more than its one-second interval.
	var backoffs []time.Duration
	for _, d := range fake.SleepCalls() {
		if d >= BaseBackoff {
			backoffs = append(backoffs, d)
		}
	}
	if len(backoffs) < 2 {
		t.Fatalf("recorded %d backoff sleeps, want 2: %v", len(backoffs), fake.SleepCalls())
	}
	if backoffs[1] <= backoffs[0] {
		t.Errorf("backoff did not increase: %v then %v", backoffs[0], backoffs[1])
	}
	t.Logf("backoff sequence: %v", backoffs)
}

func TestMusicBrainzGivesUpAfter3Retries(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	fake := clock.NewFake(mbEpoch)
	mb := NewMusicBrainzWithThrottle(srv.URL, srv.Client(), fake, &Throttle{})

	_, err := mb.ArtistFacts(context.Background(), "Artist")
	if !errors.Is(err, ErrEnrichSourceMiss) {
		t.Fatalf("err = %v, want ErrEnrichSourceMiss", err)
	}
	if hits != MaxAttempts {
		t.Errorf("server saw %d requests, want exactly %d", hits, MaxAttempts)
	}
}

// TestMusicBrainzNotFoundIsNotAnError: a self-hosted library is full of artists
// MusicBrainz has never heard of, and an empty dossier is a supported outcome.
func TestMusicBrainzNotFoundIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	got, err := NewMusicBrainzWithThrottle(srv.URL, srv.Client(), clock.NewFake(mbEpoch), &Throttle{}).
		ArtistFacts(context.Background(), "Nobody At All")
	if err != nil {
		t.Fatalf("a 404 is normal, not an error: %v", err)
	}
	if got.Found {
		t.Error("Found is true for a 404")
	}
	if got.Name != "" {
		t.Errorf("Name = %q, want empty", got.Name)
	}
}

// TestMusicBrainzEmptyResultIsNotAnError: a 200 with no artists is the same
// outcome as a 404 and must not be reported differently.
func TestMusicBrainzEmptyResultIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"artists": []any{}}) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := NewMusicBrainzWithThrottle(srv.URL, srv.Client(), clock.NewFake(mbEpoch), &Throttle{}).
		ArtistFacts(context.Background(), "Nobody")
	if err != nil {
		t.Fatalf("an empty result set is not an error: %v", err)
	}
	if got.Found {
		t.Error("Found is true for an empty result set")
	}
}

func TestMusicBrainzSendsContactableUserAgent(t *testing.T) {
	var ua, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua, query = r.Header.Get("User-Agent"), r.URL.RawQuery
		json.NewEncoder(w).Encode(mbArtist("A", "GB", "Group", "1985")) //nolint:errcheck
	}))
	defer srv.Close()

	if _, err := NewMusicBrainzWithThrottle(srv.URL, srv.Client(), clock.NewFake(mbEpoch), &Throttle{}).
		ArtistFacts(context.Background(), "Artist"); err != nil {
		t.Fatal(err)
	}

	// MusicBrainz blocks clients with a generic or missing User-Agent, and asks
	// for a contact URL specifically.
	low := strings.ToLower(ua)
	if !strings.Contains(low, "jockora") {
		t.Errorf("User-Agent = %q, want it to name the application", ua)
	}
	if !strings.Contains(low, "http") {
		t.Errorf("User-Agent = %q, want it to carry a contact URL", ua)
	}
	if strings.Contains(low, "go-http-client") {
		t.Errorf("User-Agent = %q is the Go default; MusicBrainz blocks it", ua)
	}
	for _, want := range []string{"query=", "fmt=json", "limit=1"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q is missing %s", query, want)
		}
	}
}

func TestMusicBrainzParsesFacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mbArtist("New Order", "GB", "Group", "1980-07")) //nolint:errcheck
	}))
	defer srv.Close()

	got, err := NewMusicBrainzWithThrottle(srv.URL, srv.Client(), clock.NewFake(mbEpoch), &Throttle{}).
		ArtistFacts(context.Background(), "New Order")
	if err != nil {
		t.Fatal(err)
	}

	if !got.Found {
		t.Fatal("Found is false for a populated reply")
	}
	if got.Name != "New Order" || got.Country != "GB" || got.Type != "Group" {
		t.Errorf("facts = %+v", got)
	}
	if got.BeginYear != 1980 {
		t.Errorf("BeginYear = %d, want 1980 (parsed from 1980-07)", got.BeginYear)
	}
	if got.MBID != "abc-123" {
		t.Errorf("MBID = %q", got.MBID)
	}
	if got.Source != SourceMusicBrainz {
		t.Errorf("Source = %q, want %q", got.Source, SourceMusicBrainz)
	}
}

func TestMusicBrainzHonoursContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mbArtist("A", "GB", "Group", "1985")) //nolint:errcheck
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewMusicBrainzWithThrottle(srv.URL, srv.Client(), clock.NewFake(mbEpoch), &Throttle{}).
		ArtistFacts(ctx, "Artist"); err == nil {
		t.Error("a cancelled context was ignored")
	}
}

// TestMusicBrainzThrottleIsProcessWide: the limit is per IP, so two clients in
// one process must share it rather than each getting a request per second.
func TestMusicBrainzThrottleIsProcessWide(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(mbArtist("A", "GB", "Group", "1985")) //nolint:errcheck
	}))
	defer srv.Close()

	// Deliberately NOT isolated: these two share one throttle, exactly as two
	// clients in a real process share DefaultThrottle.
	fake := clock.NewFake(mbEpoch)
	shared := &Throttle{}
	a := NewMusicBrainzWithThrottle(srv.URL, srv.Client(), fake, shared)
	b := NewMusicBrainzWithThrottle(srv.URL, srv.Client(), fake, shared)

	for i := 0; i < 3; i++ {
		if _, err := a.ArtistFacts(context.Background(), "A"); err != nil {
			t.Fatal(err)
		}
		if _, err := b.ArtistFacts(context.Background(), "B"); err != nil {
			t.Fatal(err)
		}
	}

	elapsed := fake.Now().Sub(mbEpoch)
	if elapsed < 5*time.Second {
		t.Errorf("six requests across two clients took %v, want at least 5s: "+
			"the throttle is per IP and must be shared across the process", elapsed)
	}
}
