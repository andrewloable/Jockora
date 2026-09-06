// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

type stubCompleter struct {
	got enrich.CompletionRequest
	out enrich.Completion
	err error
}

func (s *stubCompleter) Complete(_ context.Context, req enrich.CompletionRequest) (enrich.Completion, error) {
	s.got = req
	return s.out, s.err
}

func TestWriterPassesSchemaToTheSampler(t *testing.T) {
	c := &stubCompleter{out: enrich.Completion{Content: `{"line":"hi"}`, StopType: "eos"}}
	schema := map[string]any{"type": "object"}

	got, err := NewWriter(c, 0).WriteBreak(context.Background(), "prompt", schema)
	if err != nil {
		t.Fatalf("WriteBreak: %v", err)
	}
	if got != `{"line":"hi"}` {
		t.Errorf("content = %q", got)
	}
	if c.got.JSONSchema == nil {
		t.Error("schema was not passed: without it the grammar does not constrain the sampler and an ungrounded fact id becomes representable")
	}
	if c.got.NPredict != BreakTokenBudget {
		t.Errorf("NPredict = %d, want the %d default", c.got.NPredict, BreakTokenBudget)
	}
}

// TestWriterRejectsTruncatedOutput: a response cut at the token limit is JSON
// up to the cut and then is not JSON at all. Reporting it as an error gives the
// validator a clean drop reason instead of a parse failure it has to diagnose.
func TestWriterRejectsTruncatedOutput(t *testing.T) {
	c := &stubCompleter{out: enrich.Completion{Content: `{"line":"hi`, StopType: "limit", TokensPredicted: 200}}
	_, err := NewWriter(c, 0).WriteBreak(context.Background(), "prompt", nil)
	if err == nil {
		t.Fatal("accepted a truncated break")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("error = %v, want it to name truncation", err)
	}
}

func TestWriterRejectsEmptyContent(t *testing.T) {
	c := &stubCompleter{out: enrich.Completion{Content: "", StopType: "eos"}}
	if _, err := NewWriter(c, 0).WriteBreak(context.Background(), "p", nil); err == nil {
		t.Fatal("accepted an empty break")
	}
}

func TestWriterPropagatesModelErrors(t *testing.T) {
	c := &stubCompleter{err: errors.New("connection refused")}
	if _, err := NewWriter(c, 0).WriteBreak(context.Background(), "p", nil); err == nil {
		t.Fatal("swallowed a model error")
	}
}

// TestWriterUsesAWritingTemperature: the model is asked to do two opposite
// things and the temperature was hardcoded for one of them. Running the writer
// at the fact-cataloguing temperature collapsed a ten-advert rotation to one
// brand across forty attempts.
func TestWriterUsesAWritingTemperature(t *testing.T) {
	c := &stubCompleter{out: enrich.Completion{Content: `{"line":"hi"}`, StopType: "eos"}}
	if _, err := NewWriter(c, 0).WriteBreak(context.Background(), "p", nil); err != nil {
		t.Fatal(err)
	}
	if c.got.Temperature != enrich.WritingTemperature {
		t.Errorf("break temperature = %v, want %v (extraction runs at %v)",
			c.got.Temperature, enrich.WritingTemperature, enrich.DefaultTemperature)
	}

	if _, err := NewWriterAt(c, 0, enrich.InventionTemperature).WriteBreak(context.Background(), "p", nil); err != nil {
		t.Fatal(err)
	}
	if c.got.Temperature != enrich.InventionTemperature {
		t.Errorf("advert temperature = %v, want %v", c.got.Temperature, enrich.InventionTemperature)
	}
}
