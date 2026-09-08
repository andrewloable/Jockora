// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/andrewloable/jockora/internal/enrich"
)

// TestAdsGateLive writes adverts from real briefs against a real model.
//
// THE LIVE HALF OF THE Jockora-iyw GATE, and the only half that can catch a
// prompt defect. It was TestLiveAdBrief, built for Jockora-iyw.2; the gate
// needs the same run over a wider set of briefs, so it grew rather than being
// duplicated -- a second test making the same six model calls would double the
// cost of the one run nobody can automate.
//
// It replaces TestLiveAdPool, which exercised the invented-brand generator that
// Jockora-iyw.2 deleted. The discipline it carried is the part worth keeping:
// A PERSON READS EVERY ADVERT BEFORE IT AIRS, and this prints them so somebody
// can. What changed is that the brands are now the operator's real advertisers
// rather than the model's inventions, which moves the risk from "is this
// somebody's actual company" to "did it invent a fact about a real one".
//
//	JOCKORA_LIVE_LLM=http://127.0.0.1:8123 \
//	go test ./internal/dj/ -run TestAdsGateLive -v -timeout 20m
func TestAdsGateLive(t *testing.T) {
	llmURL := os.Getenv("JOCKORA_LIVE_LLM")
	if llmURL == "" {
		t.Skip("set JOCKORA_LIVE_LLM to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	w := NewWriterAt(enrich.NewLlamaCPP(llmURL, nil), 400, enrich.InventionTemperature)

	for _, in := range []AdBrief{
		{
			Brand:    "Stillwater Ceramics",
			About:    "a pottery that has made exactly one mug for eleven years",
			Delivery: "deadpan, unhurried, no exclamation marks",
		},
		{
			Brand:    "Harrow & Fen Bookshop",
			About:    "a second-hand bookshop with a cat and no computer",
			Delivery: "warm, conspiratorial",
		},
		{
			// NO DELIVERY: the straight-read path, live.
			Brand: "Marchetti Tyres",
			About: "a tyre fitter open on Sundays, been on the same corner since 1974",
		},
		{
			// A BRIEF WITH NUMBERS IN IT. The model may repeat these; it must
			// not invent others beside them.
			Brand:    "The Ninth Wave",
			About:    "a swimming club that meets at six in the morning, all year",
			Delivery: "brisk, slightly amused",
		},
		{
			// A SPECIFIC CLAIM THE OPERATOR GAVE. The one number in the brief
			// is the one number allowed in the script, and a model that
			// helpfully rounds it or adds a second offer beside it is the
			// exact failure this gate exists to catch.
			Brand:    "Corvi Coffee",
			About:    "a coffee roaster whose subscription is 450 pesos a month, nothing else",
			Delivery: "hard sell",
		},
		{
			// A REAL TRADEMARK, AS THE BRAND. The operator's client may be a
			// real company: the denylist must WARN and the writer must still
			// produce usable copy. Refusing here would make the whole feature
			// useless to anybody with a real advertiser.
			Brand:    "Jollibee",
			About:    "the branch on Lacson Street, open late on Fridays",
			Delivery: "warm",
		},
	} {
		t.Run(in.Brand, func(t *testing.T) {
			ad, err := WriteAdFromBrief(ctx, w, in)
			if err != nil {
				t.Fatalf("WriteAdFromBrief(%q): %v", in.Brand, err)
			}
			t.Logf("\n  BRAND  %s\n  SCRIPT %s", ad.Brand, ad.Script)

			if err := ad.Validate(); err != nil {
				t.Errorf("the advert failed validation after generation: %v", err)
			}
			if !strings.Contains(strings.ToLower(ad.Script), strings.ToLower(strings.Fields(in.Brand)[0])) {
				t.Errorf("the advert never says the brand: %q", ad.Script)
			}
			// THE SLOT, stated rather than left to Validate. A live run that
			// reports "validation failed" without the number makes the reader
			// go and find which rule; these two are the ones that decide
			// whether the advert fits its twenty-five seconds.
			if n := utf8.RuneCountInString(ad.Script); n > MaxAdChars {
				t.Errorf("%d characters, over the %d-character slot", n, MaxAdChars)
			}
			if n := len(strings.Fields(ad.Script)); n < MinAdWords {
				t.Errorf("%d words, under the %d-word floor", n, MinAdWords)
			}
			// THE WARNING, not a refusal. Printed either way so the reader can
			// see it fired on the trademark and did not fire on the others.
			if matched, flagged := ad.RealBrandWarning(); flagged {
				t.Logf("  WARNING  names %q, which somebody else owns -- advisory, not a refusal", matched)
			}
			// INVENTED FACTS ARE WHAT THIS RUN IS FOR. No assertion can catch
			// them, so it prints the brief beside the script and a person
			// compares. A real advert that invents a discount is the
			// operator's problem with a real advertiser.
			t.Logf("  BRIEF  %s", in.About)
			t.Log("  READ THESE TWO TOGETHER: anything in the script that is not in the brief " +
				"is invented, and this is the only place it can be caught.")
		})
	}
}
