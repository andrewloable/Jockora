// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// THE DOCUMENTED BASE URLS, PINNED TO THE REQUESTS THEY PRODUCE.
//
// Whether a hosted endpoint works with Jockora is entirely a question of the
// URL and the headers: the "openai" dialect posts to {base}/chat/completions
// with an ordinary Authorization bearer, so any host that accepts that shape
// needs no code. Cloudflare's AI Gateway does, on its PROVIDER-SPECIFIC route
// -- that route is a transparent proxy and takes the provider's own key. Its
// /compat route does not: it authenticates with cf-aig-authorization, which is
// not a header this client sends.
//
// Pinned because docs/configuration.md prints these URLs, and a trailing slash
// or a changed join here would make every one of them wrong with no test to
// say so.
//
// Every test here is TestGatewayURL*, which is the -run pattern.

func TestGatewayURLMatchesWhatTheDocsPrint(t *testing.T) {
	for _, tc := range []struct{ name, base, wantPath string }{
		{
			name:     "OpenRouter direct",
			base:     "https://openrouter.ai/api/v1",
			wantPath: "/api/v1/chat/completions",
		},
		{
			name:     "Cloudflare AI Gateway in front of OpenRouter",
			base:     "https://gateway.ai.cloudflare.com/v1/acct/gw/openrouter/v1",
			wantPath: "/v1/acct/gw/openrouter/v1/chat/completions",
		},
		{
			name:     "Cloudflare Workers AI",
			base:     "https://api.cloudflare.com/client/v4/accounts/acct/ai/v1",
			wantPath: "/client/v4/accounts/acct/ai/v1/chat/completions",
		},
		{
			// A base URL pasted with a trailing slash must not double it.
			name:     "trailing slash",
			base:     "https://gateway.ai.cloudflare.com/v1/acct/gw/openrouter/v1/",
			wantPath: "/v1/acct/gw/openrouter/v1/chat/completions",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
				_ = json.NewEncoder(w).Encode(okReply(`{"ok":true}`, "stop"))
			}))
			defer srv.Close()

			// The client under test, pointed at the fake but built from the
			// documented base so the JOIN is what is being checked.
			base := srv.URL + pathOf(tc.base)
			c := NewOpenAICompatible(base, "some/model", "provider-key", nil)
			if _, err := c.Complete(context.Background(), CompletionRequest{Prompt: "hi"}); err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("posted to %q, want %q", gotPath, tc.wantPath)
			}
			// The provider's own key, in the ordinary header. This is what
			// makes the gateway's provider route a drop-in and its /compat
			// route not.
			if gotAuth != "Bearer provider-key" {
				t.Errorf("Authorization = %q, want the provider key as a bearer", gotAuth)
			}
		})
	}
}

// pathOf is the path part of a documented base URL, so the fake server can
// stand in for the real host without changing the join being tested.
func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Path
}
