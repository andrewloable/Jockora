// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"testing"
)

func TestLLMConfigStoreRoundTrip(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()

	if _, ok, err := s.LLMSettings(ctx); ok || err != nil {
		t.Fatalf("a fresh database reported settings: ok=%v err=%v", ok, err)
	}

	want := LLMSettings{Provider: "cloudflare", Account: "abc", Model: "@cf/x", Key: "secret"}
	if err := s.SaveLLMSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LLMSettings(ctx)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Errorf("= %+v, want %+v", got, want)
	}

	// Saving again REPLACES: a provider is a choice, not a history.
	if err := s.SaveLLMSettings(ctx, LLMSettings{Provider: "openrouter", Model: "m", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.LLMSettings(ctx)
	if got.Provider != "openrouter" || got.Account != "" {
		t.Errorf("= %+v, want the new choice with nothing left of the old", got)
	}
}

func TestLLMConfigStoreSurfacesFailures(t *testing.T) {
	s := jockStore(t)
	ctx := context.Background()
	// Written by this program and unreadable now. Silently falling back to the
	// startup configuration would make the station ignore the operator's
	// choice without saying so.
	if err := s.SetSetting(ctx, llmSetting, "not json"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LLMSettings(ctx); err == nil {
		t.Error("unreadable settings were reported as none")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLLMSettings(ctx, LLMSettings{Provider: "openrouter"}); err == nil {
		t.Error("saved against a closed database")
	}
	if _, _, err := s.LLMSettings(ctx); err == nil {
		t.Error("read from a closed database")
	}
}
