// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/enrich"
)

// TWO GENERATIONS PER BREAK, AND THE SECOND IS FOR CONTENT.
//
// This file was written when the budget was one. It went to one because the
// second generation was being spent rewriting breaks that came out too long,
// and a break that does not fit is now re-placed into the wider gap instead --
// that reasoning was about LENGTH and still holds.
//
// It came back to two on 2026-09-10, when four real generations against the
// deployment model produced one usable break and three that the content checks
// reject: a loop, an instruction echo and a recitation. With a budget of one,
// every catch was also a lost break. The tests here still say the writer is not
// asked over and over -- they say it twice instead of once.
//
// A REFUSAL IS NOT A GENERATION. A rate limit produces no output at all -- the
// model never wrote anything -- so retrying one is not a second attempt at
// writing, it is the first attempt finally happening. The two are counted
// separately, or the DJ goes silent for a minute every time a burst lands on it.
//
// Every test here is TestOneShot*, which is the -run pattern for this change.

func TestOneShotAsksTheModelTwiceAtMost(t *testing.T) {
	// A break that fails every check costs exactly MaxAttempts calls and is
	// then dropped, and music is what plays. The bound is what matters: a
	// writer that could be asked until it succeeded would spend a break's whole
	// lookahead on a model having a bad minute.
	w := &scriptedWriter{replies: []string{"not json", "not json either", "still not json"}}
	v := &Validator{Writer: w, Said: saidStore(t)}

	if _, err := v.Generate(context.Background(), "prompt", 0, nil, nil, nil); err == nil {
		t.Fatal("a malformed answer was accepted")
	}
	if got := v.Stats().Attempts; got != MaxAttempts {
		t.Errorf("%d attempts, want %d", got, MaxAttempts)
	}
	if w.calls != MaxAttempts {
		t.Errorf("%d writer calls, want %d", w.calls, MaxAttempts)
	}
}

func TestOneShotTakesTheFirstUsableAnswer(t *testing.T) {
	w := &scriptedWriter{replies: []string{
		`{"opening":"","body":"That was Bayside and this is Everlong.","handoff":"","asserted_facts":[]}`,
	}}
	v := &Validator{Writer: w, Said: saidStore(t)}

	b, err := v.Generate(context.Background(), "prompt", 0, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if b.Text() == "" {
		t.Fatal("no break")
	}
	if got := v.Stats().Attempts; got != 1 {
		t.Errorf("%d attempts, want 1", got)
	}
}

func TestOneShotStillWaitsOutARefusal(t *testing.T) {
	// The model said nothing at all, so nothing has been generated yet. This is
	// the same one generation, delayed -- not a second one.
	w := &refusingThenFine{clk: time.Now}
	v := &Validator{Writer: w, Said: saidStore(t), Backoff: 30 * time.Millisecond}

	b, err := v.Generate(context.Background(), "prompt", 0, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if b.Text() == "" {
		t.Fatal("no break")
	}
	if w.calls != 2 {
		t.Errorf("%d calls, want 2: one refused, one answered", w.calls)
	}
	// And the refusal did not eat the generation budget.
	if got := v.Stats().Attempts; got != 1 {
		t.Errorf("%d generations counted, want 1: a refusal produced nothing", got)
	}
}

func TestOneShotGivesUpOnAModelThatOnlyRefuses(t *testing.T) {
	w := &alwaysRefusing{}
	v := &Validator{Writer: w, Said: saidStore(t), Backoff: time.Millisecond}

	if _, err := v.Generate(context.Background(), "prompt", 0, nil, nil, nil); err == nil {
		t.Fatal("a model that never answered produced a break")
	}
	if w.calls > MaxRefusals {
		t.Errorf("%d calls against a refusing model, want at most %d", w.calls, MaxRefusals)
	}
}

type alwaysRefusing struct{ calls int }

func (a *alwaysRefusing) WriteBreak(ctx context.Context, prompt string, schema map[string]any, _ int) (string, error) {
	a.calls++
	return "", enrich.ErrLLMUnavailable
}
