// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// A JOCKPACK IS THE PERSONA FILE, EXTENDED. There is no archive, no manifest
// and no new file format.
//
// That is a deliberate choice against the obvious one. A zip with a manifest
// inside would need packing, unpacking, path validation and a version
// negotiation, and would stop being reviewable in a pull request or a gist. A
// pack is instead ONE TOML FILE: human-readable, diffable, and installable by
// dropping it in the personas directory. Everything the format already carried
// -- id, name, voice, genres, moods, speech style, personality, forbidden --
// stays exactly where it was, so every existing card is already a valid pack.
//
// This matters more than convenience. External code contributions are closed,
// so JockPacks are the ONLY community contribution surface this project has,
// and a format nobody can read in a browser is a surface nobody uses.

// PackVersion is the format version a pack may declare.
//
// Absent means version 1, which is what every hand-written card already is. A
// pack declaring a HIGHER version is refused rather than half-read: silently
// ignoring fields a future format added is how a jock arrives sounding subtly
// wrong with nothing in the log.
const PackVersion = 1

// Advert is one fictional spot carried by a pack.
type Advert struct {
	ID     string `toml:"id"`
	Brand  string `toml:"brand"`
	Script string `toml:"script"`
}

// Chain is the pack's audio processing hint for its jock's voice.
//
// A HINT, not a guarantee. The loudness contract is fixed by the mixer -- music
// at -16 LUFS, speech at -16, ducked to -28, ceiling -1 dBTP -- and a pack
// cannot raise its own jock above the others by asking. What it can carry is
// character: a little room on a late-night voice, none on a shouting one.
type Chain struct {
	ReverbMix float64 `toml:"reverb_mix"` // 0 to 1
	HighPass  float64 `toml:"high_pass"`  // Hz, 0 for none
}

// packFile is the on-disk shape of everything a pack adds to a persona card.
type packFile struct {
	PackVersion int      `toml:"pack_version"`
	Author      string   `toml:"author"`
	Licence     string   `toml:"licence"`
	Adverts     []Advert `toml:"advert"`
	Chain       *Chain   `toml:"chain"`
}

// Pack is a persona plus everything that travels with it.
type Pack struct {
	Persona *Persona
	Author  string
	Licence string
	Adverts []Advert
	Chain   *Chain
}

// LoadPack reads a persona card and whatever pack extras it carries.
//
// A plain persona card with none of them is a valid pack of one jock, which is
// what makes every card written before this format existed still load.
func LoadPack(path string) (*Pack, error) {
	persona, err := LoadPersona(path)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dj: reading pack: %w", err)
	}
	var f packFile
	if _, err := toml.Decode(string(raw), &f); err != nil {
		return nil, fmt.Errorf("dj: parsing pack %s: %w", path, err)
	}

	if f.PackVersion > PackVersion {
		return nil, fmt.Errorf("dj: pack %s declares format version %d, this build understands %d; "+
			"upgrade jockora rather than running it half-read", path, f.PackVersion, PackVersion)
	}

	p := &Pack{Persona: persona, Author: f.Author, Licence: f.Licence, Chain: f.Chain}
	for i, ad := range f.Adverts {
		if strings.TrimSpace(ad.Script) == "" {
			return nil, fmt.Errorf("dj: pack %s advert %d has no script", path, i+1)
		}
		if ad.ID == "" {
			ad.ID = fmt.Sprintf("%s-ad-%d", persona.ID(), i+1)
		}
		// The SAME rules as a generated advert. A pack is a contribution
		// surface, so its adverts are exactly where a real brand name would
		// arrive from outside, and they are checked on load rather than at
		// airtime -- a pack that would say a real company's name on air must
		// fail to install, not fail quietly in front of a listener.
		if err := (Ad{ID: ad.ID, Brand: ad.Brand, Script: ad.Script}).Validate(); err != nil {
			return nil, fmt.Errorf("dj: pack %s advert %q: %w", path, ad.ID, err)
		}
		p.Adverts = append(p.Adverts, ad)
	}
	return p, nil
}

// Ads converts the pack's adverts into the rotation's type.
func (p *Pack) Ads() []Ad {
	out := make([]Ad, 0, len(p.Adverts))
	for _, a := range p.Adverts {
		out = append(out, Ad{ID: a.ID, Brand: a.Brand, Script: a.Script})
	}
	return out
}

// WritePack renders a pack back to TOML so a jock can be shared.
//
// Round-trips through the SAME shape it reads, so exporting and re-importing is
// lossless for everything the format carries.
//
// NO CALLER YET, and when one is written it must read the adverts from the
// DATABASE, not from p.Adverts. Pack adverts are imported into the ads table on
// load (app.ImportPackAds) and the operator edits and deletes them there; a
// pack exported from this struct would carry the adverts it shipped with and
// silently undo every one of those edits for whoever installs it.
func WritePack(path string, p *Pack) error {
	var b strings.Builder
	b.WriteString("# Copyright (C) 2026 Andrew Loable\n")
	b.WriteString("# SPDX-License-Identifier: AGPL-3.0-only\n#\n")
	b.WriteString("# A Jockora JockPack. Drop it in your personas directory to install.\n")
	b.WriteString("# The persona card is IMMUTABLE ground truth: nothing at runtime\n")
	b.WriteString("# may contradict what is written here.\n\n")

	fmt.Fprintf(&b, "pack_version = %d\n", PackVersion)
	if p.Author != "" {
		fmt.Fprintf(&b, "author = %q\n", p.Author)
	}
	if p.Licence != "" {
		fmt.Fprintf(&b, "licence = %q\n", p.Licence)
	}

	per := p.Persona
	fmt.Fprintf(&b, "\nid = %q\nname = %q\nvoice_id = %q\n\n", per.ID(), per.Name(), per.VoiceID())
	fmt.Fprintf(&b, "good_for_genres = %s\n", tomlList(per.GoodForGenres()))
	fmt.Fprintf(&b, "good_for_moods = %s\n\n", tomlList(per.GoodForMoods()))
	fmt.Fprintf(&b, "speech_style = \"\"\"\n%s\n\"\"\"\n\n", per.SpeechStyle())
	fmt.Fprintf(&b, "personality = \"\"\"\n%s\n\"\"\"\n\n", per.Personality())

	b.WriteString("forbidden = [\n")
	for _, f := range per.Forbidden() {
		fmt.Fprintf(&b, "  %q,\n", f)
	}
	b.WriteString("]\n")

	if p.Chain != nil {
		fmt.Fprintf(&b, "\n[chain]\nreverb_mix = %g\nhigh_pass = %g\n", p.Chain.ReverbMix, p.Chain.HighPass)
	}
	for _, ad := range p.Adverts {
		fmt.Fprintf(&b, "\n[[advert]]\nid = %q\nbrand = %q\nscript = %q\n", ad.ID, ad.Brand, ad.Script)
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func tomlList(in []string) string {
	quoted := make([]string, len(in))
	for i, v := range in {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
