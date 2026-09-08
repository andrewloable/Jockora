// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

func llmApp(t *testing.T) (*App, *store.Store) {
	t.Helper()
	s := openUpgraded(t, filepath.Join(t.TempDir(), "llm.db"))
	return &App{
		cfg:  testConfig(t),
		log:  slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		opts: Options{Library: &Library{Store: s}, LLM: enrich.NewSwitchable(nil)},
	}, s
}

func TestLLMConfigAppSavesAndSwitchesLive(t *testing.T) {
	// Everything that writes holds the same switchable, so the next break uses
	// the new model without a restart -- which is the whole point of the page.
	a, _ := llmApp(t)
	ctx := context.Background()

	if _, ok := a.LLMSettings(); ok {
		t.Fatal("a fresh library reported a model")
	}
	if a.opts.LLM.Configured() {
		t.Fatal("configured before anything was chosen")
	}

	cfg := enrich.LLMConfig{Provider: enrich.ProviderLlamaCPP, URL: "http://127.0.0.1:8081"}
	if err := a.SetLLMConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if !a.opts.LLM.Configured() {
		t.Error("the running station was not pointed at the new model")
	}
	got, ok := a.LLMSettings()
	if !ok || got.URL != "http://127.0.0.1:8081" {
		t.Errorf("= %+v ok=%v", got, ok)
	}
}

func TestLLMConfigAppRefusesWhatCannotWork(t *testing.T) {
	a, _ := llmApp(t)
	ctx := context.Background()
	if err := a.SetLLMConfig(ctx, enrich.LLMConfig{Provider: "banana"}); err == nil {
		t.Error("saved an unknown provider")
	}
	if err := a.TestLLMConfig(ctx, enrich.LLMConfig{Provider: enrich.ProviderOpenRouter}); err == nil {
		t.Error("tested a configuration with no key")
	}

	// No library is a real state: the spike path has no database to save in.
	bare := &App{cfg: testConfig(t), log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if _, ok := bare.LLMSettings(); ok {
		t.Error("settings from a server with no library")
	}
	if err := bare.SetLLMConfig(ctx, enrich.LLMConfig{
		Provider: enrich.ProviderLlamaCPP, URL: "http://x"}); err == nil {
		t.Error("saved with no library to save into")
	}
}

func TestLLMConfigAppTestsAgainstTheRealCheck(t *testing.T) {
	// The same health check the server makes at startup: a model that answers
	// but ignores the schema makes ungrounded facts possible.
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":"{\"ok\":true}","stop_type":"eos"}`))
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusServiceUnavailable)
	}))
	defer bad.Close()

	a, _ := llmApp(t)
	ctx := context.Background()
	if err := a.TestLLMConfig(ctx, enrich.LLMConfig{
		Provider: enrich.ProviderLlamaCPP, URL: good.URL}); err != nil {
		t.Errorf("a working server was refused: %v", err)
	}
	if err := a.TestLLMConfig(ctx, enrich.LLMConfig{
		Provider: enrich.ProviderLlamaCPP, URL: bad.URL}); err == nil {
		t.Error("a server that is not answering passed")
	}
}

func TestLLMConfigAppListsModels(t *testing.T) {
	list := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"one"},{"id":"two"}]}`))
	}))
	defer list.Close()

	a, _ := llmApp(t)
	got, err := a.LLMModels(context.Background(),
		enrich.LLMConfig{Provider: enrich.ProviderLlamaCPP, URL: list.URL})
	if err != nil || len(got) != 2 {
		t.Errorf("= %v, %v", got, err)
	}
}

func TestLLMConfigAppReportsUnreadableSettings(t *testing.T) {
	// Written by this program and unreadable now. Falling back silently would
	// make the station ignore the operator's choice without saying so.
	a, s := llmApp(t)
	if err := s.SetSetting(context.Background(), "llm_config", "not json"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.LLMSettings(); ok {
		t.Error("unreadable settings were reported as a choice")
	}
}

func TestLLMConfigAppSaysWhenItCannotWriteTheChoice(t *testing.T) {
	// Applied but not remembered is the worst outcome: the station would use
	// the new model until it restarted and then quietly go back.
	a, s := llmApp(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	err := a.SetLLMConfig(context.Background(),
		enrich.LLMConfig{Provider: enrich.ProviderLlamaCPP, URL: "http://127.0.0.1:8081"})
	if err == nil {
		t.Error("a choice that could not be written down reported success")
	}
	if a.opts.LLM.Configured() {
		t.Error("the station was switched to a choice that was not saved")
	}
}

func TestLLMConfigAppShowsWhatIsActuallyInUse(t *testing.T) {
	// An operator opening the page while the station happily runs on the
	// startup configuration must not be told nothing is chosen. That reads as
	// broken, and the first thing they would do is choose something -- losing
	// the working configuration to a form they filled in blind.
	a, _ := llmApp(t)
	a.cfg.LLMAPI = "openai"
	a.cfg.LLMBaseURL = "https://api.cloudflare.com/client/v4/accounts/acc123/ai/v1"
	a.cfg.LLMModelPath = "@cf/meta/llama-3.3-70b-instruct-fp8-fast"
	a.cfg.LLMAPIKey = "from-the-environment"

	got, ok := a.LLMSettings()
	if !ok {
		t.Fatal("the page was told nothing is configured while the station is using one")
	}
	if got.Provider != enrich.ProviderCloudflare {
		t.Errorf("provider = %q, want it recognised from the URL", got.Provider)
	}
	if got.Account != "acc123" {
		t.Errorf("account = %q, want it read out of the URL", got.Account)
	}
	if got.Model != "@cf/meta/llama-3.3-70b-instruct-fp8-fast" {
		t.Errorf("model = %q", got.Model)
	}
	if got.Key != "from-the-environment" {
		t.Error("the key from the environment did not carry over")
	}
}

func TestLLMConfigAppRecognisesEveryStartupShape(t *testing.T) {
	for _, tc := range []struct {
		url, api, want string
	}{
		{"https://openrouter.ai/api/v1", "openai", enrich.ProviderOpenRouter},
		{"http://127.0.0.1:8081", "llamacpp", enrich.ProviderLlamaCPP},
		{"https://api.cloudflare.com/client/v4/accounts/x/ai/v1", "openai", enrich.ProviderCloudflare},
		// Something else OpenAI-shaped is still reachable, as OpenRouter is:
		// one key, one base URL, one model.
		{"https://example.test/v1", "openai", enrich.ProviderOpenRouter},
	} {
		a, _ := llmApp(t)
		a.cfg.LLMBaseURL, a.cfg.LLMAPI = tc.url, tc.api
		a.cfg.LLMModelPath, a.cfg.LLMAPIKey = "m", "k"
		got, ok := a.LLMSettings()
		if !ok || got.Provider != tc.want {
			t.Errorf("%s -> %q ok=%v, want %q", tc.url, got.Provider, ok, tc.want)
		}
	}

	// A Workers AI URL with nothing after the account, and one with no account
	// segment at all: both are shapes a pasted URL can take.
	for _, tc := range []struct{ url, want string }{
		{"https://api.cloudflare.com/client/v4/accounts/tail", "tail"},
		{"https://api.cloudflare.com/client/v4/ai/v1", ""},
	} {
		if got := cloudflareAccount(tc.url); got != tc.want {
			t.Errorf("cloudflareAccount(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}

	// And nothing configured at all is still nothing.
	a, _ := llmApp(t)
	a.cfg.LLMBaseURL = ""
	if _, ok := a.LLMSettings(); ok {
		t.Error("reported a configuration with no URL")
	}
}

func TestLLMConfigAppPrefersWhatTheOperatorChose(t *testing.T) {
	// The stored choice is what they last said; the environment is the default
	// underneath it, exactly as the cadence works.
	a, _ := llmApp(t)
	a.cfg.LLMBaseURL = "https://openrouter.ai/api/v1"
	a.cfg.LLMModelPath = "from-env"
	if err := a.SetLLMConfig(context.Background(), enrich.LLMConfig{
		Provider: enrich.ProviderLlamaCPP, URL: "http://chosen:8081"}); err != nil {
		t.Fatal(err)
	}
	got, _ := a.LLMSettings()
	if got.URL != "http://chosen:8081" {
		t.Errorf("= %+v, want the operator's choice", got)
	}
}
