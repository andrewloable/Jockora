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

// OllamaAPI is the dialect name an operator selects.
const OllamaAPI = "ollama"

// LlamaCPPAPI is the default dialect: llama.cpp's own /completion.
const LlamaCPPAPI = "llamacpp"

// Ollama talks to an Ollama-compatible server.
//
// It exists so an operator can point Jockora at whatever they already run --
// Ollama on another machine, or anything speaking its API -- rather than being
// made to run llama-server. That matters more than convenience here: the
// deployment target has a 2 GB GPU, so the model very often belongs on a
// different box entirely.
//
// It uses /api/generate with a JSON schema in `format`, NOT the OpenAI-shaped
// /v1/chat/completions. The reasons are the same ones recorded for llama.cpp:
// the chat envelope hides failures behind an empty string and can emit
// unescaped control characters. /api/generate also applies the model's own
// chat template server-side, which is the bug that made every instruct model
// behave like a base model on the llama.cpp path.
type Ollama struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllama returns a client. model is an Ollama model name such as
// "qwen2.5:7b-instruct", not a file path.
func NewOllama(baseURL, model string, client *http.Client) *Ollama {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Ollama{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		model:   model,
		client:  client,
	}
}

type ollamaRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	Format  map[string]any `json:"format,omitempty"`
	Stream  bool           `json:"stream"`
	Options map[string]any `json:"options,omitempty"`
}

type ollamaResponse struct {
	Response   string `json:"response"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason"`
	EvalCount  int    `json:"eval_count"`
	Error      string `json:"error"`
}

// Complete runs one completion.
func (o *Ollama) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	if o.model == "" {
		return Completion{}, fmt.Errorf("%w: no model name; Ollama needs one, such as -llm-model qwen2.5:7b", ErrLLMUnavailable)
	}

	temperature := req.Temperature
	if temperature <= 0 {
		temperature = DefaultTemperature
	}
	body, err := json.Marshal(ollamaRequest{
		Model:  o.model,
		Prompt: req.Prompt,
		// A JSON schema here constrains generation the same way llama.cpp's
		// json_schema does, so an ungrounded fact id stays unrepresentable
		// rather than merely rejected afterwards.
		Format: req.JSONSchema,
		Stream: false,
		Options: map[string]any{
			"temperature": temperature,
			"num_predict": req.NPredict,
		},
	})
	if err != nil {
		return Completion{}, fmt.Errorf("enrich: encoding ollama request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return Completion{}, fmt.Errorf("enrich: building ollama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return Completion{}, fmt.Errorf("%w: %v", ErrLLMUnavailable, err)
	}
	defer resp.Body.Close() //nolint:errcheck // body decoded below

	if resp.StatusCode == http.StatusNotFound {
		return Completion{}, fmt.Errorf("%w: %s/api/generate returned 404; is this an Ollama server?",
			ErrLLMUnavailable, o.baseURL)
	}
	if resp.StatusCode != http.StatusOK {
		return Completion{}, fmt.Errorf("%w: ollama returned %s", ErrLLMUnavailable, resp.Status)
	}

	var out ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Completion{}, fmt.Errorf("%w: decoding ollama reply: %v", ErrLLMBadJSON, err)
	}
	if out.Error != "" {
		// Ollama reports a missing model in the BODY with a 200, so this is not
		// redundant with the status check above.
		return Completion{}, fmt.Errorf("%w: %s", ErrLLMUnavailable, out.Error)
	}
	if strings.TrimSpace(out.Response) == "" {
		return Completion{}, fmt.Errorf("%w: empty content", ErrLLMEmpty)
	}

	return Completion{
		Content: out.Response,
		// Ollama says "length" where llama.cpp says "limit". Normalised here so
		// every caller can test one value: a truncated response is JSON up to
		// the cut and then is not JSON at all, and callers depend on spotting it.
		StopType:        normaliseStopReason(out.DoneReason),
		TokensPredicted: out.EvalCount,
	}, nil
}

// normaliseStopReason maps Ollama's vocabulary onto llama.cpp's.
func normaliseStopReason(reason string) string {
	switch reason {
	case "length":
		return "limit"
	case "stop", "":
		return "eos"
	default:
		return reason
	}
}

// Health reports whether the server is reachable and knows the model.
//
// It checks the MODEL, not just the server: an Ollama that is up but has never
// pulled the requested model fails every generation with a 200 and an error in
// the body, which reads as a broken writer rather than a missing download.
func (o *Ollama) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLLMUnavailable, err)
	}
	defer resp.Body.Close() //nolint:errcheck // body decoded below
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s/api/tags returned %s", ErrLLMUnavailable, o.baseURL, resp.Status)
	}

	var tags struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return fmt.Errorf("%w: %s does not answer like Ollama", ErrLLMUnavailable, o.baseURL)
	}
	if o.model == "" {
		return fmt.Errorf("%w: no model name configured", ErrLLMUnavailable)
	}
	for _, m := range tags.Models {
		if m.Name == o.model || m.Model == o.model ||
			strings.HasPrefix(m.Name, o.model+":") || strings.HasPrefix(m.Model, o.model+":") {
			return nil
		}
	}

	available := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		available = append(available, m.Name)
	}
	return fmt.Errorf("%w: %s has no model %q; it has %v (pull it: ollama pull %s)",
		ErrLLMUnavailable, o.baseURL, o.model, available, o.model)
}
