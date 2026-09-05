// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const repoPersona = "../../personas/midnight_vale.toml"

func TestLoadsValidPersona(t *testing.T) {
	p, err := LoadPersona(repoPersona)
	if err != nil {
		t.Fatalf("LoadPersona: %v", err)
	}

	if p.ID() != "midnight_vale" {
		t.Errorf("ID = %q", p.ID())
	}
	if !strings.Contains(p.Name(), "Vale") {
		t.Errorf("Name = %q", p.Name())
	}
	if p.VoiceID() != "kokoro:am_michael" {
		t.Errorf("VoiceID = %q", p.VoiceID())
	}
	if len(p.GoodForGenres()) == 0 || len(p.GoodForMoods()) == 0 {
		t.Errorf("genres = %v, moods = %v", p.GoodForGenres(), p.GoodForMoods())
	}
	if len(p.SpeechStyle()) < 40 || len(p.Personality()) < 80 {
		t.Errorf("speech_style (%d chars) or personality (%d chars) is too thin to shape a voice",
			len(p.SpeechStyle()), len(p.Personality()))
	}
	if len(p.Forbidden()) == 0 {
		t.Error("forbidden is empty; a persona with no boundaries has no character")
	}
}

func TestRejectsMissingRequiredField(t *testing.T) {
	for _, field := range []string{"id", "name", "voice_id", "speech_style", "personality"} {
		path := personaWithout(t, field)
		_, err := LoadPersona(path)
		if err == nil {
			t.Errorf("omitting %s produced no error", field)
			continue
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("omitting %s gave %q, which does not name the missing field", field, err)
		}
	}
}

func TestRejectsMissingFile(t *testing.T) {
	if _, err := LoadPersona(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("LoadPersona accepted a missing file")
	}
}

func TestRejectsMalformedTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("id = \nthis is not toml ["), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPersona(path); err == nil {
		t.Error("LoadPersona accepted malformed TOML")
	}
}

// TestPersonaIsImmutable is the invariant, not a style preference. Distilled
// memory is a mutable overlay that may never contradict the card; if the card
// itself can be edited in place, weeks of summarisation drift turn a cynical DJ
// cheerful and nothing catches it.
func TestPersonaIsImmutable(t *testing.T) {
	rt := reflect.TypeOf(Persona{})
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).IsExported() {
			t.Errorf("Persona field %s is exported and therefore writable", rt.Field(i).Name)
		}
	}

	pt := reflect.TypeOf(&Persona{})
	for i := 0; i < pt.NumMethod(); i++ {
		name := pt.Method(i).Name
		if strings.HasPrefix(name, "Set") || strings.HasPrefix(name, "Add") ||
			strings.HasPrefix(name, "Update") || strings.HasPrefix(name, "Append") {
			t.Errorf("Persona has a mutating method %s", name)
		}
	}
}

// TestPersonaReturnsSliceCopies: handing out the backing array would let a
// caller rewrite the DJ's boundaries by accident.
func TestPersonaReturnsSliceCopies(t *testing.T) {
	p, err := LoadPersona(repoPersona)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		get  func() []string
	}{
		{"GoodForGenres", p.GoodForGenres},
		{"GoodForMoods", p.GoodForMoods},
		{"Forbidden", p.Forbidden},
	} {
		first := c.get()
		if len(first) == 0 {
			t.Fatalf("%s is empty", c.name)
		}
		original := first[0]
		first[0] = "TAMPERED"

		if again := c.get(); again[0] != original {
			t.Errorf("%s handed out its backing array: a caller rewrote %q to %q",
				c.name, original, again[0])
		}
	}
}

// TestForbiddenRulesReachThePrompt: a boundary the writer never sees is
// decoration.
func TestPersonaRendersForPrompt(t *testing.T) {
	p, err := LoadPersona(repoPersona)
	if err != nil {
		t.Fatal(err)
	}

	out := p.PromptBlock()
	if !strings.Contains(out, p.Name()) {
		t.Error("the prompt block omits the DJ's name")
	}
	for _, rule := range p.Forbidden() {
		if !strings.Contains(out, rule) {
			t.Errorf("the prompt block omits a forbidden rule: %q", rule)
		}
	}
	if !strings.Contains(out, p.SpeechStyle()) {
		t.Error("the prompt block omits the speech style")
	}
}

// personaWithout writes the repository persona with one field removed.
func personaWithout(t *testing.T, field string) string {
	t.Helper()
	raw, err := os.ReadFile(repoPersona)
	if err != nil {
		t.Fatal(err)
	}

	var kept []string
	skipping := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, field+" =") || strings.HasPrefix(trimmed, field+"=") {
			skipping = strings.HasSuffix(trimmed, `"""`) && !strings.HasSuffix(trimmed, `""""""`)
			continue
		}
		if skipping {
			if strings.HasSuffix(trimmed, `"""`) {
				skipping = false
			}
			continue
		}
		kept = append(kept, line)
	}

	path := filepath.Join(t.TempDir(), "partial.toml")
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
