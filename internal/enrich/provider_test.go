// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// CHOOSING THE MODEL FROM THE CONSOLE. In one evening this deployment moved
// provider three times: a withdrawn free tier took the station off air, and
// every alternative failed differently. Each move was an ssh, an edit and a
// restart.

func TestLLMConfigProvidersAreDescribed(t *testing.T) {
	got := Providers()
	if len(got) != 3 {
		t.Fatalf("%d providers, want llama.cpp, OpenRouter and Cloudflare", len(got))
	}
	for _, p := range got {
		if p.ID == "" || p.Name == "" || p.Help == "" {
			t.Errorf("%+v is missing its name or its help", p)
		}
		// THE PAGE HAS TO SAY WHERE THE KEY COMES FROM. A field an operator
		// cannot fill is a field that sends them to a search engine.
		if p.NeedsKey && p.KeyHelp == "" {
			t.Errorf("%s wants a key and does not say where to get one", p.ID)
		}
		if p.NeedsKey && !strings.Contains(p.KeyHelp, "http") {
			t.Errorf("%s does not link to where the key is made: %q", p.ID, p.KeyHelp)
		}
	}
}

func TestLLMConfigBuildsTheRightClient(t *testing.T) {
	// A local server needs no key and no model: it was started with one.
	local, err := LLMConfig{Provider: ProviderLlamaCPP, URL: "http://127.0.0.1:8081"}.Client(nil)
	if err != nil || local == nil {
		t.Fatalf("llama.cpp client: %v", err)
	}
	// Cloudflare's URL is BUILT from the account id, because an operator
	// copying a URL out of documentation gets the account part wrong.
	cf := LLMConfig{Provider: ProviderCloudflare, Account: "abc123",
		Model: "@cf/meta/llama-3.3-70b-instruct-fp8-fast", Key: "k"}
	if got := cf.BaseURL(); !strings.Contains(got, "abc123") || !strings.HasSuffix(got, "/ai/v1") {
		t.Errorf("cloudflare url = %q", got)
	}
	// OpenRouter's is fixed, so it cannot be typed wrong at all.
	or := LLMConfig{Provider: ProviderOpenRouter, Model: "x", Key: "k"}
	if got := or.BaseURL(); got != OpenRouterURL {
		t.Errorf("openrouter url = %q, want the fixed one", got)
	}
}

func TestLLMConfigRefusesWhatItCannotUse(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  LLMConfig
		want string
	}{
		{"no provider", LLMConfig{}, "provider"},
		{"unknown provider", LLMConfig{Provider: "banana"}, "provider"},
		{"local with no url", LLMConfig{Provider: ProviderLlamaCPP}, "address"},
		{"openrouter with no key", LLMConfig{Provider: ProviderOpenRouter, Model: "m"}, "key"},
		{"openrouter with no model", LLMConfig{Provider: ProviderOpenRouter, Key: "k"}, "model"},
		{"cloudflare with no account", LLMConfig{Provider: ProviderCloudflare, Model: "m", Key: "k"}, "account"},
	} {
		err := tc.cfg.Validate()
		if err == nil {
			t.Errorf("%s was accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want it to name the %s", tc.name, err, tc.want)
		}
	}
	ok := LLMConfig{Provider: ProviderCloudflare, Account: "a", Model: "m", Key: "k"}
	if err := ok.Validate(); err != nil {
		t.Errorf("a complete config was refused: %v", err)
	}
}

func TestLLMConfigListsTheModelsAPlatformHosts(t *testing.T) {
	// A slug typed by hand is a 404 the operator meets at the next break.
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/ai/models/search") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []map[string]any{
			{"name": "@cf/meta/llama-3.3-70b-instruct-fp8-fast", "task": map[string]any{"name": "Text Generation"}},
			{"name": "@cf/baai/bge-m3", "task": map[string]any{"name": "Text Embeddings"}},
		}})
	}))
	defer cf.Close()

	got, err := LLMConfig{Provider: ProviderCloudflare, Account: "a", Key: "k",
		listBase: cf.URL}.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// ONLY the models that could write a break. An embeddings model in the
	// picker is a choice that can only be wrong.
	if len(got) != 1 || got[0] != "@cf/meta/llama-3.3-70b-instruct-fp8-fast" {
		t.Errorf("models = %v, want only the text generation one", got)
	}
}

func TestLLMConfigListsOpenRouterModels(t *testing.T) {
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "minimax/minimax-m3"}, {"id": "meta-llama/llama-3.3-70b-instruct"},
		}})
	}))
	defer or.Close()

	got, err := LLMConfig{Provider: ProviderOpenRouter, Key: "k",
		listBase: or.URL}.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "meta-llama/llama-3.3-70b-instruct" {
		t.Errorf("models = %v, want both, sorted", got)
	}
}

func TestLLMConfigListsTheOneModelALocalServerLoaded(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "/models/Qwen3.5-4B.Q4_K_M.gguf"},
		}})
	}))
	defer local.Close()

	got, err := LLMConfig{Provider: ProviderLlamaCPP, URL: local.URL}.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("models = %v, want the one it was started with", got)
	}
}

func TestLLMConfigSaysWhenAListingFails(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer dead.Close()

	if _, err := (LLMConfig{Provider: ProviderOpenRouter, Key: "k",
		listBase: dead.URL}).Models(context.Background()); err == nil {
		t.Error("a refused listing was reported as an empty list")
	}
}

func TestLLMConfigSwitchesWithoutARestart(t *testing.T) {
	// The completer handed to the enricher and to every station's break writer
	// is built once. Without an indirection, changing provider means a restart
	// -- which is exactly what the console exists to avoid.
	first := &stubCompleter{out: Completion{Content: "one"}}
	sw := NewSwitchable(first)

	got, err := sw.Complete(context.Background(), CompletionRequest{})
	if err != nil || got.Content != "one" {
		t.Fatalf("= %+v, %v", got, err)
	}

	sw.Use(&stubCompleter{out: Completion{Content: "two"}})
	got, _ = sw.Complete(context.Background(), CompletionRequest{})
	if got.Content != "two" {
		t.Errorf("= %q after switching, want the new client", got.Content)
	}
}

func TestLLMConfigSwitchableSaysWhenNothingIsConfigured(t *testing.T) {
	// A station with no model yet must answer plainly rather than dereference
	// nothing: this runs on the break goroutine.
	sw := NewSwitchable(nil)
	if _, err := sw.Complete(context.Background(), CompletionRequest{}); err == nil {
		t.Error("a request with no model configured was accepted")
	}
	if sw.Configured() {
		t.Error("Configured with no client")
	}
	sw.Use(&stubCompleter{})
	if !sw.Configured() {
		t.Error("not Configured after a client was set")
	}
}

func TestLLMConfigSwitchableUnderRace(t *testing.T) {
	// Saved from an HTTP handler, read on every break and every enrichment.
	sw := NewSwitchable(&stubCompleter{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			sw.Use(&stubCompleter{})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = sw.Complete(context.Background(), CompletionRequest{})
			_ = sw.Configured()
		}
	}()
	wg.Wait()
}

// stubCompleter answers with whatever it was built with.
type stubCompleter struct{ out Completion }

func (s *stubCompleter) Complete(context.Context, CompletionRequest) (Completion, error) {
	return s.out, nil
}

func TestLLMConfigClientRefusesAnIncompleteConfig(t *testing.T) {
	if _, err := (LLMConfig{Provider: ProviderOpenRouter}).Client(nil); err == nil {
		t.Error("built a client from a configuration that cannot work")
	}
	// A hosted one is the OpenAI-compatible client; a local one is the native
	// route, where the schema binds at the sampler rather than being advisory.
	hosted, err := LLMConfig{Provider: ProviderOpenRouter, Model: "m", Key: "k"}.Client(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hosted.(*OpenAICompatible); !ok {
		t.Errorf("hosted client is %T", hosted)
	}
	local, err := LLMConfig{Provider: ProviderLlamaCPP, URL: "http://x"}.Client(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := local.(*LlamaCPP); !ok {
		t.Errorf("local client is %T", local)
	}
	// A local URL is used as given, so an operator can point at another host.
	if got := (LLMConfig{Provider: ProviderLlamaCPP, URL: "http://box:8081"}).BaseURL(); got != "http://box:8081" {
		t.Errorf("local url = %q", got)
	}
}

func TestLLMConfigNamesEveryMissingField(t *testing.T) {
	// The remaining limbs of Validate: each one has to name its own field,
	// because "invalid configuration" sends an operator back to a form with no
	// idea which box is wrong.
	for _, tc := range []struct {
		cfg  LLMConfig
		want string
	}{
		{LLMConfig{Provider: ProviderCloudflare, Account: "a", Model: "m"}, "token"},
		{LLMConfig{Provider: ProviderCloudflare, Account: "a", Key: "k"}, "model"},
	} {
		err := tc.cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v = %v, want it to name the %s", tc.cfg, err, tc.want)
		}
	}
}

func TestLLMConfigSaysWhenAListingIsNotJSON(t *testing.T) {
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer junk.Close()

	for _, c := range []LLMConfig{
		{Provider: ProviderOpenRouter, Key: "k", listBase: junk.URL},
		{Provider: ProviderCloudflare, Account: "a", Key: "k", listBase: junk.URL},
	} {
		if _, err := c.Models(context.Background()); err == nil {
			t.Errorf("%s accepted a page of HTML as a model list", c.Provider)
		}
	}
	// And an address nothing is listening on.
	if _, err := (LLMConfig{Provider: ProviderLlamaCPP, URL: "http://127.0.0.1:1"}).Models(context.Background()); err == nil {
		t.Error("a dead address listed models")
	}
	// And one that is not an address at all, which is what a pasted line with
	// a stray character in it looks like.
	if _, err := (LLMConfig{Provider: ProviderLlamaCPP, URL: "http://\x7f"}).Models(context.Background()); err == nil {
		t.Error("a malformed address listed models")
	}
}

func TestLLMConfigCheckProvesAModelBeforeItIsChosen(t *testing.T) {
	// The same call the server makes at startup. A model that answers and
	// ignores the schema makes ungrounded facts possible, which is why the
	// console tests the model and not merely the address.
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":"{\"ok\":true}","stop_type":"eos"}`))
	}))
	defer good.Close()
	if err := (LLMConfig{Provider: ProviderLlamaCPP, URL: good.URL}).Check(context.Background()); err != nil {
		t.Errorf("a working local server was refused: %v", err)
	}

	hosted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	}))
	defer hosted.Close()
	// A hosted provider whose model has gone away, which is exactly how this
	// deployment lost its DJ.
	if err := (LLMConfig{Provider: ProviderCloudflare, Account: "a", Model: "m", Key: "k",
		listBase: hosted.URL}).Check(context.Background()); err == nil {
		t.Error("a withdrawn model passed its check")
	}
	// And one that cannot work at all is refused before anything is dialled.
	if err := (LLMConfig{Provider: ProviderOpenRouter}).Check(context.Background()); err == nil {
		t.Error("an incomplete configuration was checked rather than refused")
	}
}
