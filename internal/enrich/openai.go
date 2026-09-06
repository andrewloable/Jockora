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

// OpenAIAPI is the dialect name for any OpenAI-compatible endpoint, including
// OpenRouter, Groq, Together and a local vLLM.
const OpenAIAPI = "openai"

// OpenAICompatible talks to a hosted OpenAI-shaped chat endpoint.
//
// This is the GPU-LESS path. The target self-hoster runs a NUC or a NAS and
// very often owns no GPU at all; Kokoro is fine on CPU but a writing model is
// not, so a hosted endpoint is the difference between the product working and
// not working for them.
//
// THREE THINGS AN OPERATOR MUST KNOW, and none of them is hypothetical:
//
//  1. TRACK METADATA AND LYRICS LEAVE THE MACHINE. Jockora's whole premise is
//     self-hosting, and this is the one configuration that breaks it. The
//     "lyrics are read then discarded" guarantee still holds locally -- nothing
//     is stored -- but the text is sent to a third party on the way.
//
//  2. STRUCTURED OUTPUT IS NOT UNIVERSAL. The anti-hallucination design leans
//     on a schema enum making an ungrounded fact id UNREPRESENTABLE. Models
//     that ignore response_format degrade that to post-validation, which
//     rejects rather than prevents. Health() checks this rather than assuming.
//
//  3. FREE TIERS ARE RATE LIMITED. A full library is one request per track;
//     several thousand of those will exceed a free daily allowance, and the
//     enrichment queue is deliberately serial so it cannot go faster.
//
// It uses /chat/completions because that is the only route these services
// expose. The warnings recorded against llama.cpp's chat route do not transfer:
// there is no template to omit, since the server owns it.
type OpenAICompatible struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client

	// referer and title are OpenRouter's attribution headers. Harmless
	// elsewhere and requested by OpenRouter for free-tier identification.
	referer, title string
}

// NewOpenAICompatible returns a client. baseURL includes the version path, as
// in "https://openrouter.ai/api/v1".
func NewOpenAICompatible(baseURL, model, apiKey string, client *http.Client) *OpenAICompatible {
	if client == nil {
		// Generous: a hosted free tier queues, and a cold model can take
		// tens of seconds before the first token.
		client = &http.Client{Timeout: 3 * time.Minute}
	}
	return &OpenAICompatible{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		model:   model,
		apiKey:  apiKey,
		client:  client,
		referer: "https://github.com/andrewloable/jockora",
		title:   "Jockora",
	}
}

type openAIRequest struct {
	Model          string          `json:"model"`
	Messages       []openAIMessage `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat map[string]any  `json:"response_format,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			// Reasoning models put their working here and leave Content empty.
			Reasoning string `json:"reasoning"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// Complete runs one completion.
func (o *OpenAICompatible) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	if o.model == "" {
		return Completion{}, fmt.Errorf("%w: no model; set -llm-model, for example deepseek/deepseek-chat-v3-0324:free",
			ErrLLMUnavailable)
	}

	temperature := req.Temperature
	if temperature <= 0 {
		temperature = DefaultTemperature
	}

	body := openAIRequest{
		Model:       o.model,
		Messages:    []openAIMessage{{Role: "user", Content: req.Prompt}},
		Temperature: temperature,
		MaxTokens:   req.NPredict,
	}
	if req.JSONSchema != nil {
		// strict asks the provider to ENFORCE the schema rather than merely
		// suggest it. Providers that do not support it ignore the field, which
		// is why Health probes for it instead of trusting it.
		body.ResponseFormat = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "jockora",
				"strict": true,
				"schema": req.JSONSchema,
			},
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return Completion{}, fmt.Errorf("enrich: encoding request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return Completion{}, fmt.Errorf("enrich: building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	httpReq.Header.Set("HTTP-Referer", o.referer)
	httpReq.Header.Set("X-Title", o.title)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return Completion{}, fmt.Errorf("%w: %v", ErrLLMUnavailable, err)
	}
	defer resp.Body.Close() //nolint:errcheck // decoded below

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return Completion{}, fmt.Errorf("%w: %s rejected the API key", ErrLLMUnavailable, o.baseURL)
	case http.StatusTooManyRequests:
		// Named specifically because it is the failure a free tier actually
		// hits, and it looks nothing like a broken model.
		return Completion{}, fmt.Errorf("%w: rate limited by %s; a free tier cannot enrich a whole library",
			ErrLLMUnavailable, o.baseURL)
	default:
		return Completion{}, fmt.Errorf("%w: %s returned %s", ErrLLMUnavailable, o.baseURL, resp.Status)
	}

	var out openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Completion{}, fmt.Errorf("%w: decoding reply: %v", ErrLLMBadJSON, err)
	}
	if out.Error != nil {
		return Completion{}, fmt.Errorf("%w: %s", ErrLLMUnavailable, out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return Completion{}, fmt.Errorf("%w: no choices returned", ErrLLMEmpty)
	}

	choice := out.Choices[0]
	content := strings.TrimSpace(choice.Message.Content)
	if content == "" {
		if choice.Message.Reasoning != "" {
			// A reasoning model spent the budget thinking and returned nothing.
			// Named explicitly: this was recorded as a trap when the llama.cpp
			// backend was chosen, and it is the same trap here.
			return Completion{}, fmt.Errorf("%w: %s returned only reasoning and no answer; choose a non-reasoning model",
				ErrLLMEmpty, o.model)
		}
		return Completion{}, fmt.Errorf("%w: empty content", ErrLLMEmpty)
	}

	return Completion{
		Content:         content,
		StopType:        normaliseOpenAIFinish(choice.FinishReason),
		TokensPredicted: out.Usage.CompletionTokens,
	}, nil
}

// normaliseOpenAIFinish maps the OpenAI vocabulary onto llama.cpp's.
func normaliseOpenAIFinish(reason string) string {
	switch reason {
	case "length":
		return "limit"
	case "stop", "":
		return "eos"
	default:
		return reason
	}
}

// Health checks reachability AND whether the model honours a JSON schema.
//
// The schema is the whole point. A provider that ignores response_format still
// answers every request, so the failure is invisible until a break asserts a
// fact no dossier supports -- which is exactly the thing this design exists to
// make impossible.
func (o *OpenAICompatible) Health(ctx context.Context) error {
	out, err := o.Complete(ctx, CompletionRequest{
		Prompt:   "Reply with the JSON object {\"ok\": true}.",
		NPredict: 64,
		JSONSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"ok": map[string]any{"type": "boolean"}},
			"required":             []any{"ok"},
			"additionalProperties": false,
		},
	})
	if err != nil {
		return err
	}

	var shaped map[string]any
	if err := json.Unmarshal([]byte(out.Content), &shaped); err != nil {
		return fmt.Errorf("%w: %s ignored the JSON schema (got %.60q); "+
			"ungrounded facts become possible, so choose a model with structured output",
			ErrLLMUnavailable, o.model, out.Content)
	}
	if _, ok := shaped["ok"]; !ok {
		return fmt.Errorf("%w: %s did not honour the schema", ErrLLMUnavailable, o.model)
	}
	return nil
}
