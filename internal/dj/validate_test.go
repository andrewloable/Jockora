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

func (w *scriptedWriter) WriteBreak(ctx context.Context, prompt string, schema map[string]any, _ int) (string, error) {
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

// TestValidateCollisionDropsTheBreak: a rejected break buys one more go.
//
// The budget went to one when the second generation was being spent rewriting
// breaks that came out too long, and came back to two when it was measured what
// the CONTENT checks reject: three of four real generations against the
// deployment model were caught as loops, echoes or recitations, and with a
// budget of one every catch was also a lost break. The name is kept because the
// second failure still drops it -- see the end of this test.
func TestValidateCollisionDropsTheBreak(t *testing.T) {
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

	b, err := v.Generate(ctx, "prompt", 0, nil, nil, nil)
	if err != nil {
		t.Fatalf("the retry did not save the break: %v", err)
	}
	if !strings.Contains(b.Text(), "saxophones") {
		t.Errorf("aired %q, want the second attempt", b.Text())
	}
	if w.calls != 2 {
		t.Errorf("%d generate calls, want 2: a collision has to cost a retry, not the break", w.calls)
	}

	// AND THE BUDGET IS TWO, NOT UNLIMITED. A model that collides twice is not
	// having a bad roll, and the music playing is the designed outcome.
	again := &scriptedWriter{replies: []string{
		breakJSON("They cut it in a converted chapel outside Bristol during a heatwave."),
		breakJSON("They cut it in a converted chapel outside Bristol in the winter."),
		breakJSON("A different sentence entirely, mentioning saxophones and cold mornings."),
	}}
	v.Writer = again
	if _, err := v.Generate(ctx, "prompt", 0, nil, nil, nil); !errors.Is(err, ErrBreakRepetitive) {
		t.Fatalf("Generate returned %v, want ErrBreakRepetitive", err)
	}
	if again.calls != 2 {
		t.Errorf("%d generate calls, want exactly 2", again.calls)
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

	b, err := v.Generate(ctx, "prompt", 0, nil, nil, nil)
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

// TestValidateUngroundedKeepsTheBreakAndDropsTheClaim. It used to be fatal, on
// the reasoning that the schema enum makes an ungrounded id unrepresentable --
// true of a grammar-constrained backend and not of a hosted one. Measured live:
// the model declared the fact TEXT and every fact-bearing break was lost.
func TestValidateUngroundedKeepsTheBreakAndDropsTheClaim(t *testing.T) {
	v := validator(t, nil)
	cur := dossierWith(enrich.ConfidenceHigh, "The band formed in 1980.")

	w := &scriptedWriter{replies: []string{
		breakJSON("They won a Grammy, apparently.", "cur.grammy_wins"),
		breakJSON("Something clean and grounded about a band.", "cur.artist_facts[0]"),
	}}
	v.Writer = w

	b, err := v.Generate(context.Background(), "prompt", 0, nil, cur, nil)
	if err != nil {
		t.Fatalf("a break was lost over its declaration: %v", err)
	}
	if len(b.AssertedFacts) != 0 {
		t.Errorf("AssertedFacts = %v, want the unresolvable one removed", b.AssertedFacts)
	}
	if w.calls != 1 {
		t.Errorf("%d generate calls, want 1", w.calls)
	}
	// Counted, so the console can show that the model is mislabelling.
	if got := v.Stats().Ungrounded; got != 1 {
		t.Errorf("Ungrounded = %d, want 1", got)
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

	// The clean one FIRST now: with a single generation there is no second
	// chance, and what this test is about is that a rejected break is never
	// recorded -- so the rejected text is put into the index up front and the
	// break that airs is checked against it.
	v.Writer = &scriptedWriter{replies: []string{breakJSON(accepted)}}
	if _, err := v.Generate(ctx, "prompt", 0, nil, nil, nil); err != nil {
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
			if _, err := v.Generate(ctx, "p", 0, nil, nil, nil); !errors.Is(err, ErrBreakRepetitive) {
				t.Fatalf("break %d: err = %v, want a drop", i, err)
			}
			continue
		}

		v.Writer = &scriptedWriter{replies: []string{breakJSON(line)}}
		if _, err := v.Generate(ctx, "p", 0, nil, nil, nil); err != nil {
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

	if _, err := v.Generate(context.Background(), "p", 0, nil, nil, nil); err == nil {
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

	_, err := v.Generate(context.Background(), "prompt", 0, nil, nil, nil)
	if !errors.Is(err, ErrBreakRepetitive) {
		t.Fatalf("a collision with seeded row 1, 199 rows back, was not caught: %v\n"+
			"the validator and the prompt are looking at the same narrow window", err)
	}
}

// TestGenerateNamesTheRealDropReason: reporting a malformed response as
// repetition points a reader at the wrong half of the system -- a caller
// checking errors.Is(err, ErrBreakRepetitive) would treat a broken model as a
// chatty one.
func TestGenerateNamesTheRealDropReason(t *testing.T) {
	v := &Validator{
		Writer: &scriptedWriter{replies: []string{"not json at all", "still not json"}},
		Said:   saidStore(t),
	}
	_, err := v.Generate(context.Background(), "prompt", 0, nil, nil, nil)
	if err == nil {
		t.Fatal("accepted two malformed responses")
	}
	if errors.Is(err, ErrBreakRepetitive) {
		t.Errorf("error = %v, reported as repetition when nothing was repeated", err)
	}
	if !errors.Is(err, ErrBreakDropped) {
		t.Errorf("error = %v, want ErrBreakDropped", err)
	}
	if !strings.Contains(err.Error(), string(DropBadJSON)) {
		t.Errorf("error = %v, want it to name %q", err, DropBadJSON)
	}
}

// TestBreakSchemaForbidsAnEmptyBody: required-but-empty is what the schema
// allowed, and the model took that option on 12 of 35 live attempts.
func TestBreakSchemaForbidsAnEmptyBody(t *testing.T) {
	schema := BreakSchema(nil, nil, nil)
	props, _ := schema["properties"].(map[string]any)
	body, _ := props["body"].(map[string]any)
	if body["minLength"] != 1 {
		t.Errorf("body minLength = %v, want 1: without it an empty break is schema-valid", body["minLength"])
	}
}

// TestGenerateRejectsInstructionEcho: the said-lines index catches the SECOND
// occurrence as repetition, which means the first one airs. Measured live, this
// was the single largest source of drops -- and every one of those would have
// been a listener hearing the prompt read out in a DJ voice.
func TestGenerateRejectsInstructionEcho(t *testing.T) {
	for _, text := range []string{
		"State only facts listed above.",
		"I will state only the facts listed above and not invent anything.",
		"Return only the JSON object.",
	} {
		if _, echoed := echoesInstructions(text, nil); !echoed {
			t.Errorf("echoesInstructions(%q) = false, want true", text)
		}
	}
	for _, text := range []string{
		"That was Linkin Park, and the state of things is about to get louder.",
		"Three in the morning and nobody is listening but you.",
	} {
		if phrase, echoed := echoesInstructions(text, nil); echoed {
			t.Errorf("echoesInstructions(%q) matched %q on a real break", text, phrase)
		}
	}
}

// TestGenerateRejectsModelScaffolding. A break that is chat-template tokens
// rather than English aired in a live run -- "}<tool_call|>```json" -- because
// every existing check passed: it parsed, asserted no facts, collided with
// nothing, and was the right length.
func TestGenerateRejectsModelScaffolding(t *testing.T) {
	for _, text := range []string{
		"}<tool_call|>```json }<tool_call|>```json",
		"```json {\"opening\": \"hi\"}",
		"<start_of_turn>model",
	} {
		if _, echoed := echoesInstructions(text, nil); !echoed {
			t.Errorf("echoesInstructions(%q) = false; this would have aired", text)
		}
	}
}

// TestGenerateRejectsTemplatePlaceholders. Every string here AIRED in a live
// run: the model was filling a form rather than talking, and a placeholder is
// grammatical, the right length, and collides with nothing.
func TestGenerateRejectsTemplatePlaceholders(t *testing.T) {
	for _, text := range []string{
		"Tuning in now. Your radio break sentence goes here. INTRO",
		"Midnight here... TEXT OF YOUR RADIO BREAK HERE.",
		"The song just ended. Your spoken radio break goes here. It must fit within 30 words.",
		"Here's what's coming up. Your spoken word here.",
		"This track has just started. Your spoken dialogue here.",
	} {
		if _, echoed := echoesInstructions(text, nil); !echoed {
			t.Errorf("echoesInstructions(%q) = false; this aired", text)
		}
	}

	// Real breaks, from the same run, must survive.
	for _, text := range []string{
		"Over the last song, we looked at the struggle to maintain a facade and the desire for honesty.",
		"It's been a heavy one, but the next track is a powerful statement of defiance. Let's hear them hit the floor.",
		"Three in the morning is the only honest hour.",
	} {
		if phrase, echoed := echoesInstructions(text, nil); echoed {
			t.Errorf("echoesInstructions(%q) matched %q on a real break", text, phrase)
		}
	}
}

// TestGenerateRejectsPlaceholdersStructurally. A phrase list lost this fight:
// blocking "your radio break sentence goes here" produced "YOUR SPEECH HERE",
// then "YOUR 30 WORDS HERE", then "YOUR LINES HERE". Every string below aired
// in a live run.
func TestGenerateRejectsPlaceholdersStructurally(t *testing.T) {
	aired := []string{
		"Welcome to Midnight, YOUR SPEECH HERE Linkin Park",
		"Now, as we prepare to stand up... YOUR TEXT HERE Linkin Park, 'Hit The Floor'",
		"Linkin Park formed in 1996. YOUR 30 WORDS HERE. The next track is...",
		"next.genre YOUR LINES HERE next.track_name",
		"Good morning. YOUR SPANISH HERE Next up, a band with a history.",
		"NEXT UP YOUR AUDIO HERE INTRO",
		"THANK YOU FOR LISTENING TO YOUR SPEECH HERE NEXT TRACK STARTS",
		"your 30 words go here",
	}
	for _, text := range aired {
		if _, echoed := echoesInstructions(text, nil); !echoed {
			t.Errorf("echoesInstructions(%q) = false; this aired", text)
		}
	}

	// Real breaks from the same runs must survive, including ones that shout a
	// little or mention the word "here".
	real := []string{
		"Next up, a band that's been making waves since 1996, Linkin Park, with a track that delves into the shadows of past regrets.",
		"And now, as we try to untangle the threads of our past, Linkin Park with a song about finding your way out of the chaos.",
		"Three in the morning is the only honest hour. Stay here.",
		"That was AC DC, and yes it was as loud as you remember.",
		"OK. Here is something quieter.",
	}
	for _, text := range real {
		if phrase, echoed := echoesInstructions(text, nil); echoed {
			t.Errorf("echoesInstructions(%q) matched %q on a real break", text, phrase)
		}
	}
}

// TestValidateRecitedDossierIsCaught: Jockora-ey4's sibling. The writer is now
// pointed at the subject summary, so the sentence it is most likely to copy is
// the subject summary.
func TestValidateRecitedDossierIsCaught(t *testing.T) {
	d := &enrich.Dossier{
		Confidence:     enrich.ConfidenceHigh,
		SubjectSummary: "Someone counts the hours until a shift ends and admits they have nowhere to be afterwards.",
		Themes:         []string{"work", "loneliness"},
	}

	// MEASURED LIVE, 2026-09-10, and this is the handoff verbatim: the model
	// was given the summary and read it out.
	recited := "The next track is Clocking Off by Shift Work. " +
		"Someone counts the hours until a shift ends and admits they have nowhere to be afterwards."
	phrase, caught := recitesDossier(recited, []string{"Shift Work", "Clocking Off"}, nil, d, nil)
	if !caught {
		t.Fatalf("a break that reads the summary out was not caught\ntext: %q", recited)
	}
	if phrase == "" {
		t.Error("nothing was named as the copied run, so a log line cannot say what happened")
	}

	// A BREAK THAT IS ABOUT THE SAME THING IN ITS OWN WORDS IS THE POINT.
	// A threshold that punished overlap would forbid exactly what the summary
	// is in the prompt for, so this must pass.
	own := "Next up, somebody watching the clock on a late shift with nobody waiting at the other end of it."
	if phrase, caught := recitesDossier(own, nil, nil, d, nil); caught {
		t.Errorf("a paraphrase was called a recitation: %q", phrase)
	}

	// Nothing to copy from is not a recitation.
	if _, caught := recitesDossier(recited, nil, nil, nil, nil); caught {
		t.Error("a break was called a recitation with no dossier to recite")
	}
	if _, caught := recitesDossier("", nil, nil, d, nil); caught {
		t.Error("an empty break was called a recitation")
	}
	// AND A DOSSIER WITH NO PROSE IN IT. Half this library has no summary and
	// no themes, and comparing against nothing must not become comparing
	// against everything.
	bare := &enrich.Dossier{Confidence: enrich.ConfidenceHigh, Release: "Nightdrive, 1984"}
	if _, caught := recitesDossier(recited, nil, nil, bare, nil); caught {
		t.Error("a dossier with no meaning produced a recitation hit")
	}
}

// TestValidateRecitedBreakIsDropped: catching a recitation is only half of it.
// The check has to be WIRED, and a mutation that removed it from Generate
// survived the whole suite until this existed.
func TestValidateRecitedBreakCostsARetryThenTheBreak(t *testing.T) {
	d := &enrich.Dossier{
		Confidence:     enrich.ConfidenceHigh,
		SubjectSummary: "Someone counts the hours until a shift ends and admits they have nowhere to be afterwards.",
	}

	// THE RECITATION COSTS A GENERATION, NOT THE BREAK. That is the whole
	// reason MaxAttempts went back to two: catching a recitation is only worth
	// something if the writer gets to try again while there is still time.
	w := &scriptedWriter{replies: []string{
		breakJSON("Someone counts the hours until a shift ends and admits they have nowhere to be afterwards."),
		breakJSON("Somebody watching the clock on a late one, with nobody waiting at the far end of it."),
	}}
	v := validator(t, w)

	b, err := v.Generate(context.Background(), "prompt", 40, nil, d, nil)
	if err != nil {
		t.Fatalf("the retry did not save the break: %v", err)
	}
	if strings.Contains(b.Text(), "nowhere to be afterwards") {
		t.Error("the recited break aired; the summary goes out as a description read " +
			"aloud, and the said-lines index then refuses it the next time the track " +
			"comes round")
	}
	if w.calls != 2 {
		t.Errorf("the writer was called %d times, want 2: the recitation costs a retry", w.calls)
	}

	// TWICE IS A DROP. The reason is what the console counts.
	twice := &scriptedWriter{replies: []string{
		breakJSON("Someone counts the hours until a shift ends and admits they have nowhere to be afterwards."),
		breakJSON("Someone counts the hours until a shift ends and admits they have nowhere to be afterwards, again."),
	}}
	if _, err := validator(t, twice).Generate(context.Background(), "prompt", 40, nil, d, nil); err == nil {
		t.Error("a break that recited twice still aired")
	} else if !strings.Contains(err.Error(), string(DropRecited)) {
		t.Errorf("dropped for %v, want %s", err, DropRecited)
	}

	// AND A BREAK IN ITS OWN WORDS ABOUT THE SAME THING STILL AIRS. A check
	// that punished overlap would forbid exactly what the summary is in the
	// prompt for.
	own := &scriptedWriter{replies: []string{
		breakJSON("Somebody watching the clock on a late one, with nobody waiting at the far end of it."),
	}}
	if _, err := validator(t, own).Generate(context.Background(), "prompt", 40, nil, d, nil); err != nil {
		t.Errorf("a paraphrase was dropped as a recitation: %v", err)
	}
}
