// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"strings"
	"testing"
)

// The shipped cards are the fixture. Testing selection against invented cards
// would prove the arithmetic and nothing about whether a real station gets a
// sensible jock.
const personaDir = "../../personas"

func TestSelectLoadsEveryShippedPersona(t *testing.T) {
	all, err := LoadPersonas(personaDir)
	if err != nil {
		t.Fatalf("LoadPersonas: %v", err)
	}
	if len(all) < 5 {
		t.Fatalf("loaded %d personas, want at least the five shipped", len(all))
	}

	seenVoice := map[string]string{}
	for _, p := range all {
		if p.ID() == "" || p.Name() == "" {
			t.Errorf("persona %q has no id or name", p.ID())
		}
		if len(p.GoodForGenres()) == 0 {
			t.Errorf("%s declares no genres, so it can never be selected for a station", p.ID())
		}
		if len(p.Forbidden()) == 0 {
			t.Errorf("%s has no forbidden list; the persona card is the only thing holding the DJ back", p.ID())
		}
		// A shared voice would make two jocks indistinguishable on air, which
		// defeats the point of having two.
		if other, dup := seenVoice[p.VoiceID()]; dup {
			t.Errorf("%s and %s share the voice %q", p.ID(), other, p.VoiceID())
		}
		seenVoice[p.VoiceID()] = p.ID()
	}
}

// EVERY persona must forbid inventing facts. That rule is the whole
// anti-hallucination design, and a card that omits it quietly opts out.
func TestSelectEveryPersonaForbidsInventingFacts(t *testing.T) {
	all, err := LoadPersonas(personaDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		found := false
		for _, f := range p.Forbidden() {
			l := strings.ToLower(f)
			if strings.Contains(l, "fact") && (strings.Contains(l, "not been given") || strings.Contains(l, "invent")) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not forbid claiming facts it was not given: %q", p.ID(), p.Forbidden())
		}
	}
}

func TestSelectMatchesTheMusic(t *testing.T) {
	all, err := LoadPersonas(personaDir)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name          string
		genres, moods []string
		want          string
	}{
		{"a rock station", []string{"rock", "punk"}, []string{"aggressive"}, "dutch_mahoney"},
		{"a disco station", []string{"disco", "funk"}, []string{"euphoric"}, "roxy_sinclair"},
		{"a hip hop station", []string{"hip hop"}, []string{"cool"}, "prosper_okonkwo"},
		{"a classical station", []string{"classical", "orchestral"}, []string{"grand"}, "wendell_pike"},
		{"a synthwave station", []string{"synthwave", "ambient"}, []string{"nocturnal"}, "midnight_vale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, scores := SelectPersona(all, tc.genres, tc.moods)
			if got.ID() != tc.want {
				t.Errorf("chose %s for %v/%v, want %s (scores: %v)",
					got.ID(), tc.genres, tc.moods, tc.want, describe(scores))
			}
		})
	}
}

// TestSelectPrefersGenreOverMood: a jock is hired for the MUSIC. A melancholic
// rock station wants the rock presenter, not the ambient one who also does
// melancholy.
func TestSelectPrefersGenreOverMood(t *testing.T) {
	all, err := LoadPersonas(personaDir)
	if err != nil {
		t.Fatal(err)
	}
	got, scores := SelectPersona(all, []string{"metal"}, []string{"melancholic"})
	if got.ID() != "dutch_mahoney" {
		t.Errorf("chose %s for metal/melancholic, want dutch_mahoney (scores: %v)", got.ID(), describe(scores))
	}
}

// TestSelectAlwaysReturnsAJock: silence is not an improvement on an imperfect
// presenter, so a station whose music matches nothing still gets one.
func TestSelectAlwaysReturnsAJock(t *testing.T) {
	all, err := LoadPersonas(personaDir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := SelectPersona(all, []string{"gamelan"}, []string{"unclassifiable"})
	if got == nil {
		t.Fatal("no jock for an unmatched station")
	}

	// And the same one every time, so a restart does not change presenter.
	again, _ := SelectPersona(all, []string{"gamelan"}, nil)
	if again.ID() != got.ID() {
		t.Errorf("unmatched station got %s then %s; the choice must be stable across restarts", got.ID(), again.ID())
	}

	if p, _ := SelectPersona(nil, []string{"rock"}, nil); p != nil {
		t.Error("returned a persona from an empty set")
	}
}

// TestSelectNormalisesTags: dossiers write "hip hop", cards may say "hip-hop".
// Matching is on whole names, so "rock" must not match "rockabilly".
func TestSelectNormalisesTags(t *testing.T) {
	all, err := LoadPersonas(personaDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{"hip hop", "Hip-Hop", "HIP_HOP", "  hip hop  "} {
		if got, _ := SelectPersona(all, []string{spelling}, nil); got.ID() != "prosper_okonkwo" {
			t.Errorf("%q chose %s, want prosper_okonkwo", spelling, got.ID())
		}
	}

	// A substring must not count as a match.
	_, scores := SelectPersona(all, []string{"rockabilly"}, nil)
	for _, s := range scores {
		if s.Persona.ID() == "dutch_mahoney" && s.Score > 0 {
			t.Errorf("rockabilly matched the rock jock on a substring: %v", s.Matched)
		}
	}
}

func describe(scores []PersonaScore) string {
	var b strings.Builder
	for _, s := range scores {
		b.WriteString(s.Persona.ID())
		b.WriteString("=")
		b.WriteString(itoaLocal(s.Score))
		b.WriteString(" ")
	}
	return b.String()
}

func itoaLocal(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}
