// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/mix"
)

// BetweenWordBudget is what a break may run to when it sits in the gap between
// two tracks. That window is not bounded by a vocal, so it is the widest one
// available and the natural place for an overlong break to escape to.
const BetweenWordBudget = 120

// BetweenWindowSeconds is that budget as time, at the writer's speaking rate.
const BetweenWindowSeconds = BetweenWordBudget / dj.WordsPerSecond

// ShorterRetryFactor is how much the word target shrinks on the one retry.
//
// Three quarters, not a half: the writer's first attempt is usually close, and
// halving it produces a break too terse to carry a persona.
const ShorterRetryFactor = 0.75

// Writer produces break text for a word target and a placement.
type Writer interface {
	Write(ctx context.Context, wordTarget int, placement mix.Placement) (string, error)
}

// Renderer synthesises text to a WAV and reports its real duration.
type Renderer interface {
	Render(ctx context.Context, text, voice, outPath string) (float64, error)
}

// RendererFunc adapts a function to Renderer, so tts.Render can be passed
// without either package knowing about the other.
type RendererFunc func(ctx context.Context, text, voice, outPath string) (float64, error)

// Render implements Renderer.
func (f RendererFunc) Render(ctx context.Context, text, voice, outPath string) (float64, error) {
	return f(ctx, text, voice, outPath)
}

// LengthCheck turns a written break into one that actually fits.
type LengthCheck struct {
	Writer   Writer
	Renderer Renderer
	// Dir is where rendered breaks are written.
	Dir string
	// Voice names the TTS voice; empty means the sidecar's default.
	//
	// VoiceOf, when set, wins: it is asked afresh for every render, so a jock
	// changed mid-session is spoken in the new jock's voice rather than the one
	// captured when this struct was built.
	Voice   string
	VoiceOf func() string
}

// Rendered is the outcome of the ladder.
type Rendered struct {
	Path string
	// Text is what was spoken, kept so the status endpoint can show it. The
	// transcript is free here -- it was written a moment ago -- and it is how
	// an operator checks what the jock actually said without reading logs.
	Text      string
	Placement mix.Placement
	Seconds   float64
	// Dropped means no break airs at this boundary. Not an error.
	Dropped bool
	// Reason says WHY it dropped, in a form a log line can carry.
	//
	// Without this a drop is indistinguishable from any other drop, and a
	// systematic failure looks exactly like normal operation. Measured live,
	// a single wrong voice id dropped 20 breaks out of 20 while every counter
	// in the system said "breaks are optional, this is fine".
	Reason string
	// Attempts is how many times the writer was called, for timing measurement.
	Attempts int
}

// Enforce renders a break and makes it fit, or drops it.
//
// The writer aims at a word count; TTS decides the real duration, and speech
// rate varies enough with content that the target alone cannot be trusted. So
// the length is MEASURED after synthesis and the result walks a ladder:
//
//	render -> fits?           air it
//	       -> rewrite shorter -> fits?  air it
//	                          -> fits the between gap?  air it there
//	                          -> drop
//
// Every rung leaves the stream unaffected. The last one is not an error:
// breaks are optional, music is not.
//
// The WAV is never truncated to fit. A break cut off mid-sentence is worse than
// no break at all, and it is the one outcome a listener would call broken.
func (lc LengthCheck) Enforce(ctx context.Context, placement mix.Placement, windowSeconds float64) (Rendered, error) {
	target := dj.WordTarget(windowSeconds)

	path, text, seconds, attempts, err := lc.attempt(ctx, target, placement, 0)
	if err != nil {
		return Rendered{Dropped: true, Attempts: attempts, Reason: err.Error()}, ctxErr(ctx, err)
	}
	if seconds <= windowSeconds {
		return Rendered{Path: path, Text: text, Placement: placement, Seconds: seconds, Attempts: attempts}, nil
	}

	// One retry, and only one. Two LLM calls plus two renders is already the
	// ceiling inside the lookahead window.
	shorter := int(float64(target)*ShorterRetryFactor + 0.5)
	retryPath, retryText, retrySeconds, retryAttempts, err := lc.attempt(ctx, shorter, placement, 1)
	remove(path) // the overlong first render must not stay in the break directory
	attempts += retryAttempts
	if err != nil {
		return Rendered{Dropped: true, Attempts: attempts, Reason: err.Error()}, ctxErr(ctx, err)
	}
	if retrySeconds <= windowSeconds {
		return Rendered{Path: retryPath, Text: retryText, Placement: placement, Seconds: retrySeconds, Attempts: attempts}, nil
	}

	// Still long. Re-PLACE rather than rewrite: the between gap is wider than
	// any ramp, and moving audio that already exists costs nothing, while a
	// third render would blow the budget the lookahead was sized for.
	if placement != mix.PlacementBetween && retrySeconds <= BetweenWindowSeconds {
		return Rendered{Path: retryPath, Text: retryText, Placement: mix.PlacementBetween, Seconds: retrySeconds, Attempts: attempts}, nil
	}

	remove(retryPath)
	return Rendered{
		Dropped:  true,
		Seconds:  retrySeconds,
		Attempts: attempts,
		Reason: fmt.Sprintf("%.1fs of speech does not fit the %.1fs %s window or the %.1fs between gap",
			retrySeconds, windowSeconds, placement, BetweenWindowSeconds),
	}, nil
}

// attempt writes and renders once.
func (lc LengthCheck) attempt(ctx context.Context, target int, placement mix.Placement, n int) (string, string, float64, int, error) {
	text, err := lc.Writer.Write(ctx, target, placement)
	if err != nil {
		return "", "", 0, 1, fmt.Errorf("writing break: %w", err)
	}
	path := filepath.Join(lc.Dir, fmt.Sprintf("break-%s-%d.wav", placement, n))
	seconds, err := lc.Renderer.Render(ctx, text, lc.voice(), path)
	if err != nil {
		return "", "", 0, 1, fmt.Errorf("rendering break: %w", err)
	}
	return path, text, seconds, 1, nil
}

// voice is the voice to speak this break in, asked for at render time.
func (lc LengthCheck) voice() string {
	if lc.VoiceOf != nil {
		if v := lc.VoiceOf(); v != "" {
			return v
		}
	}
	return lc.Voice
}

// ctxErr reports only cancellation upwards. Every other failure is a dropped
// break, which the caller must not treat as an error.
func ctxErr(ctx context.Context, _ error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func remove(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}
