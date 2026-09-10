// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

//go:build liveprobe

// Writing out one real break request, so a prompt change can be judged against
// a real model rather than against a unit test.
//
// THE PROMPT IS THE PRODUCT HERE. Three dossier defects in this project were
// only ever found by running the real thing -- field echoing, a worked example
// copied verbatim, and supplied lyrics quoted into a persisted field -- and
// none of them could fail a unit test, because every one produced output the
// schema accepted. The break writer has the same property.
//
// It writes a file rather than calling a model, because the model that matters
// is the one on the deployment host and it listens on a docker bridge address
// only reachable from there. Build the body here, copy it over, curl it in.
//
//	go test -tags liveprobe ./internal/dj/ -run TestLiveProbeBreakRequest
package dj

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

func TestLiveProbeBreakRequest(t *testing.T) {
	out := os.Getenv("JOCKORA_PROBE_OUT")
	if out == "" {
		t.Skip("set JOCKORA_PROBE_OUT to the file to write the request to")
	}

	// A DOSSIER SHAPED LIKE THE REAL ONES. Taken from the reporting library:
	// artist facts are a MusicBrainz record rendered into sentences, which is
	// Country, Type and BeginYear and nothing else, and release is "Album,
	// Year". The question the probe answers is whether the writer still
	// reaches for those when it has been given something better.
	cur := &enrich.Dossier{
		Confidence:     enrich.ConfidenceHigh,
		SubjectSummary: "A narrator drives out of a city he has decided not to come back to, and keeps talking to somebody who is not in the car.",
		Themes:         []string{"leaving", "insomnia", "regret"},
		StationTags:    []string{"synthwave"},
		Mood:           []string{"nocturnal"},
		ArtistFacts:    []string{"Howard Shore is a Canadian score composer.", "Howard Shore was formed in 1946."},
		Release:        "Nightdrive, 1984",
	}
	next := &enrich.Dossier{
		Confidence:     enrich.ConfidenceHigh,
		SubjectSummary: "Someone counts the hours until a shift ends and admits they have nowhere to be afterwards.",
		Themes:         []string{"work", "loneliness"},
		StationTags:    []string{"synthwave"},
		Mood:           []string{"melancholy"},
		ArtistFacts:    []string{"The group is from Sweden.", "The group formed in 1979."},
		Release:        "Graveyard Shift, 1986",
	}

	prompt, err := BuildBreakPrompt(PromptInput{
		Persona:       testPersona(t),
		Current:       cur,
		CurrentArtist: "Nightdrive", CurrentTitle: "Not Going Back",
		Next:      next,
		NextArtist: "Shift Work", NextTitle: "Clocking Off",
		Placement:     "outro",
		WindowSeconds: 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(map[string]any{
		"prompt":      prompt,
		// EVERY SAMPLER SETTING COMES FROM THE CONSTANT PRODUCTION USES, never
		// from a number typed here. The first version of this probe hardcoded
		// temperature 0.2 -- which is the DOSSIER temperature -- and measured
		// the break writer at a setting the break writer never runs at. Low
		// temperature is where a model loops, so the probe manufactured the
		// defect it was sent to look for, and two rounds of conclusions were
		// drawn from it before anybody checked. WritingTemperature is 0.8.
		"n_predict":   TokensFor(50),
		"temperature": enrich.WritingTemperature,
		"json_schema": BreakSchema(nil, cur, next),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d bytes to %s", len(body), out)
	t.Logf("resolvable ids: %v", ResolvableFactIDs(nil, cur, next))
}
