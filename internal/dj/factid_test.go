// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

// THE SCHEMA TALKS TO THE SAMPLER, NOT TO THE MODEL.
//
// asserted_facts is an enum of ids like "cur.artist_facts[0]". Under llama.cpp
// a grammar makes those the only emittable values, so the model cannot get it
// wrong. A hosted endpoint treats the schema as a REQUEST -- and the model has
// never been shown an id anywhere, because the prompt renders facts as their
// text. So it declares the text.
//
// Live, first break after deploying to a hosted model:
//
//	break dropped ... ungrounded: asserts an unresolvable fact:
//	"Incubus is an alternative rock band from the United States"
//
// Every fact-bearing break would drop, and the DJ would be permanently silent
// on any host without sampler-level enforcement. This is the same failure as
// the model inventing its own field names, and it has the same fix: say it in
// the prompt.
//
// Every test here is TestFactID*, which is the -run pattern for this fix.

func TestFactIDAppearsBesideItsFact(t *testing.T) {
	d := &enrich.Dossier{
		Confidence:     enrich.ConfidenceHigh,
		SubjectSummary: "A song about driving at night.",
		Release:        "Released in 1983.",
		ArtistFacts:    []string{"Formed in 1980.", "From Manchester."},
	}
	prompt, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t),
		Current: d, CurrentArtist: "New Order", CurrentTitle: "Blue Monday",
		WindowSeconds: 48,
	})
	if err != nil {
		t.Fatalf("BuildBreakPrompt: %v", err)
	}

	// Every id the schema would accept has to be visible to the model.
	for _, id := range ResolvableFactIDs(nil, d, nil) {
		if !strings.Contains(prompt, id) {
			t.Errorf("prompt never shows %q, so the model cannot name it:\n%s", id, prompt)
		}
	}
	// And the fact text is still there to be said in the DJ's own words.
	if !strings.Contains(prompt, "Formed in 1980.") {
		t.Error("the fact text went missing along with the id")
	}
}

func TestFactIDSpokenAloudIsStillRefused(t *testing.T) {
	// Showing ids to the model is only safe because saying one is caught.
	// Whatever the prompt shows, the model says -- this file's oldest lesson.
	for _, said := range []string{
		"Coming up, cur.artist_facts[0] and then some music.",
		"That was next.subject_summary, which is a hell of a thing.",
	} {
		if _, echoed := echoesInstructions(said, nil); !echoed {
			t.Errorf("a spoken field reference aired: %q", said)
		}
	}
}

func TestFactIDUnresolvableDeclarationDropsTheFactNotTheBreak(t *testing.T) {
	// One generation means there is no second chance, so a break whose PROSE is
	// fine must not be thrown away over a malformed declaration. Declaring
	// nothing is already an accepted outcome -- a personality-only break -- and
	// this is no weaker than that.
	cur := &enrich.Dossier{Confidence: enrich.ConfidenceHigh, ArtistFacts: []string{"Formed in 1991."}}
	raw := `{"opening":"","body":"Incubus, and they have been at this a while.","handoff":"",` +
		`"asserted_facts":["Incubus is an alternative rock band from the United States"]}`

	v := &Validator{Writer: &scriptedWriter{replies: []string{raw}}, Said: saidStore(t)}
	b, err := v.Generate(t.Context(), "prompt", 0, nil, cur, nil)
	if err != nil {
		t.Fatalf("a good break was dropped over its declaration: %v", err)
	}
	if !strings.Contains(b.Text(), "Incubus") {
		t.Errorf("break text = %q", b.Text())
	}
	if len(b.AssertedFacts) != 0 {
		t.Errorf("AssertedFacts = %v, want the unresolvable one removed", b.AssertedFacts)
	}
}

func TestFactIDResolvableDeclarationSurvives(t *testing.T) {
	cur := &enrich.Dossier{Confidence: enrich.ConfidenceHigh, ArtistFacts: []string{"Formed in 1991."}}
	raw := `{"opening":"","body":"Incubus formed back in ninety-one.","handoff":"",` +
		`"asserted_facts":["cur.artist_facts[0]"]}`

	v := &Validator{Writer: &scriptedWriter{replies: []string{raw}}, Said: saidStore(t)}
	b, err := v.Generate(t.Context(), "prompt", 0, nil, cur, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(b.AssertedFacts) != 1 || b.AssertedFacts[0] != "cur.artist_facts[0]" {
		t.Errorf("AssertedFacts = %v, want the real id kept", b.AssertedFacts)
	}
}
