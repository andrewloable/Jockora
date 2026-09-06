// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"testing"

	"github.com/andrewloable/jockora/internal/dj"
)

// TestSetPersonaMovesAllThreeThings is the invariant a mid-session jock swap
// lives or dies on.
//
// The persona the prompt is built from, the said-lines index the validator
// checks against, and the voice the break is spoken in must move TOGETHER. A
// jock swapped in only one of them speaks as one character in the voice of
// another, or inherits somebody else's phrase history and gets its own writing
// rejected as repetition.
func TestSetPersonaMovesAllThreeThings(t *testing.T) {
	all, err := dj.LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}
	var first, second *dj.Persona
	for _, p := range all {
		switch p.ID() {
		case "baby_concepcion":
			first = p
		case "dutch_mahoney":
			second = p
		}
	}
	if first == nil || second == nil {
		t.Fatal("expected both personas in the roster")
	}
	if first.VoiceID() == second.VoiceID() {
		t.Fatal("the two fixtures share a voice; this test cannot detect a swap")
	}

	said := &dj.SaidLines{JockID: first.ID()}
	w := &BreakWriter{Persona: first, Validator: &dj.Validator{Said: said}}

	if w.Voice() != first.VoiceID() {
		t.Fatalf("Voice() = %q before any swap, want %q", w.Voice(), first.VoiceID())
	}

	w.SetPersona(second)

	if got := w.CurrentPersona().ID(); got != second.ID() {
		t.Errorf("persona = %q, want %q", got, second.ID())
	}
	if w.Voice() != second.VoiceID() {
		t.Errorf("voice = %q, want %q -- the new jock would speak in the old voice",
			w.Voice(), second.VoiceID())
	}
	if said.JockID != second.ID() {
		t.Errorf("said-lines jock = %q, want %q -- the new jock inherits the old phrase history",
			said.JockID, second.ID())
	}
}

// TestSetPersonaIgnoresNil: a failed lookup must not silence the DJ.
func TestSetPersonaIgnoresNil(t *testing.T) {
	all, _ := dj.LoadPersonas("../../personas")
	w := &BreakWriter{Persona: all[0]}
	w.SetPersona(nil)
	if w.CurrentPersona() == nil {
		t.Error("a nil persona replaced the working one")
	}
}

// TestLengthCheckAsksForTheVoiceEachTime: capturing the voice when the struct
// is built means a jock changed mid-session is still spoken in the old voice.
func TestLengthCheckAsksForTheVoiceEachTime(t *testing.T) {
	current := "kokoro:af_heart"
	lc := LengthCheck{Voice: "kokoro:am_michael", VoiceOf: func() string { return current }}

	if got := lc.voice(); got != "kokoro:af_heart" {
		t.Errorf("voice = %q, want the live one", got)
	}
	current = "kokoro:am_fenrir"
	if got := lc.voice(); got != "kokoro:am_fenrir" {
		t.Errorf("voice = %q after a swap, want the new one", got)
	}
	// With no live source it falls back to the fixed field rather than empty.
	if got := (LengthCheck{Voice: "kokoro:am_michael"}).voice(); got != "kokoro:am_michael" {
		t.Errorf("fallback voice = %q", got)
	}
}
