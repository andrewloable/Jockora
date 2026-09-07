// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/enrich"
)

// RETRYING A RATE LIMIT INSTANTLY IS A GUARANTEED SECOND FAILURE.
//
// The writer gets two attempts, and it used to spend them back to back. That is
// right for a bad answer and wrong for a refusal: a hosted endpoint allows 20
// requests a minute, the enrichment queue runs continuously at about eight, and
// the DJ's one request loses a burst. Both attempts then fail inside the same
// minute and the break is dropped.
//
// Heard as silence at a cadence of one break per track. The break had 148
// seconds of lookahead and spent six of them.
//
// Every test here is TestRateLimit*, which is the -run pattern for this fix.

// refusingThenFine fails with ErrLLMUnavailable once, then answers.
type refusingThenFine struct {
	calls int
	gaps  []time.Duration
	last  time.Time
	clk   func() time.Time
}

func (r *refusingThenFine) WriteBreak(ctx context.Context, prompt string, schema map[string]any, _ int) (string, error) {
	now := r.clk()
	if !r.last.IsZero() {
		r.gaps = append(r.gaps, now.Sub(r.last))
	}
	r.last = now
	r.calls++
	if r.calls == 1 {
		return "", enrich.ErrLLMUnavailable
	}
	return `{"opening":"","body":"That was Bayside and this is Everlong.","handoff":"","asserted_facts":[]}`, nil
}

func TestRateLimitWaitsBeforeTryingAgain(t *testing.T) {
	w := &refusingThenFine{clk: time.Now}
	v := &Validator{Writer: w, Said: saidStore(t), Backoff: 40 * time.Millisecond}

	start := time.Now()
	b, err := v.Generate(context.Background(), "prompt", 0, nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if b.Text() == "" {
		t.Fatal("no break")
	}
	if w.calls != 2 {
		t.Errorf("%d attempts, want 2", w.calls)
	}
	// THE WAIT IS THE FIX. Without it both attempts land inside the same
	// rate-limit window and the second is refused for the same reason as the
	// first.
	if len(w.gaps) != 1 || w.gaps[0] < 40*time.Millisecond {
		t.Errorf("gaps = %v, want one of at least the backoff", w.gaps)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Error("Generate returned without waiting at all")
	}
}

func TestRateLimitDoesNotWaitForAnOrdinaryBadAnswer(t *testing.T) {
	// A model that ANSWERS badly will answer differently at once. Waiting there
	// would spend lookahead for nothing.
	w := &scriptedWriter{replies: []string{"not json", "not json either"}}
	v := &Validator{Writer: w, Said: saidStore(t), Backoff: 2 * time.Second}

	start := time.Now()
	if _, err := v.Generate(context.Background(), "prompt", 0, nil, nil, nil); err == nil {
		t.Fatal("accepted two malformed answers")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v: it waited for a failure that will not clear by waiting", elapsed)
	}
}

func TestRateLimitGivesUpWhenTheWaitOutlastsTheContext(t *testing.T) {
	// The lookahead budget is finite. A break that is going to be late is a
	// break that should be dropped, not one that holds the pipeline open.
	w := &refusingThenFine{clk: time.Now}
	v := &Validator{Writer: w, Said: saidStore(t), Backoff: time.Minute}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := v.Generate(ctx, "prompt", 0, nil, nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
	if w.calls != 1 {
		t.Errorf("%d attempts, want 1: the second never became affordable", w.calls)
	}
}
