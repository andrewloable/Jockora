// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"fmt"

	"github.com/andrewloable/jockora/internal/enrich"
)

// BreakTokenBudget is the FLOOR under a break response, and the cap for a
// caller that does not say how long the break should be.
//
// It used to be the whole answer, and it was too small for the windows this
// system actually asks for. Measured live on 2026-09-07: a 48-second gap asks
// for 120 words, which is around 160 tokens of English, and the answer is JSON
// -- four keys, their quoting, the asserted_facts array and the braces add
// another 20 to 30. A break written to the target it was GIVEN therefore landed
// at 180 to 190 against a cap of 200, and roughly one in nine was truncated,
// which the validator turns into a dropped break and a listener hears as
// silence.
const BreakTokenBudget = 200

// TokensFor is the budget for a break of about this many words.
//
// Two tokens a word rather than the 1.3 English averages, because a jock shouts
// in capitals and writes "two thousand and seven", both of which tokenise
// badly; plus a fixed allowance for the JSON envelope around it. Still a CAP: a
// model that starts rambling is cut off well before it can eat the lookahead
// budget, which is what this exists for.
func TokensFor(words int) int {
	if words <= 0 {
		return BreakTokenBudget
	}
	if n := words*TokensPerWord + JSONEnvelopeTokens; n > BreakTokenBudget {
		return n
	}
	return BreakTokenBudget
}

// TokensPerWord and JSONEnvelopeTokens size the budget above.
const (
	TokensPerWord      = 2
	JSONEnvelopeTokens = 40
)

// llmWriter adapts a Completer to the Writer the Validator needs.
//
// It exists because nothing outside tests implemented Writer: Validator could
// parse, ground and de-duplicate a break, but there was no way to hand it a
// real language model. Both interfaces already existed and neither knew about
// the other.
type llmWriter struct {
	c           enrich.Completer
	nPredict    int
	temperature float64
}

// NewWriter returns a Writer backed by a language model, for writing breaks.
func NewWriter(c enrich.Completer, nPredict int) Writer {
	return NewWriterAt(c, nPredict, enrich.WritingTemperature)
}

// NewWriterAt is NewWriter with an explicit sampling temperature.
//
// It was added for the advert generator that invented ten brands, which needed
// more variety than a break does. That generator is gone: the operator names
// the brand now and app.WriteAd uses NewWriter, because an advert about a real
// product must not be written by a hotter sampler than a break is. The one
// caller left is cmd/jockora-breaks, where the temperature is a flag.
func NewWriterAt(c enrich.Completer, nPredict int, temperature float64) Writer {
	if nPredict <= 0 {
		nPredict = BreakTokenBudget
	}
	return &llmWriter{c: c, nPredict: nPredict, temperature: temperature}
}

// WriteBreak asks the model for one break, constrained by the schema.
//
// The schema is enforced at the SAMPLER, through llama.cpp's json_schema, so an
// ungrounded fact id is not merely rejected afterwards -- it is unrepresentable
// in the first place.
func (w *llmWriter) WriteBreak(ctx context.Context, prompt string, schema map[string]any,
	maxTokens int) (string, error) {

	// The CALLER'S budget wins, because only the caller knows how long a break
	// this window can hold. A writer built with an explicit nPredict keeps it,
	// which is how the ad writer asks for its own length.
	n := w.nPredict
	if maxTokens > 0 && w.nPredict == BreakTokenBudget {
		n = maxTokens
	}
	out, err := w.c.Complete(ctx, enrich.CompletionRequest{
		Prompt:      prompt,
		JSONSchema:  schema,
		NPredict:    n,
		Temperature: w.temperature,
	})
	if err != nil {
		return "", err
	}
	// A truncated response is JSON-shaped up to the cut and then is not JSON at
	// all. Saying so here gives the validator a clean DropBadJSON rather than a
	// parse error it has to guess the cause of.
	if out.StopType == "limit" {
		return "", fmt.Errorf("dj: break truncated at %d tokens", out.TokensPredicted)
	}
	if out.Content == "" {
		return "", fmt.Errorf("dj: model returned no content")
	}
	return out.Content, nil
}
