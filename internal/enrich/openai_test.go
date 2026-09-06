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

func fakeOpenAI(t *testing.T, status int, reply any, capture *openAIRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			_ = json.NewDecoder(r.Body).Decode(capture)
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func okReply(content, finish string) map[string]any {
	return map[string]any{
		"choices": []map[string]any{{
			"message":       map[string]any{"content": content},
			"finish_reason": finish,
		}},
		"usage": map[string]any{"completion_tokens": 11},
	}
}

func TestOpenAISendsAStrictSchema(t *testing.T) {
	var got openAIRequest
	srv := fakeOpenAI(t, http.StatusOK, okReply(`{"ok":true}`, "stop"), &got)

	c := NewOpenAICompatible(srv.URL, "deepseek/deepseek-chat-v3-0324:free", "sk-test", srv.Client())
	out, err := c.Complete(context.Background(), CompletionRequest{
		Prompt: "write", JSONSchema: map[string]any{"type": "object"}, NPredict: 200,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out.Content != `{"ok":true}` {
		t.Errorf("Content = %q", out.Content)
	}
	if got.ResponseFormat == nil {
		t.Fatal("no response_format sent; without it an ungrounded fact id becomes representable")
	}
	js, _ := got.ResponseFormat["json_schema"].(map[string]any)
	if js == nil || js["strict"] != true {
		t.Errorf("schema was not marked strict: %v", got.ResponseFormat)
	}
	if got.Model != "deepseek/deepseek-chat-v3-0324:free" {
		t.Errorf("model = %q", got.Model)
	}
}

// TestOpenAINamesRateLimiting. This is the failure a free tier actually hits,
// and it looks nothing like a broken model.
func TestOpenAINamesRateLimiting(t *testing.T) {
	srv := fakeOpenAI(t, http.StatusTooManyRequests, nil, nil)
	_, err := NewOpenAICompatible(srv.URL, "m", "k", srv.Client()).
		Complete(context.Background(), CompletionRequest{Prompt: "x"})
	if err == nil {
		t.Fatal("a 429 was accepted")
	}
	if !strings.Contains(err.Error(), "rate limited") || !strings.Contains(err.Error(), "free tier") {
		t.Errorf("err = %v, want it to name rate limiting and what it means for a library", err)
	}
}

func TestOpenAINamesABadKey(t *testing.T) {
	srv := fakeOpenAI(t, http.StatusUnauthorized, nil, nil)
	_, err := NewOpenAICompatible(srv.URL, "m", "bad", srv.Client()).
		Complete(context.Background(), CompletionRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("err = %v, want it to name the key", err)
	}
}

func TestOpenAINormalisesTruncation(t *testing.T) {
	srv := fakeOpenAI(t, http.StatusOK, okReply(`{"line":"hi`, "length"), nil)
	out, err := NewOpenAICompatible(srv.URL, "m", "k", srv.Client()).
		Complete(context.Background(), CompletionRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if out.StopType != "limit" {
		t.Errorf("StopType = %q, want %q so every backend reads alike", out.StopType, "limit")
	}
}

// TestOpenAIHealthRejectsAModelThatIgnoresTheSchema. A provider that ignores
// response_format still answers every request, so the failure stays invisible
// until a break asserts something no dossier supports.
func TestOpenAIHealthRejectsAModelThatIgnoresTheSchema(t *testing.T) {
	srv := fakeOpenAI(t, http.StatusOK, okReply("Sure! Here is your JSON: ok is true.", "stop"), nil)
	err := NewOpenAICompatible(srv.URL, "chatty/model:free", "k", srv.Client()).Health(context.Background())
	if err == nil {
		t.Fatal("a model that ignored the schema reported healthy")
	}
	if !strings.Contains(err.Error(), "structured output") {
		t.Errorf("err = %v, want it to say what to choose instead", err)
	}

	good := fakeOpenAI(t, http.StatusOK, okReply(`{"ok":true}`, "stop"), nil)
	if err := NewOpenAICompatible(good.URL, "good/model", "k", good.Client()).Health(context.Background()); err != nil {
		t.Errorf("a schema-honouring model reported unhealthy: %v", err)
	}
}

func TestOpenAINeedsAModel(t *testing.T) {
	srv := fakeOpenAI(t, http.StatusOK, okReply("{}", "stop"), nil)
	_, err := NewOpenAICompatible(srv.URL, "", "k", srv.Client()).
		Complete(context.Background(), CompletionRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "-llm-model") {
		t.Errorf("err = %v, want it to name the flag that fixes it", err)
	}
}

// TestOpenAIUsesAReasoningModelAnyway. Most free OpenRouter models reason, so
// refusing them would rule out most of the free tier. Reasoning is excluded at
// the source and stripped from the content, and the answer is used.
func TestOpenAIUsesAReasoningModelAnyway(t *testing.T) {
	var got openAIRequest
	srv := fakeOpenAI(t, http.StatusOK,
		okReply("<think>The user wants JSON. Be careful.</think>{\"ok\":true}", "stop"), &got)

	out, err := NewOpenAICompatible(srv.URL, "some/reasoner:free", "k", srv.Client()).
		Complete(context.Background(), CompletionRequest{Prompt: "x", NPredict: 200})
	if err != nil {
		t.Fatalf("a reasoning model was refused: %v", err)
	}
	if out.Content != `{"ok":true}` {
		t.Errorf("Content = %q, want the answer with the thinking removed", out.Content)
	}
	if got.Reasoning == nil || got.Reasoning["exclude"] != true {
		t.Errorf("reasoning was not excluded at the source: %v", got.Reasoning)
	}
}

// TestOpenAIBudgetExhaustedByReasoningSaysSo. When the model reasons anyway and
// runs out, the fix is BUDGET, not a different model -- reasoning is already
// excluded and stripped by the time this fires.
func TestOpenAIBudgetExhaustedByReasoningSaysSo(t *testing.T) {
	srv := fakeOpenAI(t, http.StatusOK, map[string]any{
		"choices": []map[string]any{{
			"message":       map[string]any{"content": "<think>still going", "reasoning": "..."},
			"finish_reason": "length",
		}},
	}, nil)

	_, err := NewOpenAICompatible(srv.URL, "some/reasoner:free", "k", srv.Client()).
		Complete(context.Background(), CompletionRequest{Prompt: "x", NPredict: 200})
	if !errors.Is(err, ErrLLMEmpty) {
		t.Fatalf("err = %v, want ErrLLMEmpty", err)
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("err = %v, want it to point at the budget", err)
	}
	if !strings.Contains(err.Error(), "200-token") {
		t.Errorf("err = %v, want it to name the budget it had", err)
	}
}
