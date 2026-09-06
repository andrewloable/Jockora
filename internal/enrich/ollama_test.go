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
)

// fakeOllama serves /api/tags and /api/generate.
func fakeOllama(t *testing.T, models []string, gen func(ollamaRequest) any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			out := map[string]any{"models": []map[string]string{}}
			list := make([]map[string]string, 0, len(models))
			for _, m := range models {
				list = append(list, map[string]string{"name": m, "model": m})
			}
			out["models"] = list
			_ = json.NewEncoder(w).Encode(out)
		case "/api/generate":
			var in ollamaRequest
			_ = json.NewDecoder(r.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(gen(in))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOllamaCompletesWithASchema(t *testing.T) {
	var got ollamaRequest
	srv := fakeOllama(t, []string{"qwen2.5:7b"}, func(in ollamaRequest) any {
		got = in
		return map[string]any{"response": `{"ok":true}`, "done": true, "done_reason": "stop", "eval_count": 12}
	})

	c := NewOllama(srv.URL, "qwen2.5:7b", srv.Client())
	out, err := c.Complete(context.Background(), CompletionRequest{
		Prompt:      "write a break",
		JSONSchema:  map[string]any{"type": "object"},
		NPredict:    200,
		Temperature: WritingTemperature,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if out.Content != `{"ok":true}` {
		t.Errorf("Content = %q", out.Content)
	}
	if out.StopType != "eos" {
		t.Errorf("StopType = %q, want eos", out.StopType)
	}
	if got.Format == nil {
		t.Error("the schema was not sent; without it an ungrounded fact id becomes representable")
	}
	if got.Stream {
		t.Error("streaming was requested; the caller wants one whole response")
	}
	if got.Options["temperature"] != WritingTemperature {
		t.Errorf("temperature = %v, want %v", got.Options["temperature"], WritingTemperature)
	}
}

// TestOllamaNormalisesTruncation. Ollama says "length" where llama.cpp says
// "limit", and callers depend on spotting a truncated response: it is JSON up
// to the cut and then is not JSON at all.
func TestOllamaNormalisesTruncation(t *testing.T) {
	srv := fakeOllama(t, []string{"m"}, func(ollamaRequest) any {
		return map[string]any{"response": `{"line":"hi`, "done": true, "done_reason": "length"}
	})
	out, err := NewOllama(srv.URL, "m", srv.Client()).Complete(context.Background(), CompletionRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out.StopType != "limit" {
		t.Errorf("StopType = %q, want %q so callers can treat both servers alike", out.StopType, "limit")
	}
}

// TestOllamaReportsAnErrorInTheBody. Ollama answers 200 with an error field
// when the model is missing, which would otherwise read as an empty writer.
func TestOllamaReportsAnErrorInTheBody(t *testing.T) {
	srv := fakeOllama(t, []string{"m"}, func(ollamaRequest) any {
		return map[string]any{"error": `model "nope" not found, try pulling it first`}
	})
	_, err := NewOllama(srv.URL, "nope", srv.Client()).Complete(context.Background(), CompletionRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("a 200 carrying an error was accepted")
	}
	if !errors.Is(err, ErrLLMUnavailable) {
		t.Errorf("err = %v, want ErrLLMUnavailable", err)
	}
	if !strings.Contains(err.Error(), "pulling it") {
		t.Errorf("the server's own explanation was dropped: %v", err)
	}
}

func TestOllamaEmptyResponseIsNotContent(t *testing.T) {
	srv := fakeOllama(t, []string{"m"}, func(ollamaRequest) any {
		return map[string]any{"response": "   ", "done": true, "done_reason": "stop"}
	})
	_, err := NewOllama(srv.URL, "m", srv.Client()).Complete(context.Background(), CompletionRequest{Prompt: "x"})
	if !errors.Is(err, ErrLLMEmpty) {
		t.Errorf("err = %v, want ErrLLMEmpty", err)
	}
}

// TestOllamaHealthChecksTheModelNotJustTheServer. An Ollama that is up but has
// never pulled the model fails every generation, which reads as a broken writer
// rather than a missing download.
func TestOllamaHealthChecksTheModelNotJustTheServer(t *testing.T) {
	srv := fakeOllama(t, []string{"llama3:8b", "qwen2.5:7b"}, func(ollamaRequest) any { return nil })

	if err := NewOllama(srv.URL, "qwen2.5:7b", srv.Client()).Health(context.Background()); err != nil {
		t.Errorf("a present model reported unhealthy: %v", err)
	}
	// A bare name must match a tagged one, so "qwen2.5" finds "qwen2.5:7b".
	if err := NewOllama(srv.URL, "qwen2.5", srv.Client()).Health(context.Background()); err != nil {
		t.Errorf("a bare model name did not match its tagged form: %v", err)
	}

	err := NewOllama(srv.URL, "mistral:7b", srv.Client()).Health(context.Background())
	if err == nil {
		t.Fatal("a model the server does not have reported healthy")
	}
	if !strings.Contains(err.Error(), "ollama pull") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
	if !strings.Contains(err.Error(), "llama3:8b") {
		t.Errorf("the error does not list what IS available: %v", err)
	}
}

// TestOllamaRejectsANonOllamaServer: pointing -llm-api ollama at llama-server
// must say so, not fail obscurely on every break.
func TestOllamaRejectsANonOllamaServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := NewOllama(srv.URL, "m", srv.Client()).Health(context.Background()); err == nil {
		t.Fatal("a server with no /api/tags reported healthy")
	}
	_, err := NewOllama(srv.URL, "m", srv.Client()).Complete(context.Background(), CompletionRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "Ollama") {
		t.Errorf("err = %v, want it to question whether this is an Ollama server", err)
	}
}

func TestOllamaNeedsAModelName(t *testing.T) {
	srv := fakeOllama(t, []string{"m"}, func(ollamaRequest) any { return nil })
	_, err := NewOllama(srv.URL, "", srv.Client()).Complete(context.Background(), CompletionRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "-llm-model") {
		t.Errorf("err = %v, want it to name the flag that fixes it", err)
	}
}
