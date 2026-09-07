// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

// A FLAT CAP TRUNCATED THE LONGEST WINDOWS AND THOSE BREAKS WERE DROPPED.
//
// Measured live on 2026-09-07: a 48-second gap asks for 120 words, which is
// around 160 tokens of English, and the answer is JSON -- the keys, the quoting
// and the asserted_facts array add 20 to 30 more. Against a cap of 200 that is
// marginal rather than unlucky, and roughly one trial in nine came back
// "truncated at 200 tokens", which the validator turns into a dropped break and
// a listener hears as silence.

func TestBreakBudgetGrowsWithTheWindow(t *testing.T) {
	if got := TokensFor(WordTarget(48)); got <= BreakTokenBudget {
		t.Errorf("a 48-second window budgets %d tokens, want more than the flat %d",
			got, BreakTokenBudget)
	}
	// A SHORT WINDOW KEEPS THE FLOOR. Twenty words is 80 tokens by the
	// arithmetic, and a cap that tight would truncate the JSON envelope itself.
	if got := TokensFor(20); got != BreakTokenBudget {
		t.Errorf("a 20-word break budgets %d, want the floor of %d", got, BreakTokenBudget)
	}
	if got := TokensFor(0); got != BreakTokenBudget {
		t.Errorf("no stated length budgets %d, want the flat default", got)
	}
	// Still a CAP: a model that rambles is cut off well before it can eat the
	// lookahead budget.
	if got := TokensFor(120); got > 400 {
		t.Errorf("a 120-word break budgets %d tokens, which is no longer a cap", got)
	}
}

func TestBreakBudgetReachesTheModel(t *testing.T) {
	c := &stubCompleter{out: enrich.Completion{Content: `{"line":"hi"}`, StopType: "eos"}}
	if _, err := NewWriter(c, 0).WriteBreak(context.Background(), "p", nil, 280); err != nil {
		t.Fatal(err)
	}
	if c.got.NPredict != 280 {
		t.Errorf("asked the model for %d tokens, want the caller's 280", c.got.NPredict)
	}

	// A WRITER BUILT WITH ITS OWN BUDGET KEEPS IT. That is how the advert
	// writer asks for a length of its own, and a window has no say over it.
	if _, err := NewWriter(c, 64).WriteBreak(context.Background(), "p", nil, 280); err != nil {
		t.Fatal(err)
	}
	if c.got.NPredict != 64 {
		t.Errorf("asked for %d tokens, want the writer's own 64", c.got.NPredict)
	}
}

func TestBreakBudgetIsDerivedFromTheAskedLength(t *testing.T) {
	// The whole path: the validator is told how many words the prompt asked
	// for, and that is what the model is capped at.
	c := &stubCompleter{out: enrich.Completion{
		Content: `{"opening":"Good evening to you and to nobody else at all",` +
			`"body":"That was a record I have opinions about and no facts to hang them on",` +
			`"handoff":"Here comes another one","asserted_facts":[]}`,
		StopType: "eos",
	}}
	v := &Validator{Writer: NewWriter(c, 0), Said: saidStore(t)}
	if _, err := v.Generate(context.Background(), "prompt", 120, nil, nil, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if c.got.NPredict != TokensFor(120) {
		t.Errorf("the model was capped at %d, want %d for a 120-word break",
			c.got.NPredict, TokensFor(120))
	}
}
