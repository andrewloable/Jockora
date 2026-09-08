// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"errors"
	"sync"
)

// ErrNoModel means no language model has been configured yet.
//
// A REAL STATE, not a fault: a library with no model is a working shuffle, and
// this is what the enricher and the break writer are told rather than a nil
// pointer they would dereference on their own goroutines.
var ErrNoModel = errors.New("enrich: no language model is configured")

// Switchable is the completer everything else holds, so the model behind it can
// change without a restart.
//
// The client is built once at startup and handed to the enricher and to every
// station's break writer; without this indirection, choosing a different
// provider in the console would mean restarting the station -- which is the
// thing the console exists to avoid.
type Switchable struct {
	mu  sync.RWMutex
	cur Completer

	// WHAT THE MODEL LAST DID, which is not the same question as whether one is
	// wired -- and the difference cost this deployment fifty minutes.
	//
	// On 2026-09-08 a Cloudflare token stopped being accepted at 02:58. The
	// station dropped nine consecutive breaks and enrichment died, while
	// /now.json reported "llm": "ok" throughout, because the status was
	// computed from whether a DJ existed. Every call passes through here, so
	// this is the one place that knows the answer.
	failures int
	reason   string
}

// ModelHealth is what the model last did.
//
// Consecutive failures rather than a total: a provider that comes back should
// not carry the memory of an outage, and the console should go green again on
// its own the moment a key is fixed.
type ModelHealth struct {
	// Configured is whether there is a model at all. None is a working
	// shuffle, not a fault.
	Configured bool
	// Failures is how many calls in a row have been refused.
	Failures int
	// Reason is the provider's own words for the last refusal. It names the
	// endpoint and what it objected to; it never carries the key, which is a
	// header rather than part of any URL this builds.
	Reason string
}

// NewSwitchable wraps the client to start with, which may be nil.
func NewSwitchable(c Completer) *Switchable { return &Switchable{cur: c} }

// Use replaces the client. Called from an HTTP handler while breaks are being
// written, so it takes the write lock rather than assigning a field.
func (s *Switchable) Use(c Completer) {
	s.mu.Lock()
	s.cur = c
	s.mu.Unlock()
}

// Configured reports whether there is a model to talk to.
func (s *Switchable) Configured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur != nil
}

// Complete asks whichever model is current, and remembers how it went.
func (s *Switchable) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	s.mu.RLock()
	cur := s.cur
	s.mu.RUnlock()
	if cur == nil {
		return Completion{}, ErrNoModel
	}
	out, err := cur.Complete(ctx, req)
	// A CANCELLED CONTEXT IS THIS PROCESS STOPPING, not the provider refusing.
	// Counting it would turn every shutdown into a red light.
	if ctx.Err() == nil {
		s.record(err)
	}
	return out, err
}

// record keeps the outcome of one call.
func (s *Switchable) record(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.failures++
		s.reason = err.Error()
		return
	}
	s.failures, s.reason = 0, ""
}

// Health is what the model last did, for the status endpoint and the console.
func (s *Switchable) Health() ModelHealth {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ModelHealth{Configured: s.cur != nil, Failures: s.failures, Reason: s.reason}
}
