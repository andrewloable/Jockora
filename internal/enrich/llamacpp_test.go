// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLlamaCPPAppliesTheChatTemplate guards the single largest quality problem
// the DJ has had.
//
// /completion applies NO template -- that is why it is the right endpoint, a
// grammar can force JSON from the first token -- but it also means an instruct
// model receives a bare document and behaves like a BASE model, continuing the
// text instead of obeying it. Two different models failed identically because
// of it, answering a prompt that described a radio break with "Your radio break
// sentence goes here".
func TestLlamaCPPAppliesTheChatTemplate(t *testing.T) {
	var gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			var in struct {
				Messages []struct{ Content string } `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"prompt": "<|im_start|>user\n" + in.Messages[0].Content + "<|im_end|>\n<|im_start|>assistant\n",
			})
		case "/completion":
			var in struct {
				Prompt string `json:"prompt"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			gotPrompt = in.Prompt
			_ = json.NewEncoder(w).Encode(map[string]any{"content": `{"ok":true}`, "stop_type": "eos"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewLlamaCPP(srv.URL, srv.Client())
	if _, err := c.Complete(context.Background(), CompletionRequest{Prompt: "WRITE A BREAK"}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if !strings.Contains(gotPrompt, "<|im_start|>assistant") {
		t.Errorf("the completion got an UNTEMPLATED prompt %q; an instruct model reading that continues the document rather than answering it", gotPrompt)
	}
	if !strings.Contains(gotPrompt, "WRITE A BREAK") {
		t.Errorf("the prompt body was lost in templating: %q", gotPrompt)
	}
	if !c.TemplateApplied() {
		t.Error("TemplateApplied() = false after a successful template round trip")
	}
}

// TestLlamaCPPFallsBackWhenTemplatingIsUnavailable: an older llama.cpp has no
// /apply-template. That must degrade to the previous behaviour rather than
// failing every completion, and it must be probed once rather than per call.
func TestLlamaCPPFallsBackWhenTemplatingIsUnavailable(t *testing.T) {
	var templateCalls, gotPrompt = 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apply-template":
			templateCalls++
			w.WriteHeader(http.StatusNotFound)
		case "/completion":
			var in struct {
				Prompt string `json:"prompt"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			gotPrompt = in.Prompt
			_ = json.NewEncoder(w).Encode(map[string]any{"content": `{"ok":true}`, "stop_type": "eos"})
		}
	}))
	defer srv.Close()

	c := NewLlamaCPP(srv.URL, srv.Client())
	for i := 0; i < 3; i++ {
		if _, err := c.Complete(context.Background(), CompletionRequest{Prompt: "RAW"}); err != nil {
			t.Fatalf("Complete %d: %v", i, err)
		}
	}

	if gotPrompt != "RAW" {
		t.Errorf("prompt = %q, want the raw prompt when templating is unavailable", gotPrompt)
	}
	if templateCalls != 1 {
		t.Errorf("/apply-template probed %d times, want 1: the answer does not change", templateCalls)
	}
	if c.TemplateApplied() {
		t.Error("TemplateApplied() = true against a server that has no such endpoint")
	}
}
