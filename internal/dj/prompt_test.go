// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

func testPersona(t *testing.T) *Persona {
	t.Helper()
	p, err := LoadPersona(repoPersona)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testDossier(facts int) *enrich.Dossier {
	d := &enrich.Dossier{
		StationTags:    []string{"synthwave"},
		Mood:           []string{"nocturnal"},
		SubjectSummary: "A narrator drives out of a city he is not going back to.",
		Sources:        []string{enrich.SourceMusicBrainz},
		Confidence:     enrich.ConfidenceHigh,
	}
	for i := 0; i < facts; i++ {
		d.ArtistFacts = append(d.ArtistFacts, fmt.Sprintf("Distinct fact number %d about the band.", i))
	}
	return d
}

func prohibitions(openings, grams int) Prohibitions {
	var p Prohibitions
	for i := 0; i < openings; i++ {
		p.Openings = append(p.Openings, fmt.Sprintf("opening formula number %d here", i))
	}
	for i := 0; i < grams; i++ {
		p.NGrams = append(p.NGrams, fmt.Sprintf("phrase gram number %d", i))
	}
	return p
}

func TestPromptUnderBudget(t *testing.T) {
	got, err := BuildBreakPrompt(PromptInput{
		Persona:       testPersona(t),
		Current:       testDossier(3),
		Next:          testDossier(3),
		Prohibitions:  prohibitions(20, 100),
		Placement:     "ramp",
		WindowSeconds: 9,
		Schema:        map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("BuildBreakPrompt: %v", err)
	}

	if n := EstimateTokens(got); n > TokenBudget {
		t.Errorf("prompt is %d tokens, over the %d budget", n, TokenBudget)
	}
	t.Logf("full prompt: %d estimated tokens of %d", EstimateTokens(got), TokenBudget)
}

// TestPromptTruncatesProhibitionsNotPersona: the persona is ground truth and the
// prohibition list is the softest input in the prompt.
func TestPromptTruncatesProhibitionsNotPersona(t *testing.T) {
	p := testPersona(t)

	got, err := BuildBreakPrompt(PromptInput{
		Persona:       p,
		Current:       testDossier(3),
		Prohibitions:  prohibitions(200, 5000),
		WindowSeconds: 9,
	})
	if err != nil {
		t.Fatalf("BuildBreakPrompt: %v", err)
	}

	if n := EstimateTokens(got); n > TokenBudget {
		t.Errorf("prompt is %d tokens with 5000 prohibitions, over the %d budget", n, TokenBudget)
	}
	// Every word of the persona survived.
	if !strings.Contains(got, p.Personality()) {
		t.Error("the persona's character text was trimmed")
	}
	if !strings.Contains(got, p.SpeechStyle()) {
		t.Error("the persona's speech style was trimmed")
	}
	for _, rule := range p.Forbidden() {
		if !strings.Contains(got, rule) {
			t.Errorf("a forbidden rule was trimmed: %q", rule)
		}
	}
	// And the instructions that make the response parseable survived.
	if !strings.Contains(got, "Return ONLY the JSON object") {
		t.Error("the output instruction was trimmed")
	}
	// The prohibitions were what gave way.
	if strings.Count(got, "phrase gram number") >= 5000 {
		t.Error("the prohibition list was not trimmed")
	}
}

func TestPromptIncludesOnlyThreeFacts(t *testing.T) {
	got, err := BuildBreakPrompt(PromptInput{
		Persona:       testPersona(t),
		Current:       testDossier(10),
		WindowSeconds: 9,
	})
	if err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(got, "  fact: "); n != MaxFactsInPrompt {
		t.Errorf("%d facts in the prompt, want exactly %d", n, MaxFactsInPrompt)
	}
	for i := MaxFactsInPrompt; i < 10; i++ {
		if strings.Contains(got, fmt.Sprintf("Distinct fact number %d ", i)) {
			t.Errorf("fact %d leaked into the prompt", i)
		}
	}
}

func TestPromptDurationBudgetInPrompt(t *testing.T) {
	got, err := BuildBreakPrompt(PromptInput{
		Persona:       testPersona(t),
		Current:       testDossier(2),
		WindowSeconds: 9.0,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 9.0s x 2.5 words/second.
	if want := WordTarget(9.0); want != 22 && want != 23 {
		t.Fatalf("WordTarget(9.0) = %d, want about 22", want)
	}
	// A CEILING, not an approximation. "about N words" was measured over 49
	// breaks as permission to overshoot -- ramps ran 1.5x their target and
	// outros 2.1x -- so the wording is part of the contract, not decoration.
	if !strings.Contains(got, fmt.Sprintf("%d words MAXIMUM", WordTarget(9.0))) {
		t.Errorf("the prompt does not state the word target as a ceiling:\n%s", got)
	}
	if strings.Contains(got, fmt.Sprintf("about %d words", WordTarget(9.0))) {
		t.Error("the prompt approximates the length again; that is the thing that overran")
	}
	if !strings.Contains(got, "9.0 seconds") {
		t.Error("the prompt does not state the speaking window")
	}
}

// TestPromptEmptyDossierProducesPersonalityOnlyPrompt: an empty dossier is a
// designed outcome. The DJ must be told to say less, not to fill the gap.
func TestPromptEmptyDossierProducesPersonalityOnlyPrompt(t *testing.T) {
	got, err := BuildBreakPrompt(PromptInput{
		Persona:       testPersona(t),
		WindowSeconds: 9,
	})
	if err != nil {
		t.Fatal(err)
	}

	low := strings.ToLower(got)
	if !strings.Contains(low, "no facts") {
		t.Error("the prompt does not say the DJ has no facts")
	}
	// Asserted by MEANING, not by phrase. The empty-dossier block was rewritten
	// to let the jock be entertaining about not knowing rather than merely
	// terse, and pinning its exact wording would make every future rewrite look
	// like a regression. What must never change is the last clause: a guess the
	// listener can hear is a guess is entertainment, and the same sentence said
	// flatly is a lie they will repeat.
	if !strings.Contains(low, "may not do is state something as though you knew it") {
		t.Error("the prompt does not forbid stating an invented detail as fact")
	}
	for _, forbidden := range []string{"year", "place", "label", "band member"} {
		if !strings.Contains(low, forbidden) {
			t.Errorf("the prompt does not name %q among the details that may not be invented", forbidden)
		}
	}
	if strings.Contains(got, "  fact: ") {
		t.Error("the prompt lists facts despite there being no dossier")
	}
}

// TestPromptSchemaCountsAgainstBudget: the json_schema travels with the request
// and is charged against the same budget.
//
// A realistic asserted_facts enum lists only the facts actually resolvable for
// this pair of tracks, which is at most MaxFactsInPrompt per dossier.
func TestPromptSchemaCountsAgainstBudget(t *testing.T) {
	schema := factEnumSchema(2 * MaxFactsInPrompt)

	p := testPersona(t)
	got, err := BuildBreakPrompt(PromptInput{
		Persona:       p,
		Current:       testDossier(3),
		Next:          testDossier(3),
		Prohibitions:  prohibitions(20, 100),
		WindowSeconds: 9,
		Schema:        schema,
	})
	if err != nil {
		t.Fatalf("BuildBreakPrompt: %v", err)
	}

	schemaTokens := EstimateTokens(mustJSONString(t, schema))
	total := EstimateTokens(got) + schemaTokens
	if total > TokenBudget {
		t.Errorf("prompt %d + schema %d = %d tokens, over the %d budget",
			EstimateTokens(got), schemaTokens, total, TokenBudget)
	}
	if !strings.Contains(got, p.Personality()) {
		t.Error("the persona was trimmed to make room for the schema")
	}
	t.Logf("prompt %d + schema %d = %d of %d", EstimateTokens(got), schemaTokens, total, TokenBudget)
}

// TestPromptLargeSchemaTrimsProhibitionsFirst: a schema big enough to squeeze
// the prompt must cost prohibitions, never the persona.
func TestPromptLargeSchemaTrimsProhibitionsFirst(t *testing.T) {
	p := testPersona(t)
	small, err := BuildBreakPrompt(PromptInput{
		Persona: p, Current: testDossier(3), Prohibitions: prohibitions(20, 100),
		WindowSeconds: 9, Schema: factEnumSchema(6),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Large enough that prompt plus schema genuinely exceeds the budget, so
	// something has to give.
	bigSchema := factEnumSchema(120)
	big, err := BuildBreakPrompt(PromptInput{
		Persona: p, Current: testDossier(3), Prohibitions: prohibitions(20, 100),
		WindowSeconds: 9, Schema: bigSchema,
	})
	if err != nil {
		t.Fatal(err)
	}

	if EstimateTokens(big) >= EstimateTokens(small) {
		t.Errorf("a schema large enough to exceed the budget did not shrink the prompt: %d then %d",
			EstimateTokens(small), EstimateTokens(big))
	}
	if total := EstimateTokens(big) + EstimateTokens(mustJSONString(t, bigSchema)); total > TokenBudget {
		t.Errorf("prompt plus schema is %d tokens, over the %d budget", total, TokenBudget)
	}
	if !strings.Contains(big, p.Personality()) {
		t.Error("the persona gave way to the schema")
	}
	for _, rule := range p.Forbidden() {
		if !strings.Contains(big, rule) {
			t.Errorf("a forbidden rule gave way to the schema: %q", rule)
		}
	}
}

// TestPromptAbsurdSchemaIsReportedNotSwallowed: if the schema alone cannot fit,
// that is a caller bug and must surface. Silently sending an over-budget request
// is how a context-window overflow becomes a mystery at 3am.
func TestPromptAbsurdSchemaIsReportedNotSwallowed(t *testing.T) {
	_, err := BuildBreakPrompt(PromptInput{
		Persona:       testPersona(t),
		Current:       testDossier(3),
		WindowSeconds: 9,
		Schema:        factEnumSchema(300),
	})
	if err == nil {
		t.Fatal("a schema too large to fit was accepted silently")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Errorf("error %q does not explain the problem", err)
	}
}

// factEnumSchema builds a break schema whose asserted_facts enum lists n facts.
func factEnumSchema(n int) map[string]any {
	enum := make([]any, n)
	for i := range enum {
		enum[i] = fmt.Sprintf("fact_%d_of_the_current_track", i)
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"asserted_facts": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string", "enum": enum},
			},
		},
	}
}

func TestPromptPlacementGuidance(t *testing.T) {
	for placement, want := range map[string]string{
		"ramp":    "before the singing starts",
		"outro":   "instrumental TAIL",
		"between": "gap between two tracks",
	} {
		got, err := BuildBreakPrompt(PromptInput{
			Persona: testPersona(t), Current: testDossier(1),
			Placement: placement, WindowSeconds: 9,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, want) {
			t.Errorf("placement %q: prompt does not mention %q", placement, want)
		}
	}
}

func TestPromptRequiresPersona(t *testing.T) {
	if _, err := BuildBreakPrompt(PromptInput{WindowSeconds: 9}); err == nil {
		t.Error("BuildBreakPrompt accepted a nil persona")
	}
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestPromptNamesTheRecordsEitherSide closes the invention hole found while
// sampling empty-dossier breaks.
//
// The writer used to be told the PREVIOUS track's name and nothing else, so it
// could never correctly introduce what it was about to play. Asked to open an
// unknown record it announced "Unknown Territory by The Unseen" -- a song and a
// band that do not exist -- because a name was the one thing it needed and the
// one thing it was never given.
func TestPromptNamesTheRecordsEitherSide(t *testing.T) {
	got, err := BuildBreakPrompt(PromptInput{
		Persona:       testPersona(t),
		WindowSeconds: 20,
		CurrentArtist: "Radiohead", CurrentTitle: "Creep",
		NextArtist: "Bush", NextTitle: "Glycerine",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"Radiohead", "Creep", "Bush", "Glycerine"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt never names %q, so the writer must invent one", want)
		}
	}
}

// TestPromptNamesARecordItKnowsNothingElseAbout: a name with no dossier is the
// common case for an untagged library, and it is the case that matters most.
// The jock must be told what is playing even when nothing else is known.
func TestPromptNamesARecordItKnowsNothingElseAbout(t *testing.T) {
	got, err := BuildBreakPrompt(PromptInput{
		Persona:       testPersona(t),
		WindowSeconds: 20,
		NextTitle:     "Albularyong Buta",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "Albularyong Buta") {
		t.Error("a track with no dossier is not named at all; the writer will make a title up")
	}
	if !strings.Contains(got, "nothing else is known") {
		t.Error("the prompt does not say that the name is ALL that is known")
	}
}
