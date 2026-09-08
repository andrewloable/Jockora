// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

// Choosing the language model from the console.
//
// The client is built once at startup and handed to the enricher and to every
// station's break writer, so changing provider used to mean an ssh, an edit of
// a compose file and a restart. This deployment did that three times in one
// evening -- a withdrawn free tier took the station off air, and every
// alternative failed differently -- which is the whole argument for the screen
// these methods serve.

// LLMSettings is the current choice, key included.
//
// The KEY is here because the handler needs it to carry across a save that left
// the box blank; the handler is what makes sure it never reaches the browser.
func (a *App) LLMSettings() (enrich.LLMConfig, bool) {
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return enrich.LLMConfig{}, false
	}
	s, ok, err := a.opts.Library.Store.LLMSettings(context.Background())
	if err == nil && ok {
		return enrich.LLMConfig{Provider: s.Provider, URL: s.URL, Account: s.Account,
			Model: s.Model, Key: s.Key}, true
	}
	// NOTHING STORED YET, so show what the station is ACTUALLY using. Telling
	// an operator nothing is chosen while it plays reads as broken, and the
	// first thing they would do is choose something -- losing a working
	// configuration to a form filled in blind.
	return a.startupLLM()
}

// startupLLM is the flags and environment, read back as a provider choice.
func (a *App) startupLLM() (enrich.LLMConfig, bool) {
	url := a.cfg.LLMBaseURL
	if url == "" {
		return enrich.LLMConfig{}, false
	}
	c := enrich.LLMConfig{Model: a.cfg.LLMModelPath, Key: a.cfg.LLMAPIKey}
	switch {
	case strings.Contains(url, "api.cloudflare.com"):
		c.Provider = enrich.ProviderCloudflare
		c.Account = cloudflareAccount(url)
	case !strings.EqualFold(a.cfg.LLMAPI, "openai"):
		// Anything not spoken to as OpenAI-compatible is the native route,
		// which is llama.cpp.
		c.Provider = enrich.ProviderLlamaCPP
		c.URL = url
	default:
		// OpenRouter, or something else with one key, one base URL and one
		// model -- which is the same form.
		c.Provider = enrich.ProviderOpenRouter
	}
	return c, true
}

// cloudflareAccount reads the account id out of a Workers AI URL, so the page
// can show it in its own box rather than as part of an address.
func cloudflareAccount(url string) string {
	const marker = "/accounts/"
	i := strings.Index(url, marker)
	if i < 0 {
		return ""
	}
	rest := url[i+len(marker):]
	if j := strings.Index(rest, "/"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// TestLLMConfig proves a configuration by asking the model to honour a schema.
//
// THE SAME CHECK THE SERVER MAKES AT STARTUP, deliberately: a model that
// answers but ignores the schema makes ungrounded facts possible, and every
// provider failure met on this deployment was diagnosable in exactly this one
// call.
func (a *App) TestLLMConfig(ctx context.Context, c enrich.LLMConfig) error {
	return c.Check(ctx)
}

// SetLLMConfig saves the choice and points the running station at it.
func (a *App) SetLLMConfig(ctx context.Context, c enrich.LLMConfig) error {
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return fmt.Errorf("no library to save the model settings in")
	}
	client, err := c.Client(nil)
	if err != nil {
		return err
	}
	if err := a.opts.Library.Store.SaveLLMSettings(ctx, store.LLMSettings{
		Provider: c.Provider, URL: c.URL, Account: c.Account, Model: c.Model, Key: c.Key,
	}); err != nil {
		return err
	}
	// LIVE. Everything that writes -- the enricher and every station's break
	// writer -- holds the same switchable, so the next break uses the new model
	// without a restart.
	if a.opts.LLM != nil {
		a.opts.LLM.Use(client)
	}
	// AND THE WORKER THAT GAVE UP. Enrichment ends its run after ten refusals
	// in a row, so without this an operator who fixes a rejected key gets
	// breaks back, sees a green light, and never learns that enrichment has
	// been dead since the outage.
	// AND THE WORKER THAT GAVE UP. Enrichment ends its run after ten refusals
	// in a row, so without this an operator who fixes a rejected key gets
	// breaks back, sees a green light, and never learns that enrichment has
	// been dead since the outage.
	a.wakeEnrichment()
	a.log.Info("language model changed", "provider", c.Provider, "model", c.Model)
	return nil
}

// LLMModels is what the chosen platform hosts.
func (a *App) LLMModels(ctx context.Context, c enrich.LLMConfig) ([]string, error) {
	return c.Models(ctx)
}

// wakeEnrichment restarts a worker that gave up on the old model.
//
// Non-blocking on a buffered channel: the signal is "there is a new model", not
// a queue of them, and this runs on an HTTP handler that must never park. Safe
// on an App with no worker at all, which is every test that does not build one.
func (a *App) wakeEnrichment() {
	if a.enrichWake == nil {
		return
	}
	select {
	case a.enrichWake <- struct{}{}:
	default:
	}
}

// LLMHealth is what the model last did, in the provider's own words.
func (a *App) LLMHealth() string {
	return llmHealth(a.opts.LLM, a.hasDJ() || a.opts.Enricher != nil)
}
