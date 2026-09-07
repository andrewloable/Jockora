// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package doctor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// THE DOCTOR MUST CHECK THE ENDPOINT THE SERVER WILL ACTUALLY CALL.
//
// It probed llama.cpp's native /completion whatever -llm-api said, so pointing
// Jockora at OpenRouter and running the doctor reported
// "https://openrouter.ai/api/v1/completion returned 404" and advised that the
// server was OpenAI-only -- about a configuration that works. The check the
// documentation tells an operator to run before trusting a hosted model was
// incapable of passing for one.
//
// Every test here is TestDoctorDialect*, which is the -run pattern for this fix.

func TestDoctorDialectChecksTheHostedEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"content": `{"ok":true}`},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	c := checkLLM(context.Background(), Config{
		LLMBaseURL: srv.URL, LLMAPI: "openai", LLMModel: "minimax/minimax-m3:free",
		HTTPClient: srv.Client(),
	})
	if !c.OK {
		t.Fatalf("a working hosted endpoint reported: %s (%s)", c.Detail, c.Fix)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("probed %q, want /chat/completions", gotPath)
	}
}

func TestDoctorDialectFailsAHostThatIgnoresTheSchema(t *testing.T) {
	// The whole point of the probe: a host that answers but disregards the
	// schema still serves every request, and the damage only shows up when a
	// break asserts a fact no dossier supports.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]any{"content": "Sure! Here you go."},
				"finish_reason": "stop",
			}},
		})
	}))
	defer srv.Close()

	c := checkLLM(context.Background(), Config{
		LLMBaseURL: srv.URL, LLMAPI: "openai", LLMModel: "m",
		HTTPClient: srv.Client(),
	})
	if c.OK {
		t.Error("passed a host that ignored the schema")
	}
	if !strings.Contains(c.Detail, "schema") {
		t.Errorf("detail = %q, want it to name the schema", c.Detail)
	}
}

func TestDoctorDialectStillProbesLlamaCppNatively(t *testing.T) {
	// The default dialect is unchanged: native /completion, as before.
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"content": `{"ok":true}`, "stop_type": "eos"})
	}))
	defer srv.Close()

	c := checkLLM(context.Background(), Config{LLMBaseURL: srv.URL, HTTPClient: srv.Client()})
	if !c.OK {
		t.Fatalf("llama.cpp probe reported: %s", c.Detail)
	}
	if gotPath != "/completion" {
		t.Errorf("probed %q, want /completion", gotPath)
	}
}

func TestDoctorDialectNeedsAModelName(t *testing.T) {
	// The likely operator mistake: -llm-api openai set, -llm-model forgotten.
	// The client's own error for this names a flag, so the doctor says so
	// before spending a request finding out.
	c := checkLLM(context.Background(), Config{
		LLMBaseURL: "https://openrouter.ai/api/v1", LLMAPI: "openai",
		HTTPClient: http.DefaultClient,
	})
	if c.OK {
		t.Error("passed with no model configured")
	}
	if !strings.Contains(c.Fix, "llm-model") {
		t.Errorf("fix = %q, want it to name the flag", c.Fix)
	}
}

func TestDoctorDialectChecksOllamaWhereOllamaLives(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{"name": "qwen"}},
		})
	}))
	defer srv.Close()

	c := checkLLM(context.Background(), Config{
		LLMBaseURL: srv.URL, LLMAPI: "ollama", LLMModel: "qwen",
		HTTPClient: srv.Client(),
	})
	if !c.OK {
		t.Fatalf("a running Ollama reported: %s", c.Detail)
	}
	if gotPath != "/api/tags" {
		t.Errorf("probed %q, want Ollama's /api/tags", gotPath)
	}
}
