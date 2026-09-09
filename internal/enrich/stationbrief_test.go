// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Jockora-g1t.2. A STATION IS DESCRIBED, NOT TAGGED: the operator writes a
// sentence and one schema-constrained pass turns it into the parameters a song
// can be selected on.

type scriptedCompleter struct {
	replies []string
	err     error
	calls   int
	got     CompletionRequest
}

func (s *scriptedCompleter) Complete(_ context.Context, req CompletionRequest) (Completion, error) {
	s.got = req
	s.calls++
	if s.err != nil {
		return Completion{}, s.err
	}
	i := s.calls - 1
	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	return Completion{Content: s.replies[i], StopType: "eos"}, nil
}

func derive(t *testing.T, replies ...string) (StationParams, error, *scriptedCompleter) {
	t.Helper()
	c := &scriptedCompleter{replies: replies}
	p, err := DeriveStationParams(context.Background(), c, "late-night rock for driving")
	return p, err, c
}

// TestStationParamsSchemaEnums: the vocabularies go in as enums so the SAMPLER
// cannot emit an out-of-list value. vocab.go records what happens without it --
// "retrowave", "night_drive", "80s_vibe" -- which is forty near-duplicate
// stations rather than a dial.
func TestStationParamsSchemaEnums(t *testing.T) {
	schema := StationParamsSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("no properties: %#v", schema)
	}

	for _, tc := range []struct {
		field string
		want  []string
	}{{"genres", StationTags}, {"moods", Moods}} {
		arr, ok := props[tc.field].(map[string]any)
		if !ok {
			t.Fatalf("%s is not an array schema: %#v", tc.field, props[tc.field])
		}
		items, ok := arr["items"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no items schema", tc.field)
		}
		got, ok := items["enum"].([]any)
		if !ok {
			t.Fatalf("%s items carry no enum, so the sampler is unconstrained", tc.field)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s enum has %d values, want the %d in the vocabulary", tc.field, len(got), len(tc.want))
		}
		for i, v := range tc.want {
			if got[i] != v {
				t.Errorf("%s enum[%d] = %v, want %q", tc.field, i, got[i], v)
			}
		}
	}

	// EVERY PROPERTY IS REQUIRED, asserted against the properties map rather
	// than a count. This said "want all five properties" and broke the moment
	// tempo and length were added, which is a golden number pretending to be a
	// claim: what it guards is that a model cannot simply omit a field, and
	// that survives the set growing.
	req, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("required = %#v", schema["required"])
	}
	required := map[string]bool{}
	for _, r := range req {
		required[r.(string)] = true
	}
	for field := range props {
		if !required[field] {
			t.Errorf("%s is a property but is not required, so the model may not answer it "+
				"-- and an absent bound and an unbounded one then look identical", field)
		}
	}
	if len(req) != len(props) {
		t.Errorf("required lists %d fields and there are %d properties", len(req), len(props))
	}
	if schema["additionalProperties"] != false {
		t.Error("additionalProperties is not false, so the model may invent a field")
	}
}

func TestStationParamsPromptCarriesBrief(t *testing.T) {
	p := BuildStationBriefPrompt("late-night rock, nothing after 2005")
	if !strings.Contains(p, "late-night rock, nothing after 2005") {
		t.Error("the brief is not in the prompt")
	}
	// FENCED AS DATA. A brief is operator-supplied text arriving in a prompt,
	// and an unfenced one that says "ignore the above" is an instruction.
	if !strings.Contains(p, "```") {
		t.Error("the brief is not fenced, so it reads as instructions")
	}
	// THE YEAR RULE, in three parts, and each one is here because a live run
	// against a real model produced the wrong answer without it.
	low := strings.ToLower(p)
	// A model told to give a number invents one rather than declining, so the
	// default has to be stated and stated last.
	if !strings.Contains(low, "both are 0") || !strings.Contains(low, "do not invent") {
		t.Errorf("the prompt does not say that no dates means no years:\n%s", p)
	}
	// A named decade has TWO ends: "mostly nineties" came back as 0 to 1999,
	// an open floor admitting every record made before 1990.
	if !strings.Contains(low, "both ends") || !strings.Contains(p, "1990") {
		t.Errorf("the prompt does not say a decade has two ends:\n%s", p)
	}
	// And a genuinely one-sided bound still uses 0 on the open side.
	if !strings.Contains(low, "leaves open") {
		t.Errorf("the prompt does not cover a one-sided bound:\n%s", p)
	}
}

func TestStationParamsHappy(t *testing.T) {
	got, err, c := derive(t, `{"name":"Night Rock","genres":["rock","alternative"],
		"moods":["nocturnal","propulsive"],"year_min":1975,"year_max":2005}`)
	if err != nil {
		t.Fatalf("DeriveStationParams: %v", err)
	}
	if got.Name != "Night Rock" {
		t.Errorf("Name = %q", got.Name)
	}
	if len(got.Genres) != 2 || got.Genres[0] != "rock" {
		t.Errorf("Genres = %v", got.Genres)
	}
	if got.YearMin != 1975 || got.YearMax != 2005 {
		t.Errorf("years = %d..%d", got.YearMin, got.YearMax)
	}
	// CATALOGUING, not writing: the same temperature the dossier pass uses.
	if c.got.Temperature != 0 && c.got.Temperature != DefaultTemperature {
		t.Errorf("temperature = %v, want the cataloguing default", c.got.Temperature)
	}
	if c.got.JSONSchema == nil {
		t.Error("no schema was sent, so the sampler was never constrained")
	}
}

// TestStationParamsDropsUnknownTags: the enum is the first line of defence and
// this is the second. A hosted model treats a schema as advisory.
func TestStationParamsDropsUnknownTags(t *testing.T) {
	got, err, _ := derive(t, `{"name":"N","genres":["retrowave","synthwave","night_drive"],
		"moods":["80s_vibe","nocturnal"],"year_min":0,"year_max":0}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Genres) != 1 || got.Genres[0] != "synthwave" {
		t.Errorf("Genres = %v, want only synthwave", got.Genres)
	}
	if len(got.Moods) != 1 || got.Moods[0] != "nocturnal" {
		t.Errorf("Moods = %v, want only nocturnal", got.Moods)
	}
}

// TestStationParamsCapsLists: a station selecting nine genres is not a place on
// a dial.
func TestStationParamsCapsLists(t *testing.T) {
	got, err, _ := derive(t, `{"name":"N",
		"genres":["rock","pop","jazz","metal","punk","folk","blues","soul","funk"],
		"moods":["calm","raw","euphoric","melancholic","nocturnal","hypnotic"],
		"year_min":0,"year_max":0}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Genres) != MaxBriefGenres {
		t.Errorf("%d genres, want the cap of %d", len(got.Genres), MaxBriefGenres)
	}
	if len(got.Moods) != MaxBriefMoods {
		t.Errorf("%d moods, want the cap of %d", len(got.Moods), MaxBriefMoods)
	}
}

func TestStationParamsYearClamp(t *testing.T) {
	next := time.Now().Year() + 1
	for _, tc := range []struct {
		name             string
		inMin, inMax     int
		wantMin, wantMax int
	}{
		{"unbounded stays unbounded", 0, 0, 0, 0},
		{"a real range is kept", 1985, 1989, 1985, 1989},
		{"a future year is clamped and the pair is ordered", 3000, 1200, 1900, next},
		{"an inverted pair is swapped", 1989, 1985, 1985, 1989},
		{"a negative is clamped", -50, 1990, 1900, 1990},
		{"only a floor", 1990, 0, 1990, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			min, max := clampYears(tc.inMin, tc.inMax)
			if min != tc.wantMin || max != tc.wantMax {
				t.Errorf("clampYears(%d, %d) = %d, %d; want %d, %d",
					tc.inMin, tc.inMax, min, max, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestStationParamsName(t *testing.T) {
	long := strings.Repeat("é", 60)
	for _, tc := range []struct{ in, want string }{
		{"  Night Rock  ", "Night Rock"},
		{"Night\n\t  Rock", "Night Rock"},
		{"", ""},
		{long, strings.Repeat("é", MaxBriefName)},
	} {
		if got := cleanName(tc.in); got != tc.want {
			t.Errorf("cleanName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// CUT ON A RUNE BOUNDARY, not a byte one: an accented name cut mid-rune is
	// a replacement character on the dial.
	if got := cleanName(long); len([]rune(got)) != MaxBriefName {
		t.Errorf("a 60-rune name became %d runes", len([]rune(got)))
	}
}

func TestStationParamsRetriesOnce(t *testing.T) {
	got, err, c := derive(t, `not json at all`,
		`{"name":"N","genres":["rock"],"moods":[],"year_min":0,"year_max":0}`)
	if err != nil {
		t.Fatalf("the retry did not recover: %v", err)
	}
	if len(got.Genres) != 1 {
		t.Errorf("Genres = %v", got.Genres)
	}
	if c.calls != 2 {
		t.Errorf("the model was called %d times, want exactly 2", c.calls)
	}
}

// TestStationParamsFailsAfterTwo: DELIBERATELY DIFFERENT FROM THE DOSSIER PATH.
// A failed dossier leaves one track unenriched; a failed brief would build the
// wrong station, and the operator is standing at the form where an error is
// actionable.
func TestStationParamsFailsAfterTwo(t *testing.T) {
	_, err, c := derive(t, `garbage`)
	if err == nil {
		t.Fatal("two malformed answers produced no error")
	}
	if !strings.Contains(err.Error(), "late-night rock for driving") {
		t.Errorf("error = %v, want it to name the brief the operator typed", err)
	}
	if c.calls != 2 {
		t.Errorf("the model was called %d times, want 2", c.calls)
	}
}

// TestStationParamsEmptyResultIsAnError: no genres, no moods and no years is
// the whole library. Legal as a filter, never what a brief meant.
func TestStationParamsEmptyResultIsAnError(t *testing.T) {
	_, err, c := derive(t, `{"name":"N","genres":[],"moods":[],"year_min":0,"year_max":0}`)
	if err == nil {
		t.Fatal("a station matching the entire library was accepted")
	}
	if c.calls != 2 {
		t.Errorf("the model was called %d times; an empty result is retried once", c.calls)
	}
	// A year alone is a real station: "anything from 1994".
	if _, err, _ := derive(t,
		`{"name":"N","genres":[],"moods":[],"year_min":1994,"year_max":1994}`); err != nil {
		t.Errorf("a year-only station was refused: %v", err)
	}
}

func TestStationParamsBlankBrief(t *testing.T) {
	for _, brief := range []string{"", "   ", "\n\t "} {
		c := &scriptedCompleter{replies: []string{`{}`}}
		_, err := DeriveStationParams(context.Background(), c, brief)
		if err == nil {
			t.Errorf("a blank brief %q was accepted", brief)
		}
		// WITHOUT CALLING THE MODEL: there is nothing to derive from, and a
		// round trip to say so is a round trip wasted.
		if c.calls != 0 {
			t.Errorf("the model was called %d times for a blank brief", c.calls)
		}
	}
}

// TestStationParamsPropagatesModelErrors: a refusal is not a parse failure and
// must not be retried into a second refusal.
func TestStationParamsPropagatesModelErrors(t *testing.T) {
	c := &scriptedCompleter{err: errors.New("rejected the API key")}
	_, err := DeriveStationParams(context.Background(), c, "a brief")
	if err == nil {
		t.Fatal("a model error was swallowed")
	}
	if !errors.Is(err, ErrLLMUnavailable) {
		t.Errorf("error = %v, want it to wrap ErrLLMUnavailable", err)
	}
	if c.calls != 1 {
		t.Errorf("a refusal was retried %d times; retrying a refusal refuses again", c.calls)
	}
}

// TestStationParamsRespectsCancellation: the operator navigated away, or the
// server is shutting down. That is not a model failure and must not be reported
// as one -- ErrLLMUnavailable would send somebody to check a provider that is
// perfectly fine.
func TestStationParamsRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &scriptedCompleter{err: context.Canceled}

	_, err := DeriveStationParams(ctx, c, "a brief")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation itself", err)
	}
	if errors.Is(err, ErrLLMUnavailable) {
		t.Error("a cancelled request was blamed on the model")
	}
}

// Jockora-g1t.11. Jockora-g1t.2 was specced to derive tempo and length as well
// and was closed without them: its twelve listed tests never mentioned either,
// so the task graded green with two parameters missing. The stations table has
// carried tempo_min, tempo_max, duration_min_s and duration_max_s since
// migration 10, station.Filter selects on all four, and NOTHING could write
// them. These are the tests that were never written.

func TestStationParamsTempoSchemaBounds(t *testing.T) {
	props := StationParamsSchema()["properties"].(map[string]any)
	for _, c := range []struct {
		field   string
		lo, hi  float64
		whatFor string
	}{
		{"tempo_min", SlowestBPM, FastestBPM, "the sidecar refuses a tempo outside this"},
		{"tempo_max", SlowestBPM, FastestBPM, "the sidecar refuses a tempo outside this"},
		{"duration_min_s", ShortestTrackS, LongestTrackS, "half a minute to an album side"},
		{"duration_max_s", ShortestTrackS, LongestTrackS, "half a minute to an album side"},
	} {
		spec, ok := props[c.field].(map[string]any)
		if !ok {
			t.Errorf("the schema has no %s, so the sampler can return anything", c.field)
			continue
		}
		// BOUNDED AT THE SAMPLER, not merely repaired afterwards. The clamps
		// are the second line; a value that cannot be generated is the first.
		if spec["minimum"] != c.lo || spec["maximum"] != c.hi {
			t.Errorf("%s bounds = %v..%v, want %v..%v (%s)",
				c.field, spec["minimum"], spec["maximum"], c.lo, c.hi, c.whatFor)
		}
	}
	// REQUIRED, like the years. A model that may omit a field omits it, and an
	// absent tempo and an unbounded one then look identical.
	req := StationParamsSchema()["required"].([]any)
	for _, want := range []string{"tempo_min", "tempo_max", "duration_min_s", "duration_max_s"} {
		var found bool
		for _, r := range req {
			if r == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not required, so the model may simply not answer it", want)
		}
	}
}

func TestStationParamsTempoFromBrief(t *testing.T) {
	got, err, _ := derive(t, `{"name":"Runners","genres":["electronic"],"moods":["propulsive"],
		"year_min":0,"year_max":0,"tempo_min":150,"tempo_max":180,
		"duration_min_s":0,"duration_max_s":0}`)
	if err != nil {
		t.Fatalf("DeriveStationParams: %v", err)
	}
	if got.TempoMin != 150 || got.TempoMax != 180 {
		t.Errorf("tempo = %v..%v, want 150..180", got.TempoMin, got.TempoMax)
	}
}

func TestStationParamsLengthFromBrief(t *testing.T) {
	got, err, _ := derive(t, `{"name":"Long Form","genres":["ambient"],"moods":["calm"],
		"year_min":0,"year_max":0,"tempo_min":0,"tempo_max":0,
		"duration_min_s":420,"duration_max_s":1800}`)
	if err != nil {
		t.Fatalf("DeriveStationParams: %v", err)
	}
	if got.DurationMinS != 420 || got.DurationMaxS != 1800 {
		t.Errorf("length = %v..%v, want 420..1800", got.DurationMinS, got.DurationMaxS)
	}
}

// TestStationParamsTempoSilentBriefHasNoBound is the one that matters.
//
// An UNBIDDEN range silently shrinks a playlist, which is worse than a missing
// one because nothing on screen explains it. The year rule needed exactly this
// fix live, twice: "mostly nineties" came back as 0 to 1999, and then, once
// told to give both ends, the model started inventing decades for briefs that
// mentioned no dates at all.
func TestStationParamsTempoSilentBriefHasNoBound(t *testing.T) {
	got, err, _ := derive(t, `{"name":"Angry Guitars","genres":["rock"],"moods":["aggressive"],
		"year_min":0,"year_max":0,"tempo_min":0,"tempo_max":0,
		"duration_min_s":0,"duration_max_s":0}`)
	if err != nil {
		t.Fatalf("DeriveStationParams: %v", err)
	}
	if got.TempoMin != 0 || got.TempoMax != 0 {
		t.Errorf("a brief about nothing but feel came back bounded at %v..%v",
			got.TempoMin, got.TempoMax)
	}
	if got.DurationMinS != 0 || got.DurationMaxS != 0 {
		t.Errorf("a brief saying nothing about length came back bounded at %v..%v",
			got.DurationMinS, got.DurationMaxS)
	}
}

// TestStationParamsTempoDeriveClamps: the clamps are WIRED, not merely written.
//
// clampTempo has its own table test, and unwiring it from parseStationParams
// broke nothing else -- the same gap the advert brief check had. A hosted model
// treats a schema as advisory, so this is the path that actually runs.
func TestStationParamsTempoDeriveClamps(t *testing.T) {
	got, err, _ := derive(t, `{"name":"Runners","genres":["electronic"],"moods":["propulsive"],
		"year_min":0,"year_max":0,"tempo_min":900,"tempo_max":5,
		"duration_min_s":99999,"duration_max_s":2}`)
	if err != nil {
		t.Fatalf("DeriveStationParams: %v", err)
	}
	// Out of range on both sides AND inverted, which is what a model ignoring
	// the schema actually produces.
	if got.TempoMin != SlowestBPM || got.TempoMax != FastestBPM {
		t.Errorf("tempo = %v..%v, want it clamped and ordered to %v..%v",
			got.TempoMin, got.TempoMax, SlowestBPM, FastestBPM)
	}
	if got.DurationMinS != ShortestTrackS || got.DurationMaxS != LongestTrackS {
		t.Errorf("length = %v..%v, want it clamped and ordered to %v..%v",
			got.DurationMinS, got.DurationMaxS, ShortestTrackS, LongestTrackS)
	}
}

func TestStationParamsTempoClamps(t *testing.T) {
	for _, tc := range []struct {
		name             string
		inMin, inMax     float64
		wantMin, wantMax float64
	}{
		{"unbounded stays unbounded", 0, 0, 0, 0},
		{"a real range is kept", 150, 180, 150, 180},
		{"too slow is pulled to the floor", 5, 180, SlowestBPM, 180},
		{"too fast is pulled to the ceiling", 150, 900, 150, FastestBPM},
		{"an inverted pair is swapped", 180, 150, 150, 180},
		{"a negative is clamped", -20, 150, SlowestBPM, 150},
		{"only a floor", 150, 0, 150, 0},
		// Jockora-csr, reported from the deployment: a brief reading
		// "Late-night driving music, mostly 80s, nothing cheerful" derived
		// Slowest 193 and Fastest 193. That is a POINT, not a range, and bpm
		// is a float measurement -- so no track can ever satisfy it and the
		// station is empty for a reason nothing on screen explains. No bound
		// is honest; a bound nothing can meet is not.
		{"a range of one value is not a range", 193, 193, 0, 0},
		{"and neither is one at the floor", SlowestBPM, SlowestBPM, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			min, max := clampTempo(tc.inMin, tc.inMax)
			if min != tc.wantMin || max != tc.wantMax {
				t.Errorf("clampTempo(%v, %v) = %v, %v; want %v, %v",
					tc.inMin, tc.inMax, min, max, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestStationParamsLengthClamps(t *testing.T) {
	for _, tc := range []struct {
		name             string
		inMin, inMax     float64
		wantMin, wantMax float64
	}{
		{"unbounded stays unbounded", 0, 0, 0, 0},
		{"a real range is kept", 420, 1800, 420, 1800},
		{"too short is pulled to the floor", 5, 600, ShortestTrackS, 600},
		{"longer than an album side is pulled in", 600, 99999, 600, LongestTrackS},
		{"an inverted pair is swapped", 600, 300, 300, 600},
		{"only a ceiling", 0, 240, 0, 240},
		// The same rule, because they share clampRange. A duration is a float
		// too, so "exactly 240 seconds" selects nothing. Jockora-csr.
		{"a range of one value is not a range", 240, 240, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			min, max := clampLength(tc.inMin, tc.inMax)
			if min != tc.wantMin || max != tc.wantMax {
				t.Errorf("clampLength(%v, %v) = %v, %v; want %v, %v",
					tc.inMin, tc.inMax, min, max, tc.wantMin, tc.wantMax)
			}
		})
	}
}

// TestStationParamsTempoPromptStatesTheDefault: the LAST instruction is the one
// a small model remembers, which is why the year rule ends on its default.
func TestStationParamsTempoPromptStatesTheDefault(t *testing.T) {
	p := BuildStationBriefPrompt("something to run to")
	for _, want := range []string{"tempo_min", "tempo_max", "duration_min_s", "duration_max_s"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt never names %s:\n%s", want, p)
		}
	}
	// SECONDS, said out loud. A model given "nothing over four minutes" and no
	// unit will answer 4.
	if !strings.Contains(strings.ToLower(p), "seconds") {
		t.Errorf("the prompt does not say the length is in seconds:\n%s", p)
	}
	// BPM, likewise: a tempo with no unit invites a word.
	if !strings.Contains(strings.ToUpper(p), "BPM") {
		t.Errorf("the prompt does not say the tempo is in BPM:\n%s", p)
	}
	// AND THE DEFAULT IS STATED LAST for both, after the conditional rule, in
	// the shape the years had to be rewritten into after a live run.
	//
	// MATCHED ON ITS OWN SENTENCE, not on "0 and 0": the YEAR rule ends on
	// those same three words, so a looser check passed against a prompt whose
	// tempo rule had no default at all -- which is the exact defect that put an
	// invented decade on a date-free brief.
	const mustSay = "MENTIONS NEITHER PACE NOR LENGTH, ALL FOUR ARE 0"
	if !strings.Contains(p, mustSay) {
		t.Errorf("the tempo and length rules do not state their own default:\n%s", p)
	}
	if strings.Index(p, mustSay) < strings.Index(p, "tempo_min") {
		t.Errorf("the default comes before the rule it is the default for:\n%s", p)
	}
	// It is also the LAST thing said about the ranges, which is the whole point
	// of the ordering: the last instruction is the one a small model remembers.
	if strings.Index(p, mustSay) < strings.LastIndex(p, "duration_max_s") {
		t.Errorf("something about the ranges comes after their default:\n%s", p)
	}
	if !strings.Contains(p, "Do not invent a tempo or a length") {
		t.Errorf("the prompt does not forbid inventing one:\n%s", p)
	}
}
