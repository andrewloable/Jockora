// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A REAL OUTAGE, 2026-09-08: a Cloudflare token stopped being accepted at
// 02:58 and the station dropped nine consecutive breaks while /now.json
// reported "llm": "ok" for fifty minutes. The status was computed from whether
// a DJ was WIRED, never from whether the model ANSWERED. These pin the other
// half: the completer everything shares remembers what the model last did.

type refusingCompleter struct {
	err  error
	call int
}

func (r *refusingCompleter) Complete(context.Context, CompletionRequest) (Completion, error) {
	r.call++
	if r.err != nil {
		return Completion{}, r.err
	}
	return Completion{Content: "{}"}, nil
}

func TestModelOutageRemembersThatTheModelIsRefusing(t *testing.T) {
	c := &refusingCompleter{err: errors.New("rejected the API key")}
	s := NewSwitchable(c)

	if h := s.Health(); h.Failures != 0 || h.Reason != "" {
		t.Fatalf("a completer that has not been called yet is already failing: %+v", h)
	}

	for i := 0; i < 3; i++ {
		if _, err := s.Complete(context.Background(), CompletionRequest{}); err == nil {
			t.Fatal("the stub was supposed to refuse")
		}
	}
	h := s.Health()
	if h.Failures != 3 {
		t.Errorf("failures = %d, want 3", h.Failures)
	}
	if !strings.Contains(h.Reason, "rejected the API key") {
		t.Errorf("reason = %q, want the provider's own words", h.Reason)
	}
	if !h.Configured {
		t.Error("a model that is refusing is still a model that is configured")
	}
}

// TestModelOutageRecoversOnItsOwn: the operator fixes the key, the next call
// works, and the status has to go back to ok WITHOUT a restart -- otherwise the
// console shows a permanent red light and the fix looks like it did nothing.
func TestModelOutageRecoversOnItsOwn(t *testing.T) {
	c := &refusingCompleter{err: errors.New("rejected the API key")}
	s := NewSwitchable(c)
	if _, err := s.Complete(context.Background(), CompletionRequest{}); err == nil {
		t.Fatal("expected a refusal")
	}
	c.err = nil
	if _, err := s.Complete(context.Background(), CompletionRequest{}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if h := s.Health(); h.Failures != 0 || h.Reason != "" {
		t.Errorf("still reporting a failure after a good call: %+v", h)
	}
}

// TestModelOutageDoesNotBlameTheModelForAShutdown: cancelling the context is
// this process stopping, not the provider refusing. Counting it would light the
// console red on every shutdown.
func TestModelOutageDoesNotBlameTheModelForAShutdown(t *testing.T) {
	s := NewSwitchable(&refusingCompleter{err: context.Canceled})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = s.Complete(ctx, CompletionRequest{})
	if h := s.Health(); h.Failures != 0 {
		t.Errorf("a cancelled context counted as the model refusing: %+v", h)
	}
}

// TestModelOutageSaysNothingIsConfigured: no model at all is a working shuffle,
// not a failure, and it must not read as one.
func TestModelOutageSaysNothingIsConfigured(t *testing.T) {
	h := NewSwitchable(nil).Health()
	if h.Configured {
		t.Error("an empty switchable claims a model")
	}
	if h.Failures != 0 {
		t.Errorf("an unconfigured model is reported as failing: %+v", h)
	}
}

// TestModelOutageNeverPutsTheKeyInTheReason: the reason is shown in the console
// and written to a log. The provider's message names the endpoint and says the
// key was rejected; it must never carry the key itself.
func TestModelOutageNeverPutsTheKeyInTheReason(t *testing.T) {
	const key = "hunter2-not-a-real-token"
	c := LLMConfig{Provider: ProviderCloudflare, Account: "acct", Model: "@cf/m", Key: key}
	client, err := c.Client(nil)
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	s := NewSwitchable(client)
	_, _ = s.Complete(context.Background(), CompletionRequest{NPredict: 1})
	if strings.Contains(s.Health().Reason, key) {
		t.Error("the API key is in the health reason, which the console renders and the log keeps")
	}
}
