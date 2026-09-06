// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"fmt"

	"github.com/andrewloable/jockora/internal/enrich"
)

// BreakTokenBudget caps a break response.
//
// A break is one or two sentences. This is generous enough that a good one is
// never truncated and tight enough that a model which starts rambling is cut
// off rather than eating the lookahead budget -- and the validator sees
// StopType "limit" and drops it rather than airing half a sentence.
const BreakTokenBudget = 200

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

// NewWriterAt is NewWriter with an explicit sampling temperature, for callers
// that need more variety than a break does -- adverts have to be ten different
// things, not one thing ten times.
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
func (w *llmWriter) WriteBreak(ctx context.Context, prompt string, schema map[string]any) (string, error) {
	out, err := w.c.Complete(ctx, enrich.CompletionRequest{
		Prompt:      prompt,
		JSONSchema:  schema,
		NPredict:    w.nPredict,
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
