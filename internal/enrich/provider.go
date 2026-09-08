// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The three ways to reach a language model.
//
// Named rather than free-form, because the differences between them are not
// cosmetic: one needs no key, one has a fixed address, and one builds its
// address out of an account id. An operator choosing "OpenRouter" should not
// have to know any of that.
const (
	ProviderLlamaCPP   = "llamacpp"
	ProviderOpenRouter = "openrouter"
	ProviderCloudflare = "cloudflare"
)

// OpenRouterURL is fixed, so it cannot be typed wrong.
const OpenRouterURL = "https://openrouter.ai/api/v1"

// cloudflareBase is where an account's Workers AI lives.
const cloudflareBase = "https://api.cloudflare.com/client/v4"

// Provider describes one option to the page that offers it.
//
// THE SERVER OWNS THE HELP so it cannot drift from the validation. A field the
// console asks for and the server does not want, or a key with no word about
// where to get one, is a form an operator abandons.
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Help string `json:"help"`
	// NeedsURL, NeedsKey, NeedsAccount and NeedsModel say which fields to show.
	NeedsURL     bool   `json:"needs_url"`
	NeedsKey     bool   `json:"needs_key"`
	NeedsAccount bool   `json:"needs_account"`
	NeedsModel   bool   `json:"needs_model"`
	KeyHelp      string `json:"key_help,omitempty"`
	AccountHelp  string `json:"account_help,omitempty"`
	URLHelp      string `json:"url_help,omitempty"`
	// Suggested is a model known to work, so the picker opens on something
	// sensible rather than on the first slug alphabetically.
	Suggested string `json:"suggested,omitempty"`
}

// Providers is every option, with the words the console shows.
func Providers() []Provider {
	return []Provider{
		{
			ID: ProviderLlamaCPP, Name: "Local llama.cpp",
			Help: "A model running on your own hardware. Nothing leaves the machine and " +
				"nothing is charged. Start it with: llama-server -m model.gguf --host 0.0.0.0 --port 8081",
			NeedsURL: true,
			URLHelp:  "Where llama-server is listening. Usually http://127.0.0.1:8081.",
		},
		{
			ID: ProviderOpenRouter, Name: "OpenRouter",
			Help: "One key, many providers. Free tiers are withdrawn without notice -- " +
				"this station lost one mid-run -- so prefer a paid model for anything you rely on.",
			NeedsKey: true, NeedsModel: true,
			KeyHelp:   "Create one at https://openrouter.ai/keys and paste it here.",
			Suggested: "meta-llama/llama-3.3-70b-instruct",
		},
		{
			ID: ProviderCloudflare, Name: "Cloudflare Workers AI",
			Help: "Cheap, fast, and priced per token. A day of radio costs pennies and " +
				"a whole library's enrichment costs a couple of pounds.",
			NeedsKey: true, NeedsAccount: true, NeedsModel: true,
			KeyHelp: "Cloudflare dashboard, AI, Workers AI, then Use REST API. It makes a " +
				"token with Workers AI permission: https://dash.cloudflare.com/profile/api-tokens",
			AccountHelp: "The account ID on the right of any Cloudflare dashboard page, or run: wrangler whoami",
			Suggested:   "@cf/meta/llama-3.3-70b-instruct-fp8-fast",
		},
	}
}

// LLMConfig is one chosen way to reach a model.
type LLMConfig struct {
	Provider string `json:"provider"`
	URL      string `json:"url,omitempty"`
	Account  string `json:"account,omitempty"`
	Model    string `json:"model,omitempty"`
	// Key NEVER travels back to the console. See the API layer: it answers
	// with a boolean saying whether one is set, the way a user answers with a
	// role and never a password hash.
	Key string `json:"key,omitempty"`

	// listBase overrides where models are listed FROM, for tests only.
	listBase string
}

// Validate refuses a configuration that cannot work, naming the missing field.
//
// BEFORE anything is saved or dialled, because "it does not work" arrives at
// the next break otherwise, minutes later and in a log.
func (c LLMConfig) Validate() error {
	switch c.Provider {
	case ProviderLlamaCPP:
		if strings.TrimSpace(c.URL) == "" {
			return fmt.Errorf("a local model needs the address llama-server is listening on")
		}
	case ProviderOpenRouter:
		if strings.TrimSpace(c.Key) == "" {
			return fmt.Errorf("OpenRouter needs an API key")
		}
		if strings.TrimSpace(c.Model) == "" {
			return fmt.Errorf("OpenRouter needs a model")
		}
	case ProviderCloudflare:
		if strings.TrimSpace(c.Account) == "" {
			return fmt.Errorf("Cloudflare needs the account id its Workers AI belongs to")
		}
		if strings.TrimSpace(c.Key) == "" {
			return fmt.Errorf("Cloudflare needs an API token")
		}
		if strings.TrimSpace(c.Model) == "" {
			return fmt.Errorf("Cloudflare needs a model")
		}
	default:
		return fmt.Errorf("choose a provider: local llama.cpp, OpenRouter or Cloudflare")
	}
	return nil
}

// BaseURL is where this configuration talks to.
func (c LLMConfig) BaseURL() string {
	switch c.Provider {
	case ProviderOpenRouter:
		return OpenRouterURL
	case ProviderCloudflare:
		return fmt.Sprintf("%s/accounts/%s/ai/v1", cloudflareBase, c.Account)
	default:
		return c.URL
	}
}

// Client builds the completer this configuration describes.
func (c LLMConfig) Client(hc *http.Client) (Completer, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Provider == ProviderLlamaCPP {
		// The native route, where a grammar makes the schema binding at the
		// sampler rather than advisory.
		return NewLlamaCPP(c.URL, hc), nil
	}
	return NewOpenAICompatible(c.BaseURL(), c.Model, c.Key, hc), nil
}

// Check proves a configuration by asking the model to honour a schema.
//
// Switched on the provider rather than type-asserted for a Health method: both
// clients have one, so the assertion could never fail, and a branch no input
// can reach is a branch no test can cover honestly.
func (c LLMConfig) Check(ctx context.Context) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Provider == ProviderLlamaCPP {
		return NewLlamaCPP(c.URL, nil).Health(ctx)
	}
	return NewOpenAICompatible(c.BaseURL(), c.Model, c.Key, nil).Health(ctx)
}

// Models is what this platform hosts, filtered to what could write a break.
//
// A SLUG TYPED BY HAND IS A 404 the operator meets at the next break. The list
// is not sufficient on its own -- a model can be listed, answer, and still be
// unusable, which is what the Test button is for -- but it removes the
// typing mistakes entirely.
func (c LLMConfig) Models(ctx context.Context) ([]string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	switch c.Provider {
	case ProviderCloudflare:
		return c.cloudflareModels(ctx, client)
	case ProviderOpenRouter:
		return c.openAIModels(ctx, client, firstNonEmpty(c.listBase, OpenRouterURL)+"/models")
	default:
		return c.openAIModels(ctx, client, strings.TrimSuffix(firstNonEmpty(c.listBase, c.URL), "/")+"/v1/models")
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// cloudflareModels asks the account what it can run, keeping only text
// generation: an embeddings model in the picker is a choice that can only be
// wrong.
func (c LLMConfig) cloudflareModels(ctx context.Context, client *http.Client) ([]string, error) {
	base := firstNonEmpty(c.listBase, cloudflareBase)
	url := fmt.Sprintf("%s/accounts/%s/ai/models/search?per_page=200", base, c.Account)
	var body struct {
		Result []struct {
			Name string `json:"name"`
			Task struct {
				Name string `json:"name"`
			} `json:"task"`
		} `json:"result"`
	}
	if err := c.getJSON(ctx, client, url, &body); err != nil {
		return nil, err
	}
	out := []string{}
	for _, m := range body.Result {
		if m.Task.Name == "Text Generation" {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// openAIModels reads an OpenAI-shaped /models listing.
func (c LLMConfig) openAIModels(ctx context.Context, client *http.Client, url string) ([]string, error) {
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, client, url, &body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	sort.Strings(out)
	return out, nil
}

func (c LLMConfig) getJSON(ctx context.Context, client *http.Client, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("enrich: asking %s for its models: %w", c.Provider, err)
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("enrich: asking %s for its models: %w", c.Provider, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	if resp.StatusCode != http.StatusOK {
		// NAMED, because the two reasons are different actions: a rejected key
		// is a key to replace, and a refused account is an id to correct.
		return fmt.Errorf("enrich: %s answered %s listing its models; check the key and the account",
			c.Provider, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("enrich: reading %s's model list: %w", c.Provider, err)
	}
	return nil
}
