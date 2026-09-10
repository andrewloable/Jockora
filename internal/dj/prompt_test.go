// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/say"
)

// fourDigitYearRE is 1900-2099, the range a record could plausibly carry.
var fourDigitYearRE = regexp.MustCompile(`\b(19|20)[0-9]{2}\b`)

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

func TestPromptTradesTriviaForMeaning(t *testing.T) {
	// THE WRITER TALKS ABOUT WHAT IS ON THE TABLE, and the table was three
	// parts trivia to one part meaning. Measured on the reporting library:
	// artist_facts on 87.6% of dossiers, release on 82.7%, subject_summary on
	// 49.5% -- and the operator reported the DJ saying the year, the country
	// and the album over and over.
	//
	// So a track whose meaning is known offers ONE fact, and one whose meaning
	// is not offers the old three, because there it is all the writer has.
	meaning := testDossier(10)
	if got, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t), Current: meaning, WindowSeconds: 9,
	}); err != nil {
		t.Fatal(err)
	} else if n := strings.Count(got, "  fact ["); n != 0 {
		t.Errorf("%d artist facts offered beside a subject summary, want none: "+
			"the MusicBrainz record holds Country, Type and BeginYear and "+
			"nothing else, so any budget above zero is spent on the year or "+
			"the country the operator asked the DJ to stop saying", n)
	} else if strings.Contains(got, "release [cur") {
		t.Error("the release was offered beside a subject summary; it is " +
			"\"Album, Year\" and it is the easiest line here to recite")
	}

	blank := testDossier(10)
	blank.SubjectSummary = ""
	blank.Themes = nil
	got, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t), Current: blank, WindowSeconds: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, "  fact ["); n != MaxFactsInPrompt {
		t.Errorf("%d facts offered with no meaning to offer instead, want %d", n, MaxFactsInPrompt)
	}
	// AND THE RECORD DETAILS COME BACK, because here they are all there is.
	// writeDossier's own measurement says a writer with less to say pads until
	// it hits the token budget and the break is truncated away entirely, so
	// withholding these from a dossier with no meaning would cost breaks.
	blank.Release = "Nightdrive, 1984"
	withRelease, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t), Current: blank, WindowSeconds: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withRelease, "release [cur.release]: Nightdrive, 1984") {
		t.Error("a dossier with no meaning was denied its release too, which " +
			"leaves the writer padding to the token budget")
	}
	for i := MaxFactsInPrompt; i < 10; i++ {
		if strings.Contains(got, fmt.Sprintf("Distinct fact number %d ", i)) {
			t.Errorf("fact %d leaked into the prompt", i)
		}
	}
}

func TestPromptShowsThemes(t *testing.T) {
	// COLLECTED AND NEVER SHOWN, on 49.8% of the library. The enricher filled
	// them, port.go sanitised them, the store kept them, and no break had ever
	// seen one -- nothing printed them and no id resolved to them. This is the
	// cheapest meaning available because it was already paid for.
	d := testDossier(3)
	d.Themes = []string{"leaving", "insomnia"}
	got, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t), Current: d, WindowSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "themes [cur.themes]: leaving, insomnia") {
		t.Errorf("themes are not in the prompt:\n%s", got)
	}
}

func TestPromptPutsMeaningAheadOfTheRecordDetails(t *testing.T) {
	// THE ALBUM AND THE YEAR ARE THE EASIEST THING TO TURN INTO A SENTENCE, and
	// a writer that meets them before it has read what the song is about says
	// them first and then pads. Release used to be the second line; it is now
	// the last.
	d := testDossier(3)
	d.Themes = []string{"leaving"}
	d.Release = "Nightdrive, 1984"
	got, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t), Current: d, WindowSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	about, themes := strings.Index(got, "about [cur"), strings.Index(got, "themes [cur")
	if about < 0 || themes < 0 {
		t.Fatalf("a meaning line is missing: about=%d themes=%d", about, themes)
	}
	if about > themes {
		t.Errorf("order is about=%d themes=%d; what the song is about leads", about, themes)
	}
	// AND THE RECORD DETAILS ARE NOT THERE AT ALL. They were second in the
	// list before this; now a dossier that knows what the song is about does
	// not offer the album, the year or the artist's country.
	for _, gone := range []string{"release [cur", "  fact ["} {
		if strings.Contains(got, gone) {
			t.Errorf("%q is still offered beside the meaning:\n%s", gone, got)
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

// Jockora-3dp. THE MODEL COPIES THE FORM IT IS SHOWN. A year in digits comes
// back either as digits for the TTS to mangle or as the model's own bad
// expansion -- "two thousand and seven" is already on record in llmwriter.go.
//
// This is the "asks" half of the house rule at validate.go: Jockora-hm2
// normalises at render time and is the floor, catching whatever arrives
// including years out of dossiers written long ago. This one reduces how often
// anything needs catching. It also fixes the break TEXT, which is what lands in
// said_lines and in /now.json -- where no TTS normaliser can reach.

// yearPrompt builds a sleeve-only prompt for a track from the given year.
//
// NO DOSSIER, because the sleeve line is the only place a year is formatted
// into a break prompt, and it renders only where there is nothing better.
func yearPrompt(t *testing.T, year int) string {
	t.Helper()
	got, err := BuildBreakPrompt(PromptInput{
		Persona:       testPersona(t),
		CurrentArtist: "The Midnight Signal",
		CurrentTitle:  "Long Way Out",
		CurrentAlbum:  "Long Way Out",
		CurrentYear:   year,
		Placement:     "ramp",
		WindowSeconds: 9,
	})
	if err != nil {
		t.Fatalf("BuildBreakPrompt: %v", err)
	}
	return got
}

func TestPromptYearWords(t *testing.T) {
	got := yearPrompt(t, 1993)
	if !strings.Contains(got, "nineteen ninety three") {
		t.Errorf("the prompt does not say the year the way a person does:\n%s", got)
	}
	if strings.Contains(got, "1993") {
		t.Errorf("the prompt still shows the digits, which is the form we do not want:\n%s", got)
	}
}

// TestPromptYearNoDigitExamples is the test that keeps this from being undone
// by a helpful example six months from now. THE EXAMPLES IN A PROMPT ARE
// FOLLOWED FAR MORE RELIABLY THAN THE INSTRUCTIONS, so one carrying "1993"
// would quietly teach the model the digits again.
//
// The fixture carries no year in any DATA field, so anything this finds came
// from the prompt's own scaffolding. Dossier text is deliberately left out of
// that guarantee: enrichment stores "released in 1993" forever and by decision
// is not being changed, which is exactly why the render-time normaliser stays.
func TestPromptYearNoDigitExamples(t *testing.T) {
	for _, year := range []int{0, 1993, 2007, 2026} {
		got := yearPrompt(t, year)
		for _, m := range fourDigitYearRE.FindAllString(got, -1) {
			t.Errorf("a four-digit year %q survives in a prompt built for %d; "+
				"an example in digits undoes the whole change:\n%s", m, year, got)
		}
	}

	// AND THE FULLY-BUILT PROMPT, not only the sleeve path: persona,
	// both dossiers, the prohibition lists, the placement guidance and the
	// schema. An example in digits could be anywhere in that scaffolding, and
	// the sleeve-only fixture would never see it. Every field the fixture
	// supplies is year-free, so anything found here is the prompt's own.
	full, err := BuildBreakPrompt(PromptInput{
		Persona:       testPersona(t),
		Previous:      testDossier(2),
		Current:       testDossier(3),
		Next:          testDossier(3),
		CurrentArtist: "The Midnight Signal",
		CurrentTitle:  "Long Way Out",
		CurrentAlbum:  "Long Way Out",
		CurrentYear:   1993,
		NextArtist:    "Corvi",
		NextTitle:     "Second Signal",
		NextAlbum:     "Second Signal",
		NextYear:      2007,
		Prohibitions:  prohibitions(20, 100),
		Placement:     "ramp",
		WindowSeconds: 9,
		Schema:        map[string]any{"type": "object"},
		IsColdOpen:    true,
	})
	if err != nil {
		t.Fatalf("BuildBreakPrompt: %v", err)
	}
	for _, m := range fourDigitYearRE.FindAllString(full, -1) {
		t.Errorf("a four-digit year %q survives in a fully-built prompt:\n%s", m, full)
	}
}

func TestPromptYearZero(t *testing.T) {
	got := yearPrompt(t, 0)
	// The guard at the sleeve line is year > 0 and must stay: a track with no
	// year has no year clause, and certainly not "(zero)".
	for _, bad := range []string{"(zero)", "(0)", "( )", "()"} {
		if strings.Contains(got, bad) {
			t.Errorf("a track with no year produced %q:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "Long Way Out") {
		t.Errorf("the album line is gone entirely:\n%s", got)
	}
}

// TestPromptYearUsesSay asserts against the FUNCTION, never a hardcoded string.
// Two spellings of the same year is how the said-lines index starts treating
// one break as two, so the prompt and the normaliser must not be able to drift.
func TestPromptYearUsesSay(t *testing.T) {
	for _, year := range []int{1993, 2007, 2000, 1900, 2026} {
		want := say.YearWords(year)
		if got := yearPrompt(t, year); !strings.Contains(got, want) {
			t.Errorf("year %d: the prompt does not contain say.YearWords(%d) = %q:\n%s",
				year, year, want, got)
		}
	}
}

func TestPromptPointsTheWriterAtTheMeaning(t *testing.T) {
	// MEASURED LIVE BEFORE THIS LINE EXISTED. Given the about and themes lines
	// and nothing else, the deployment's model returned "The track is ending."
	// and "The next track is coming up." -- it had the meaning and did not use
	// it, because nothing in several hundred words of instruction said to.
	// With the line, the same model and the same dossier produced a handoff
	// about somebody counting the hours until a shift ends.
	d := testDossier(0)
	d.Themes = []string{"leaving"}
	got, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t), Current: d, WindowSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "TALK ABOUT WHAT THE SONG IS ABOUT") {
		t.Errorf("nothing points the writer at the meaning:\n%s", got)
	}

	// AND NOT WHEN THERE IS NO MEANING TO TALK ABOUT. Pointing a writer at
	// material it has not been given is how it invents some, which is the one
	// thing the whole dossier design exists to prevent.
	blank := testDossier(2)
	blank.SubjectSummary = ""
	blank.Themes = nil
	bare, err := BuildBreakPrompt(PromptInput{
		Persona: testPersona(t), Current: blank, WindowSeconds: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bare, "TALK ABOUT WHAT THE SONG IS ABOUT") {
		t.Error("the writer is pointed at meaning the dossier does not have")
	}
}
