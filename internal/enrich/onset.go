// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ConfidenceAnalysis means the ramp came from audio analysis rather than from
// synced lyrics.
//
// It is a separate value from ConfidenceLRC on purpose. A number derived from a
// spectral guess must never be indistinguishable from one a human typed into an
// LRC file: when a break lands badly, the first question is which of the two
// produced the number, and a shared label would make that unanswerable.
const ConfidenceAnalysis = "analysis"

// MaxInstrumentalRamp caps how long the DJ may talk over a track with no
// detected vocal at all.
//
// Without a cap an instrumental would be one long ramp and the DJ could talk
// over the whole thing, which is a different product.
const MaxInstrumentalRamp = 20.0

// MaxAnalysisRamp caps a ramp or outro derived from detected vocal bounds.
//
// THIS CAP IS THE SAFETY MECHANISM, and it was measured rather than chosen.
// Against forty tracks with human-typed LRC timestamps as ground truth, the
// detector puts the first vocal too late often enough to matter, and the cap is
// what bounds the consequence: at 20 seconds the worst case was 17.7 seconds of
// talking over singing, at 10 seconds it is 7.7, and that held at every
// detection threshold tried. Neither the threshold nor a proportional shrink
// moved it -- only the cap did.
//
// Ten seconds is also long enough for a real break, so the cost is small: most
// intros a DJ would talk over are shorter than that anyway.
const MaxAnalysisRamp = 10.0

// instrumentalShare is the most of an instrumental either window may take, so a
// short interlude is not talked over end to end.
const instrumentalShare = 0.4

// OnsetSource returns a ramp derived from a track's audio.
type OnsetSource interface {
	FetchRamp(ctx context.Context, path string, duration float64) (Ramp, error)
}

// OnsetClient asks the sidecar where the singing starts and stops.
//
// It talks to the EXISTING Kokoro sidecar rather than a second process: the
// analysis is librosa, the sidecar is already a Python process with an HTTP
// front door, and a cgo binding was ruled out because it would end the
// cgo-free cross-compile.
type OnsetClient struct {
	baseURL string
	client  *http.Client
}

// NewOnsetClient returns a client. baseURL is injectable so tests never touch a
// real sidecar.
func NewOnsetClient(baseURL string, client *http.Client) *OnsetClient {
	if client == nil {
		// Generous: this decodes and analyses a whole track, unlike synthesis.
		client = &http.Client{Timeout: 120 * time.Second}
	}
	return &OnsetClient{baseURL: trimSlash(baseURL), client: client}
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// onsetResponse is the sidecar's reply. Null bounds mean no vocal was found.
type onsetResponse struct {
	VocalStart *float64 `json:"vocal_start"`
	VocalEnd   *float64 `json:"vocal_end"`
	// Duration is what the sidecar actually decoded. It is used only when the
	// caller did not supply one: without it, a caller passing zero gets a
	// clean-looking ConfidenceNone for a perfectly good detection, because
	// every ramp fails validation against a zero-length track.
	Duration float64 `json:"duration"`
}

// FetchRamp analyses one track.
//
// An unavailable or unhappy sidecar is reported as an error, and every caller
// treats that as ConfidenceNone rather than a reason to stop: analysis is a
// nice-to-have that widens ramp placement, never a precondition for enrichment.
func (c *OnsetClient) FetchRamp(ctx context.Context, path string, duration float64) (Ramp, error) {
	none := Ramp{Confidence: ConfidenceNone}

	q := url.Values{}
	q.Set("path", path)
	q.Set("duration", strconv.FormatFloat(duration, 'f', 3, 64))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/onset?"+q.Encode(), nil)
	if err != nil {
		return none, fmt.Errorf("enrich: building onset request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return none, fmt.Errorf("enrich: onset: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // body drained below

	if resp.StatusCode != http.StatusOK {
		return none, fmt.Errorf("enrich: onset returned %s", resp.Status)
	}
	var body onsetResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return none, fmt.Errorf("enrich: decoding onset reply: %w", err)
	}
	if duration <= 0 {
		duration = body.Duration
	}
	return DeriveOnsetRamp(body.VocalStart, body.VocalEnd, duration), nil
}

// DeriveOnsetRamp turns detected vocal bounds into a validated Ramp.
//
// Nil bounds mean the analysis found no singing anywhere. That is a real
// answer, not a failure: instrumentals, field recordings and score cues are
// common in a real library, and they are the safest material to talk over.
func DeriveOnsetRamp(vocalStart, vocalEnd *float64, duration float64) Ramp {
	if vocalStart == nil || vocalEnd == nil {
		window := MaxInstrumentalRamp
		if share := duration * instrumentalShare; share < window {
			window = share
		}
		return clampRamp(window, window, duration, ConfidenceAnalysis)
	}
	// Reversed bounds are a malformed answer and must be rejected HERE, before
	// the cap. clampRamp's overlap guard used to catch them, but capping both
	// numbers at 10 seconds makes any pair sum to 20 and look perfectly
	// reasonable -- the cap silently defeated the validation that was covering
	// this case.
	if *vocalEnd <= *vocalStart {
		return Ramp{Confidence: ConfidenceNone}
	}

	// The same safety margin the LRC path uses: analysis is less accurate than
	// a human-typed timestamp, not more, so it does not get a smaller cushion.
	// The cap on top of it is what makes a wrong answer survivable.
	ramp := capAt(*vocalStart-SafetyMargin, MaxAnalysisRamp)
	outro := capAt(duration-*vocalEnd, MaxAnalysisRamp)
	return clampRamp(ramp, outro, duration, ConfidenceAnalysis)
}

func capAt(v, limit float64) float64 {
	if v > limit {
		return limit
	}
	return v
}

// ResolveRamp returns the best ramp available for a track.
//
// LRC wins whenever it produced one: it is cheaper -- an HTTP lookup against a
// decoded track -- and more accurate, because a person listened. Analysis runs
// only for the rest, which on the measured library is most of it: coverage came
// back at 41.5%.
func ResolveRamp(ctx context.Context, lrc Ramp, onset OnsetSource, path string, duration float64) (Ramp, error) {
	if lrc.Confidence == ConfidenceLRC || onset == nil {
		return lrc, nil
	}
	ramp, err := onset.FetchRamp(ctx, path, duration)
	if err != nil {
		// Analysis is optional. A dead sidecar costs ramp placement on this
		// track, not the enrichment run.
		if ctx.Err() != nil {
			return lrc, ctx.Err()
		}
		return lrc, nil
	}
	return ramp, nil
}

// bpmResponse is the sidecar's tempo reply.
type bpmResponse struct {
	BPM float64 `json:"bpm"`
}

// BPM estimates a track's tempo through the same sidecar that does onset
// detection and speech.
//
// SAME PROCESS, on purpose. librosa lives beside Kokoro because that process
// already exists, and both a separate analysis service and a cgo binding were
// ruled out. librosa is ISC licensed, so it adds nothing to the dependency gate
// that keeps the commercial track alive -- and it is out-of-process regardless.
//
// An error means NO TEMPO, which is a valid answer: the selector widens its
// matching window until something fits, so an unmeasured track stays playable.
func (c *OnsetClient) BPM(ctx context.Context, path string) (float64, error) {
	q := url.Values{}
	q.Set("path", path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/bpm?"+q.Encode(), nil)
	if err != nil {
		return 0, fmt.Errorf("enrich: building bpm request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("enrich: bpm: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // body drained below

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("enrich: bpm returned %s", resp.Status)
	}
	var body bpmResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("enrich: decoding bpm reply: %w", err)
	}
	if body.BPM <= 0 {
		return 0, fmt.Errorf("enrich: bpm reported %.1f", body.BPM)
	}
	return body.BPM, nil
}
