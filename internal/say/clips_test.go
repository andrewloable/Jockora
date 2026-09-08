// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package say

import (
	"os"
	"testing"
)

// TestSpeakableShowsTheClipScripts prints exactly what the sidecar will be
// asked to say for the six cases Jockora-hm2's VERIFY calls for, so a person
// can read them before anyone renders anything.
//
// Skipped unless asked for by name: it asserts nothing and exists to be read.
func TestSpeakableShowsTheClipScripts(t *testing.T) {
	if os.Getenv("JOCKORA_SHOW_CLIPS") == "" {
		t.Skip("set JOCKORA_SHOW_CLIPS=1 to print the clip scripts")
	}
	for _, s := range []string{
		"Feeder's 8.18 came out in 1993, and it has not aged a day.",
		"Recorded in 1905, which is about as far back as this dial goes.",
		"That one landed in 2000, right on the turn.",
		"A 2010 record that still sounds like next week.",
		"Their 1993-1995 run is the one people mean.",
		"Pure 1990s, and unembarrassed about it.",
		"nineteen hundred and ninety three was a good year",
		"released two thousand and seven on a tiny label",
		"a 70s record from '93, somehow",
	} {
		t.Logf("\n  IN : %s\n  OUT: %s", s, SpeakableText(s))
	}
}
