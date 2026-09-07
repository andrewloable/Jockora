// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

func dossierWith(confidence string, facts ...string) *enrich.Dossier {
	return &enrich.Dossier{
		StationTags:    []string{"synthwave"},
		Mood:           []string{"nocturnal"},
		SubjectSummary: "A narrator drives out of a city he is not going back to.",
		ArtistFacts:    facts,
		Sources:        []string{enrich.SourceMusicBrainz},
		Confidence:     confidence,
	}
}

func TestBreakValidBreakParses(t *testing.T) {
	b, err := ParseBreak(`{"opening":"Three in the morning.","body":"That was a record about leaving.",
		"handoff":"Stay where you are.","asserted_facts":["cur.artist_facts[0]"]}`)
	if err != nil {
		t.Fatalf("ParseBreak: %v", err)
	}
	if b.Opening == "" || b.Body == "" || b.Handoff == "" {
		t.Errorf("break = %+v", b)
	}
	if len(b.AssertedFacts) != 1 {
		t.Errorf("AssertedFacts = %v", b.AssertedFacts)
	}
	if !strings.Contains(b.Text(), "Three in the morning") || !strings.Contains(b.Text(), "Stay where you are") {
		t.Errorf("Text() = %q, want all three parts", b.Text())
	}
}

func TestBreakMalformedJSON(t *testing.T) {
	for _, raw := range []string{"", "   ", "not json", `{"opening":`} {
		if _, err := ParseBreak(raw); !errors.Is(err, ErrBreakBadJSON) {
			t.Errorf("ParseBreak(%q) err = %v, want ErrBreakBadJSON", raw, err)
		}
	}
}

func TestBreakFactIDResolves(t *testing.T) {
	cur := dossierWith(enrich.ConfidenceHigh, "The band formed in 1980.")
	b := &Break{AssertedFacts: []string{"cur.artist_facts[0]", "cur.subject_summary", "cur.mood"}}

	if err := ResolveFacts(b, nil, cur, nil); err != nil {
		t.Fatalf("ResolveFacts: %v", err)
	}

	text, ok := ResolveFactText("cur.artist_facts[0]", nil, cur, nil)
	if !ok || text != "The band formed in 1980." {
		t.Errorf("ResolveFactText = %q, %v", text, ok)
	}
}

func TestBreakUnresolvableFactIDFails(t *testing.T) {
	cur := dossierWith(enrich.ConfidenceHigh, "The band formed in 1980.")

	for _, id := range []string{
		"cur.grammy_wins",       // no such field
		"cur.artist_facts[7]",   // out of range
		"next.artist_facts[0]",  // no next dossier
		"other.artist_facts[0]", // no such prefix
		"artist_facts[0]",       // no prefix at all
	} {
		b := &Break{AssertedFacts: []string{id}}
		err := ResolveFacts(b, nil, cur, nil)
		if !errors.Is(err, ErrBreakUngrounded) {
			t.Errorf("%q: err = %v, want ErrBreakUngrounded", id, err)
			continue
		}
		if !strings.Contains(err.Error(), id) {
			t.Errorf("%q: error %q does not name the offending id", id, err)
		}
	}
}

func TestBreakLowConfidenceFactFails(t *testing.T) {
	cur := dossierWith(enrich.ConfidenceNone, "The band formed in 1980.")
	b := &Break{AssertedFacts: []string{"cur.artist_facts[0]"}}

	if err := ResolveFacts(b, nil, cur, nil); !errors.Is(err, ErrBreakUngrounded) {
		t.Errorf("err = %v, want ErrBreakUngrounded for a confidence-none dossier", err)
	}
}

// TestBreakConfidenceThresholdIsPointSix reconciles the numeric threshold this
// task specifies with the high/low/none vocabulary the dossier actually carries.
func TestBreakConfidenceThresholdIsPointSix(t *testing.T) {
	if FactConfidenceThreshold != 0.6 {
		t.Fatalf("FactConfidenceThreshold = %v, want 0.6", FactConfidenceThreshold)
	}
	// Only "high" clears the bar. "low" scores 0.5, which is below 0.6 on
	// purpose: a poorly-grounded claim spoken on air is the failure the whole
	// dossier design exists to prevent.
	for label, wantPass := range map[string]bool{
		enrich.ConfidenceHigh: true,
		enrich.ConfidenceLow:  false,
		enrich.ConfidenceNone: false,
		"":                    false,
	} {
		score := ConfidenceScore(label)
		if got := score >= FactConfidenceThreshold; got != wantPass {
			t.Errorf("confidence %q scores %v, passes = %v, want %v", label, score, got, wantPass)
		}
	}
	if ConfidenceScore(enrich.ConfidenceLow) >= 0.6 {
		t.Error("a 0.5 score cleared a 0.6 threshold")
	}
	if ConfidenceScore(enrich.ConfidenceHigh) < 0.61 {
		t.Error("a high-confidence dossier failed a 0.61 bar")
	}
}

// TestBreakEmptyAssertedFactsIsValid: personality-only breaks assert nothing and
// are entirely legal.
func TestBreakEmptyAssertedFactsIsValid(t *testing.T) {
	b, err := ParseBreak(`{"opening":"Three in the morning.","body":"Nothing to tell you about this one.",
		"handoff":"Stay where you are.","asserted_facts":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ResolveFacts(b, nil, nil, nil); err != nil {
		t.Errorf("a break asserting nothing was rejected: %v", err)
	}
	if err := ResolveFacts(b, nil, dossierWith(enrich.ConfidenceNone), nil); err != nil {
		t.Errorf("a personality-only break against an empty dossier was rejected: %v", err)
	}
}

// TestBreakFactEnumBuiltFromResolvableIds is what makes an ungrounded fact
// unrepresentable rather than merely rejected.
func TestBreakFactEnumBuiltFromResolvableIds(t *testing.T) {
	cur := dossierWith(enrich.ConfidenceHigh, "Fact one.", "Fact two.")
	next := dossierWith(enrich.ConfidenceNone, "Should not appear.")

	ids := ResolvableFactIDs(nil, cur, next)

	for _, want := range []string{"cur.artist_facts[0]", "cur.artist_facts[1]",
		"cur.subject_summary", "cur.station_tags", "cur.mood"} {
		if !containsStr(ids, want) {
			t.Errorf("resolvable ids missing %q: %v", want, ids)
		}
	}
	for _, banned := range ids {
		if strings.HasPrefix(banned, "next.") {
			t.Errorf("a confidence-none dossier contributed %q", banned)
		}
	}
	if containsStr(ids, "cur.artist_facts[2]") {
		t.Error("an id was offered for a fact that does not exist")
	}

	schema := BreakSchema(nil, cur, next)
	raw := mustJSONString(t, schema)
	if !strings.Contains(raw, "cur.artist_facts[0]") {
		t.Error("the schema enum omits a resolvable id")
	}
	if strings.Contains(raw, "next.artist_facts[0]") {
		t.Error("the schema enum includes an id from a confidence-none dossier")
	}
	if !strings.Contains(raw, `"additionalProperties":false`) {
		t.Error("the schema permits extra keys")
	}
}

// TestBreakUngroundedFactIsUnrepresentable: an id outside the enum has no way
// into a valid result.
func TestBreakUngroundedFactIsUnrepresentable(t *testing.T) {
	cur := dossierWith(enrich.ConfidenceHigh, "Fact one.")
	schema := BreakSchema(nil, cur, nil)

	enum := schema["properties"].(map[string]any)["asserted_facts"].(map[string]any)["items"].(map[string]any)["enum"].([]any)
	for _, v := range enum {
		if v.(string) == "cur.grammy_wins" {
			t.Fatal("an unresolvable id is in the enum")
		}
	}

	// And if one arrived anyway, resolution still rejects it.
	b := &Break{AssertedFacts: []string{"cur.grammy_wins"}}
	if err := ResolveFacts(b, nil, cur, nil); !errors.Is(err, ErrBreakUngrounded) {
		t.Error("resolution accepted an id outside the enum")
	}
}

// TestBreakPersonalityOnlyIsStillGenerable: with nothing assertable, the schema
// must still permit a break rather than making one impossible.
func TestBreakPersonalityOnlyIsStillGenerable(t *testing.T) {
	schema := BreakSchema(nil, nil, nil)
	raw := mustJSONString(t, schema)

	required, _ := schema["required"].([]any)
	for _, r := range required {
		if r.(string) == "asserted_facts" {
			t.Error("asserted_facts is required when nothing is assertable; no break could be generated")
		}
	}
	if !strings.Contains(raw, `"opening"`) || !strings.Contains(raw, `"body"`) {
		t.Errorf("schema lost its text fields: %s", raw)
	}
}

func TestBreakDuplicateFactsAreDeduped(t *testing.T) {
	b, err := ParseBreak(`{"opening":"Midnight Vale.","body":"That was loud.","handoff":"Stay there.",
		"asserted_facts":["cur.artist_facts[0]","cur.artist_facts[0]"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.AssertedFacts) != 1 {
		t.Errorf("AssertedFacts = %v, want one entry: GBNF does not enforce uniqueItems", b.AssertedFacts)
	}
}

func TestBreakSchemaBoundsTextLength(t *testing.T) {
	raw := mustJSONString(t, BreakSchema(nil, dossierWith(enrich.ConfidenceHigh, "f"), nil))
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	props := parsed["properties"].(map[string]any)
	for _, f := range []string{"opening", "body", "handoff"} {
		if _, ok := props[f].(map[string]any)["maxLength"]; !ok {
			t.Errorf("%s has no maxLength; injected prose would be unbounded", f)
		}
	}
}

func containsStr(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// TestParseBreakRejectsEmptySpokenText: every field is optional in the grammar,
// so a break with nothing in it parses cleanly, resolves no facts, collides
// with nothing, is recorded as aired, and then fails at the TTS sidecar with a
// 400 -- reported as an unavailable sidecar, which is the wrong diagnosis
// entirely. Measured live, that was 4 breaks in 20.
func TestParseBreakRejectsEmptySpokenText(t *testing.T) {
	for _, raw := range []string{
		`{"opening":"","body":"","handoff":"","asserted_facts":[]}`,
		`{"opening":"   ","body":"\n","handoff":""}`,
		`{"asserted_facts":["artist_facts.0"]}`,
	} {
		if _, err := ParseBreak(raw); err == nil {
			t.Errorf("ParseBreak(%s) accepted a break with no spoken text", raw)
		} else if !errors.Is(err, ErrBreakBadJSON) {
			t.Errorf("ParseBreak(%s) error = %v, want ErrBreakBadJSON", raw, err)
		}
	}

	// Any one populated field is enough.
	if _, err := ParseBreak(`{"opening":"Midnight Vale.","body":"","handoff":""}`); err != nil {
		t.Errorf("rejected a break that has an opening: %v", err)
	}
}

// TestParseBreakRejectsWordlessText. "... ... ..." aired in a live run: it was
// schema-valid, non-empty, the right length, and collided with nothing. A break
// has to be made of words, and nothing checked that.
func TestParseBreakRejectsWordlessText(t *testing.T) {
	for _, raw := range []string{
		`{"opening":"... ... ...","body":"","handoff":""}`,
		`{"opening":"","body":"---","handoff":"..."}`,
		`{"opening":"a b","body":"","handoff":""}`,
		`{"opening":"?! ?!","body":"","handoff":""}`,
	} {
		if _, err := ParseBreak(raw); err == nil {
			t.Errorf("ParseBreak(%s) accepted a break with no words in it", raw)
		}
	}

	// A terse but real break must survive: this is a station ID, not noise.
	for _, ok := range []string{
		`{"opening":"Three in the morning.","body":"","handoff":""}`,
		`{"opening":"Midnight Vale.","body":"","handoff":""}`,
	} {
		if _, err := ParseBreak(ok); err != nil {
			t.Errorf("rejected a real break %s: %v", ok, err)
		}
	}
}

// TestBreakCollapsesRepeatedOpening. Models restate the opening at the start of
// the body, because each field is written knowing what the break should sound
// like and not what the neighbouring field already said. Every input here came
// from a live run and it is the most audible flaw in otherwise good output.
func TestBreakCollapsesRepeatedOpening(t *testing.T) {
	cases := []struct{ opening, body, want string }{
		{"Next up,", "Next up, a powerful declaration of love from Bryan Adams.",
			"Next up, a powerful declaration of love from Bryan Adams."},
		{"Good evening.", "Good evening. The music is shifting.",
			"Good evening. The music is shifting."},
		{"And now, as we", "And now, as we roll with it, let's turn to The Smashing Pumpkins.",
			"And now, as we roll with it, let's turn to The Smashing Pumpkins."},
		{"The previous track was", "The previous track was aggressive.",
			"The previous track was aggressive."},
		// Not a repeat: a shared single word is ordinary English and must stay.
		{"Three in the morning.", "The only honest hour.",
			"Three in the morning. The only honest hour."},
		// Nothing shared at all.
		{"Midnight here.", "Coming up, something louder.",
			"Midnight here. Coming up, something louder."},
	}
	for _, tc := range cases {
		b := &Break{Opening: tc.opening, Body: tc.body}
		if got := b.Text(); got != tc.want {
			t.Errorf("Text() = %q\n    want %q", got, tc.want)
		}
	}
}

// TestBreakRejectsSnakeCaseTokens: nobody says "3am_listening" out loud. Both
// of these aired, lifted from the station-tag vocabulary.
func TestBreakRejectsSnakeCaseTokens(t *testing.T) {
	for _, text := range []string{
		"3am_listening Next up, we have 'I See Red' by Frida. radio_intro_music",
		"Here is something from the night_drive playlist.",
	} {
		if _, echoed := echoesInstructions(text, nil); !echoed {
			t.Errorf("echoesInstructions(%q) = false; this aired", text)
		}
	}
	if phrase, echoed := echoesInstructions("Next up, Counting Crows with a wistful tune.", nil); echoed {
		t.Errorf("matched %q on a real break", phrase)
	}
}

// TestBreakStripsSpeakerLabels. The model writes a SCRIPT rather than speaking,
// and the label is the stage direction around the break, not part of it. Both
// of these aired.
func TestBreakStripsSpeakerLabels(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Dutch: And now, as we welcome Laurindo Almeida, let's swing.",
			"And now, as we welcome Laurindo Almeida, let's swing."},
		{"Dutch 'The Hammer' Mahoney: The next track is about to fill the airwaves.",
			"The next track is about to fill the airwaves."},
		// A legitimate opening that happens to end in a colon must survive.
		{"Coming up: something with more teeth in it.",
			"Coming up: something with more teeth in it."},
		{"Three in the morning. The only honest hour.",
			"Three in the morning. The only honest hour."},
	}
	for _, tc := range cases {
		if got := (&Break{Opening: tc.in}).Text(); got != tc.want {
			t.Errorf("Text() = %q\n    want %q", got, tc.want)
		}
	}
}

// TestBreakRejectsStageDirections: the model describing its own delivery
// instead of performing it. "Loudly and enthusiastically." aired as a break.
func TestBreakRejectsStageDirections(t *testing.T) {
	for _, text := range []string{
		"Loudly and enthusiastically. And now, as we reflect on ABBA's summer.",
		"Excitedly. Here comes the next one.",
		"[shouting] ROCK ON",
	} {
		if _, echoed := echoesInstructions(text, nil); !echoed {
			t.Errorf("echoesInstructions(%q) = false; this aired", text)
		}
	}
}
