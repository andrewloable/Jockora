// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackLoadsEveryExistingPersona is the compatibility guarantee: a pack IS
// the persona file, so every card written before the format existed is already
// a valid pack of one jock.
func TestPackLoadsEveryExistingPersona(t *testing.T) {
	files, err := filepath.Glob("../../personas/*.toml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no persona files found: %v", err)
	}
	for _, f := range files {
		p, err := LoadPack(f)
		if err != nil {
			t.Errorf("%s does not load as a pack: %v", filepath.Base(f), err)
			continue
		}
		if p.Persona == nil || p.Persona.ID() == "" {
			t.Errorf("%s loaded without a persona", filepath.Base(f))
		}
	}
}

func writePack(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "pack.toml")
	head := `id = "test_jock"
name = "Test Jock"
voice_id = "kokoro:am_puck"
speech_style = "Short."
personality = "Brief."
good_for_genres = ["rock"]
good_for_moods = ["raw"]
forbidden = ["never says 'folks'"]
`
	if err := os.WriteFile(path, []byte(head+body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPackCarriesAdvertsAndChain(t *testing.T) {
	path := writePack(t, `
[chain]
reverb_mix = 0.15
high_pass = 90

[[advert]]
id = "brindle-1"
brand = "Brindlewax Motor Oil"
script = "Brindlewax Motor Oil. It is oil, it is ours, and it goes in the hole at the top of the engine. Other oils make claims. Brindlewax makes no claims whatsoever, and that is the honest position."
`)
	p, err := LoadPack(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Adverts) != 1 || p.Adverts[0].Brand != "Brindlewax Motor Oil" {
		t.Errorf("adverts = %+v", p.Adverts)
	}
	if p.Chain == nil || p.Chain.ReverbMix != 0.15 || p.Chain.HighPass != 90 {
		t.Errorf("chain = %+v", p.Chain)
	}
	if got := p.Ads(); len(got) != 1 || got[0].ID != "brindle-1" {
		t.Errorf("Ads() = %+v", got)
	}
}

// TestPackRefusesARealBrand is the reason adverts are validated at INSTALL
// rather than at airtime.
//
// A pack is this project's only community contribution surface, so it is
// exactly where a real company's name arrives from outside. A pack that would
// say one on air must fail to install, loudly, rather than fail quietly in
// front of a listener.
func TestPackRefusesARealBrand(t *testing.T) {
	path := writePack(t, `
[[advert]]
id = "bad-1"
brand = "Coca-Cola"
script = "Drink Coca-Cola, the real thing."
`)
	if _, err := LoadPack(path); err == nil {
		t.Fatal("a pack naming a real brand installed successfully")
	}
}

// TestPackRefusesANewerFormat: silently ignoring fields a future version added
// is how a jock arrives sounding subtly wrong with nothing in the log.
func TestPackRefusesANewerFormat(t *testing.T) {
	path := writePack(t, "\npack_version = 99\n")
	_, err := LoadPack(path)
	if err == nil {
		t.Fatal("a pack from a newer format loaded anyway")
	}
	if !strings.Contains(err.Error(), "upgrade jockora") {
		t.Errorf("the error does not say what to do about it: %v", err)
	}
}

// TestPackRoundTrips: export then re-import must lose nothing, or sharing a
// jock quietly degrades it.
func TestPackRoundTrips(t *testing.T) {
	path := writePack(t, `
author = "Andrew Loable"
licence = "CC-BY-4.0"

[chain]
reverb_mix = 0.2
high_pass = 80

[[advert]]
id = "ad-1"
brand = "Hollowfield Dental"
script = "Hollowfield Dental has been looking into the mouths of this town for thirty years and we have seen worse than whatever you are hiding in there. Come in. Bring the tooth. We will not say a word about it."
`)
	original, err := LoadPack(path)
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "exported.toml")
	if err := WritePack(out, original); err != nil {
		t.Fatal(err)
	}
	again, err := LoadPack(out)
	if err != nil {
		t.Fatalf("the exported pack does not load: %v", err)
	}

	if again.Persona.ID() != original.Persona.ID() ||
		again.Persona.Name() != original.Persona.Name() ||
		again.Persona.VoiceID() != original.Persona.VoiceID() {
		t.Error("identity was lost in the round trip")
	}
	if len(again.Persona.Forbidden()) != len(original.Persona.Forbidden()) {
		t.Errorf("forbidden rules: %d out, %d back", len(original.Persona.Forbidden()), len(again.Persona.Forbidden()))
	}
	if len(again.Adverts) != 1 || again.Adverts[0].Brand != "Hollowfield Dental" {
		t.Errorf("adverts lost: %+v", again.Adverts)
	}
	if again.Chain == nil || again.Chain.ReverbMix != 0.2 {
		t.Errorf("chain lost: %+v", again.Chain)
	}
	if again.Author != "Andrew Loable" || again.Licence != "CC-BY-4.0" {
		t.Errorf("attribution lost: %q / %q", again.Author, again.Licence)
	}
	// The speech style and personality are what make the jock; losing a line
	// break or a trailing space is survivable, losing the text is not.
	if !strings.Contains(again.Persona.Personality(), "Brief") {
		t.Errorf("personality lost: %q", again.Persona.Personality())
	}
}
