// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

// scriptedWriter returns canned responses in order.
type scriptedWriter struct {
	replies []string
	errs    []error
	calls   int
}

func (w *scriptedWriter) WriteBreak(ctx context.Context, prompt string, schema map[string]any) (string, error) {
	i := w.calls
	w.calls++
	if i < len(w.errs) && w.errs[i] != nil {
		return "", w.errs[i]
	}
	if i < len(w.replies) {
		return w.replies[i], nil
	}
	return "", fmt.Errorf("scriptedWriter: no reply %d", i)
}

func breakJSON(body string, facts ...string) string {
	f := "[]"
	if len(facts) > 0 {
		f = `["` + strings.Join(facts, `","`) + `"]`
	}
	return fmt.Sprintf(`{"opening":"","body":%q,"handoff":"","asserted_facts":%s}`, body, f)
}

func validator(t *testing.T, w Writer) *Validator {
	t.Helper()
	return &Validator{Writer: w, Said: saidStore(t)}
}

func TestValidateCollisionTriggersOneRegeneration(t *testing.T) {
	v := validator(t, nil)
	ctx := context.Background()
	if err := v.Said.Record(ctx, "They cut it in a converted chapel outside Bristol."); err != nil {
		t.Fatal(err)
	}

	w := &scriptedWriter{replies: []string{
		breakJSON("They cut it in a converted chapel outside Bristol during a heatwave."),
		breakJSON("A different sentence entirely, mentioning saxophones and cold mornings."),
	}}
	v.Writer = w

	b, err := v.Generate(ctx, "prompt", nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if w.calls != 2 {
		t.Errorf("%d generate calls, want exactly 2", w.calls)
	}
	if !strings.Contains(b.Text(), "saxophones") {
		t.Errorf("returned break = %q, want the clean one", b.Text())
	}
}

func TestValidateSecondCollisionDropsBreak(t *testing.T) {
	v := validator(t, nil)
	ctx := context.Background()
	if err := v.Said.Record(ctx, "They cut it in a converted chapel outside Bristol."); err != nil {
		t.Fatal(err)
	}

	w := &scriptedWriter{replies: []string{
		breakJSON("They cut it in a converted chapel outside Bristol during a heatwave."),
		breakJSON("Also cut in a converted chapel outside Bristol, would you believe."),
	}}
	v.Writer = w

	b, err := v.Generate(ctx, "prompt", nil, nil, nil)
	if !errors.Is(err, ErrBreakRepetitive) {
		t.Fatalf("err = %v, want ErrBreakRepetitive", err)
	}
	if b != nil {
		t.Errorf("a break was returned despite the drop: %q", b.Text())
	}
	if w.calls != MaxAttempts {
		t.Errorf("%d generate calls, want exactly %d: a third pushes past the lookahead budget", w.calls, MaxAttempts)
	}
}

func TestValidateUngroundedTriggersRegeneration(t *testing.T) {
	v := validator(t, nil)
	cur := dossierWith(enrich.ConfidenceHigh, "The band formed in 1980.")

	w := &scriptedWriter{replies: []string{
		breakJSON("They won a Grammy, apparently.", "cur.grammy_wins"),
		breakJSON("Something clean and grounded about a band.", "cur.artist_facts[0]"),
	}}
	v.Writer = w

	b, err := v.Generate(context.Background(), "prompt", nil, cur, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if w.calls != 2 {
		t.Errorf("%d generate calls, want 2", w.calls)
	}
	if len(b.AssertedFacts) != 1 || b.AssertedFacts[0] != "cur.artist_facts[0]" {
		t.Errorf("AssertedFacts = %v", b.AssertedFacts)
	}
}

// TestValidateCleanBreakIsRecordedOnce: a rejected break must NEVER be recorded,
// or the index is poisoned against future good breaks that reuse a phrase the DJ
// never actually aired.
func TestValidateCleanBreakIsRecorded(t *testing.T) {
	v := validator(t, nil)
	ctx := context.Background()

	rejected := "They cut it in a converted chapel outside Bristol during a heatwave."
	accepted := "A different sentence entirely, mentioning saxophones and cold mornings."
	if err := v.Said.Record(ctx, rejected); err != nil {
		t.Fatal(err)
	}

	v.Writer = &scriptedWriter{replies: []string{breakJSON(rejected), breakJSON(accepted)}}
	if _, err := v.Generate(ctx, "prompt", nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := v.Said.Store.DB().QueryRow(
		`SELECT count(*) FROM said_lines WHERE jock_id = ? AND text LIKE ?`,
		v.Said.JockID, "%saxophones%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the accepted break was recorded %d times, want once", n)
	}

	// The rejected one was recorded exactly once: by the test's own seeding,
	// never by the validator.
	if err := v.Said.Store.DB().QueryRow(
		`SELECT count(*) FROM said_lines WHERE jock_id = ? AND text LIKE ?`,
		v.Said.JockID, "%converted chapel%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the rejected break appears %d times; the validator recorded a reject", n)
	}
}

func TestValidateDropRateIsCounted(t *testing.T) {
	v := validator(t, nil)
	ctx := context.Background()

	// Every line uses a distinct set of content words, so the only collisions
	// are the ones deliberately seeded. Lines built from a shared template would
	// collide with EACH OTHER on the template's own words.
	nouns := []string{"lighthouse", "quarry", "ferry", "kiln", "arcade", "foundry",
		"tramline", "cannery", "boathouse", "reservoir", "aerodrome", "brewery",
		"observatory", "dockyard", "glasshouse", "icehouse", "junkyard", "warehouse",
		"chapel", "carpark"}
	verbs := []string{"rattles", "hums", "drips", "creaks", "glows", "shivers",
		"echoes", "flickers", "groans", "settles", "hisses", "sways", "tilts",
		"drifts", "clatters", "murmurs", "pulses", "ripples", "aches", "leans"}

	// 20 requested breaks, 3 of which collide twice and are dropped.
	for i := 0; i < 20; i++ {
		line := fmt.Sprintf("%s %s while somebody counts %d empty %s crates.",
			capitalise(nouns[i]), verbs[i], i+3, nouns[(i+5)%len(nouns)])

		if i%7 == 0 { // i = 0, 7, 14 -> three drops
			if err := v.Said.Record(ctx, line); err != nil {
				t.Fatal(err)
			}
			v.Writer = &scriptedWriter{replies: []string{breakJSON(line), breakJSON(line)}}
			if _, err := v.Generate(ctx, "p", nil, nil, nil); !errors.Is(err, ErrBreakRepetitive) {
				t.Fatalf("break %d: err = %v, want a drop", i, err)
			}
			continue
		}

		v.Writer = &scriptedWriter{replies: []string{breakJSON(line)}}
		if _, err := v.Generate(ctx, "p", nil, nil, nil); err != nil {
			t.Fatalf("break %d (%q): %v", i, line, err)
		}
	}

	if got := v.DropRate(); got != 0.15 {
		t.Errorf("DropRate() = %v, want 0.15 (3 drops in 20)", got)
	}
	s := v.Stats()
	if s.Drops != 3 || s.Breaks != 17 {
		t.Errorf("Stats = %+v, want 3 drops and 17 breaks", s)
	}
	if s.Reasons[DropRepetitive] != 3 {
		t.Errorf("repetitive drops = %d, want 3", s.Reasons[DropRepetitive])
	}
}

func TestValidateLLMErrorIsADropNotAPanic(t *testing.T) {
	v := validator(t, &scriptedWriter{errs: []error{
		errors.New("connection refused"), errors.New("connection refused")}})

	if _, err := v.Generate(context.Background(), "p", nil, nil, nil); err == nil {
		t.Fatal("an unreachable model produced no error")
	}
	if got := v.Stats().Reasons[DropLLMError]; got != 1 {
		t.Errorf("llm_error drops = %d, want 1", got)
	}
}

// --- golden cases over a committed seed table ---

// capitalise upper-cases the first letter. strings.Title is deprecated and
// applies to every word, which is not what is wanted here.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func seedFromTestdata(t *testing.T, sl *SaidLines) []string {
	t.Helper()
	f, err := os.Open("testdata/said_lines_seed.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := sl.Record(context.Background(), line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 200 {
		t.Fatalf("seed table has %d rows, want 200", len(lines))
	}
	return lines
}

func TestValidateGoldenKnownCollisionRejected(t *testing.T) {
	v := validator(t, nil)
	lines := seedFromTestdata(t, v.Said)

	// A script sharing a content-word run with seeded row 7.
	row7 := lines[6]
	hit, gram, err := v.Said.CheckCollision(context.Background(), "Funny thing: "+row7)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatalf("a script reusing seeded row 7 was not rejected\n  row 7: %q", row7)
	}
	t.Logf("rejected on gram %q", gram)
}

func TestValidateGoldenKnownCleanPasses(t *testing.T) {
	v := validator(t, nil)
	seedFromTestdata(t, v.Said)

	// Shares only stopwords with every seeded row.
	clean := "It is one of the best things that has been on in a while."
	hit, gram, err := v.Said.CheckCollision(context.Background(), clean)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Errorf("a script sharing only function words was rejected on %q", gram)
	}
}

// TestValidateGoldenCollisionOutsideInjectionWindow is the one that proves the
// validator checks the FULL table rather than only the prohibitions it showed
// the model. Row 1 is far outside any last-20 or last-100 window.
func TestValidateGoldenCollisionOutsideInjectionWindow(t *testing.T) {
	v := validator(t, nil)
	lines := seedFromTestdata(t, v.Said)
	row1 := lines[0]

	w := &scriptedWriter{replies: []string{
		breakJSON("Here is a thought. " + row1),
		breakJSON("Here is a thought. " + row1),
	}}
	v.Writer = w

	_, err := v.Generate(context.Background(), "prompt", nil, nil, nil)
	if !errors.Is(err, ErrBreakRepetitive) {
		t.Fatalf("a collision with seeded row 1, 199 rows back, was not caught: %v\n"+
			"the validator and the prompt are looking at the same narrow window", err)
	}
}
