// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadPersonas reads every persona card in a directory.
//
// A directory rather than one file because a station picks its jock from what
// it PLAYS, and that choice cannot be made if only one card was ever loaded.
func LoadPersonas(dir string) ([]*Persona, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("dj: reading persona directory %s: %w", dir, err)
	}

	var out []*Persona
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		p, err := LoadPersona(filepath.Join(dir, e.Name()))
		if err != nil {
			// One malformed card must not cost the operator every other jock.
			// It is reported, not swallowed, by being returned alongside the
			// ones that loaded.
			return out, fmt.Errorf("dj: %s: %w", e.Name(), err)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("dj: no persona cards in %s", dir)
	}

	// Stable order, so an unmatched station gets the same jock every restart
	// rather than a different one each time the directory is read.
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, nil
}

// PersonaScore is how well one jock fits a station.
type PersonaScore struct {
	Persona *Persona
	Score   int
	// Matched names the genres and moods that earned the score, so the choice
	// can be logged as a reason rather than as a number.
	Matched []string
}

// genreWeight is worth more than a mood match.
//
// A jock is hired for the MUSIC first. Mood is a real signal but a secondary
// one: a melancholic rock station still wants the rock presenter, not the
// ambient one who also does melancholy.
const genreWeight = 3

// SelectPersona picks the jock that best fits what a station plays.
//
// Genres and moods come from the dossiers of the tracks in rotation, so the
// choice follows the MUSIC rather than a setting somebody has to remember to
// change. Matching is on whole normalised names, never substrings: "rock" must
// not match "rockabilly" by accident, and "hard rock" must not match "rock"
// unless the card actually says so.
func SelectPersona(personas []*Persona, genres, moods []string) (*Persona, []PersonaScore) {
	if len(personas) == 0 {
		return nil, nil
	}

	wantGenres := normaliseSet(genres)
	wantMoods := normaliseSet(moods)

	scores := make([]PersonaScore, 0, len(personas))
	for _, p := range personas {
		s := PersonaScore{Persona: p}
		for _, g := range p.GoodForGenres() {
			if wantGenres[normaliseTag(g)] {
				s.Score += genreWeight
				s.Matched = append(s.Matched, "genre:"+g)
			}
		}
		for _, m := range p.GoodForMoods() {
			if wantMoods[normaliseTag(m)] {
				s.Score++
				s.Matched = append(s.Matched, "mood:"+m)
			}
		}
		scores = append(scores, s)
	}

	// Highest score, then id, so a tie resolves the same way every time. A
	// station whose music matches nothing still gets a jock: silence is not an
	// improvement on an imperfect presenter.
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].Score != scores[j].Score {
			return scores[i].Score > scores[j].Score
		}
		return scores[i].Persona.ID() < scores[j].Persona.ID()
	})
	return scores[0].Persona, scores
}

func normaliseSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, v := range in {
		if n := normaliseTag(v); n != "" {
			out[n] = true
		}
	}
	return out
}

// normaliseTag lowercases and collapses separators, so "Hard Rock", "hard-rock"
// and "hard_rock" are the same tag.
func normaliseTag(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("-", " ", "_", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
