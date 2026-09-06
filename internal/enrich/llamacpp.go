// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// LlamaCPP talks to llama-server's NATIVE /completion endpoint.
//
// Not /v1/chat/completions. Three separate failure modes were verified by live
// probe on that route and all of them are avoidable here:
//
//  1. Without --jinja the chat route returns EMPTY content and no error at all,
//     because it cannot apply a chat template.
//  2. A reasoning model on the chat route spends its budget on reasoning_content
//     and returns finish_reason=length with content empty, which this package
//     would misclassify as ErrLLMEmpty and retry forever.
//  3. The chat envelope has been observed emitting unescaped control characters
//     that break strict JSON parsers.
//
// The native endpoint takes a raw prompt, has no template surface, and under a
// grammar forces JSON from the first token, so no preamble is representable.
type LlamaCPP struct {
	baseURL string
	client  *http.Client
}

// NewLlamaCPP returns a client for a llama-server at baseURL.
func NewLlamaCPP(baseURL string, client *http.Client) *LlamaCPP {
	if client == nil {
		// Generous: a dossier pass on a modest GPU is measured in tens of
		// seconds, and enrichment is a background job with no listener waiting.
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &LlamaCPP{baseURL: strings.TrimSuffix(baseURL, "/"), client: client}
}

type llamaRequest struct {
	Prompt      string         `json:"prompt"`
	JSONSchema  map[string]any `json:"json_schema,omitempty"`
	NPredict    int            `json:"n_predict,omitempty"`
	Temperature float64        `json:"temperature"`
}

type llamaResponse struct {
	Content         string `json:"content"`
	StopType        string `json:"stop_type"`
	TokensPredicted int    `json:"tokens_predicted"`
}

// Complete runs one completion.
func (l *LlamaCPP) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	temperature := req.Temperature
	if temperature <= 0 {
		// Cataloguing facts is not a creative task, and a low temperature also
		// makes re-enrichment reproducible. Callers that are WRITING rather
		// than extracting must say so.
		temperature = DefaultTemperature
	}
	body, err := json.Marshal(llamaRequest{
		Prompt:      req.Prompt,
		JSONSchema:  req.JSONSchema,
		NPredict:    req.NPredict,
		Temperature: temperature,
	})
	if err != nil {
		return Completion{}, fmt.Errorf("enrich: encoding llama request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		l.baseURL+"/completion", bytes.NewReader(body))
	if err != nil {
		return Completion{}, fmt.Errorf("enrich: building llama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := l.client.Do(httpReq)
	if err != nil {
		return Completion{}, fmt.Errorf("%w: %v", ErrLLMUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Completion{}, fmt.Errorf("%w: llama-server returned %s", ErrLLMUnavailable, resp.Status)
	}

	var out llamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Completion{}, fmt.Errorf("enrich: decoding llama reply: %w", err)
	}

	return Completion{
		Content:         out.Content,
		StopType:        out.StopType,
		TokensPredicted: out.TokensPredicted,
	}, nil
}

// Health reports whether llama-server is reachable and has a model loaded.
//
// llama-server has no /api/tags; /health is the endpoint it does serve.
func (l *LlamaCPP) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.baseURL+"/health", nil)
	if err != nil {
		return fmt.Errorf("enrich: building health request: %w", err)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLLMUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: /health returned %s", ErrLLMUnavailable, resp.Status)
	}
	return nil
}
