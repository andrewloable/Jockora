// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

// THE OUTAGE OF 2026-09-08. A Cloudflare token stopped being accepted at 02:58;
// the station dropped nine breaks in a row and enrichment gave up permanently,
// and the only place any of it appeared was the container log.

type refusing struct{ err error }

func (r refusing) Complete(context.Context, enrich.CompletionRequest) (enrich.Completion, error) {
	if r.err != nil {
		return enrich.Completion{}, r.err
	}
	return enrich.Completion{Content: "{}"}, nil
}

// TestModelOutageReportsDegradedRatherThanOK: /now.json said "llm": "ok" for
// fifty minutes because the status asked whether a DJ was WIRED. It has to ask
// whether the model ANSWERED.
func TestModelOutageReportsDegradedRatherThanOK(t *testing.T) {
	s := enrich.NewSwitchable(refusing{err: errors.New("rejected the API key")})

	if got := llmHealth(s, true); got != "ok" {
		t.Errorf("a model that has not been called yet reports %q, want ok", got)
	}
	for i := 0; i < DegradedAfter; i++ {
		_, _ = s.Complete(context.Background(), enrich.CompletionRequest{})
	}
	got := llmHealth(s, true)
	if !strings.HasPrefix(got, "degraded") {
		t.Errorf("llm health = %q after %d refusals, want it to say degraded", got, DegradedAfter)
	}
	if !strings.Contains(got, "rejected the API key") {
		t.Errorf("llm health = %q, want the provider's own reason in it", got)
	}
}

// TestModelOutageIsWiredIntoTheStatusEndpoint: /now.json is the thing that lied
// for fifty minutes, and testing llmHealth on its own does not prove /now.json
// calls it. Reverting the one wired line to the old dependencyHealth passed
// every other test in this file, which is exactly the shape of the original
// defect: the function was never wrong, the wiring was.
func TestModelOutageIsWiredIntoTheStatusEndpoint(t *testing.T) {
	llm := enrich.NewSwitchable(refusing{err: errors.New("rejected the API key")})
	a := &App{opts: Options{LLM: llm, Enricher: &enrich.Queue{}}}
	a.enriching.Store(true)

	if got := a.Status().Health.LLM; got != "ok" {
		t.Fatalf("llm health = %q before any call, want ok", got)
	}
	for i := 0; i < DegradedAfter; i++ {
		_, _ = llm.Complete(context.Background(), enrich.CompletionRequest{})
	}
	got := a.Status().Health.LLM
	if !strings.Contains(got, "rejected the API key") {
		t.Errorf("/now.json reports llm = %q while every call is refused; "+
			"it has to carry what the model last DID", got)
	}
}

// TestModelOutageGoesGreenWhenTheKeyIsFixed: the operator saves a working key
// and the light has to come back on its own, or the fix looks like it did
// nothing and they change something else.
func TestModelOutageGoesGreenWhenTheKeyIsFixed(t *testing.T) {
	bad := refusing{err: errors.New("rejected the API key")}
	s := enrich.NewSwitchable(bad)
	for i := 0; i < DegradedAfter; i++ {
		_, _ = s.Complete(context.Background(), enrich.CompletionRequest{})
	}
	s.Use(refusing{})
	if _, err := s.Complete(context.Background(), enrich.CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
	if got := llmHealth(s, true); got != "ok" {
		t.Errorf("llm health = %q after a good call, want ok", got)
	}
}

// TestModelOutageStillSaysNotConfiguredWithNoModel: a library with no model is
// a working shuffle. It must not read as an outage.
func TestModelOutageStillSaysNotConfiguredWithNoModel(t *testing.T) {
	if got := llmHealth(enrich.NewSwitchable(nil), false); got != "not configured" {
		t.Errorf("llm health = %q with no model at all, want not configured", got)
	}
	// And a switchable that exists but holds nothing is the same state.
	if got := llmHealth(enrich.NewSwitchable(nil), true); got != "not configured" {
		t.Errorf("llm health = %q with an empty switchable, want not configured", got)
	}
}

// TestModelOutageWakesTheWorkerFromTheSaveItself: testing wakeEnrichment on its
// own does not prove SetLLMConfig calls it, and deleting that one line passed
// every other test here. Third time in this task that the WIRING was the whole
// defect while the function was fine, which is also what the outage itself was.
func TestModelOutageWakesTheWorkerFromTheSaveItself(t *testing.T) {
	a, _ := llmApp(t)
	a.enrichWake = make(chan struct{}, 1)

	err := a.SetLLMConfig(context.Background(), enrich.LLMConfig{
		Provider: enrich.ProviderLlamaCPP, URL: "http://127.0.0.1:9", Model: "m"})
	if err != nil {
		t.Fatalf("SetLLMConfig: %v", err)
	}
	select {
	case <-a.enrichWake:
	default:
		t.Error("saving a model did not wake enrichment; an operator who fixes a rejected " +
			"key gets breaks back and leaves the worker dead under a green light")
	}
}

// TestModelOutageBringsEnrichmentBack: queue.Run returns for good after ten
// refusals in a row and the goroutine ended, so fixing the key in the console
// restored breaks and left enrichment dead at 3,923 of 7,595 with a green light
// over it. Saving a configuration has to wake it.
func TestModelOutageBringsEnrichmentBack(t *testing.T) {
	a := &App{}
	a.enrichWake = make(chan struct{}, 1)

	a.wakeEnrichment()
	select {
	case <-a.enrichWake:
	default:
		t.Fatal("saving a model did not wake the enrichment worker")
	}

	// TWICE IN A ROW must not block: the channel is the signal, not a queue,
	// and SetLLMConfig runs on an HTTP handler that must never park.
	a.wakeEnrichment()
	a.wakeEnrichment()

	// And with no worker at all it is still safe to call.
	(&App{}).wakeEnrichment()
}

// TestModelOutageDoesNotCallADeadWorkerRunning: measured on the box at 03:33,
// /now.json reported enrichment as "done": 3923, "running": true -- with a
// goroutine that had exited at 03:07. The flag was the operator's PAUSE
// TOGGLE, not whether anything was enriching, so the console showed a green
// light, the word running, and a number that had not moved in an hour.
func TestModelOutageDoesNotCallADeadWorkerRunning(t *testing.T) {
	a := &App{}
	a.enriching.Store(true)

	if a.enrichmentRunning() {
		t.Error("enrichment reports running before the worker has ever started")
	}

	a.enrichAlive.Store(true)
	if !a.enrichmentRunning() {
		t.Error("a live worker the operator has not paused reports as stopped")
	}

	// PAUSED is a third state and must not read as the worker having died.
	// An operator who paused it for the evening knows where it went.
	a.enriching.Store(false)
	a.enrichAlive.Store(false)
	if a.enrichmentRunning() {
		t.Error("a paused worker reports as running")
	}
	if why := a.enrichmentTrouble(); why != "" {
		t.Errorf("a worker paused on purpose is reported as trouble: %q", why)
	}

	// And a worker that gave up is not running however the toggle is set.
	a.enriching.Store(true)
	a.enrichAlive.Store(false)
	if a.enrichmentRunning() {
		t.Error("a worker that gave up still reports as running")
	}
	// NOT YET TROUBLE: not-alive also covers a worker that has not started and
	// one that is on its way back from a restart. The stored reason is what
	// separates the three.
	if why := a.enrichmentTrouble(); why != "" {
		t.Errorf("a worker with no recorded failure is reported as trouble: %q", why)
	}
	a.enrichGaveUp.Store("rejected the API key")
	if why := a.enrichmentTrouble(); !strings.Contains(why, "rejected the API key") {
		t.Errorf("enrichment trouble = %q, want the reason it gave up", why)
	}
}

// TestModelOutageWakeChannelExistsBeforeTheServerDoes: the wake must survive a
// request that lands during startup.
//
// Run starts the HTTP server and only forty lines later reached the enricher
// branch that used to create this channel. For the whole of that window a POST
// to /admin/enriching or /admin/llm read the field on a handler goroutine while
// Run wrote it -- a data race outright, and one that also LOST the wake: the
// read saw nil, returned, and the operator's restart did nothing. The box that
// gets a request the moment it comes up is the deployed one.
func TestModelOutageWakeChannelExistsBeforeTheServerDoes(t *testing.T) {
	a, err := New(testConfig(t), Options{
		Tracks: []string{toneFile(t, t.TempDir(), "a.mp3", 1, 220)},
		Log:    slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// BEFORE Run. Nothing has started, and a wake asked for now must still be
	// waiting when the worker eventually looks.
	if a.enrichWake == nil {
		t.Fatal("the wake channel is nil until Run gets to it, so a restart asked for " +
			"during startup is silently dropped")
	}
	a.wakeEnrichment()
	select {
	case <-a.enrichWake:
	default:
		t.Error("the wake was lost")
	}
}
