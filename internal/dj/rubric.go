// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/andrewloable/jockora/internal/enrich"
)

// MaxRubricWords is the ceiling on a spoken break regardless of placement.
const MaxRubricWords = 120

// RubricReason is why a break failed.
type RubricReason string

const (
	// ReasonFact means an asserted fact does not resolve at the confidence
	// threshold. The anti-hallucination criterion.
	ReasonFact RubricReason = "fact"
	// ReasonPhrasing means it collides with something already said.
	ReasonPhrasing RubricReason = "phrasing"
	// ReasonLength means it overruns its window.
	ReasonLength RubricReason = "length"
	// ReasonTone means it breaks a persona rule.
	ReasonTone RubricReason = "tone"
)

// RubricResult is one break's verdict.
type RubricResult struct {
	Text     string
	Pass     bool
	Failures map[RubricReason]string
}

// Rubric scores generated breaks against four CHECKABLE criteria.
//
// TASTE IS DELIBERATELY NOT JUDGED. Whoever wrote the persona knows what it will
// say and cannot hear it fresh, so a self-assessment of whether a break is any
// GOOD is worth nothing. That judgement belongs to an unbriefed listener at room
// test B. What can be checked mechanically is checked here, and nothing else is
// claimed.
type Rubric struct {
	Said    *SaidLines
	Persona *Persona
}

// Score grades one break.
//
// wordTarget is the placement's budget; the effective limit is the lower of it
// and MaxRubricWords, because a ramp shorter than 120 words is still the real
// constraint.
func (r Rubric) Score(ctx context.Context, b *Break, prev, cur, next *enrich.Dossier, wordTarget int, names []string) (RubricResult, error) {
	text := b.Text()
	out := RubricResult{Text: text, Failures: map[RubricReason]string{}}

	// (a) groundedness
	for _, id := range b.AssertedFacts {
		if _, ok := ResolveFactText(id, prev, cur, next); !ok {
			out.Failures[ReasonFact] = fmt.Sprintf("asserted %q, which resolves to nothing at confidence >= %.1f",
				id, FactConfidenceThreshold)
			break
		}
	}

	// (b) phrasing, against the FULL index rather than this run's breaks. A
	// break that is fresh today and a repeat of something said last week is
	// still a repeat.
	if r.Said != nil {
		hit, gram, err := r.Said.CheckCollisionIgnoring(ctx, text, names)
		if err != nil {
			return out, err
		}
		if hit {
			out.Failures[ReasonPhrasing] = fmt.Sprintf("collides on %q", gram)
		}
	}

	// (c) length
	limit := MaxRubricWords
	if wordTarget > 0 && wordTarget < limit {
		limit = wordTarget
	}
	if n := len(strings.Fields(text)); n > limit {
		out.Failures[ReasonLength] = fmt.Sprintf("%d spoken words, limit %d", n, limit)
	}

	// (d) persona
	if r.Persona != nil {
		if rule, broke := brokenRule(text, r.Persona); broke {
			out.Failures[ReasonTone] = rule
		}
	}

	out.Pass = len(out.Failures) == 0
	return out, nil
}

// quoted pulls the phrases a forbidden rule names explicitly.
var quoted = regexp.MustCompile(`'([^']{1,40})'`)

// brokenRule checks a break against the persona's forbidden list.
//
// ONLY THE MECHANICALLY CHECKABLE PART. A rule like "never uses exclamation
// marks" or "never addresses the listener as 'guys'" can be tested; "never
// sentimental about the past" cannot, and pretending otherwise would turn this
// rubric into exactly the taste judgement it exists to avoid. CheckableRules
// reports which is which so the gap is visible rather than assumed away.
//
// ponytail: a quoted phrase is matched unconditionally, so a CONDITIONAL rule
// is read more strictly than it is written -- midnight_vale's "never says 'that
// was' followed by the artist name" fires on any "that was". That is why the
// report prints the rule verbatim beside the break: the reader sees the actual
// wording and can dismiss it in a glance. Parse the condition only if these
// turn out to be frequent enough to distort which failure category is largest.
func brokenRule(text string, p *Persona) (string, bool) {
	lower := strings.ToLower(text)

	for _, rule := range p.Forbidden() {
		r := strings.ToLower(rule)

		// Phrases the rule quotes are forbidden verbatim.
		broke := false
		for _, m := range quoted.FindAllStringSubmatch(rule, -1) {
			phrase := strings.ToLower(strings.TrimSpace(m[1]))
			if phrase == "" {
				continue
			}
			if containsWord(lower, phrase) {
				broke = true
			}
		}
		if broke {
			return rule, true
		}

		if strings.Contains(r, "exclamation") && strings.Contains(text, "!") {
			return rule, true
		}
	}
	return "", false
}

// containsWord matches a phrase on word boundaries, so "folks" does not fire
// inside another word.
func containsWord(haystack, phrase string) bool {
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(phrase) + `\b`)
	if err != nil {
		return strings.Contains(haystack, phrase)
	}
	return re.MatchString(haystack)
}

// CheckableRules splits a persona's forbidden list into the rules this rubric
// can enforce and the ones only a listener can.
//
// Reported rather than hidden: a rubric that silently ignores half a persona
// card would let an operator believe tone was verified when it was not.
func CheckableRules(p *Persona) (checkable, human []string) {
	for _, rule := range p.Forbidden() {
		if len(quoted.FindAllString(rule, -1)) > 0 || strings.Contains(strings.ToLower(rule), "exclamation") {
			checkable = append(checkable, rule)
			continue
		}
		human = append(human, rule)
	}
	return checkable, human
}

// RubricSummary aggregates a round.
type RubricSummary struct {
	Total, Passed int
	Counts        map[RubricReason]int
	Results       []RubricResult
}

// PassRate is the fraction that met all four criteria.
func (s RubricSummary) PassRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.Passed) / float64(s.Total)
}

// Summarise counts failures by reason.
//
// A break failing two ways counts against BOTH, because the question this
// answers is "what should the next prompt change fix", not "how many breaks
// were bad".
func Summarise(results []RubricResult) RubricSummary {
	s := RubricSummary{Total: len(results), Counts: map[RubricReason]int{}, Results: results}
	for _, r := range results {
		if r.Pass {
			s.Passed++
			continue
		}
		for reason := range r.Failures {
			s.Counts[reason]++
		}
	}
	return s
}

// String renders the round.
func (s RubricSummary) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d breaks, %d passed (%.0f%%)\n", s.Total, s.Passed, 100*s.PassRate())

	reasons := make([]string, 0, len(s.Counts))
	for r := range s.Counts {
		reasons = append(reasons, string(r))
	}
	sort.Strings(reasons)
	for _, r := range reasons {
		fmt.Fprintf(&b, "  %-9s %d\n", r, s.Counts[RubricReason(r)])
	}
	return b.String()
}
