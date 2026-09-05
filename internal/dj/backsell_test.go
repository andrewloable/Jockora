// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

func TestBacksellOutroPromptIncludesPreviousTrack(t *testing.T) {
	got, err := BuildBreakPrompt(PromptInput{
		Persona:        testPersona(t),
		Previous:       dossierWith(enrich.ConfidenceHigh, "Recorded in one take."),
		PreviousArtist: "New Order",
		PreviousTitle:  "Blue Monday",
		Current:        testDossier(2),
		Placement:      "outro",
		WindowSeconds:  12,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"Blue Monday", "New Order", "BACKSELL"} {
		if !strings.Contains(got, want) {
			t.Errorf("the outro prompt omits %q", want)
		}
	}
	if !strings.Contains(got, "THE TRACK THAT JUST FINISHED") {
		t.Error("the previous track's dossier is not offered to the writer")
	}
	if !strings.Contains(got, "Recorded in one take.") {
		t.Error("the previous track's facts are not offered")
	}
}

// TestBacksellRampPromptDoesNotBacksell: the song is beginning, and naming what
// just ended there is confusing.
func TestBacksellRampPromptDoesNotBacksell(t *testing.T) {
	got, err := BuildBreakPrompt(PromptInput{
		Persona:        testPersona(t),
		Previous:       dossierWith(enrich.ConfidenceHigh, "Recorded in one take."),
		PreviousArtist: "New Order",
		PreviousTitle:  "Blue Monday",
		Current:        testDossier(2),
		Placement:      "ramp",
		WindowSeconds:  9,
	})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(got, "BACKSELL") {
		t.Error("a ramp prompt contains backsell instructions")
	}
	if strings.Contains(got, "Blue Monday") {
		t.Error("a ramp prompt names the previous track")
	}
	if !strings.Contains(got, "Talk up what is STARTING") {
		t.Error("a ramp prompt does not point the writer at the incoming track")
	}
}

func TestBacksellFactsResolveAgainstPreviousTrack(t *testing.T) {
	prev := dossierWith(enrich.ConfidenceHigh, "Recorded in one take.")
	cur := dossierWith(enrich.ConfidenceHigh, "A different fact.")

	ids := ResolvableFactIDs(prev, cur, nil)
	if !containsStr(ids, "prev.artist_facts[0]") {
		t.Fatalf("prev ids are not resolvable: %v", ids)
	}

	b := &Break{AssertedFacts: []string{"prev.artist_facts[0]"}}
	if err := ResolveFacts(b, prev, cur, nil); err != nil {
		t.Errorf("a prev fact was rejected: %v", err)
	}

	text, ok := ResolveFactText("prev.artist_facts[0]", prev, cur, nil)
	if !ok || text != "Recorded in one take." {
		t.Errorf("ResolveFactText = %q, %v", text, ok)
	}

	// And the schema offers it.
	if !strings.Contains(mustJSONString(t, BreakSchema(prev, cur, nil)), "prev.artist_facts[0]") {
		t.Error("the schema enum omits the previous track's facts")
	}
}

// TestBacksellNoPreviousTrackAtSessionStart: the first item has nothing behind
// it, so no backsell and no prev id may exist.
func TestBacksellNoPreviousTrackAtSessionStart(t *testing.T) {
	cur := dossierWith(enrich.ConfidenceHigh, "A fact.")

	got, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t), Current: cur,
		Placement: "outro", WindowSeconds: 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "BACKSELL") {
		t.Error("a backsell was requested with no previous track")
	}

	for _, id := range ResolvableFactIDs(nil, cur, nil) {
		if strings.HasPrefix(id, "prev.") {
			t.Errorf("a prev fact id was offered with no previous dossier: %q", id)
		}
	}
	b := &Break{AssertedFacts: []string{"prev.artist_facts[0]"}}
	if err := ResolveFacts(b, nil, cur, nil); err == nil {
		t.Error("a prev fact resolved with no previous dossier")
	}
}

// TestBacksellUngroundedPrevFactRejected: adding a prefix to the resolver
// without adding it to the validator's allowed set would open a hole.
func TestBacksellUngroundedPrevFactRejected(t *testing.T) {
	prev := dossierWith(enrich.ConfidenceNone, "Should not be assertable.")
	cur := dossierWith(enrich.ConfidenceHigh, "A fact.")

	b := &Break{AssertedFacts: []string{"prev.artist_facts[0]"}}
	if err := ResolveFacts(b, prev, cur, nil); err == nil {
		t.Error("a fact from a confidence-none previous dossier was accepted")
	}
}
