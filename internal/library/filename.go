// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"path/filepath"
	"regexp"
	"strings"
)

// A filename is METADATA THAT EXISTS, not a guess. Reading "Agay - Agressive
// Audio.mp3" as an artist and a title is not the same kind of act as inventing
// a birthplace: the words are really there, put there by whoever made the file.
// Measured on the development library, 519 of 7,595 playable tracks carry
// NEITHER an artist nor a title tag, and every one of them was getting an empty
// dossier and personality-only talk forever.

var (
	// "01 ", "02. ", "13 - " and so on. Album rips are full of these and they
	// are never part of the title.
	trackNumberRE = regexp.MustCompile(`^\s*\d{1,3}\s*[.\-_)]?\s+`)
	// A bare leading number with no separator, as in "01Numb". Go's regexp is
	// RE2 and has no lookahead, so the letter is captured and put back.
	bareNumberRE = regexp.MustCompile(`^\d{1,3}([A-Z])`)
	// CamelCase runs, for "AlbularyongButa".
	camelRE = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	// Collapsed whitespace after the substitutions above.
	spacesRE = regexp.MustCompile(`\s+`)
)

// NamesFromPath reads an artist and a title out of a file's name.
//
// It returns what it is CONFIDENT of and nothing more. An artist is only
// reported when the name splits cleanly on " - ", because a wrong artist is
// materially worse than no artist: it is looked up against MusicBrainz, and a
// bad match hands the DJ true facts about the wrong person to say on air. A
// bare "AlbularyongButa.mp3" therefore yields a title and no artist.
//
// Either return may be empty. The caller decides what to do with that.
func NamesFromPath(path string) (artist, title string) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.TrimSpace(base)
	if base == "" {
		return "", ""
	}

	// macOS resource forks and other dotfiles are not tracks.
	if strings.HasPrefix(base, ".") {
		return "", ""
	}

	base = trackNumberRE.ReplaceAllString(base, "")
	base = bareNumberRE.ReplaceAllString(base, "$1")

	// Split on " - " with spaces, never a bare hyphen: "Jean-Luc" and
	// "Twenty-One" are one word, and splitting them invents an artist.
	if left, right, found := strings.Cut(base, " - "); found {
		artist, title = tidy(left), tidy(right)
		if artist == "" || title == "" {
			// A dangling separator says nothing useful; keep whichever side
			// survived as the title and claim no artist.
			return "", tidy(base)
		}
		return artist, title
	}

	return "", tidy(base)
}

// tidy turns a filename fragment into something speakable.
func tidy(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	// Only split CamelCase when the fragment has no spaces of its own.
	// "The Fellowship of the Ring" must survive untouched.
	if !strings.Contains(strings.TrimSpace(s), " ") {
		s = camelRE.ReplaceAllString(s, "$1 $2")
	}
	return strings.TrimSpace(spacesRE.ReplaceAllString(s, " "))
}
