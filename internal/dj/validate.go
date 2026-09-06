// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/andrewloable/jockora/internal/enrich"
)

// ErrBreakRepetitive means the writer could not produce a fresh break.
//
// It is not an error to surface to the listener. A dropped break is a designed
// outcome: the music continues, the mixer extends the crossfade, and nothing
// about it is audible as a fault.
var ErrBreakRepetitive = errors.New("dj: break repeated something already said")

// ErrBreakDropped means a break could not be produced, for a reason the wrapped
// error names.
//
// It exists because Generate used to report EVERY failure as ErrBreakRepetitive,
// including malformed JSON and model errors. A caller asking errors.Is(err,
// ErrBreakRepetitive) would then treat a broken model as a chatty one, and the
// log line said "break repeated something already said: not valid json" -- an
// answer that points at the wrong half of the system.
var ErrBreakDropped = errors.New("dj: break dropped")

// MaxAttempts is the ceiling on generation attempts per break.
//
// Two, not three. A third call pushes past the lookahead budget and the break
// arrives late anyway, which costs more than the break was worth.
const MaxAttempts = 2

// DropReason records why a break did not air.
type DropReason string

const (
	DropRepetitive DropReason = "repetitive"
	// DropInstructionEcho means the model spoke the prompt back instead of
	// writing a break.
	DropInstructionEcho DropReason = "instruction_echo"
	DropUngrounded      DropReason = "ungrounded"
	DropBadJSON         DropReason = "bad_json"
	DropLLMError        DropReason = "llm_error"
)

// Writer produces one break from an assembled prompt and schema.
type Writer interface {
	WriteBreak(ctx context.Context, prompt string, schema map[string]any) (string, error)
}

// Validator enforces non-repetition and groundedness AFTER generation.
//
// The prompt asks; this enforces. Negative instructions are weakly followed by
// every model, so the prohibition list in the prompt reduces the rate and the
// validator sets the floor.
type Validator struct {
	Writer Writer
	Said   *SaidLines

	mu       sync.Mutex
	attempts int
	breaks   int
	drops    int
	reasons  map[DropReason]int
}

// Stats is a snapshot of validator counters.
type Stats struct {
	Breaks   int
	Drops    int
	Attempts int
	Reasons  map[DropReason]int
}

// DropRate is the fraction of requested breaks that did not air.
//
// It feeds the status endpoint and GATE 5. A rising drop rate means the DJ is
// running out of things to say, which is the failure mode that arrives slowly.
func (v *Validator) DropRate() float64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	total := v.breaks + v.drops
	if total == 0 {
		return 0
	}
	return float64(v.drops) / float64(total)
}

// Stats returns the counters.
func (v *Validator) Stats() Stats {
	v.mu.Lock()
	defer v.mu.Unlock()
	reasons := make(map[DropReason]int, len(v.reasons))
	for k, n := range v.reasons {
		reasons[k] = n
	}
	return Stats{Breaks: v.breaks, Drops: v.drops, Attempts: v.attempts, Reasons: reasons}
}

func (v *Validator) record(reason DropReason, aired bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.reasons == nil {
		v.reasons = map[DropReason]int{}
	}
	if aired {
		v.breaks++
		return
	}
	v.drops++
	v.reasons[reason]++
}

func (v *Validator) countAttempt() {
	v.mu.Lock()
	v.attempts++
	v.mu.Unlock()
}

// Generate writes one break, regenerating at most once, and records it only if
// it passes.
//
// A rejected break is NEVER written to said_lines: recording rejects would
// poison the index against future good breaks that happen to reuse a phrase the
// DJ never actually aired.
func (v *Validator) Generate(ctx context.Context, prompt string, prev, cur, next *enrich.Dossier) (*Break, error) {
	schema := BreakSchema(prev, cur, next)
	lastReason := DropRepetitive
	var lastErr error

	for attempt := 0; attempt < MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		v.countAttempt()

		raw, err := v.Writer.WriteBreak(ctx, prompt, schema)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastReason, lastErr = DropLLMError, err
			continue
		}

		b, err := ParseBreak(raw)
		if err != nil {
			lastReason, lastErr = DropBadJSON, err
			continue
		}

		// Should be unreachable: the schema enum is built from resolvable ids
		// only, so an ungrounded id cannot be emitted. If this ever fires the
		// enum construction is buggy, not the model.
		if err := ResolveFacts(b, prev, cur, next); err != nil {
			lastReason, lastErr = DropUngrounded, err
			continue
		}

		// The model sometimes recites the instructions instead of following
		// them. The said-lines index catches the SECOND occurrence as
		// repetition, which means the first one airs -- a listener hearing
		// "state only facts listed above" in a DJ voice. Measured live, this
		// was the single largest source of drops.
		if phrase, echoed := echoesInstructions(b.Text()); echoed {
			lastReason = DropInstructionEcho
			lastErr = fmt.Errorf("%w: the break recites the prompt: %q", ErrBreakDropped, phrase)
			continue
		}

		hit, gram, err := v.Said.CheckCollision(ctx, b.Text())
		if err != nil {
			return nil, err
		}
		if hit {
			lastReason = DropRepetitive
			lastErr = fmt.Errorf("%w: %q", ErrBreakRepetitive, gram)
			continue
		}

		if err := v.Said.Record(ctx, b.Text()); err != nil {
			return nil, err
		}
		v.record("", true)
		return b, nil
	}

	v.record(lastReason, false)
	if lastReason == DropRepetitive {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, ErrBreakRepetitive
	}
	return nil, fmt.Errorf("%w (%s): %v", ErrBreakDropped, lastReason, lastErr)
}

// instructionPhrases are fragments of the writing prompt that must never be
// spoken on air. Kept deliberately short and specific: a phrase here is one no
// DJ would say and every prompt does.
var instructionPhrases = []string{
	"only facts listed",
	"facts listed above",
	"state only",
	"do not invent",
	"asserted_facts",
	"the following facts",
	"return only the json",
	"json object",
	"word target",
	"speaking window",
	// Persona-card scaffolding. A small model hands these back as content when
	// the card is in the prompt at all.
	"you never do these things",
	"never mentions the weather",
	"backsells like a person",
	"like someone talking to one person",
	"announcer's script",
	"brand name",
	"the name of the business",
	// Model scaffolding that is not English at all. Observed live: a break came
	// back as "}<tool_call|>```json }<tool_call|>```json" and aired, because
	// nothing checked that a break was made of words.
	"<tool_call",
	"</tool_call",
	"```",
	"<|im_start|",
	"<|im_end|",
	"<start_of_turn>",
	"<end_of_turn>",
}

// echoesInstructions reports whether a break is reciting its own prompt.
func echoesInstructions(text string) (string, bool) {
	lower := strings.ToLower(text)
	for _, phrase := range instructionPhrases {
		if strings.Contains(lower, phrase) {
			return phrase, true
		}
	}
	return "", false
}
