// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// THE TAGS FILL A HOLE, NOTHING MORE. The scanner reads album and year out of
// every file and the DJ was never shown either, so an unenriched record left
// the jock with a bare title. They are rendered ONLY where there is no dossier:
// a track that has one already carries a release field somebody reasoned about,
// and a raw tag beside it would sooner or later contradict it.

func tagPrompt(t *testing.T, in PromptInput) string {
	t.Helper()
	in.Persona = testPersona(t)
	if in.WindowSeconds == 0 {
		in.WindowSeconds = 9
	}
	got, err := BuildBreakPrompt(in)
	if err != nil {
		t.Fatalf("BuildBreakPrompt: %v", err)
	}
	return got
}

func TestTrackTagsAlbumRenderedWhenDossierNil(t *testing.T) {
	got := tagPrompt(t, PromptInput{
		CurrentArtist: "Bloc Party", CurrentTitle: "Banquet",
		CurrentAlbum: "Silent Alarm", CurrentYear: 2005,
	})
	if !strings.Contains(got, "album: Silent Alarm") {
		t.Errorf("prompt has no album line for an unenriched track:\n%s", got)
	}
}

func TestTrackTagsYearParentheticalNotOwnLine(t *testing.T) {
	got := tagPrompt(t, PromptInput{
		CurrentArtist: "Bloc Party", CurrentTitle: "Banquet",
		CurrentAlbum: "Silent Alarm", CurrentYear: 2005,
	})
	if !strings.Contains(got, "album: Silent Alarm (2005)") {
		t.Errorf("the year is not on the album line:\n%s", got)
	}
	// A BARE YEAR LINE READS AS A FACT STATED FLATLY, and a tag year is often
	// the reissue rather than the release -- confidently wrong in a way a
	// dossier release date is not.
	bare := regexp.MustCompile(`(?m)^\s*(year|released)?\s*:?\s*\(?\d{4}\)?\s*$`)
	for _, line := range strings.Split(got, "\n") {
		if bare.MatchString(line) {
			t.Errorf("a bare year line: %q", line)
		}
	}
}

func TestTrackTagsAlbumOmittedWhenDossierPresent(t *testing.T) {
	got := tagPrompt(t, PromptInput{
		Current:       testDossier(3),
		CurrentArtist: "Bloc Party", CurrentTitle: "Banquet",
		CurrentAlbum: "Silent Alarm", CurrentYear: 2005,
	})
	if strings.Contains(got, "Silent Alarm") {
		t.Errorf("a raw tag sits beside a reasoned dossier:\n%s", got)
	}
}

func TestTrackTagsNoAlbumKeepsNothingKnownLine(t *testing.T) {
	got := tagPrompt(t, PromptInput{CurrentArtist: "Bloc Party", CurrentTitle: "Banquet"})
	if !strings.Contains(got, "(nothing else is known about this record)") {
		t.Errorf("the no-dossier line is gone for a track with no tags either:\n%s", got)
	}
	// A YEAR WITH NO ALBUM IS STILL NOT A LINE. It has nothing to hang on.
	got = tagPrompt(t, PromptInput{CurrentArtist: "Bloc Party", CurrentTitle: "Banquet", CurrentYear: 2005})
	if strings.Contains(got, "2005") {
		t.Errorf("a year printed with no album to sit beside:\n%s", got)
	}
}

func TestTrackTagsNextTrackCarriesThemToo(t *testing.T) {
	// A break that says what is coming up is half of what a break is for.
	got := tagPrompt(t, PromptInput{
		NextArtist: "Bloc Party", NextTitle: "Banquet",
		NextAlbum: "Silent Alarm", NextYear: 2005,
	})
	if !strings.Contains(got, "album: Silent Alarm (2005)") {
		t.Errorf("the coming track has no album line:\n%s", got)
	}
}

func TestTrackTagsNoFactsBranchPermitsSleeveYear(t *testing.T) {
	got := tagPrompt(t, PromptInput{
		CurrentArtist: "Bloc Party", CurrentTitle: "Banquet",
		CurrentAlbum: "Silent Alarm", CurrentYear: 2005,
	})
	if !strings.Contains(got, "YOU HAVE NO FACTS ABOUT EITHER TRACK") {
		t.Fatalf("the no-facts block is gone:\n%s", got)
	}
	// It used to forbid naming a year outright. With a sleeve year on the page
	// that is a contradiction, and a model handed one is a model that ignores
	// the rest of the paragraph too.
	if strings.Contains(got, "Do not name a year, a place, a label or a band member as fact.") {
		t.Error("the block still forbids the year it now prints")
	}
	// Every OTHER prohibition stands exactly as written.
	for _, forbidden := range []string{"place", "label", "band member"} {
		if !strings.Contains(got, forbidden) {
			t.Errorf("the no-facts block no longer forbids naming a %s", forbidden)
		}
	}
}

func TestTrackTagsSchemaUnchanged(t *testing.T) {
	// These are CONTEXT the jock speaks from, exactly as the title is, not
	// facts it declares. asserted_facts stays an enum of resolvable dossier
	// facts, so grounding is untouched.
	with, err := json.Marshal(BreakSchema(nil, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	tagPrompt(t, PromptInput{
		CurrentArtist: "Bloc Party", CurrentTitle: "Banquet",
		CurrentAlbum: "Silent Alarm", CurrentYear: 2005,
	})
	without, err := json.Marshal(BreakSchema(nil, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if string(with) != string(without) {
		t.Error("the break schema moved when the tags were added")
	}
}
