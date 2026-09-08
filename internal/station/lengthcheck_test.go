// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/mix"
)

// All named TestLength*, matching the task's own `-run TestLength`. None of the
// five names it proposed did. Eighth instance in this plan.

// scriptedRenderer returns a fixed duration per attempt and records the word
// targets it was asked for.
type scriptedRenderer struct {
	seconds  []float64
	targets  []int
	texts    []string
	writeErr error
}

func (s *scriptedRenderer) Write(_ context.Context, wordTarget int, _ mix.Placement) (string, error) {
	s.targets = append(s.targets, wordTarget)
	if s.writeErr != nil {
		return "", s.writeErr
	}
	return "script for a target of some words", nil
}

func (s *scriptedRenderer) Render(_ context.Context, text, _, outPath string) (float64, error) {
	s.texts = append(s.texts, text)
	n := len(s.texts) - 1
	if n >= len(s.seconds) {
		n = len(s.seconds) - 1
	}
	if err := os.WriteFile(outPath, []byte("wav"), 0o600); err != nil {
		return 0, err
	}
	return s.seconds[n], nil
}

func newCheck(t *testing.T, seconds ...float64) (LengthCheck, *scriptedRenderer) {
	t.Helper()
	s := &scriptedRenderer{seconds: seconds}
	return LengthCheck{Writer: s, Renderer: s, Dir: t.TempDir()}, s
}

func TestLengthFitsIsAired(t *testing.T) {
	lc, s := newCheck(t, 7.0)
	got, err := lc.Enforce(context.Background(), mix.PlacementRamp, 9.0)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.Dropped {
		t.Fatal("dropped a break that fits")
	}
	if got.Placement != mix.PlacementRamp {
		t.Errorf("placement = %v, want ramp", got.Placement)
	}
	if got.Seconds != 7.0 {
		t.Errorf("Seconds = %v, want 7.0 -- the WAV must never be truncated to fit", got.Seconds)
	}
	if len(s.targets) != 1 {
		t.Errorf("wrote %d times, want 1: a fitting break must not be regenerated", len(s.targets))
	}
	if _, err := os.Stat(got.Path); err != nil {
		t.Errorf("aired break is not on disk: %v", err)
	}
}

// TestLengthOverrunMovesRatherThanRewrites. It used to rewrite at three
// quarters of the word target and render again -- a second LLM call and a
// second TTS render inside a lookahead window sized for one. ONE GENERATION per
// break now: a break that overran its intro airs in the gap instead, because
// moving audio that already exists costs nothing.
func TestLengthOverrunMovesRatherThanRewrites(t *testing.T) {
	lc, s := newCheck(t, 12.0, 8.0)
	got, err := lc.Enforce(context.Background(), mix.PlacementRamp, 9.0)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.Dropped {
		t.Fatal("dropped a 12s break that fits the between gap")
	}
	if got.Placement != mix.PlacementBetween {
		t.Errorf("placement = %v, want between", got.Placement)
	}
	if got.Seconds != 12.0 {
		t.Errorf("Seconds = %v, want the first render reused", got.Seconds)
	}
	if len(s.targets) != 1 {
		t.Fatalf("wrote %d times, want exactly 1", len(s.targets))
	}
}

// TestLengthSecondOverrunFallsBackToBetween: the between gap is not bounded by
// a vocal, so it holds a longer break than any ramp. Re-placing costs nothing;
// a third render would blow the lookahead budget.
func TestLengthSecondOverrunFallsBackToBetween(t *testing.T) {
	lc, s := newCheck(t, 20.0, 20.0)
	got, err := lc.Enforce(context.Background(), mix.PlacementRamp, 9.0)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.Dropped {
		t.Fatal("dropped a 20s break that fits the between window")
	}
	if got.Placement != mix.PlacementBetween {
		t.Errorf("placement = %v, want between", got.Placement)
	}
	if got.Seconds != 20.0 {
		t.Errorf("Seconds = %v, want the one render reused rather than a second", got.Seconds)
	}
	if len(s.targets) != 1 {
		t.Errorf("wrote %d times, want 1: falling back re-places the audio, it does not rewrite it", len(s.targets))
	}
}

func TestLengthBetweenOverrunDropsBreak(t *testing.T) {
	lc, s := newCheck(t, 90.0, 80.0)
	got, err := lc.Enforce(context.Background(), mix.PlacementRamp, 9.0)
	if err != nil {
		t.Fatalf("a dropped break is not an error: %v", err)
	}
	if !got.Dropped {
		t.Fatalf("aired an %.0fs break; the between window is %.0fs", got.Seconds, BetweenWindowSeconds)
	}
	if got.Path != "" {
		if _, err := os.Stat(got.Path); err == nil {
			t.Error("dropped break left its WAV on disk; the scheduler would find it")
		}
	}
	if len(s.targets) != 1 {
		t.Errorf("wrote %d times, want 1: a break too long for any window costs one call, not two", len(s.targets))
	}
}

// TestLengthAlreadyBetweenDoesNotFallBackTwice: when the break was already
// going in the gap, there is no wider window to escape to.
func TestLengthAlreadyBetweenDoesNotFallBackTwice(t *testing.T) {
	lc, _ := newCheck(t, 90.0, 80.0)
	got, err := lc.Enforce(context.Background(), mix.PlacementBetween, BetweenWindowSeconds)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if !got.Dropped {
		t.Fatal("aired a break longer than the only window it had")
	}
}

// TestLengthDiscardsOverlongRenders: every attempt writes a file, and the ones
// that lose must not accumulate in the break directory.
func TestLengthDiscardsOverlongRenders(t *testing.T) {
	lc, _ := newCheck(t, 12.0, 8.0)
	got, err := lc.Enforce(context.Background(), mix.PlacementRamp, 9.0)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	entries, err := os.ReadDir(lc.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("break directory holds %d files %v, want only the aired one", len(entries), names)
	}
	if filepath.Dir(got.Path) != lc.Dir {
		t.Errorf("aired break %s is not in the break directory %s", got.Path, lc.Dir)
	}
}

// TestLengthWriterFailureDropsQuietly: breaks are optional, music is not.
func TestLengthWriterFailureDropsQuietly(t *testing.T) {
	s := &scriptedRenderer{seconds: []float64{7.0}, writeErr: errors.New("llm said no")}
	lc := LengthCheck{Writer: s, Renderer: s, Dir: t.TempDir()}
	got, err := lc.Enforce(context.Background(), mix.PlacementRamp, 9.0)
	if err != nil {
		t.Fatalf("a failed writer is a dropped break, not an error: %v", err)
	}
	if !got.Dropped {
		t.Fatal("aired a break the writer never produced")
	}
}

// TestLengthDropAlwaysSaysWhy: a drop with no reason is indistinguishable from
// any other drop, so a systematic failure looks exactly like normal operation.
// Measured live, one wrong voice id dropped 20 breaks out of 20 while every
// counter in the system reported healthy.
func TestLengthDropAlwaysSaysWhy(t *testing.T) {
	// Writer failure.
	s := &scriptedRenderer{seconds: []float64{7.0}, writeErr: errors.New("llm said no")}
	got, err := LengthCheck{Writer: s, Renderer: s, Dir: t.TempDir()}.
		Enforce(context.Background(), mix.PlacementRamp, 9.0)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dropped || got.Reason == "" {
		t.Errorf("writer failure dropped with reason %q, want a reason naming the cause", got.Reason)
	}

	// Too long for every window.
	lc, _ := newCheck(t, 90.0, 80.0)
	got, err = lc.Enforce(context.Background(), mix.PlacementRamp, 9.0)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Dropped || got.Reason == "" {
		t.Errorf("overlong break dropped with reason %q, want a reason", got.Reason)
	}
}

// TestLengthCheckRefusesWithoutTheParts: both halves of a pipeline built wrong.
// SAID, NOT DEREFERENCED -- this runs on the break goroutine, where a panic is
// caught by a recover that stops generation for the life of the process.
func TestLengthCheckRefusesWithoutTheParts(t *testing.T) {
	ctx := context.Background()

	got, err := LengthCheck{}.Enforce(ctx, mix.PlacementBetween, 20)
	if err != nil {
		t.Fatalf("Enforce with no writer: %v", err)
	}
	if !got.Dropped || !strings.Contains(got.Reason, "no writer") {
		t.Errorf("= %+v, want a drop naming the missing writer", got)
	}

	// A writer but nothing to render with: the write still happens, because the
	// break's track context is applied as it does.
	wrote := false
	got, err = LengthCheck{Writer: writerFunc(func(context.Context, int, mix.Placement) (string, error) {
		wrote = true
		return "a line", nil
	})}.Enforce(ctx, mix.PlacementBetween, 20)
	if err != nil {
		t.Fatalf("Enforce with no renderer: %v", err)
	}
	if !wrote {
		t.Error("the writer was skipped, so a break's context would never be applied")
	}
	if !got.Dropped || !strings.Contains(got.Reason, "no renderer") {
		t.Errorf("= %+v, want a drop naming the missing renderer", got)
	}
}

// writerFunc adapts a function to the Writer a LengthCheck needs.
type writerFunc func(ctx context.Context, target int, placement mix.Placement) (string, error)

func (f writerFunc) Write(ctx context.Context, target int, placement mix.Placement) (string, error) {
	return f(ctx, target, placement)
}
