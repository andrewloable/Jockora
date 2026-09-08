// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package say

import (
	"strconv"
	"testing"
)

// Jockora-hm2. The DJ said "nineteen hundred and ninety three" for 1993. A
// person says "nineteen ninety three", and a year is in the normal path of a
// normal break -- the dossier's release field is one sentence naming the album
// and the year -- so this was most breaks, not an edge case.

func TestSpeakableYears(t *testing.T) {
	for _, tc := range []struct {
		year int
		want string
	}{
		{1900, "nineteen hundred"},
		{1905, "nineteen oh five"},
		{1910, "nineteen ten"},
		{1993, "nineteen ninety three"},
		{1999, "nineteen ninety nine"},
		{2000, "two thousand"},
		{2005, "two thousand five"},
		{2010, "twenty ten"},
		{2019, "twenty nineteen"},
		{2099, "twenty ninety nine"},
	} {
		if got := YearWords(tc.year); got != tc.want {
			t.Errorf("YearWords(%d) = %q, want %q", tc.year, got, tc.want)
		}
	}
}

// TestSpeakableCollapsesWordForms: the half that is easy to forget, and the
// half the listener actually reported. llmwriter.go already records that a
// model writes "two thousand and seven" on its own, so normalising only the
// DIGITS would leave the reported defect in place.
func TestSpeakableCollapsesWordForms(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"nineteen hundred and ninety three", "nineteen ninety three"},
		{"nineteen hundred ninety three", "nineteen ninety three"},
		{"two thousand and seven", "two thousand seven"},
		{"released in nineteen hundred and eighty five, it still holds up",
			"released in nineteen eighty five, it still holds up"},
		// A bare century is a real year and must survive untouched.
		{"nineteen hundred", "nineteen hundred"},
		{"two thousand", "two thousand"},
	} {
		if got := SpeakableText(tc.in); got != tc.want {
			t.Errorf("SpeakableText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSpeakableYearsOutsideTheRange: YearWords is exported and Jockora-3dp
// calls it with whatever the tag says, which is not always a year at all.
func TestSpeakableYearsOutsideTheRange(t *testing.T) {
	for _, y := range []int{0, 1899, 2100, 12345, -1} {
		if got, want := YearWords(y), strconv.Itoa(y); got != want {
			t.Errorf("YearWords(%d) = %q; outside 1900-2099 it hands the digits back", y, got)
		}
	}
}

func TestSpeakableRanges(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1993-1995", "nineteen ninety three to nineteen ninety five"},
		{"1993 to 1995", "nineteen ninety three to nineteen ninety five"},
		{"the 1975-1979 run", "the nineteen seventy five to nineteen seventy nine run"},
	} {
		if got := SpeakableText(tc.in); got != tc.want {
			t.Errorf("SpeakableText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSpeakableDecades(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1990s", "nineteen nineties"},
		{"1900s", "nineteen hundreds"},
		{"2000s", "two thousands"},
		// TEN IS THE ODD ONE: "tens", not "tenies".
		{"1910s", "nineteen tens"},
		// NOT A DECADE. It does not end in a zero, so it is left for the
		// sidecar -- before the guard it spoke as "nineteen " and stopped.
		{"1915s", "1915s"},
		{"1990's", "nineteen nineties"},
		{"1980s", "nineteen eighties"},
		{"90s", "nineties"},
		{"'93", "ninety three"},
		{"a 70s record", "a seventies record"},
	} {
		if got := SpeakableText(tc.in); got != tc.want {
			t.Errorf("SpeakableText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSpeakableLeavesOtherNumbersAlone: a four-digit number in a music radio
// break is a year essentially always, but only 1900 to 2099 gets the treatment.
// Everything else is left for Kokoro to read however it reads it.
func TestSpeakableLeavesOtherNumbersAlone(t *testing.T) {
	for _, s := range []string{
		"42",
		"12345",
		"1899",
		"2100",
		"call 555 0199 now",
		"the track is called 1234",
		"track 7 of 12",
	} {
		if got := SpeakableText(s); got != s {
			t.Errorf("SpeakableText(%q) = %q; it is not a year and must pass through", s, got)
		}
	}
}

func TestSpeakableInSentence(t *testing.T) {
	const in = "Feeder's 8.18 came out in 1993, and it has not aged a day."
	const want = "Feeder's 8.18 came out in nineteen ninety three, and it has not aged a day."
	if got := SpeakableText(in); got != want {
		t.Errorf("SpeakableText:\n got %q\nwant %q", got, want)
	}
}

// TestSpeakableIdempotent: the one that catches a rewrite rule eating its own
// output -- a collapse that turns "nineteen hundred" into "nineteen", say.
func TestSpeakableIdempotent(t *testing.T) {
	for _, s := range []string{
		"released in 1993 and again in 2005",
		"the 1990s were 1993-1999",
		"nineteen hundred and ninety three",
		"a 70s record from '93",
		"nineteen hundred",
	} {
		once := SpeakableText(s)
		if twice := SpeakableText(once); twice != once {
			t.Errorf("SpeakableText is not idempotent on %q:\n once %q\ntwice %q", s, once, twice)
		}
	}
}
