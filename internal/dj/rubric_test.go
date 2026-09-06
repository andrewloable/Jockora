// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

// NAMING: every test here is TestRubric*, because the step's VERIFY command is
// `go test ./internal/dj/ -run TestRubric`. The spec proposed
// TestCategorisesFailures, which that command silently does not run -- a test
// that never executes is worse than no test, because it reports as passing.

func groundedDossier() *enrich.Dossier {
	return &enrich.Dossier{
		SubjectSummary: "a long drive that never arrives anywhere",
		StationTags:    []string{"rock"},
		Mood:           []string{"restless"},
		Confidence:     enrich.ConfidenceHigh,
	}
}

func rubricPersona() *Persona {
	return &Persona{
		id:   "test_jock",
		name: "Test Jock",
		forbidden: []string{
			"Never addresses the listener as 'folks' or 'guys'.",
			"Never uses exclamation marks.",
			"Never sentimental about the past.",
		},
	}
}

func TestRubricPassesCleanBreak(t *testing.T) {
	r := Rubric{Said: saidStore(t), Persona: rubricPersona()}
	b := &Break{
		Opening:       "Coming up on the quiet side of midnight.",
		Body:          "Restless music for a drive that never arrives.",
		AssertedFacts: []string{"cur.subject_summary"},
	}

	got, err := r.Score(context.Background(), b, nil, groundedDossier(), nil, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Pass {
		t.Errorf("clean break failed the rubric: %v", got.Failures)
	}
}

func TestRubricFailsOnUngroundedFact(t *testing.T) {
	r := Rubric{Said: saidStore(t), Persona: rubricPersona()}
	b := &Break{Body: "That one was recorded in a barn in 1974.", AssertedFacts: []string{"cur.recording_location"}}

	got, err := r.Score(context.Background(), b, nil, groundedDossier(), nil, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := got.Failures[ReasonFact]; !bad {
		t.Errorf("a fact id resolving to nothing passed criterion (a): %+v", got)
	}
}

// TestRubricFailsFactOnLowConfidence: the threshold is the point. A dossier the
// enricher itself marked low is not something the DJ may assert.
func TestRubricFailsFactOnLowConfidence(t *testing.T) {
	r := Rubric{Said: saidStore(t), Persona: rubricPersona()}
	d := groundedDossier()
	d.Confidence = enrich.ConfidenceNone
	b := &Break{Body: "Something about a long drive.", AssertedFacts: []string{"cur.subject_summary"}}

	got, err := r.Score(context.Background(), b, nil, d, nil, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := got.Failures[ReasonFact]; !bad {
		t.Error("a fact below the confidence threshold passed criterion (a)")
	}
}

func TestRubricFailsOnLongBreak(t *testing.T) {
	r := Rubric{Said: saidStore(t), Persona: rubricPersona()}
	b := &Break{Body: strings.TrimSpace(strings.Repeat("word ", MaxRubricWords+5))}

	got, err := r.Score(context.Background(), b, nil, groundedDossier(), nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := got.Failures[ReasonLength]; !bad {
		t.Errorf("%d words passed a %d-word ceiling", MaxRubricWords+5, MaxRubricWords)
	}
}

// TestRubricUsesTheLowerOfTargetAndCeiling: a 20-word ramp overrun is a real
// failure even though 120 words is the absolute cap. Checking only the cap
// would let every ramp run six times its budget and still score clean.
func TestRubricUsesTheLowerOfTargetAndCeiling(t *testing.T) {
	r := Rubric{Said: saidStore(t), Persona: rubricPersona()}
	b := &Break{Body: strings.TrimSpace(strings.Repeat("word ", 40))}

	got, err := r.Score(context.Background(), b, nil, groundedDossier(), nil, 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := got.Failures[ReasonLength]; !bad {
		t.Error("40 words passed a 20-word placement target")
	}
	if !strings.Contains(got.Failures[ReasonLength], "limit 20") {
		t.Errorf("the failure blamed the wrong limit: %q", got.Failures[ReasonLength])
	}
}

func TestRubricFailsOnForbidden(t *testing.T) {
	r := Rubric{Said: saidStore(t), Persona: rubricPersona()}

	for name, text := range map[string]string{
		"quoted phrase":    "Alright folks, here is the next one.",
		"exclamation mark": "Here we go again.",
	} {
		if name == "exclamation mark" {
			text = "Here we go again!"
		}
		got, err := r.Score(context.Background(), &Break{Body: text}, nil, groundedDossier(), nil, 60, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, bad := got.Failures[ReasonTone]; !bad {
			t.Errorf("%s: %q passed the persona's forbidden list", name, text)
		}
	}
}

// TestRubricDoesNotFireOnWordFragments: "folks" forbidden must not condemn
// "folksong". A tone check that fires on substrings makes every round look
// worse than it is and sends the next prompt edit chasing nothing.
func TestRubricDoesNotFireOnWordFragments(t *testing.T) {
	r := Rubric{Said: saidStore(t), Persona: rubricPersona()}
	b := &Break{Body: "An old folksong, rearranged for guitar."}

	got, err := r.Score(context.Background(), b, nil, groundedDossier(), nil, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rule, bad := got.Failures[ReasonTone]; bad {
		t.Errorf("%q fired on a word fragment: %s", b.Body, rule)
	}
}

func TestRubricFailsOnPhrasing(t *testing.T) {
	said := saidStore(t)
	line := "Restless music for a drive that never arrives anywhere at all."
	if err := said.Record(context.Background(), line); err != nil {
		t.Fatal(err)
	}

	r := Rubric{Said: said, Persona: rubricPersona()}
	got, err := r.Score(context.Background(), &Break{Body: line}, nil, groundedDossier(), nil, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := got.Failures[ReasonPhrasing]; !bad {
		t.Error("a line already in the said_lines index passed criterion (b)")
	}
}

// TestRubricIgnoresTrackNamesInPhrasing carries GATE 5's finding into the
// rubric. An artist and title recur every time that record is played, so a
// raw n-gram check condemns correct writing for naming the song. Production
// passes the names through CheckCollisionIgnoring; if the rubric did not, it
// would report phrasing failures that the running station never has.
func TestRubricIgnoresTrackNamesInPhrasing(t *testing.T) {
	said := saidStore(t)
	names := []string{"Fleetwood Mac", "Albatross"}
	if err := said.Record(context.Background(), "Fleetwood Mac Albatross Fleetwood Mac"); err != nil {
		t.Fatal(err)
	}

	r := Rubric{Said: said, Persona: rubricPersona()}
	got, err := r.Score(context.Background(), &Break{Body: "Fleetwood Mac Albatross Fleetwood Mac"}, nil, groundedDossier(), nil, 60, names)
	if err != nil {
		t.Fatal(err)
	}
	if gram, bad := got.Failures[ReasonPhrasing]; bad {
		t.Errorf("a repeat made entirely of the track's own name counted as a collision: %s", gram)
	}
}

// TestRubricCategorisesFailures is the spec's TestCategorisesFailures, renamed
// so that -run TestRubric reaches it.
func TestRubricCategorisesFailures(t *testing.T) {
	r := Rubric{Said: saidStore(t), Persona: rubricPersona()}

	long := strings.TrimSpace(strings.Repeat("word ", MaxRubricWords+5))
	results := []RubricResult{}
	for _, b := range []*Break{
		{Body: "A clean short line about restless driving."},
		{Body: long},
		{Body: "Alright folks, " + long},
		{Body: "Hello guys."},
	} {
		got, err := r.Score(context.Background(), b, nil, groundedDossier(), nil, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, got)
	}

	s := Summarise(results)
	if s.Total != 4 || s.Passed != 1 {
		t.Errorf("total/passed = %d/%d, want 4/1", s.Total, s.Passed)
	}
	// The third break is both too long and off-tone. It must count against
	// BOTH, because the summary answers "what should the next prompt fix",
	// not "how many breaks were bad".
	if s.Counts[ReasonLength] != 2 {
		t.Errorf("length failures = %d, want 2", s.Counts[ReasonLength])
	}
	if s.Counts[ReasonTone] != 2 {
		t.Errorf("tone failures = %d, want 2", s.Counts[ReasonTone])
	}
	if s.PassRate() != 0.25 {
		t.Errorf("PassRate = %v, want 0.25", s.PassRate())
	}
}

// TestRubricReportsWhichRulesItCannotCheck keeps the rubric honest about its
// own reach. "Never sentimental about the past" has no mechanical test, and
// silently dropping it would let an operator believe tone was verified.
func TestRubricReportsWhichRulesItCannotCheck(t *testing.T) {
	checkable, human := CheckableRules(rubricPersona())

	if len(checkable) != 2 {
		t.Errorf("checkable = %v, want the quoted-phrase rule and the exclamation rule", checkable)
	}
	if len(human) != 1 || !strings.Contains(human[0], "sentimental") {
		t.Errorf("human = %v, want the sentimentality rule left to a listener", human)
	}
}
