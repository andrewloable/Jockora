// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"strings"
	"testing"
)

// Jockora-iyw.2. THE OPERATOR SELLS AIRTIME. The advert is written from what
// they say about a real product, replacing a generator that invented ten
// fictional advertisers at station start.

type briefWriter struct {
	replies []string
	prompts []string
	err     error
}

func (s *briefWriter) WriteBreak(_ context.Context, prompt string, _ map[string]any, _ int) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if s.err != nil {
		return "", s.err
	}
	i := len(s.prompts) - 1
	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	return s.replies[i], nil
}

func stillwater() AdBrief {
	return AdBrief{
		Brand:    "Stillwater Ceramics",
		About:    "a pottery that has made exactly one mug for eleven years",
		Delivery: "deadpan, unhurried, no exclamation marks",
	}
}

const goodAd = `{"brand":"Stillwater Ceramics","script":"Stillwater Ceramics have made one mug ` +
	`for eleven years. They tried a second and it did not feel right, so they stopped. ` +
	`It is heavier than you expect and that is the entire point."}`

func TestAdBriefHappy(t *testing.T) {
	w := &briefWriter{replies: []string{goodAd}}
	ad, err := WriteAdFromBrief(context.Background(), w, stillwater())
	if err != nil {
		t.Fatalf("WriteAdFromBrief: %v", err)
	}
	if ad.Brand != "Stillwater Ceramics" {
		t.Errorf("brand = %q", ad.Brand)
	}
	if !strings.Contains(ad.Script, "eleven years") {
		t.Errorf("script = %q", ad.Script)
	}
}

func TestAdBriefPromptCarriesBrief(t *testing.T) {
	in := stillwater()
	w := &briefWriter{replies: []string{goodAd}}
	if _, err := WriteAdFromBrief(context.Background(), w, in); err != nil {
		t.Fatal(err)
	}
	p := w.prompts[0]
	for _, want := range []string{
		"Stillwater Ceramics",
		"a pottery that has made exactly one mug for eleven years",
		"deadpan, unhurried, no exclamation marks",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt is missing %q:\n%s", want, p)
		}
	}
	// FENCED AS DATA -- and BETWEEN the fences, not merely near one. Checking
	// that a fence exists anywhere passes happily while the opening one is
	// deleted and the operator's words sit in the instructions.
	open := strings.Index(p, "```")
	closed := strings.Index(p[open+3:], "```")
	if open < 0 || closed < 0 {
		t.Fatalf("the brief is not fenced, so it reads as instructions:\n%s", p)
	}
	fenced := p[open+3 : open+3+closed]
	for _, want := range []string{in.Brand, in.About} {
		if !strings.Contains(fenced, want) {
			t.Errorf("%q is outside the fence, where it reads as an instruction:\n%s", want, p)
		}
	}
	// THE DELIVERY IS DELIBERATELY OUTSIDE IT, and this assertion was the
	// opposite way round until 2026-09-08. It is an instruction to the writer,
	// not something the operator said about the product, and inside the data
	// block the model read it as one: "Delivery: warm" came back live as the
	// sentence "Delivery is warm." in an advert for a food business, which
	// reads as a claim about their delivery service.
	if strings.Contains(fenced, in.Delivery) {
		t.Errorf("the delivery is inside the data fence, where it reads as a fact "+
			"about the product:\n%s", p)
	}
	if !strings.Contains(strings.ToLower(p), "must not mention the delivery") {
		t.Errorf("the prompt does not say to keep the delivery out of the script:\n%s", p)
	}
	// COPYING, NOT A PROHIBITION. "Do not change the numbers" is followed
	// weakly by every model; "copy them exactly, in digits" is a thing to do,
	// and checkAgainstBrief enforces it.
	if !strings.Contains(p, "COPY EVERY NUMBER EXACTLY") {
		t.Errorf("the prompt does not tell the model to copy the numbers:\n%s", p)
	}
	// THIS MATTERS MORE HERE THAN ANYWHERE ELSE IN THE SYSTEM. An invented
	// advert could say anything because the product was fiction; a real one
	// that invents a discount is the operator's problem with a real
	// advertiser.
	low := strings.ToLower(p)
	for _, forbidden := range []string{"price", "address", "hour", "claim"} {
		if !strings.Contains(low, forbidden) {
			t.Errorf("the prompt does not forbid inventing a %s:\n%s", forbidden, p)
		}
	}
}

func TestAdBriefNoDelivery(t *testing.T) {
	in := stillwater()
	in.Delivery = ""
	w := &briefWriter{replies: []string{goodAd}}
	if _, err := WriteAdFromBrief(context.Background(), w, in); err != nil {
		t.Fatalf("an advert with no delivery note: %v", err)
	}
	// Delivery is optional and its absence asks for a straight read rather
	// than leaving the model to invent a register.
	if !strings.Contains(strings.ToLower(w.prompts[0]), "straight") {
		t.Errorf("no delivery note produced no instruction:\n%s", w.prompts[0])
	}
}

func TestAdBriefRequiresBrandAndAbout(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   AdBrief
	}{
		{"no brand", AdBrief{About: "a pottery"}},
		{"no about", AdBrief{Brand: "Stillwater Ceramics"}},
		{"blank brand", AdBrief{Brand: "   ", About: "a pottery"}},
		{"blank about", AdBrief{Brand: "Stillwater", About: "\n\t "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &briefWriter{replies: []string{goodAd}}
			if _, err := WriteAdFromBrief(context.Background(), w, tc.in); err == nil {
				t.Error("accepted an incomplete brief")
			}
			// WITHOUT CALLING THE MODEL: there is nothing to write from, and a
			// round trip to discover that is a round trip wasted.
			if len(w.prompts) != 0 {
				t.Errorf("the model was called %d times for an incomplete brief", len(w.prompts))
			}
		})
	}
}

// TestAdBriefRetriesWithReason: A RETRY WITH AN IDENTICAL PROMPT GETS AN
// IDENTICAL ANSWER. The first live run of the old generator produced an empty
// pool because every attempt broke the same rule and nothing ever said so.
func TestAdBriefRetriesWithReason(t *testing.T) {
	long := `{"brand":"Stillwater Ceramics","script":"Stillwater Ceramics. ` +
		strings.Repeat("mugs and more mugs and other mugs besides, ", 12) + `"}`
	w := &briefWriter{replies: []string{long, goodAd}}

	ad, err := WriteAdFromBrief(context.Background(), w, stillwater())
	if err != nil {
		t.Fatalf("the retry did not recover: %v", err)
	}
	if ad.Brand != "Stillwater Ceramics" {
		t.Errorf("brand = %q", ad.Brand)
	}
	if len(w.prompts) != 2 {
		t.Fatalf("the model was called %d times, want 2", len(w.prompts))
	}
	// THE SECOND PROMPT SAYS WHAT WAS WRONG WITH THE FIRST.
	if w.prompts[1] == w.prompts[0] {
		t.Fatal("the retry sent the identical prompt, so it gets the identical answer")
	}
	if !strings.Contains(strings.ToLower(w.prompts[1]), "characters") {
		t.Errorf("the retry does not name the rule that was broken:\n%s", w.prompts[1])
	}
}

func TestAdBriefFailsAfterAttempts(t *testing.T) {
	// It names the brand, so the rule it keeps breaking is the WORD MINIMUM --
	// otherwise the missing-brand rule fires first and the error names that.
	w := &briefWriter{replies: []string{
		`{"brand":"Stillwater Ceramics","script":"Stillwater Ceramics. Mugs."}`}}
	_, err := WriteAdFromBrief(context.Background(), w, stillwater())
	if err == nil {
		t.Fatal("four bad answers produced an advert")
	}
	if len(w.prompts) != adAttempts {
		t.Errorf("the model was called %d times, want %d", len(w.prompts), adAttempts)
	}
	// The error names the LAST reason, so the operator knows what to change.
	if !strings.Contains(err.Error(), "words") {
		t.Errorf("err = %v, want it to name the rule that kept failing", err)
	}
}

// TestAdBriefValidateNoLongerRefusesRealBrands: the denylist exists because a
// MODEL inventing "Coca-Cola" is a legal problem -- parody cover is US-shaped
// and JockPacks ship worldwide. An OPERATOR naming their own advertiser is a
// different act entirely, and refusing it would make the feature useless to
// anyone whose client is a real company.
func TestAdBriefValidateNoLongerRefusesRealBrands(t *testing.T) {
	ad := Ad{
		Brand:  "Coca-Cola",
		Script: "Coca-Cola. The one in the red can, still doing what it has always done here.",
	}
	if err := ad.Validate(); err != nil {
		t.Errorf("Validate refused an operator's own advertiser: %v", err)
	}
}

func TestAdBriefRealBrandWarning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ad     Ad
		want   string
		expect bool
	}{
		{"a real one is named", Ad{Brand: "Coca-Cola",
			Script: "Coca-Cola. The one in the red can."}, "coca-cola", true},
		{"an invented one is not", Ad{Brand: "Stillwater Ceramics",
			Script: "Stillwater Ceramics. One mug, still."}, "", false},
		// THE PADDING MATTERS. Substring matching would make every one of
		// these a false positive.
		{"Nordhaven is not haven", Ad{Brand: "Nordhaven",
			Script: "Nordhaven. A harbour town that sells rope."}, "", false},
		{"Applewick Cider is not Apple", Ad{Brand: "Applewick Cider",
			Script: "Applewick Cider. Pressed in a shed since nineteen eighty."}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.ad.RealBrandWarning()
			if ok != tc.expect {
				t.Fatalf("RealBrandWarning() = %q, %v; want %v", got, ok, tc.expect)
			}
			if ok && !strings.Contains(strings.ToLower(got), tc.want) {
				t.Errorf("matched %q, want it to name %q", got, tc.want)
			}
		})
	}
}

// TestAdBriefValidateStillRefuses: everything else in Validate applies to an
// operator's own text too. A 25-second slot is a 25-second slot.
func TestAdBriefValidateStillRefuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		ad   Ad
	}{
		{"over the character cap", Ad{Brand: "Stillwater",
			Script: "Stillwater. " + strings.Repeat("mugs and jugs and cups, ", 30)}},
		{"under the minimum words", Ad{Brand: "Stillwater", Script: "Stillwater. Mugs."}},
		{"a degenerate run", Ad{Brand: "Stillwater",
			Script: "Stillwater " + strings.Repeat("mugs ", 20)}},
		{"never says the brand", Ad{Brand: "Stillwater Ceramics",
			Script: "A pottery has made one mug for eleven years and it is heavier than you expect."}},
		{"no brand at all", Ad{Script: "Stillwater Ceramics. One mug, still, and always has been."}},
		{"no script at all", Ad{Brand: "Stillwater Ceramics"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.ad.Validate(); err == nil {
				t.Error("Validate accepted it")
			}
		})
	}
}

// TestAdBriefPropagatesWriterErrors: a model that refused is not a policy
// violation and must not be retried into four refusals.
func TestAdBriefPropagatesWriterErrors(t *testing.T) {
	w := &briefWriter{err: context.Canceled}
	if _, err := WriteAdFromBrief(context.Background(), w, stillwater()); err == nil {
		t.Fatal("a writer error was swallowed")
	}
	if len(w.prompts) != 1 {
		t.Errorf("a refusal was retried %d times; retrying a refusal refuses again", len(w.prompts))
	}
}

// TestAdBriefRefusesAMisquotedPrice: the guard is WIRED, not merely written.
//
// checkAgainstBrief has its own tests; this one exists because unwiring it from
// WriteAdFromBrief broke nothing else in the suite. A check nothing calls is
// the twelve-subsystems failure in miniature.
func TestAdBriefRefusesAMisquotedPrice(t *testing.T) {
	wrong := `{"brand":"Corvi Coffee","script":"Corvi Coffee. Forty-five hundred pesos a month, and that is all you pay."}`
	w := &briefWriter{replies: []string{wrong}}
	in := AdBrief{Brand: "Corvi Coffee",
		About: "a coffee roaster whose subscription is 450 pesos a month, nothing else"}

	if ad, err := WriteAdFromBrief(context.Background(), w, in); err == nil {
		t.Fatalf("an advert quoting ten times the price was accepted: %q", ad.Script)
	} else if !strings.Contains(err.Error(), "450") {
		t.Errorf("the failure does not name the number: %v", err)
	}
	// RETRIED, and told what was wrong -- a retry with an identical prompt gets
	// an identical answer.
	if len(w.prompts) != adAttempts {
		t.Errorf("gave up after %d attempts, want %d", len(w.prompts), adAttempts)
	}
	if !strings.Contains(w.prompts[len(w.prompts)-1], "450") {
		t.Error("the retry never told the model which number was wrong")
	}

	// And the same model, quoting correctly, gets through.
	right := `{"brand":"Corvi Coffee","script":"Corvi Coffee. The subscription is 450 pesos a month, and nothing else."}`
	ok := &briefWriter{replies: []string{right}}
	if ad, err := WriteAdFromBrief(context.Background(), ok, in); err != nil {
		t.Errorf("a correctly quoted advert was refused: %v", err)
	} else if !strings.Contains(ad.Script, "450") {
		t.Errorf("script = %q", ad.Script)
	}
}
