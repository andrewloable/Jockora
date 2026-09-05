// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
)

// ErrEnrichSourceMiss means an enrichment source could not be reached. It is not
// the same as the source having nothing to say, which is a normal outcome.
var ErrEnrichSourceMiss = errors.New("enrichment source unavailable")

// SourceMusicBrainz labels facts that came from MusicBrainz, so the DJ's
// assertions can be traced back to where they came from.
const SourceMusicBrainz = "musicbrainz"

const (
	// ThrottleInterval is MusicBrainz's published limit: one request per second,
	// per IP.
	//
	// This is part of the cost model, not an afterthought. Ten thousand tracks
	// is a 2.8 hour FLOOR on this call alone, independent of how fast the LLM
	// is, and exceeding it gets the IP blocked rather than throttled.
	ThrottleInterval = time.Second

	// BaseBackoff is the first retry delay after a 503, doubling thereafter. It
	// is deliberately larger than ThrottleInterval so the two are
	// distinguishable in a trace.
	BaseBackoff = 2 * time.Second

	// MaxAttempts is how many times a request is tried before giving up.
	MaxAttempts = 3

	// MusicBrainzUserAgent must name the application and carry a contact URL.
	// MusicBrainz blocks clients sending a generic or missing User-Agent, and
	// the Go default is one of them.
	MusicBrainzUserAgent = "Jockora/0.1 ( https://github.com/andrewloable/jockora )"
)

// Throttle paces requests to one per ThrottleInterval.
//
// It is a value rather than package state so that a caller can hold one
// deliberately, and so tests can isolate themselves. The DEFAULT is shared
// process-wide on purpose: the rate limit is per IP, so per-client throttles
// would multiply the request rate by the number of clients and get the address
// blocked.
type Throttle struct {
	mu   sync.Mutex
	next time.Time
}

// DefaultThrottle is the process-wide throttle every client uses unless given
// another. Sharing it is what keeps the whole process under one request per
// second.
var DefaultThrottle = &Throttle{}

// wait blocks until the next slot is available, by the given clock.
func (t *Throttle) wait(clk clock.Clock) {
	t.mu.Lock()
	now := clk.Now()
	var sleep time.Duration
	if t.next.After(now) {
		sleep = t.next.Sub(now)
		t.next = t.next.Add(ThrottleInterval)
	} else {
		t.next = now.Add(ThrottleInterval)
	}
	t.mu.Unlock()

	if sleep > 0 {
		clk.Sleep(sleep)
	}
}

// ArtistFacts is what MusicBrainz can tell us about an artist.
//
// Found distinguishes "we asked and there is nothing" from "we never asked",
// which matters because the DJ may only assert facts that are actually present.
type ArtistFacts struct {
	Found          bool
	MBID           string
	Name           string
	Country        string
	Type           string // Group, Person, ...
	Disambiguation string
	BeginYear      int
	Source         string
}

// MusicBrainz is a throttled client for the MusicBrainz web service.
type MusicBrainz struct {
	baseURL  string
	client   *http.Client
	clk      clock.Clock
	throttle *Throttle
}

// NewMusicBrainz returns a client. baseURL and clk are injected so tests never
// touch the live service and never wait real seconds.
func NewMusicBrainz(baseURL string, client *http.Client, clk clock.Clock) *MusicBrainz {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if clk == nil {
		clk = clock.Real{}
	}
	return NewMusicBrainzWithThrottle(baseURL, client, clk, DefaultThrottle)
}

// NewMusicBrainzWithThrottle returns a client pacing against a specific
// throttle. Use it only when isolation is genuinely wanted: sharing
// DefaultThrottle is what keeps the process inside the per-IP rate limit.
func NewMusicBrainzWithThrottle(baseURL string, client *http.Client, clk clock.Clock, t *Throttle) *MusicBrainz {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if clk == nil {
		clk = clock.Real{}
	}
	if t == nil {
		t = DefaultThrottle
	}
	return &MusicBrainz{baseURL: strings.TrimSuffix(baseURL, "/"), client: client, clk: clk, throttle: t}
}

type mbSearchResponse struct {
	Artists []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Country        string `json:"country"`
		Type           string `json:"type"`
		Disambiguation string `json:"disambiguation"`
		LifeSpan       struct {
			Begin string `json:"begin"`
		} `json:"life-span"`
	} `json:"artists"`
}

// ArtistFacts looks an artist up, respecting the rate limit and retrying a
// temporarily unavailable service.
//
// An unknown artist returns empty facts and a nil error: a self-hosted library
// is full of artists MusicBrainz has never heard of, and an empty dossier is a
// supported outcome that produces personality-only talk.
func (m *MusicBrainz) ArtistFacts(ctx context.Context, name string) (ArtistFacts, error) {
	q := url.Values{}
	q.Set("query", name)
	q.Set("fmt", "json")
	q.Set("limit", "1")
	endpoint := m.baseURL + "/ws/2/artist?" + q.Encode()

	backoff := BaseBackoff
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return ArtistFacts{}, err
		}
		m.throttle.wait(m.clk)

		facts, retryable, err := m.attempt(ctx, endpoint)
		if err == nil {
			return facts, nil
		}
		if !retryable {
			return ArtistFacts{}, err
		}
		if attempt < MaxAttempts {
			m.clk.Sleep(backoff)
			backoff *= 2
		}
	}

	return ArtistFacts{}, fmt.Errorf("%w: musicbrainz did not answer after %d attempts",
		ErrEnrichSourceMiss, MaxAttempts)
}

// attempt makes one request. retryable distinguishes a temporary failure from a
// permanent one.
func (m *MusicBrainz) attempt(ctx context.Context, endpoint string) (facts ArtistFacts, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ArtistFacts{}, false, fmt.Errorf("enrich: building musicbrainz request: %w", err)
	}
	req.Header.Set("User-Agent", MusicBrainzUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ArtistFacts{}, false, ctx.Err()
		}
		return ArtistFacts{}, true, fmt.Errorf("%w: %v", ErrEnrichSourceMiss, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// Not an error. Obscure artists are the common case.
		return ArtistFacts{}, false, nil
	case resp.StatusCode == http.StatusServiceUnavailable,
		resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode >= 500:
		return ArtistFacts{}, true, fmt.Errorf("%w: musicbrainz returned %s", ErrEnrichSourceMiss, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return ArtistFacts{}, false, fmt.Errorf("enrich: musicbrainz returned %s", resp.Status)
	}

	var body mbSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ArtistFacts{}, false, fmt.Errorf("enrich: decoding musicbrainz reply: %w", err)
	}
	if len(body.Artists) == 0 {
		// Same outcome as a 404, and must not be reported differently.
		return ArtistFacts{}, false, nil
	}

	a := body.Artists[0]
	return ArtistFacts{
		Found:          true,
		MBID:           a.ID,
		Name:           a.Name,
		Country:        a.Country,
		Type:           a.Type,
		Disambiguation: a.Disambiguation,
		BeginYear:      parseLeadingYear(a.LifeSpan.Begin),
		Source:         SourceMusicBrainz,
	}, false, nil
}

// parseLeadingYear reads a year from MusicBrainz's partial dates: "1980",
// "1980-07", "1980-07-15".
func parseLeadingYear(s string) int {
	if len(s) < 4 {
		return 0
	}
	y, err := strconv.Atoi(s[:4])
	if err != nil || y < 1000 || y > 3000 {
		return 0
	}
	return y
}
