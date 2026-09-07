// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// sidecarVoices asks the TTS sidecar which voices it can actually produce.
//
// Asked rather than listed here, because the answer depends on the model file
// the operator installed. A hardcoded list would let the console offer a voice
// the sidecar does not have, and the failure would appear as a silent break,
// minutes later, on air.
type sidecarVoices struct {
	base   string
	client *http.Client
	render *http.Client
}

func newSidecarVoices(base string) *sidecarVoices {
	return &sidecarVoices{
		base: strings.TrimSuffix(base, "/"),
		// Short: this is a console request, and an operator waiting on a
		// dropdown would rather be told the sidecar is down.
		client: &http.Client{Timeout: 5 * time.Second},
		// Longer, because this one SYNTHESISES. A cold Kokoro takes a few
		// seconds for the first line and the operator is watching a button.
		render: &http.Client{Timeout: 30 * time.Second},
	}
}

// Preview speaks one line in a voice and returns the audio.
//
// It renders through the SAME sidecar the DJ uses, deliberately: a preview
// produced by anything else would be a promise rather than a demonstration,
// and the thing an operator is trying to find out is what this jock will
// actually sound like on air.
//
// The cost is that a preview and a break compete for one sidecar. That is
// bounded and cheap: the line is a few seconds long, and breaks are rendered
// into a buffer that runs seven to seventeen minutes ahead, so a preview
// delays nothing a listener can hear.
func (v *sidecarVoices) Preview(ctx context.Context, voice, text string) ([]byte, error) {
	// The console offers what /voices returned, but a jock card may carry a
	// prefixed id like "kokoro:af_heart". The sidecar wants the bare name.
	if _, bare, found := strings.Cut(voice, ":"); found {
		voice = bare
	}
	// Two strings in a map. json.Marshal cannot fail on that, and a branch no
	// input can reach is a branch no test can cover.
	body, _ := json.Marshal(map[string]string{"text": text, "voice": voice}) //nolint:errcheck // cannot fail for strings
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.base+"/synth", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("app: asking the sidecar to speak: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.render.Do(req)
	if err != nil {
		return nil, fmt.Errorf("app: asking the sidecar to speak: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // response body
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("app: the sidecar refused to speak: %s", resp.Status)
	}
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("app: reading the preview: %w", err)
	}
	return audio, nil
}

func (v *sidecarVoices) Voices(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.base+"/voices", nil)
	if err != nil {
		return nil, fmt.Errorf("app: asking the sidecar for voices: %w", err)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("app: asking the sidecar for voices: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("app: the sidecar answered %s asking for voices", resp.Status)
	}
	var body struct {
		Voices []string `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("app: reading the sidecar's voice list: %w", err)
	}
	return body.Voices, nil
}
