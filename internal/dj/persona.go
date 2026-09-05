// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package dj holds the DJ's identity and everything that writes in its voice.
package dj

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Persona is a DJ's immutable identity.
//
// The card is GROUND TRUTH and nothing at runtime may contradict it. Distilled
// memory is a separate, mutable overlay layered on top; if the card itself could
// be edited in place, weeks of summarisation drift would turn a cynical DJ
// cheerful and nothing would catch it.
//
// Immutability is enforced structurally: every field is unexported, there are no
// setters, and the slice accessors hand out copies rather than the backing
// arrays.
type Persona struct {
	id            string
	name          string
	voiceID       string
	goodForGenres []string
	goodForMoods  []string
	speechStyle   string
	personality   string
	forbidden     []string
}

// personaFile is the on-disk shape. It exists separately from Persona so that
// TOML decoding cannot write into the immutable type.
type personaFile struct {
	ID            string   `toml:"id"`
	Name          string   `toml:"name"`
	VoiceID       string   `toml:"voice_id"`
	GoodForGenres []string `toml:"good_for_genres"`
	GoodForMoods  []string `toml:"good_for_moods"`
	SpeechStyle   string   `toml:"speech_style"`
	Personality   string   `toml:"personality"`
	Forbidden     []string `toml:"forbidden"`
}

// LoadPersona reads a persona card from disk.
//
// Personas live in files, not in Go source, so that adding a second DJ is a file
// rather than a code change.
func LoadPersona(path string) (*Persona, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dj: reading persona: %w", err)
	}

	var f personaFile
	if _, err := toml.Decode(string(raw), &f); err != nil {
		return nil, fmt.Errorf("dj: parsing persona %s: %w", path, err)
	}

	// Every required field is checked by name, because "invalid persona" sends
	// the operator back to the file with nothing to look for.
	for _, req := range []struct {
		field string
		value string
	}{
		{"id", f.ID},
		{"name", f.Name},
		{"voice_id", f.VoiceID},
		{"speech_style", f.SpeechStyle},
		{"personality", f.Personality},
	} {
		if strings.TrimSpace(req.value) == "" {
			return nil, fmt.Errorf("dj: persona %s is missing the required field %q", path, req.field)
		}
	}

	return &Persona{
		id:            f.ID,
		name:          f.Name,
		voiceID:       f.VoiceID,
		goodForGenres: append([]string(nil), f.GoodForGenres...),
		goodForMoods:  append([]string(nil), f.GoodForMoods...),
		speechStyle:   strings.TrimSpace(f.SpeechStyle),
		personality:   strings.TrimSpace(f.Personality),
		forbidden:     append([]string(nil), f.Forbidden...),
	}, nil
}

// Read-only accessors. The slice ones return copies: handing out the backing
// array would let a caller rewrite the DJ's boundaries by accident.

func (p *Persona) ID() string          { return p.id }
func (p *Persona) Name() string        { return p.name }
func (p *Persona) VoiceID() string     { return p.voiceID }
func (p *Persona) SpeechStyle() string { return p.speechStyle }
func (p *Persona) Personality() string { return p.personality }

func (p *Persona) GoodForGenres() []string { return append([]string(nil), p.goodForGenres...) }
func (p *Persona) GoodForMoods() []string  { return append([]string(nil), p.goodForMoods...) }
func (p *Persona) Forbidden() []string     { return append([]string(nil), p.forbidden...) }

// PromptBlock renders the card for a writing prompt.
//
// Every forbidden rule is included: a boundary the writer never sees is
// decoration.
func (p *Persona) PromptBlock() string {
	var b strings.Builder

	fmt.Fprintf(&b, "YOU ARE %s.\n\n", p.name)
	b.WriteString("CHARACTER\n")
	b.WriteString(indent(p.personality))
	b.WriteString("\n\nHOW YOU SPEAK\n")
	b.WriteString(indent(p.speechStyle))

	if len(p.forbidden) > 0 {
		b.WriteString("\n\nYOU NEVER DO THESE THINGS\n")
		for _, rule := range p.forbidden {
			b.WriteString("  - " + rule + "\n")
		}
	}
	return b.String()
}

func indent(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "  " + strings.TrimSpace(l)
	}
	return strings.Join(lines, "\n")
}
