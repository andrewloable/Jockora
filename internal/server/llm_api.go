// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/andrewloable/jockora/internal/enrich"
)

// LLM is choosing and changing the language model, from the console.
//
// AN INTERFACE, because the thing that actually swaps the client is the app,
// and the handler should be drivable without one.
type LLM interface {
	// LLMSettings is the current choice, key included, and whether one exists.
	LLMSettings() (enrich.LLMConfig, bool)
	// TestLLMConfig proves a configuration before it is committed to.
	TestLLMConfig(ctx context.Context, c enrich.LLMConfig) error
	// SetLLMConfig saves it and points the station at it.
	SetLLMConfig(ctx context.Context, c enrich.LLMConfig) error
	// LLMModels is what that platform hosts.
	LLMModels(ctx context.Context, c enrich.LLMConfig) ([]string, error)
	// LLMHealth is what the model last DID, which is a different question from
	// what is configured. This page tested the model once, at save time, and
	// then never again -- so on 2026-09-08 the one screen dedicated to the
	// language model was the last place that would say it had stopped working.
	LLMHealth() string
}

// SetLLM wires model configuration.
func (s *Server) SetLLM(l LLM) { s.llm = l }

// llmView is the current configuration as the console may see it.
//
// NO KEY. It is stored so the console can change providers without a shell,
// and it never travels back -- the same rule the users table follows for
// password hashes. The console shows "a key is set" and an empty box that
// means "keep it".
type llmView struct {
	Provider string `json:"provider,omitempty"`
	URL      string `json:"url,omitempty"`
	Account  string `json:"account,omitempty"`
	Model    string `json:"model,omitempty"`
	HasKey   bool   `json:"has_key"`
}

// serveLLM handles /admin/llm, /admin/llm/test and /admin/llm/models.
func (s *Server) serveLLM(w http.ResponseWriter, r *http.Request, path string) {
	action := strings.Trim(strings.TrimPrefix(path, "/admin/llm"), "/")
	if action != "" && action != "test" && action != "models" {
		http.NotFound(w, r)
		return
	}
	if s.llm == nil {
		http.Error(w, "no model configuration available", http.StatusServiceUnavailable)
		return
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		s.showLLM(w)
	case action == "" && r.Method == http.MethodPut:
		s.saveLLM(w, r)
	case action == "test" && r.Method == http.MethodPost:
		s.testLLM(w, r)
	case action == "models" && r.Method == http.MethodPost:
		s.listLLMModels(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// showLLM is every provider, what each one needs, and what is chosen now.
func (s *Server) showLLM(w http.ResponseWriter) {
	cur, ok := s.llm.LLMSettings()
	view := llmView{}
	if ok {
		view = llmView{Provider: cur.Provider, URL: cur.URL, Account: cur.Account,
			Model: cur.Model, HasKey: cur.Key != ""}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"providers": enrich.Providers(),
		"current":   view,
		"health":    s.llm.LLMHealth(),
	})
}

// saveLLM proves a configuration and then commits to it.
func (s *Server) saveLLM(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.llmBody(w, r)
	if !ok {
		return
	}
	if err := cfg.Validate(); err != nil {
		s.writeFieldError(w, "provider", err.Error())
		return
	}
	// TESTED BEFORE SAVED. Every provider failure met on this deployment was
	// diagnosable in one call, and an operator should see the reason on the
	// page rather than discover silence at the next break.
	if err := s.llm.TestLLMConfig(r.Context(), cfg); err != nil {
		s.writeFieldError(w, "model", err.Error())
		return
	}
	if err := s.llm.SetLLMConfig(r.Context(), cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.showLLM(w)
}

// testLLM tries a configuration without committing to it.
func (s *Server) testLLM(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.llmBody(w, r)
	if !ok {
		return
	}
	if err := cfg.Validate(); err != nil {
		s.writeFieldError(w, "provider", err.Error())
		return
	}
	if err := s.llm.TestLLMConfig(r.Context(), cfg); err != nil {
		s.writeFieldError(w, "model", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// listLLMModels is what the chosen platform hosts, so a model is picked rather
// than typed. A slug typed by hand is a 404 met at the next break.
func (s *Server) listLLMModels(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.llmBody(w, r)
	if !ok {
		return
	}
	models, err := s.llm.LLMModels(r.Context(), cfg)
	if err != nil {
		s.writeFieldError(w, "key", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// llmBody decodes a configuration, carrying the stored key across when the form
// left the box empty.
//
// The console never receives the key, so it cannot send it back. An empty box
// has to mean "keep the one you have" or changing the MODEL would delete the
// KEY, and the station would stop at the next break.
func (s *Server) llmBody(w http.ResponseWriter, r *http.Request) (enrich.LLMConfig, bool) {
	var cfg enrich.LLMConfig
	if !s.decode(w, r, &cfg) {
		return cfg, false
	}
	if cfg.Key == "" {
		if cur, ok := s.llm.LLMSettings(); ok && cur.Provider == cfg.Provider {
			cfg.Key = cur.Key
		}
	}
	return cfg, true
}
