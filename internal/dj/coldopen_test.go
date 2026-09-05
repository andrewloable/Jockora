// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"strings"
	"testing"
)

func TestColdOpenFirstBreakIsColdOpen(t *testing.T) {
	s := NewSession()

	if !s.IsColdOpen() {
		t.Error("the first break of a session is not a cold open")
	}

	got, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t), Current: testDossier(1),
		WindowSeconds: 9, IsColdOpen: s.IsColdOpen(),
	})
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(got)
	if !strings.Contains(low, "just tuned in") && !strings.Contains(low, "have arrived") {
		t.Errorf("the cold-open prompt does not acknowledge a tune-in:\n%s", got)
	}
	if !strings.Contains(low, "without a scripted greeting") {
		t.Error("the cold-open prompt does not warn against a scripted greeting")
	}
}

func TestColdOpenSubsequentBreaksAreNot(t *testing.T) {
	s := NewSession()
	s.BreakAired()

	if s.IsColdOpen() {
		t.Error("break 2 is still marked as a cold open")
	}
	for i := 0; i < 5; i++ {
		s.BreakAired()
		if s.IsColdOpen() {
			t.Fatalf("break %d is marked as a cold open", i+3)
		}
	}

	got, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t), Current: testDossier(1),
		WindowSeconds: 9, IsColdOpen: s.IsColdOpen(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "FIRST THING YOU SAY") {
		t.Error("a later break carries cold-open instructions")
	}
}

func TestColdOpenResetsOnNewSession(t *testing.T) {
	s := NewSession()
	s.BreakAired()
	if s.IsColdOpen() {
		t.Fatal("still a cold open after one break")
	}

	s.Restart()

	if !s.IsColdOpen() {
		t.Error("a restarted session did not produce a new cold open")
	}
}

// TestColdOpenObeysSaidLinesIndex: a DJ that says the exact same greeting every
// session is precisely the repetition failure this project is built against, so
// cold opens are NOT exempt from the validator.
func TestColdOpenObeysSaidLinesIndex(t *testing.T) {
	v := validator(t, nil)
	ctx := context.Background()

	greeting := "Whatever brought you here at this hour, the frequency is holding steady."
	if err := v.Said.Record(ctx, greeting); err != nil {
		t.Fatal(err)
	}

	w := &scriptedWriter{replies: []string{breakJSON(greeting), breakJSON(greeting)}}
	v.Writer = w

	if _, err := v.Generate(ctx, "cold open prompt", nil, nil, nil); err == nil {
		t.Error("a repeated cold-open greeting was allowed through the validator")
	}
	if w.calls != MaxAttempts {
		t.Errorf("%d attempts, want %d: cold opens go through the same path", w.calls, MaxAttempts)
	}
}
