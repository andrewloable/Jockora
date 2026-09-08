// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

// llmLog is a stand-in for the app: it records what was saved and answers the
// health check however the test needs.
type llmLog struct {
	saved  enrich.LLMConfig
	models []string
	err    error
	health string
}

func (l *llmLog) LLMHealth() string {
	if l.health == "" {
		return "ok"
	}
	return l.health
}

func (l *llmLog) LLMSettings() (enrich.LLMConfig, bool) {
	return l.saved, l.saved.Provider != ""
}

func (l *llmLog) SetLLMConfig(_ context.Context, c enrich.LLMConfig) error {
	if l.err != nil {
		return l.err
	}
	l.saved = c
	return nil
}

func (l *llmLog) TestLLMConfig(_ context.Context, _ enrich.LLMConfig) error { return l.err }

func (l *llmLog) LLMModels(_ context.Context, _ enrich.LLMConfig) ([]string, error) {
	return l.models, l.err
}

func llmServer(t *testing.T) (*Server, *llmLog) {
	t.Helper()
	s, _, _ := authServer(t)
	log := &llmLog{}
	s.SetLLM(log)
	return s, log
}

func TestLLMConfigAPIDescribesEveryProvider(t *testing.T) {
	// The page has to say which fields each provider wants and where the key
	// comes from, or the operator ends up in a search engine.
	s, _ := llmServer(t)
	rec := as(t, s, http.MethodGet, "/admin/llm", "", adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Providers []enrich.Provider `json:"providers"`
		Current   struct {
			Provider string `json:"provider"`
			HasKey   bool   `json:"has_key"`
			Key      string `json:"key"`
		} `json:"current"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Providers) != 3 {
		t.Errorf("%d providers offered", len(body.Providers))
	}
	for _, p := range body.Providers {
		if p.Help == "" {
			t.Errorf("%s is offered with no explanation", p.ID)
		}
	}
}

func TestLLMConfigAPINeverReturnsTheKey(t *testing.T) {
	// The same rule the users table follows for password hashes.
	s, log := llmServer(t)
	log.saved = enrich.LLMConfig{Provider: enrich.ProviderCloudflare,
		Account: "acc", Model: "m", Key: "super-secret-token"}

	rec := as(t, s, http.MethodGet, "/admin/llm", "", adminCookie(t, s))
	if strings.Contains(rec.Body.String(), "super-secret-token") {
		t.Fatalf("the key came back: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"has_key":true`) {
		t.Errorf("the page cannot tell a key is set: %s", rec.Body)
	}
}

func TestLLMConfigAPISavesOnlyWhatWorks(t *testing.T) {
	// Every provider failure this week was diagnosable in one call. The
	// operator should meet it on the page, not at the next break.
	s, log := llmServer(t)
	admin := adminCookie(t, s)
	good := `{"provider":"cloudflare","account":"acc","model":"@cf/x","key":"k"}`

	log.err = errors.New("that model ignored the JSON schema")
	rec := as(t, s, http.MethodPut, "/admin/llm", good, admin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a failing config = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ignored the JSON schema") {
		t.Errorf("the reason was swallowed: %s", rec.Body)
	}
	if log.saved.Provider != "" {
		t.Error("a config that failed its check was saved anyway")
	}

	log.err = nil
	if rec := as(t, s, http.MethodPut, "/admin/llm", good, admin); rec.Code != http.StatusOK {
		t.Fatalf("a working config = %d: %s", rec.Code, rec.Body)
	}
	if log.saved.Account != "acc" || log.saved.Key != "k" {
		t.Errorf("saved %+v", log.saved)
	}
}

func TestLLMConfigAPIKeepsTheStoredKeyWhenTheFormLeavesItBlank(t *testing.T) {
	// The console never receives the key, so it cannot send it back. A save
	// with the box left empty must mean "keep the one you have" rather than
	// "delete it", which would break the station on a model change.
	s, log := llmServer(t)
	log.saved = enrich.LLMConfig{Provider: enrich.ProviderCloudflare,
		Account: "acc", Model: "old", Key: "kept"}

	rec := as(t, s, http.MethodPut, "/admin/llm",
		`{"provider":"cloudflare","account":"acc","model":"new"}`, adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if log.saved.Key != "kept" {
		t.Errorf("key = %q, want the stored one kept", log.saved.Key)
	}
	if log.saved.Model != "new" {
		t.Errorf("model = %q", log.saved.Model)
	}
}

func TestLLMConfigAPIListsTheModelsAPlatformHosts(t *testing.T) {
	s, log := llmServer(t)
	log.models = []string{"@cf/meta/llama-3.3-70b-instruct-fp8-fast", "@cf/meta/llama-3.2-3b-instruct"}

	rec := as(t, s, http.MethodPost, "/admin/llm/models",
		`{"provider":"cloudflare","account":"acc","key":"k"}`, adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "llama-3.3-70b") {
		t.Errorf("body = %s", rec.Body)
	}

	log.err = errors.New("that token was refused")
	rec = as(t, s, http.MethodPost, "/admin/llm/models",
		`{"provider":"cloudflare","account":"acc","key":"k"}`, adminCookie(t, s))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "refused") {
		t.Errorf("a refused listing = %d: %s", rec.Code, rec.Body)
	}
}

func TestLLMConfigAPITestsWithoutSaving(t *testing.T) {
	// Try a model before committing to it: the whole point, since a model can
	// be listed, answer, and still be unable to do the job.
	s, log := llmServer(t)
	body := `{"provider":"openrouter","model":"m","key":"k"}`
	if rec := as(t, s, http.MethodPost, "/admin/llm/test", body, adminCookie(t, s)); rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if log.saved.Provider != "" {
		t.Error("a test saved the configuration")
	}
}

func TestLLMConfigAPIRequiresAnAdmin(t *testing.T) {
	s, _ := llmServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/llm"},
		{http.MethodPut, "/admin/llm"},
		{http.MethodPost, "/admin/llm/test"},
		{http.MethodPost, "/admin/llm/models"},
	} {
		if rec := as(t, s, tc.method, tc.path, "{}", listenerCookie(t, s)); rec.Code != http.StatusForbidden {
			t.Errorf("%s as a listener = %d, want 403", tc.path, rec.Code)
		}
		if rec := as(t, s, tc.method, tc.path, "{}", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s signed out = %d, want 401", tc.path, rec.Code)
		}
	}
}

func TestLLMConfigAPIRejectsBadRequests(t *testing.T) {
	s, _ := llmServer(t)
	admin := adminCookie(t, s)
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{http.MethodPut, "/admin/llm", "not json", http.StatusBadRequest},
		{http.MethodPut, "/admin/llm", `{"provider":"banana"}`, http.StatusBadRequest},
		{http.MethodPost, "/admin/llm/nonsense", "{}", http.StatusNotFound},
		{http.MethodDelete, "/admin/llm", "", http.StatusMethodNotAllowed},
	} {
		if rec := as(t, s, tc.method, tc.path, tc.body, admin); rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body)
		}
	}

	bare, _, _ := authServer(t)
	if rec := as(t, bare, http.MethodGet, "/admin/llm", "", adminCookie(t, bare)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("with nothing wired = %d, want 503", rec.Code)
	}
}

func TestLLMConfigAPICoversEveryRefusal(t *testing.T) {
	s, log := llmServer(t)
	admin := adminCookie(t, s)

	// A test of a configuration that cannot work at all, and one that is
	// complete but whose model does not do the job. Two different messages,
	// because they are two different things to fix.
	if rec := as(t, s, http.MethodPost, "/admin/llm/test", `{"provider":"cloudflare"}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("an incomplete test = %d, want 400", rec.Code)
	}
	log.err = errors.New("that model spent its budget reasoning")
	rec := as(t, s, http.MethodPost, "/admin/llm/test", `{"provider":"openrouter","model":"m","key":"k"}`, admin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reasoning") {
		t.Errorf("a failing test = %d: %s", rec.Code, rec.Body)
	}
	log.err = nil

	// A test needs a body it can read, and so does a listing.
	if rec := as(t, s, http.MethodPost, "/admin/llm/test", "not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a test with no body = %d, want 400", rec.Code)
	}
	// Listing needs a body it can read.
	if rec := as(t, s, http.MethodPost, "/admin/llm/models", "not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a listing with no body = %d, want 400", rec.Code)
	}

	// A save that passes its check and then cannot be written down.
	saveOnly := &failingSave{}
	s.SetLLM(saveOnly)
	body := `{"provider":"openrouter","model":"m","key":"k"}`
	if rec := as(t, s, http.MethodPut, "/admin/llm", body, admin); rec.Code != http.StatusInternalServerError {
		t.Errorf("an unwritable save = %d, want 500", rec.Code)
	}
}

// failingSave passes every check and fails to write.
type failingSave struct{ llmLog }

func (f *failingSave) SetLLMConfig(context.Context, enrich.LLMConfig) error {
	return errors.New("the database went away")
}

// TestModelOutageServesTheHealthWithTheConfiguration: this page tested the
// model once, at save time, and then never again, so the one screen dedicated
// to the language model was the last place that would say it had stopped
// working. It answers with what the model last DID, alongside what is set.
func TestModelOutageServesTheHealthWithTheConfiguration(t *testing.T) {
	s, log := llmServer(t)
	log.saved = enrich.LLMConfig{Provider: enrich.ProviderCloudflare, Key: "a-secret-token"}
	log.health = "degraded: rejected the API key"

	rec := as(t, s, http.MethodGet, "/admin/llm", "", adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Health  string `json:"health"`
		Current struct {
			HasKey bool `json:"has_key"`
		} `json:"current"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if body.Health != "degraded: rejected the API key" {
		t.Errorf("health = %q, want the provider's own reason", body.Health)
	}
	if !body.Current.HasKey {
		t.Error("has_key is false with a key stored")
	}
	// AND STILL NO KEY ANYWHERE IN IT, which is the rule this endpoint has
	// always followed and which a new field is exactly how you break.
	if strings.Contains(rec.Body.String(), "a-secret-token") {
		t.Error("the API key is in the response")
	}
}
