// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import "testing"

func TestNamesFromPath(t *testing.T) {
	// Every case here is a real filename shape from the development library.
	for _, tc := range []struct{ path, artist, title string }{
		{"/m/Agay - Agressive Audio.mp3", "Agay", "Agressive Audio"},
		{"/m/Bisrock - Ang Gugma Ko Kanimo.mp3", "Bisrock", "Ang Gugma Ko Kanimo"},
		{"/m/02. Take A Chance On Me.mp3", "", "Take A Chance On Me"},
		{"/m/13 - Creep.flac", "", "Creep"},
		{"/m/AlbularyongButa.mp3", "", "Albularyong Buta"},
		{"/m/Angelita San.mp3", "", "Angelita San"},
		{"/m/some_track_name.m4a", "", "some track name"},

		// A bare hyphen must NOT split: doing so invents an artist called
		// "Jean" and a song called "Luc Ponty".
		{"/m/Jean-Luc Ponty.mp3", "", "Jean-Luc Ponty"},

		// A real title keeps its spacing; CamelCase splitting must not touch it.
		{"/m/01 The Fellowship of the Ring.mp3", "", "The Fellowship of the Ring"},

		// Nothing usable.
		{"/m/._resourcefork.mp3", "", ""},
		{"/m/.mp3", "", ""},
	} {
		artist, title := NamesFromPath(tc.path)
		if artist != tc.artist || title != tc.title {
			t.Errorf("NamesFromPath(%q) = (%q, %q), want (%q, %q)", tc.path, artist, title, tc.artist, tc.title)
		}
	}
}

// TestNamesFromPathRefusesAnUncertainArtist is the safety property, and it is
// the reason this file is conservative rather than clever.
//
// A guessed artist is looked up against MusicBrainz, and a wrong match hands
// the DJ TRUE facts about the WRONG person to say on air. That is worse than
// silence, so an artist is only claimed on an unambiguous " - " split.
func TestNamesFromPathRefusesAnUncertainArtist(t *testing.T) {
	for _, path := range []string{
		"/m/AlbularyongButa.mp3",
		"/m/Angelita San.mp3",
		"/m/02. Take A Chance On Me.mp3",
		"/m/Jean-Luc Ponty.mp3",
		"/m/Untitled.mp3",
	} {
		if artist, _ := NamesFromPath(path); artist != "" {
			t.Errorf("NamesFromPath(%q) claimed artist %q from an ambiguous name", path, artist)
		}
	}
}
