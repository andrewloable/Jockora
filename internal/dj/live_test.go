// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/enrich"
)

// TestLiveBreak writes real breaks with the real prompt against a real model.
//
// The dossier prompt has had a live probe since the three defects that only it
// could find; THE BREAK PROMPT HAD NONE, and it is the half a listener actually
// hears. The failures it exists to catch are the ones already met here:
//
//   - A hosted model that does not honour response_format answers in prose, or
//     wraps the JSON in a fence, or appends "Fact ids used: f1, f2".
//   - Worse, it returns valid JSON with keys it invented -- "text", "script",
//     "spoken_break" -- because response_format communicates a SHAPE to some
//     backends and not the field NAMES. Nothing in a unit test can see this:
//     the schema is only enforced at the sampler on a grammar-constrained
//     backend, and against a hosted one it is a request.
//
// Skipped unless the endpoint is configured, so the normal suite stays
// hermetic. Run it after ANY change to prompt.go or the break schema, and
// after changing model or host:
//
//	set -a && source .env && set +a
//	JOCKORA_LIVE_LLM=https://openrouter.ai/api/v1 \
//	JOCKORA_LIVE_LLM_MODEL=minimax/minimax-m3:free \
//	go test ./internal/dj/ -run TestLiveBreak -v
func TestLiveBreak(t *testing.T) {
	base := os.Getenv("JOCKORA_LIVE_LLM")
	model := os.Getenv("JOCKORA_LIVE_LLM_MODEL")
	if base == "" || model == "" {
		t.Skip("set JOCKORA_LIVE_LLM and JOCKORA_LIVE_LLM_MODEL to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	client := enrich.NewOpenAICompatible(base, model, os.Getenv("JOCKORA_LLM_API_KEY"), nil)
	if err := client.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}

	persona, err := LoadPersona("../../personas/dutch_mahoney.toml")
	if err != nil {
		t.Skipf("no persona to write with: %v", err)
	}

	// The window the station will actually ask for, now that a short one falls
	// through to the gap rather than demanding an eight-word break.
	in := PromptInput{
		Persona:       persona,
		CurrentArtist: "Bayside",
		CurrentTitle:  "Have Fun Storming the Castle",
		NextArtist:    "Foo Fighters",
		NextTitle:     "Everlong",
		// THE SLEEVE, which is all an unenriched record has. It is here so the
		// probe exercises the branch that prints it: the failure this catches
		// is a model told "that is the sleeve, not research" and stating the
		// year flatly anyway, which no unit test can see.
		CurrentAlbum:   "The Walking Wounded",
		CurrentYear:    2007,
		Placement:      "between",
		WindowSeconds:  48,
		PreviousArtist: "Nirvana",
		PreviousTitle:  "In Bloom",
	}
	schema := BreakSchema(nil, nil, nil)
	in.Schema = schema

	prompt, err := BuildBreakPrompt(in)
	if err != nil {
		t.Fatalf("BuildBreakPrompt: %v", err)
	}

	writer := NewWriter(client, BreakTokenBudget)
	const trials = 3
	for i := 0; i < trials; i++ {
		// THE BUDGET PRODUCTION USES. Zero here meant the probe asked for a
		// 120-word break with a 200-token cap -- the very mismatch that was
		// truncating one break in nine -- so the probe was measuring a
		// configuration nothing runs.
		raw, err := writer.WriteBreak(ctx, prompt, schema, TokensFor(WordTarget(in.WindowSeconds)))
		if err != nil {
			// NOT Fatalf. One bad trial used to end the run, so the other two
			// were never sampled -- and a probe of a stochastic thing that
			// stops at the first miss reports one sample as if it were three.
			t.Errorf("trial %d: %v", i, err)
			continue
		}

		// THE KEYS, not merely valid JSON. An invented key parses, passes every
		// structural check, and yields a break with no text in it.
		b, err := ParseBreak(raw)
		if err != nil {
			t.Fatalf("trial %d: ParseBreak: %v\nraw: %s", i, err, raw)
		}
		text := b.Text()
		names := []string{
			in.CurrentTitle, in.CurrentArtist, in.NextTitle, in.NextArtist,
			in.PreviousTitle, in.PreviousArtist,
		}
		if strings.TrimSpace(text) == "" {
			t.Fatalf("trial %d: parsed but empty -- the model used its own keys\nraw: %s", i, raw)
		}
		if phrase, echoed := echoesInstructions(text, names); echoed {
			t.Errorf("trial %d: recites the prompt (%q): %s", i, phrase, text)
		}
		// WITH THE NAMES, exactly as the validator does in production. Passing
		// nil made the probe stricter than the thing it probes: a jock saying
		// a five-word track title twice is doing its job, and the exemption
		// for that already exists at line 289 of validate.go.
		if phrase, looped := repeatsItself(text, names); looped {
			t.Errorf("trial %d: repeats %q: %s", i, phrase, text)
		}
		if got := len(strings.Fields(text)); got > WordTarget(in.WindowSeconds)*2 {
			t.Errorf("trial %d: %d words against a %d-word budget: %s",
				i, got, WordTarget(in.WindowSeconds), text)
		}
		t.Logf("trial %d (%d words): %s", i, len(strings.Fields(text)), text)
	}
}
