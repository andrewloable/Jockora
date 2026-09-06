// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package enrich gathers the facts the DJ is allowed to talk about.
package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andrewloable/jockora/internal/store"
)

// Confidence values for a derived ramp. This is a controlled vocabulary of
// exactly two members, not free text.
const (
	// ConfidenceLRC means the numbers came from validated synced lyrics.
	ConfidenceLRC = "lrc"
	// ConfidenceNone means there is no usable ramp and the DJ must not rely on
	// one. It is the value for absent, malformed and implausible data alike.
	ConfidenceNone = "none"
)

// SafetyMargin is subtracted from the first vocal timestamp.
//
// Community-submitted timings drift by up to about a second, and this number
// decides when the DJ stops talking. Ending speech early is unnoticeable;
// ending it late means talking over a vocal, which is the one thing the ramp
// rule exists to prevent.
const SafetyMargin = 1.5

// UserAgent identifies Jockora to LRCLIB, which asks for it and has no API key.
const UserAgent = "Jockora/0.1 (self-hosted AI radio; +https://github.com/andrewloable/jockora)"

// Ramp is everything kept from a lyrics lookup.
//
// The lyrics themselves are read, reduced to these numbers and DISCARDED. They
// are copyrighted and are never stored: not in the database, not in a cache
// file, not in a log line. This type deliberately has nowhere to put them.
type Ramp struct {
	// RampS is how long the DJ may talk over the intro before the first vocal.
	RampS float64
	// OutroS is how long the instrumental tail runs after the last vocal.
	OutroS float64
	// Confidence is ConfidenceLRC or ConfidenceNone.
	Confidence string
}

// LRCLib is a client for lrclib.net.
type LRCLib struct {
	baseURL string
	client  *http.Client
}

// NewLRCLib returns a client. baseURL is injectable so tests never touch the
// live API.
func NewLRCLib(baseURL string, client *http.Client) *LRCLib {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &LRCLib{baseURL: strings.TrimSuffix(baseURL, "/"), client: client}
}

// lrcResponse is the subset of LRCLIB's reply this client reads.
type lrcResponse struct {
	SyncedLyrics string `json:"syncedLyrics"`
}

// FetchRamp looks a track up and derives its ramp and outro.
//
// A track with no entry, or with unsynced lyrics only, is a normal outcome and
// returns ConfidenceNone without an error: most libraries have plenty of both.
func (c *LRCLib) FetchRamp(ctx context.Context, artist, title string, duration float64) (Ramp, error) {
	none := Ramp{Confidence: ConfidenceNone}

	// An untagged track is an ANSWER, not a failed lookup. LRCLIB needs both an
	// artist and a title and returns 400 without them, which a caller reads as
	// an error and skips -- silently dropping that track out of the coverage
	// denominator. On a real library that flatters the coverage figure, and it
	// flatters it in the wrong direction: a track with no tags is precisely one
	// that only audio analysis can ever place a break on.
	if strings.TrimSpace(artist) == "" || strings.TrimSpace(title) == "" {
		return none, nil
	}

	q := url.Values{}
	q.Set("artist_name", artist)
	q.Set("track_name", title)
	q.Set("duration", strconv.FormatFloat(duration, 'f', 0, 64))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/get?"+q.Encode(), nil)
	if err != nil {
		return none, fmt.Errorf("enrich: building lrclib request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return none, fmt.Errorf("enrich: lrclib: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No entry for this track. Extremely common and not a failure.
		return none, nil
	}
	if resp.StatusCode != http.StatusOK {
		return none, fmt.Errorf("enrich: lrclib returned %s", resp.Status)
	}

	var body lrcResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return none, fmt.Errorf("enrich: decoding lrclib reply: %w", err)
	}

	return DeriveRamp(body.SyncedLyrics, duration), nil
}

// FetchLyrics returns lyric text for TRANSIENT use alongside the derived ramp.
//
// This exists because subject_summary and themes cannot be written honestly
// without it: given only artist metadata a model either echoes the title or
// invents, and both were observed in a live probe. The architecture calls for
// lyrics to be "read then discarded" and this is the reading.
//
// The returned text must go straight into a prompt and nowhere else. It is
// deliberately NOT reachable through Ramp, which is the type that gets stored,
// and no caller should ever put it in a struct that does.
func (c *LRCLib) FetchLyrics(ctx context.Context, artist, title string, duration float64) (string, Ramp, error) {
	none := Ramp{Confidence: ConfidenceNone}

	// Same reason as FetchRamp: nothing to ask about.
	if strings.TrimSpace(artist) == "" || strings.TrimSpace(title) == "" {
		return "", none, nil
	}

	q := url.Values{}
	q.Set("artist_name", artist)
	q.Set("track_name", title)
	q.Set("duration", strconv.FormatFloat(duration, 'f', 0, 64))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/get?"+q.Encode(), nil)
	if err != nil {
		return "", none, fmt.Errorf("enrich: building lrclib request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", none, fmt.Errorf("enrich: lrclib: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", none, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", none, fmt.Errorf("enrich: lrclib returned %s", resp.Status)
	}

	var body struct {
		SyncedLyrics string `json:"syncedLyrics"`
		PlainLyrics  string `json:"plainLyrics"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", none, fmt.Errorf("enrich: decoding lrclib reply: %w", err)
	}

	// Prefer plain text: the timestamps are noise to a language model, and the
	// ramp has already taken everything it needs from the synced version.
	text := body.PlainLyrics
	if text == "" {
		text = stripTimestamps(body.SyncedLyrics)
	}
	return text, DeriveRamp(body.SyncedLyrics, duration), nil
}

// stripTimestamps turns LRC into plain lines.
func stripTimestamps(lrc string) string {
	var b strings.Builder
	for _, line := range strings.Split(lrc, "\n") {
		cleaned := strings.TrimSpace(timestampRE.ReplaceAllString(line, ""))
		if cleaned == "" {
			continue
		}
		b.WriteString(cleaned)
		b.WriteByte('\n')
	}
	return b.String()
}

// timestampRE matches an LRC timestamp: [mm:ss.xx], with 2 or 3 fraction digits.
var timestampRE = regexp.MustCompile(`\[(\d{1,3}):([0-5]?\d)(?:[.:](\d{1,3}))?\]`)

// DeriveRamp turns LRC text into the two numbers, validating hard.
//
// This is community-submitted data feeding placement maths directly, so nothing
// is trusted: timestamps must parse, must increase, and must land inside the
// track. Any failure yields ConfidenceNone, which means personality-only talk
// rather than a guess about when the singing starts.
func DeriveRamp(syncedLyrics string, duration float64) Ramp {
	none := Ramp{Confidence: ConfidenceNone}

	stamps := parseTimestamps(syncedLyrics)
	// One timestamp cannot establish an outro, and zero cannot establish
	// anything.
	if len(stamps) < 2 {
		return none
	}

	for i := 1; i < len(stamps); i++ {
		if stamps[i] < stamps[i-1] {
			return none // timestamps go backwards
		}
	}

	first, last := stamps[0], stamps[len(stamps)-1]
	return clampRamp(first-SafetyMargin, duration-last, duration, ConfidenceLRC)
}

// clampRamp validates two derived numbers and stamps their provenance.
//
// Shared by the LRC path and the audio-analysis path deliberately. Both feed
// placement maths directly, for different reasons -- one is community-submitted
// data, the other is a spectral guess -- and a copy of these rules in each would
// eventually disagree with itself. Any failure yields ConfidenceNone, which
// means personality-only talk rather than a guess about when the singing starts.
func clampRamp(ramp, outro, duration float64, confidence string) Ramp {
	none := Ramp{Confidence: ConfidenceNone}
	switch {
	case duration <= 0:
		return none
	case ramp <= 0: // the vocal starts immediately; there is no room to talk
		return none
	case ramp >= duration:
		return none
	case outro <= 0: // the last vocal is at or past the end
		return none
	case outro >= duration:
		return none
	case ramp+outro >= duration: // the two windows would overlap
		return none
	}

	return Ramp{
		RampS:      round2(ramp),
		OutroS:     round2(outro),
		Confidence: confidence,
	}
}

// parseTimestamps extracts every timestamp, in order of appearance.
//
// Only the timestamps are read. The line text is never captured, never
// returned and never logged.
func parseTimestamps(lrc string) []float64 {
	var out []float64
	for _, line := range strings.Split(lrc, "\n") {
		m := timestampRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// A timestamp with no words after it is a formatting artifact, not a
		// vocal cue.
		if strings.TrimSpace(line[len(m[0]):]) == "" {
			continue
		}

		min, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		sec, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		frac := 0.0
		if m[3] != "" {
			f, err := strconv.ParseFloat("0."+m[3], 64)
			if err == nil {
				frac = f
			}
		}
		out = append(out, min*60+sec+frac)
	}
	return out
}

// round2 keeps derived values to centisecond precision, which is the resolution
// LRC timestamps carry anyway.
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// StoreRamp records a derived ramp against a track.
//
// Only the three numbers are written. There is no code path anywhere in this
// package that could persist lyric text, because Ramp has nowhere to hold it.
func StoreRamp(ctx context.Context, s *store.Store, path string, r Ramp) error {
	_, err := s.DB().ExecContext(ctx,
		`UPDATE tracks SET ramp_s = ?, outro_s = ?, ramp_confidence = ? WHERE path = ?`,
		r.RampS, r.OutroS, r.Confidence, path)
	if err != nil {
		return fmt.Errorf("enrich: storing ramp for %s: %w", path, err)
	}
	return nil
}
