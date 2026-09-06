// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Gate5 thresholds. Each is a number the gate fails on, not a guideline.
const (
	// MaxRawCollisions is how many content-word 4-grams may be shared across
	// the sample. One is tolerated; two means the index is not discriminating.
	MaxRawCollisions = 1

	// MaxDropRate is what the validator may cost. A validator silently
	// dropping four breaks in ten would pass a collision gate while destroying
	// the product, which is why this is measured separately and at all.
	MaxDropRate = 0.10
)

// Gate5Report is the measurement GATE 5 turns on.
//
// It describes RAW model output, before validation. Measuring aired breaks
// would report a zero collision rate BY CONSTRUCTION -- the validator rejects
// every colliding one -- which is a gate that cannot fail.
type Gate5Report struct {
	// Sample is the number of raw breaks examined.
	Sample int

	// SharedGrams are the content-word 4-grams appearing in more than one
	// break, with how many breaks each appeared in.
	SharedGrams []GramCount
	// DuplicateOpenings are normalised openings used more than once.
	DuplicateOpenings []GramCount

	// Generated and Dropped describe the SECOND pass, with the validator on.
	Generated, Dropped int
	DropRate           float64

	// FactsAsserted and FactsResolved measure groundedness: every asserted
	// fact must resolve against a dossier at the confidence threshold.
	FactsAsserted, FactsResolved int

	Pass     bool
	Failures []string
}

// GramCount is one repeated phrase and how many breaks carried it.
type GramCount struct {
	Text  string
	Count int
}

// AnalyseGate5 grades a sample of raw breaks and a validated run.
//
// raw is every break the model produced with the validator DISABLED. generated
// and dropped come from running the same request count with it ENABLED.
func AnalyseGate5(raw []string, generated, dropped, factsAsserted, factsResolved int) Gate5Report {
	return AnalyseGate5Ignoring(raw, nil, generated, dropped, factsAsserted, factsResolved)
}

// AnalyseGate5Ignoring is AnalyseGate5 with proper nouns excused, exactly as
// the live collision index excuses them.
//
// names is per raw break: the artists and titles in play at that boundary.
// Without it this criterion counts NAMING THE RECORD as repetition, which
// production does not -- "laurindo almeida bossa nova" is a band's name, not a
// phrase the DJ chose. Measuring a rule the product does not enforce would
// grade something nobody ships.
func AnalyseGate5Ignoring(raw []string, names [][]string, generated, dropped, factsAsserted, factsResolved int) Gate5Report {
	r := Gate5Report{
		Sample:        len(raw),
		Generated:     generated,
		Dropped:       dropped,
		FactsAsserted: factsAsserted,
		FactsResolved: factsResolved,
	}
	if generated > 0 {
		r.DropRate = float64(dropped) / float64(generated)
	}

	// A gram counts once per BREAK, not once per occurrence: a break that
	// repeats a phrase internally is a different fault and is not this one.
	gramBreaks := map[string]int{}
	for i, text := range raw {
		nameWords := map[string]bool{}
		if i < len(names) {
			for _, n := range names[i] {
				for _, w := range ContentWords(n) {
					nameWords[w] = true
				}
			}
		}
		seen := map[string]bool{}
		for _, g := range NGrams(ContentWords(text), GramSize) {
			if len(nameWords) > 0 && allNameWords(g, nameWords) {
				continue
			}
			if !seen[g] {
				seen[g] = true
				gramBreaks[g]++
			}
		}
	}
	r.SharedGrams = repeated(gramBreaks)

	openings := map[string]int{}
	for _, text := range raw {
		if o := NormaliseOpening(text); o != "" {
			openings[o]++
		}
	}
	r.DuplicateOpenings = repeated(openings)

	// (a) raw collisions
	if n := len(r.SharedGrams); n > MaxRawCollisions {
		r.Failures = append(r.Failures, fmt.Sprintf(
			"(a) %d shared 4-grams across %d raw breaks, limit %d -- escalate the index from n-grams to embeddings",
			n, r.Sample, MaxRawCollisions))
	}
	// (b) near-duplicate openings
	if n := len(r.DuplicateOpenings); n > 0 {
		r.Failures = append(r.Failures, fmt.Sprintf(
			"(b) %d opening(s) reused across the sample", n))
	}
	// (c) groundedness
	if factsAsserted > 0 && factsResolved != factsAsserted {
		r.Failures = append(r.Failures, fmt.Sprintf(
			"(c) %d of %d asserted facts resolved at confidence >= %.1f",
			factsResolved, factsAsserted, FactConfidenceThreshold))
	}
	// (d) validator cost
	if r.DropRate > MaxDropRate {
		r.Failures = append(r.Failures, fmt.Sprintf(
			"(d) validator dropped %.0f%% (%d of %d), limit %.0f%% -- fix the PROMPT, not the validator",
			100*r.DropRate, dropped, generated, 100*MaxDropRate))
	}

	r.Pass = len(r.Failures) == 0
	return r
}

// wilson95 is the 95% Wilson score interval for a proportion.
//
// Wilson rather than the textbook normal approximation, because at n=20 with a
// small numerator the normal interval is simply wrong: it goes below zero, and
// at zero successes it collapses to the point estimate 0 +/- 0, which reads as
// certainty when the data supports nothing of the kind. Wilson stays inside
// [0,1] and keeps a real upper bound when nothing was observed at all.
func wilson95(successes, n int) (lo, hi float64) {
	if n <= 0 {
		return 0, 0
	}
	const z = 1.96
	nf := float64(n)
	p := float64(successes) / nf
	denom := 1 + z*z/nf
	centre := (p + z*z/(2*nf)) / denom
	spread := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / denom
	lo, hi = centre-spread, centre+spread
	if lo < 0 {
		lo = 0
	}
	if hi > 1 {
		hi = 1
	}
	return lo, hi
}

// repeated returns the entries seen more than once, most frequent first.
func repeated(counts map[string]int) []GramCount {
	var out []GramCount
	for text, n := range counts {
		if n > 1 {
			out = append(out, GramCount{Text: text, Count: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Text < out[j].Text
	})
	return out
}

// String renders the report, stating the decision rather than leaving it to be
// inferred from the numbers.
func (r Gate5Report) String() string {
	var b strings.Builder
	b.WriteString("GATE 5 — raw collision and validator cost\n")
	fmt.Fprintf(&b, "  raw breaks examined     %d\n", r.Sample)
	fmt.Fprintf(&b, "  shared 4-grams          %d (limit %d)\n", len(r.SharedGrams), MaxRawCollisions)
	fmt.Fprintf(&b, "  reused openings         %d (limit 0)\n", len(r.DuplicateOpenings))
	fmt.Fprintf(&b, "  asserted facts resolved %d of %d\n", r.FactsResolved, r.FactsAsserted)
	lo, hi := wilson95(r.Dropped, r.Generated)
	fmt.Fprintf(&b, "  validator drop rate     %.0f%% (%d of %d, limit %.0f%%), 95%% CI %.0f%%-%.0f%%\n",
		100*r.DropRate, r.Dropped, r.Generated, 100*MaxDropRate, 100*lo, 100*hi)

	if len(r.SharedGrams) > 0 {
		b.WriteString("\n  SHARED PHRASES\n")
		for _, g := range r.SharedGrams {
			fmt.Fprintf(&b, "    %2d breaks  %q\n", g.Count, strings.ReplaceAll(g.Text, "\n", " "))
		}
	}
	if len(r.DuplicateOpenings) > 0 {
		b.WriteString("\n  REUSED OPENINGS\n")
		for _, o := range r.DuplicateOpenings {
			fmt.Fprintf(&b, "    %2d breaks  %q\n", o.Count, o.Text)
		}
	}

	// PRINTED WITH EVERY RESULT, pass or fail. This gate is correctly sized for
	// what it is -- a go/no-go before committing a week to step 5.5 -- and the
	// risk is entirely in how it gets read afterwards. At n=20 the interval
	// above spans most of the range the decision cares about, so a pass here is
	// evidence that the design is not obviously broken, and is not a
	// characterisation of the collision rate of a station that will write
	// roughly 1,500 breaks a month.
	//
	// The real number comes free from the live-with-it month: compute the rate
	// over every break aired and dropped, and use THAT to decide whether
	// n-grams suffice or the index has to escalate to embeddings.
	fmt.Fprintf(&b, "\nSAMPLE\n  n=%d raw, %d generated. Wide interval; a pass is a go/no-go,\n  not a monthly rate.\n", r.Sample, r.Generated)
	b.WriteString("  Measure the real rate over the live-with-it month, across every\n")
	b.WriteString("  break aired and dropped, before trusting or escalating the index.\n")

	b.WriteString("\nDECISION\n")
	if r.Pass {
		b.WriteString("  PASS. The n-gram index discriminates and the validator is not\n")
		b.WriteString("  papering over a repetitive writer.\n")
		return b.String()
	}
	b.WriteString("  FAIL\n")
	for _, f := range r.Failures {
		fmt.Fprintf(&b, "    %s\n", f)
	}
	return b.String()
}
