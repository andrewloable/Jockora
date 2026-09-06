// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestLengthOverrunRegeneratesShorter(t *testing.T) {
	lc, s := newCheck(t, 12.0, 8.0)
	got, err := lc.Enforce(context.Background(), mix.PlacementRamp, 9.0)
	if err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if got.Dropped {
		t.Fatal("dropped a break that fits on the second attempt")
	}
	if got.Placement != mix.PlacementRamp {
		t.Errorf("placement = %v, want ramp", got.Placement)
	}
	if got.Seconds != 8.0 {
		t.Errorf("Seconds = %v, want the 8.0 second re-render", got.Seconds)
	}
	if len(s.targets) != 2 {
		t.Fatalf("wrote %d times, want exactly 2: two LLM calls plus two renders is the ceiling inside the lookahead", len(s.targets))
	}
}

func TestLengthRegenerationUsesReducedTarget(t *testing.T) {
	lc, s := newCheck(t, 12.0, 8.0)
	if _, err := lc.Enforce(context.Background(), mix.PlacementRamp, 9.0); err != nil {
		t.Fatalf("Enforce: %v", err)
	}
	if len(s.targets) != 2 {
		t.Fatalf("targets = %v, want two attempts", s.targets)
	}
	want := int(float64(s.targets[0])*ShorterRetryFactor + 0.5)
	if s.targets[1] != want {
		t.Errorf("second word target = %d, want %d (%.2fx of %d)", s.targets[1], want, ShorterRetryFactor, s.targets[0])
	}
	if s.targets[1] >= s.targets[0] {
		t.Errorf("second target %d is not shorter than the first %d", s.targets[1], s.targets[0])
	}
}

// TestLengthSecondOverrunFallsBackToBetween: the between gap is not bounded by
// a vocal, so it holds a longer break than any ramp. Re-placing costs nothing;
// a third render would blow the lookahead budget.
func TestLengthSecondOverrunFallsBackToBetween(t *testing.T) {
	lc, s := newCheck(t, 30.0, 20.0)
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
		t.Errorf("Seconds = %v, want the second render at 20.0 reused, not a third render", got.Seconds)
	}
	if len(s.targets) != 2 {
		t.Errorf("wrote %d times, want 2: falling back re-places the audio, it does not rewrite it", len(s.targets))
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
	if len(s.targets) != 2 {
		t.Errorf("wrote %d times, want 2", len(s.targets))
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
