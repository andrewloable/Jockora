// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

// All named TestGate5*, matching the task's own `-run TestGate5`. None of the
// three names it proposed did. Ninth instance of that pattern in this plan.

// distinctBreaks returns n breaks that provably share no 4-gram and no
// opening, built from tokens unique to each index rather than from a rotating
// list -- an earlier version cycled eight subjects and collided with itself,
// which would have made every test below pass for the wrong reason.
func distinctBreaks(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		t := uniqueToken(i)
		out = append(out, "Op"+t+". "+
			"alpha"+t+" bravo"+t+" charlie"+t+" delta"+t+" echo"+t+" foxtrot"+t+".")
	}
	return out
}

// uniqueToken is a letters-only suffix unique to i, so every content word is
// unique to its break.
func uniqueToken(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	if i < len(letters) {
		return string(letters[i])
	}
	return string(letters[i/len(letters)]) + string(letters[i%len(letters)])
}

func TestGate5CountsRawCollisions(t *testing.T) {
	raw := distinctBreaks(18)
	// Two breaks that share a long phrase, which is one shared gram family.
	collide := "Nobody expected the second half of that record to sound like that."
	raw = append(raw, "Right. "+collide, "Well. "+collide)

	r := AnalyseGate5(raw, 20, 0, 0, 0)

	if len(r.SharedGrams) == 0 {
		t.Fatal("a phrase repeated verbatim across two breaks produced no shared grams")
	}
	for _, g := range r.SharedGrams {
		if g.Count != 2 {
			t.Errorf("gram %q counted in %d breaks, want 2", g.Text, g.Count)
		}
	}
	if r.Sample != 20 {
		t.Errorf("Sample = %d, want 20", r.Sample)
	}
}

// A gram repeated INSIDE one break is a different fault and must not count as
// a collision BETWEEN breaks.
//
// The assertion is that SharedGrams is EMPTY. An earlier version checked
// `g.Count < 2`, which repeated() already guarantees can never happen -- so it
// passed whether the code counted per break or per occurrence, and proved
// nothing.
func TestGate5DoesNotCountInternalRepetition(t *testing.T) {
	raw := distinctBreaks(19)
	raw = append(raw, "Opzz. The same closing words again, the same closing words again.")

	r := AnalyseGate5(raw, 20, 0, 0, 0)
	if len(r.SharedGrams) != 0 {
		t.Errorf("one break repeating itself produced %d shared grams: %v",
			len(r.SharedGrams), r.SharedGrams)
	}
	if !r.Pass {
		t.Errorf("internal repetition failed the collision gate: %v", r.Failures)
	}
}

func TestGate5ReportsDropRate(t *testing.T) {
	r := AnalyseGate5(distinctBreaks(20), 20, 3, 0, 0)
	if r.DropRate < 0.149 || r.DropRate > 0.151 {
		t.Errorf("DropRate = %v, want 0.15", r.DropRate)
	}
	if r.Pass {
		t.Error("15%% drop rate passed a gate whose limit is 10%%")
	}
	if !containsAny(r.Failures, "(d)") {
		t.Errorf("failures do not name criterion (d): %v", r.Failures)
	}
	// The remedy matters: a validator dropping breaks is a PROMPT problem.
	if !containsAny(r.Failures, "fix the PROMPT") {
		t.Errorf("the (d) failure does not say where the fix belongs: %v", r.Failures)
	}
}

func TestGate5FailsGateOnHighCollision(t *testing.T) {
	raw := distinctBreaks(15)
	for i := 0; i < 5; i++ {
		raw = append(raw, "Number "+itoaLocal(i)+". The very same closing phrase every single time.")
	}

	r := AnalyseGate5(raw, 20, 0, 0, 0)
	if r.Pass {
		t.Fatal("five breaks sharing a phrase passed the collision gate")
	}
	if !containsAny(r.Failures, "(a)") {
		t.Errorf("failures do not name criterion (a): %v", r.Failures)
	}
	// The designed next step must be stated, not inferred.
	if !containsAny(r.Failures, "embeddings") {
		t.Errorf("the (a) failure does not name the escalation: %v", r.Failures)
	}
}

func TestGate5FailsOnReusedOpenings(t *testing.T) {
	raw := distinctBreaks(18)
	// The same opening words, different bodies. OpeningWords decides how much
	// counts as "the opening", so the shared prefix has to be at least that long.
	// OpeningWords counts CONTENT words with stopwords stripped, so the shared
	// prefix has to be that many real words -- not merely that many words.
	const shared = "Midnight radio broadcasting quietly through another restless evening."
	raw = append(raw, shared+" Trumpets.", shared+" Pianos.")

	r := AnalyseGate5(raw, 20, 0, 0, 0)
	if len(r.DuplicateOpenings) == 0 {
		t.Fatal("a reused opening was not detected")
	}
	if r.Pass {
		t.Error("reused openings passed the gate")
	}
}

// TestGate5FailsOnUngroundedFacts: (c) is the anti-hallucination criterion, and
// an unresolved fact means the DJ asserted something no dossier supports.
func TestGate5FailsOnUngroundedFacts(t *testing.T) {
	r := AnalyseGate5(distinctBreaks(20), 20, 0, 12, 11)
	if r.Pass {
		t.Fatal("an unresolved asserted fact passed the gate")
	}
	if !containsAny(r.Failures, "(c)") {
		t.Errorf("failures do not name criterion (c): %v", r.Failures)
	}

	if got := AnalyseGate5(distinctBreaks(20), 20, 0, 12, 12); !got.Pass {
		t.Errorf("a clean run failed: %v", got.Failures)
	}
}

// TestGate5PassesACleanRun. A gate that cannot pass is as useless as one that
// cannot fail.
func TestGate5PassesACleanRun(t *testing.T) {
	r := AnalyseGate5(distinctBreaks(20), 20, 1, 8, 8)
	if !r.Pass {
		t.Fatalf("a clean run failed: %v\n%s", r.Failures, r)
	}
	if !strings.Contains(r.String(), "PASS") {
		t.Errorf("report does not state the decision:\n%s", r)
	}
}

func containsAny(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

// TestGate5FactCooldownWithholdsRecentFacts. GATE 5 measured nine shared
// 4-grams across twenty raw breaks and EVERY one was an artist fact recited
// twice. The writer had no way to know it had already said them.
//
// Withheld from the SCHEMA rather than rejected afterwards, so repeating a
// fact is unrepresentable at the sampler -- the same move that makes an
// ungrounded fact impossible.
func TestGate5FactCooldownWithholdsRecentFacts(t *testing.T) {
	d := &enrich.Dossier{
		ArtistFacts: []string{
			"Linkin Park is an American rock band formed in 1996.",
			"They released Hybrid Theory in 2000.",
		},
		Confidence: enrich.ConfidenceHigh,
	}

	all := ResolvableFactIDs(d, d, d)
	if len(all) < 2 {
		t.Fatalf("fixture resolves %d fact ids, need at least 2", len(all))
	}

	v := &Validator{}
	v.rememberFacts([]string{all[0]})

	withheld := v.cooledFacts()
	if len(withheld) != 1 || withheld[0] != all[0] {
		t.Fatalf("cooledFacts = %v, want exactly %q", withheld, all[0])
	}

	schema := BreakSchemaExcluding(d, d, d, withheld)
	enum := factEnum(t, schema)
	for _, e := range enum {
		if e == all[0] {
			t.Errorf("the schema still offers %q inside its cooldown", all[0])
		}
	}
	if len(enum) != len(all)-1 {
		t.Errorf("enum has %d ids, want %d", len(enum), len(all)-1)
	}

	// Everything else must still be offered: withholding one fact must not
	// silence the DJ.
	if len(enum) == 0 {
		t.Error("withholding one fact left nothing assertable")
	}
}

// TestGate5FactCooldownExpires: a small library would otherwise run out of
// things to say permanently.
func TestGate5FactCooldownExpires(t *testing.T) {
	v := &Validator{}
	v.rememberFacts([]string{"cur.artist_facts[0]"})
	if len(v.cooledFacts()) != 1 {
		t.Fatal("a just-used fact is not in cooldown")
	}

	v.mu.Lock()
	v.breaks = FactCooldown
	v.mu.Unlock()

	if got := v.cooledFacts(); len(got) != 0 {
		t.Errorf("after %d breaks the fact is still withheld: %v", FactCooldown, got)
	}
}

// factEnum pulls the asserted_facts enum out of a schema.
func factEnum(t *testing.T, schema map[string]any) []string {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	facts, ok := props["asserted_facts"].(map[string]any)
	if !ok {
		t.Fatal("schema has no asserted_facts")
	}
	items, ok := facts["items"].(map[string]any)
	if !ok {
		return nil
	}
	raw, _ := items["enum"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

// TestGate5ProperNounsAreNotCollisions. After the fact cooldown removed the
// recited-fact collisions, EVERY remaining one GATE 5 found was a proper noun:
// consecutive breaks share tracks by design, so a DJ that says what is coming
// and then says what just played repeats the artist and title every time.
//
// Naming the record is the job. The index exists to catch reused PHRASING.
func TestGate5ProperNounsAreNotCollisions(t *testing.T) {
	s := saidStore(t)
	names := []string{"Parokya ni Edgar", "Wag Mo Na Sana"}

	first := "Coming up, Parokya ni Edgar with Wag Mo Na Sana."
	if err := s.Record(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	// The same names, different phrasing around them, must NOT collide.
	second := "That was Parokya ni Edgar, Wag Mo Na Sana, and it still lands."
	hit, gram, err := s.CheckCollisionIgnoring(context.Background(), second, names)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Errorf("naming the same record collided on %q; the DJ cannot say what is playing", gram)
	}

	// Without the excusal it DOES collide, which is what GATE 5 measured.
	if hit, _, err := s.CheckCollision(context.Background(), second); err != nil {
		t.Fatal(err)
	} else if !hit {
		t.Error("the unexcused check found no collision; this test would prove nothing")
	}
}

// TestGate5PhrasingStillCollidesAroundNames: a gram mixing a name with ordinary
// words is phrasing, and must still be caught. Excusing whole grams only.
func TestGate5PhrasingStillCollidesAroundNames(t *testing.T) {
	s := saidStore(t)
	names := []string{"Oasis"}

	// Exactly ONE 4-gram, and it mixes the name with ordinary words. A longer
	// line would carry other grams that collide anyway, so the test would pass
	// whether the rule excused whole grams or merely any gram touching a name.
	line := "Oasis absolutely defined that decade."
	if err := s.Record(context.Background(), line); err != nil {
		t.Fatal(err)
	}

	hit, gram, err := s.CheckCollisionIgnoring(context.Background(), line, names)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("a whole sentence reused verbatim was excused as a proper noun")
	}
	if gram == "" {
		t.Error("collision reported without naming the phrase")
	}
}
