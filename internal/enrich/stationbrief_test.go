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

	req, ok := schema["required"].([]any)
	if !ok || len(req) != 5 {
		t.Fatalf("required = %#v, want all five properties", schema["required"])
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
