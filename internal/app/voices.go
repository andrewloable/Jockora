// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"encoding/json"
	"fmt"
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
}

func newSidecarVoices(base string) *sidecarVoices {
	return &sidecarVoices{
		base: strings.TrimSuffix(base, "/"),
		// Short: this is a console request, and an operator waiting on a
		// dropdown would rather be told the sidecar is down.
		client: &http.Client{Timeout: 5 * time.Second},
	}
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
