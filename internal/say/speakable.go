// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package say turns written text into the form a person says it in.
//
// A LEAF PACKAGE ON PURPOSE. Two packages need these functions and neither may
// import the other: internal/tts normalises what it is about to speak, and
// internal/dj prints the year into the break prompt in the same words. The tts
// test binary already imports dj, so putting these in tts and having dj import
// them back is an import cycle in the test build -- a hard failure from a
// test-only edge. Importing nothing but the standard library keeps it out of
// everyone's way.
//
// ONE DEFINITION MATTERS BEYOND TIDINESS: if the prompt says "nineteen ninety
// three" and the normaliser produces "nineteen ninety-three", the said-lines
// index sees two different strings and stops recognising a repeat.
package say

import (
	"regexp"
	"strconv"
	"strings"
)

// The DJ said "nineteen hundred and ninety three" for 1993, which is not how a
// person says a year, and a year is in the normal path of a normal break: the
// dossier's release field is one sentence naming the album and the year. So
// this was most breaks rather than an edge case.
//
// It gets there two ways and both are handled here. The model emits digits and
// the sidecar's G2P reads them as a cardinal -- kokoro_server.py does no number
// normalisation at all -- or the model spells it out itself, badly.

var ones = [...]string{
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine",
	"ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen",
	"seventeen", "eighteen", "nineteen",
}

var tens = [...]string{
	"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety",
}

// under100 spells 0-99 with SPACES, never hyphens. See the package comment: a
// hyphen here and a space in the prompt is two strings the repeat index cannot
// match.
func under100(n int) string {
	switch {
	case n < 20:
		return ones[n]
	case n%10 == 0:
		return tens[n/10]
	default:
		return tens[n/10] + " " + ones[n%10]
	}
}

// YearWords says a year the way radio English says it.
//
// The shape is per century rather than a table, with one genuine exception: the
// 2000s are "two thousand five", not "twenty oh five", and that only holds to
// 2009 -- 2010 goes back to the century form as "twenty ten".
//
// Outside 1900-2099 it hands the digits back untouched. See SpeakableText for
// why that range and not a wider one.
func YearWords(year int) string {
	if year < 1900 || year > 2099 {
		return strconv.Itoa(year)
	}
	century, rest := year/100, year%100
	switch {
	case year < 2010 && year >= 2000:
		if rest == 0 {
			return "two thousand"
		}
		return "two thousand " + under100(rest)
	case rest == 0:
		return under100(century) + " hundred"
	case rest < 10:
		// "nineteen oh five", never "nineteen five".
		return under100(century) + " oh " + under100(rest)
	default:
		return under100(century) + " " + under100(rest)
	}
}

// decade turns a tens value into its plural: 90 -> "nineties".
//
// Ten is the one that does not follow the rule -- "tens", not "tenies" -- and
// it is reachable: the 1910s are a real decade for this library. Callers only
// ever pass a multiple of ten, which the patterns below enforce.
func decade(n int) string {
	if n == 10 {
		return "tens"
	}
	return strings.TrimSuffix(tens[n/10], "y") + "ies"
}

// ONLY 1900-2099, AND ONLY AS A WHOLE TOKEN.
//
// ponytail: a four-digit number in a music radio break is a year essentially
// always. The known ceiling is a song title that is a number in some other
// sense -- 1999 the track is still a year, but a title like 2112 is not -- and
// the upgrade path is to pass the dossier's year through explicitly rather than
// pattern match the text. Anything outside the range is left exactly as it is
// and the sidecar reads it however it reads it.
const yearPat = `(?:19\d\d|20\d\d)`

var (
	// Ranges first, so "1993-1995" becomes two years joined by the word a
	// person actually says rather than a hyphen the G2P has to guess at.
	rangeRE = regexp.MustCompile(`\b(` + yearPat + `)\s*-\s*(` + yearPat + `)\b`)

	// Decades before bare years, or "1990s" would be normalised to
	// "nineteen ninetys" by the year rule and the trailing s left stranded.
	// A DECADE ENDS IN A ZERO. Without that digit "1915s" reached decade(15),
	// which indexed a blank in the tens table and spoke the year as "nineteen "
	// with nothing after it. Coverage found the hole; the hole was a real bug.
	longDecadeRE  = regexp.MustCompile(`\b((?:19|20)\d0)'?s\b`)
	shortDecadeRE = regexp.MustCompile(`\b([2-9]0)'?s\b`)
	apostropheRE  = regexp.MustCompile(`'(\d\d)\b`)

	yearRE = regexp.MustCompile(`\b(` + yearPat + `)\b`)

	// THE WORD FORMS THE MODEL PRODUCES ON ITS OWN. llmwriter.go already
	// records that a jock writes "two thousand and seven", and a break in
	// repeatshare_test.go carries exactly that. Normalising only the digits
	// would leave the reported defect in place.
	//
	// A trailing number word is REQUIRED, so a bare "nineteen hundred" -- which
	// is the correct rendering of 1900 -- is not collapsed to "nineteen".
	numWord       = `(?:one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety)`
	hundredAndRE  = regexp.MustCompile(`\b(` + numWord + `) hundred (?:and )?(` + numWord + `)\b`)
	thousandAndRE = regexp.MustCompile(`\b(thousand) and (` + numWord + `)\b`)
)

// SpeakableText rewrites a line into the form a person says it in.
//
// CALLED AT RENDER TIME AND NOWHERE EARLIER. The said-lines index records the
// break text and the length check counts its words, so normalising before
// either would make the stored text differ from what the validator saw. Only
// the spoken form changes.
//
// Idempotent: every rule replaces digits with words, or removes a word that
// cannot reappear, so a second pass finds nothing to do.
func SpeakableText(s string) string {
	s = rangeRE.ReplaceAllString(s, "$1 to $2")

	s = longDecadeRE.ReplaceAllStringFunc(s, func(m string) string {
		y, _ := strconv.Atoi(m[:4]) //nolint:errcheck // the pattern is four digits
		if y%100 == 0 {
			// 1900s: "nineteen hundreds".
			return YearWords(y) + "s"
		}
		return under100(y/100) + " " + decade(y%100)
	})

	s = shortDecadeRE.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.Atoi(m[:2]) //nolint:errcheck // the pattern is two digits
		return decade(n)
	})

	s = apostropheRE.ReplaceAllStringFunc(s, func(m string) string {
		n, _ := strconv.Atoi(m[1:]) //nolint:errcheck // the pattern is two digits
		return under100(n)
	})

	s = yearRE.ReplaceAllStringFunc(s, func(m string) string {
		y, _ := strconv.Atoi(m) //nolint:errcheck // the pattern is four digits
		return YearWords(y)
	})

	s = hundredAndRE.ReplaceAllString(s, "$1 $2")
	return thousandAndRE.ReplaceAllString(s, "$1 $2")
}
