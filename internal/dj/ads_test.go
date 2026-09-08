// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// All named TestAds*, matching the task's own `-run TestAds`. Three of the six
// names it proposed did not.

func testPool(t *testing.T, n int) []Ad {
	t.Helper()
	ads := make([]Ad, n)
	for i := range ads {
		ads[i] = Ad{
			ID:     "ad-" + string(rune('a'+i)),
			Brand:  "Nordhaven Mattresses",
			Script: "Nordhaven. Sleep like the tide went out and wake up somewhere better.",
		}
	}
	return ads
}

// TestAdsAreExemptFromSaidLines is the deliberate carve-out. Repetition is the
// feature: the same ad every ninety minutes is authentic radio and is exactly
// why the GTA ads are quotable.
func TestAdsAreExemptFromSaidLines(t *testing.T) {
	s := saidStore(t)
	line := "Nordhaven. Sleep like the tide went out and wake up somewhere better."
	if err := s.Record(context.Background(), line); err != nil {
		t.Fatal(err)
	}

	// The same text as a break would now collide.
	hit, _, err := s.CheckCollision(context.Background(), line)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("the said-lines index did not record the line; the exemption below would prove nothing")
	}

	// As an ad it airs anyway.
	ad := Ad{ID: "ad-a", Brand: "Nordhaven Mattresses", Script: line}
	if err := ad.Validate(); err != nil {
		t.Errorf("an ad was rejected for repeating itself: %v", err)
	}
}

func TestAdsAreNotRecordedInSaidLines(t *testing.T) {
	s := saidStore(t)
	before := saidLineCount(t, s)

	r := NewAdRotation(testPool(t, 3))
	now := time.Unix(1_700_000_000, 0)
	ad, err := r.Next(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	r.aired(ad.ID, now)

	if got := saidLineCount(t, s); got != before {
		t.Errorf("said_lines grew from %d to %d when an ad aired; the ad would then block itself forever", before, got)
	}
}

func TestAdsRotationRespectsNinetyMinutes(t *testing.T) {
	r := NewAdRotation(testPool(t, 10))
	now := time.Unix(1_700_000_000, 0)

	first, err := r.Next(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	r.aired(first.ID, now)

	second, err := r.Next(context.Background(), now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Errorf("aired %s again after 10 minutes; the cooldown is %s", first.ID, AdCooldown)
	}

	// After the cooldown it is eligible again.
	r2 := NewAdRotation(testPool(t, 1))
	r2.aired("ad-a", now)
	if _, err := r2.Next(context.Background(), now.Add(AdCooldown-time.Minute)); err == nil {
		t.Error("a single-ad pool returned an ad inside its cooldown")
	}
	if _, err := r2.Next(context.Background(), now.Add(AdCooldown+time.Minute)); err != nil {
		t.Errorf("a single-ad pool refused after the cooldown expired: %v", err)
	}
}

// TestAdsPoolExhaustionReusesOldest: running out of fresh ads must not stop the
// station, and it must not pick at random either -- least-recently-aired is the
// choice a listener is least likely to notice.
func TestAdsPoolExhaustionReusesOldest(t *testing.T) {
	r := NewAdRotation(testPool(t, 10))
	now := time.Unix(1_700_000_000, 0)

	// Air all ten within the last few minutes, oldest first.
	for i := 0; i < 10; i++ {
		r.aired("ad-"+string(rune('a'+i)), now.Add(time.Duration(i)*time.Minute))
	}

	got, err := r.Next(context.Background(), now.Add(11*time.Minute))
	if err != nil {
		t.Fatalf("an exhausted pool returned an error instead of the oldest ad: %v", err)
	}
	if got.ID != "ad-a" {
		t.Errorf("chose %s, want ad-a, the least recently aired", got.ID)
	}
}

// TestAdsCarryNoAssertedFacts: an invented product must never become something
// the DJ can assert. The Ad type has NOWHERE to put a fact, in the same way the
// Ramp type has nowhere to put a lyric.
func TestAdsCarryNoAssertedFacts(t *testing.T) {
	ad := Ad{ID: "ad-a", Brand: "Nordhaven Mattresses", Script: "Sleep like the tide went out."}
	b := ad.Break()
	if len(b.AssertedFacts) != 0 {
		t.Errorf("an ad produced %d asserted facts: %q", len(b.AssertedFacts), b.AssertedFacts)
	}
	if b.Text() != ad.Script {
		t.Errorf("ad break text = %q, want the script %q", b.Text(), ad.Script)
	}
}

// TestAdsRealBrandIsRejected. Fair-use cover for parody is US-shaped and
// JockPacks ship worldwide, so the rule is invented brands only -- no real
// brand, no parody of one.
// TestAdsRealBrandIsRejected is now TestAdsRealBrandIsFlagged.
//
// REWRITTEN, NOT DELETED, and the distinction it draws is the point. The
// denylist was written when a MODEL invented the brands: one that produced
// "Coca-Cola" was a legal problem, since parody cover is US-shaped and
// JockPacks ship worldwide. An OPERATOR typing the name of their own
// advertiser is the opposite act, and refusing it would make the feature
// useless to anybody whose client is a real company. So Validate no longer
// refuses; RealBrandWarning reports, and the console confirms past it.
//
// The half that has not changed at all is the padded matching, and it is the
// half a naive implementation gets wrong.
func TestAdsRealBrandIsFlagged(t *testing.T) {
	for _, ad := range []Ad{
		{ID: "a", Brand: "Coca-Cola", Script: "Coca-Cola is refreshing and cold and exactly what you wanted tonight."},
		{ID: "b", Brand: "Nordhaven", Script: "Like a McDonald's for sleep."},
		{ID: "c", Brand: "Nordhaven", Script: "Nordhaven is better than an iPhone and it will still work tomorrow."},
		{ID: "d", Brand: "nike shoes", Script: "nike shoes will carry you further than you meant to go tonight."},
	} {
		if _, flagged := ad.RealBrandWarning(); !flagged {
			t.Errorf("did not flag an ad naming a real brand: brand=%q script=%q", ad.Brand, ad.Script)
		}
	}

	// AND IT IS NOT A REFUSAL any more: the operator decides. Checked on one
	// that breaks no OTHER rule, since "Like a McDonald's for sleep" never says
	// its own brand and Validate is still right to refuse that.
	real := Ad{ID: "a", Brand: "Coca-Cola",
		Script: "Coca-Cola. The one in the red can, still doing what it always did."}
	if err := real.Validate(); err != nil {
		t.Errorf("Validate refused an operator's own advertiser: %v", err)
	}

	// Invented names that CONTAIN a real brand as a substring must survive. A
	// naive strings.Contains would reject every one of these, and the writer
	// would burn its retries inventing names that keep getting refused.
	for _, ok := range []Ad{
		{ID: "e", Brand: "Nordhaven Mattresses", Script: "Nordhaven. Sleep like the tide went out and wake up somewhere better."},
		{ID: "f", Brand: "Applewick Cider", Script: "Applewick. Pressed in the dark, and poured for people still awake."},
		{ID: "g", Brand: "Fordham Reeds", Script: "Fordham Reeds, for the woodwind that outlives you and everyone you played with."},
		{ID: "h", Brand: "Sonyx Filters", Script: "Sonyx. Everything you did not want to hear, quietly removed for good."},
		{ID: "i", Brand: "Visalia Nightbread", Script: "Visalia. Bread for people who are up anyway, baked while you wait."},
	} {
		if matched, flagged := ok.RealBrandWarning(); flagged {
			t.Errorf("flagged the invented brand %q as the real %q", ok.Brand, matched)
		}
		if err := ok.Validate(); err != nil {
			t.Errorf("rejected the invented brand %q: %v", ok.Brand, err)
		}
	}
}

func TestAdsRejectEmptyOrOverlong(t *testing.T) {
	if err := (Ad{ID: "a", Brand: "Nordhaven", Script: "  "}).Validate(); err == nil {
		t.Error("accepted an ad with no script")
	}
	if err := (Ad{ID: "a", Brand: "", Script: "Buy it now because it is good and cheap and here."}).Validate(); err == nil {
		t.Error("accepted an ad with no brand")
	}
	long := Ad{ID: "a", Brand: "Nordhaven", Script: strings.Repeat("word ", 200)}
	if err := long.Validate(); err == nil {
		t.Error("accepted an ad far longer than an ad slot")
	}

	// The schema cap and the validator cap must be the SAME limit, or the
	// sampler can legally produce what the validator must reject -- which
	// returned an empty pool four attempts running.
	props := AdSchema()["properties"].(map[string]any)
	if got := props["script"].(map[string]any)["maxLength"]; got != MaxAdChars {
		t.Errorf("schema maxLength = %v, want MaxAdChars %d", got, MaxAdChars)
	}
}

func TestAdsEmptyRotationIsAnErrorNotAPanic(t *testing.T) {
	if _, err := NewAdRotation(nil).Next(context.Background(), time.Unix(0, 0)); err == nil {
		t.Error("an empty rotation returned an ad")
	}
}

func saidLineCount(t *testing.T, s *SaidLines) int {
	t.Helper()
	var n int
	if err := s.Store.DB().QueryRow(`SELECT count(*) FROM said_lines`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// repeatWriter always answers with the same brand, which is how a small model
// behaves once it has settled on a name it likes.
type repeatWriter struct{ calls int }

func (w *repeatWriter) WriteBreak(_ context.Context, _ string, _ map[string]any, _ int) (string, error) {
	w.calls++
	return `{"brand":"Nordhaven Mattresses","script":"Nordhaven. Sleep like the tide went out and wake up somewhere better."}`, nil
}

// TestAdsRejectPersonaEcho: with the persona card in the prompt, ten adverts
// came back carrying the persona's own speech style as their script, and every
// one of them validated -- the brand denylist cannot tell a script from a
// prompt.
func TestAdsRejectPersonaEcho(t *testing.T) {
	for _, ad := range []Ad{
		{ID: "a", Brand: "RoadMaster", Script: "Low and unhurried, like someone talking to one person rather than an audience."},
		{ID: "b", Brand: "The Beacon", Script: "YOU NEVER DO THESE THINGS - never mentions the weather more than once."},
		{ID: "c", Brand: "Brand Name", Script: "Announcer's script"},
	} {
		if err := ad.Validate(); err == nil {
			t.Errorf("accepted an advert that is really the prompt: %q", ad.Script)
		}
	}

	ok := Ad{ID: "d", Brand: "Nordhaven Mattresses", Script: "Nordhaven. Sleep like the tide went out and wake up somewhere better."}
	if err := ok.Validate(); err != nil {
		t.Errorf("rejected a real advert: %v", err)
	}
}

// TestAdsRejectGarbage covers what a small model actually produces once the
// obvious echoes are blocked: fragments that name nothing, and sampler loops.
// Every string here came out of a real generation run.
func TestAdsRejectGarbage(t *testing.T) {
	for _, ad := range []Ad{
		{ID: "a", Brand: "Comfort Zone", Script: "The station plays synthwave, alternative, electronic, ambient."},
		{ID: "b", Brand: "Sky", Script: "I'm ready."},
		{ID: "c", Brand: "Synthwave", Script: "Electronic"},
		{ID: "d", Brand: "Dots", Script: "Dots ......} } } } } } } } } } } } } } }"},
	} {
		if err := ad.Validate(); err == nil {
			t.Errorf("accepted %q / %q", ad.Brand, ad.Script)
		}
	}

	ok := Ad{ID: "e", Brand: "Nordhaven Mattresses",
		Script: "Nordhaven Mattresses. Sleep like the tide went out, and wake up somewhere better."}
	if err := ok.Validate(); err != nil {
		t.Errorf("rejected a real advert: %v", err)
	}
}

// TestAdCharCapCountsRunes: THREE UNITS FOR ONE LIMIT, which is the exact
// failure the comment above MaxAdChars was written about, in a new form.
//
// Go's len() counts BYTES and the message said characters; the JSON schema's
// maxLength counts CODE POINTS. For pure ASCII they agree and nothing ever went
// wrong. For anything else the sampler produces a script legally, the validator
// refuses it, and generateOneAd burns all four attempts on output that was
// never over the limit. "opm" is in the station vocabulary and the ads epic
// lets an operator write in any language they like, so this is not theoretical.
func TestAdCharCapCountsRunes(t *testing.T) {
	// Exactly at the cap, and deliberately NOT ASCII: accented characters at
	// two bytes each and an emoji at four. It names the brand because every
	// other rule in Validate still applies -- this test is about the LENGTH.
	const brand = "Stillwater Ceramics"
	// Varied filler, because Validate also refuses a script that degenerates
	// into one repeated word -- every other rule still applies and this test is
	// only about the LENGTH.
	words := strings.Fields("makes one mug for eleven years they tried a second " +
		"and it did not feel right so they stopped the thing holds what it holds " +
		"heavier than you expect which is the entire point of owning it at all")
	script := brand + " " + strings.Repeat("é", 20) + " 🎧 "
	for i := 0; utf8.RuneCountInString(script) < MaxAdChars; i++ {
		script += words[i%len(words)] + " "
	}
	script = string([]rune(script)[:MaxAdChars])
	if n := utf8.RuneCountInString(script); n != MaxAdChars {
		t.Fatalf("the fixture is %d runes, want exactly %d", n, MaxAdChars)
	}
	// THE WHOLE POINT: it is over the cap in bytes and at it in runes, so a
	// byte-counting validator refuses a script the sampler was allowed to make.
	if b := len(script); b <= MaxAdChars {
		t.Fatalf("the fixture is %d bytes; it must exceed %d or it proves nothing", b, MaxAdChars)
	}

	ok := Ad{Brand: brand, Script: script}
	if err := ok.Validate(); err != nil {
		t.Errorf("a script of exactly %d runes was refused: %v", MaxAdChars, err)
	}

	over := Ad{Brand: brand, Script: script + "s"}
	err := over.Validate()
	if err == nil {
		t.Fatalf("a script of %d runes was accepted", MaxAdChars+1)
	}
	// And the message is TRUE now: it says characters and counts characters.
	if !strings.Contains(err.Error(), strconv.Itoa(MaxAdChars+1)) {
		t.Errorf("error = %v, want it to report %d characters", err, MaxAdChars+1)
	}
}

// TestAdsCheckAgainstBrief: the guard for the worst thing this feature can do.
//
// FOUND LIVE 2026-09-08. The brief said 450 pesos a month and a 4B model wrote
// "Forty-five hundred pesos a month" -- ten times the price, for a real
// business, with Validate and every other check passing. Validate cannot see
// it; it never has the brief.
func TestAdsCheckAgainstBrief(t *testing.T) {
	brief := AdBrief{Brand: "Corvi Coffee",
		About: "a coffee roaster whose subscription is 450 pesos a month, nothing else"}

	if err := checkAgainstBrief(
		"Corvi Coffee. The subscription is 450 pesos a month and that is all.", brief); err != nil {
		t.Errorf("an advert quoting the brief exactly was refused: %v", err)
	}
	// THE LIVE FAILURE, verbatim. Spelling the number out is refused as well as
	// changing it: a spelled number cannot be compared at all, and internal/say
	// normalises digits for the sidecar anyway.
	if err := checkAgainstBrief(
		"Corvi Coffee. Forty-five hundred pesos a month, nothing more.", brief); err == nil {
		t.Error("an advert that misquoted the price was accepted")
	} else if !strings.Contains(err.Error(), "450") {
		t.Errorf("the retry will not know which number: %v", err)
	}
	// AND A NUMBER NOBODY GAVE IT. An invented discount is the operator's
	// problem with a real advertiser, and they find out when it airs.
	if err := checkAgainstBrief(
		"Corvi Coffee. 450 pesos a month, and 20 percent off your first bag.", brief); err == nil {
		t.Error("an invented offer was accepted")
	}

	// A brief with no numbers constrains nothing, and must not start refusing
	// an advert for saying the year it opened is not in the brief.
	plain := AdBrief{Brand: "Harrow", About: "a second-hand bookshop with a cat"}
	if err := checkAgainstBrief("Harrow. Books, and a cat that sleeps on them.", plain); err != nil {
		t.Errorf("a brief with no numbers refused an advert with none: %v", err)
	}
	if err := checkAgainstBrief("Harrow. Books since 1974.", plain); err == nil {
		t.Error("an invented year was accepted")
	}

	// THE DELIVERY IS AN INSTRUCTION, NOT A FACT. Live, "Delivery: warm" came
	// back as the sentence "Delivery is warm." in an advert for a food
	// business, which reads as a claim about their delivery service.
	warm := AdBrief{Brand: "Jollibee", About: "the branch on Lacson Street", Delivery: "warm"}
	if err := checkAgainstBrief("Jollibee on Lacson Street. Delivery is warm.", warm); err == nil {
		t.Error("the delivery instruction was allowed into the script")
	}
	if err := checkAgainstBrief("Jollibee, on Lacson Street, and open late.", warm); err != nil {
		t.Errorf("an advert that kept the delivery out was refused: %v", err)
	}
	// No delivery asked for is no rule to break.
	none := AdBrief{Brand: "Harrow", About: "a bookshop"}
	if err := checkAgainstBrief("Harrow. A bookshop, and nothing else.", none); err != nil {
		t.Errorf("with no delivery given: %v", err)
	}
}

// TestAdsCheckDeliveryOnlyRefusesTheEcho: the delivery check must catch the
// model restating its own instruction and nothing else.
//
// The first version refused the delivery word ANYWHERE, which is far too much.
// An operator asking for a WARM read of a bakery advert would have "A warm
// welcome" refused four times and then be handed an error for good copy.
func TestAdsCheckDeliveryOnlyRefusesTheEcho(t *testing.T) {
	warm := AdBrief{Brand: "Harrow Bakery", About: "a bakery on the corner", Delivery: "warm"}
	for _, good := range []string{
		"Harrow Bakery. A warm welcome, and bread out of the oven all morning.",
		"Harrow Bakery, on the corner, where the bread is still warm.",
		"Harrow Bakery. Warm bread, every morning, and nothing else to report.",
	} {
		if err := checkAgainstBrief(good, warm); err != nil {
			t.Errorf("good copy refused: %q -> %v", good, err)
		}
	}
	// THE OBSERVED FAILURE, verbatim from the live run on 2026-09-08.
	for _, bad := range []string{
		"Jollibee on Lacson Street. Delivery is warm.",
		"Harrow Bakery. Tone: warm. Bread every morning.",
		"Harrow Bakery. Read it warm, and mean it.",
	} {
		if err := checkAgainstBrief(bad, warm); err == nil {
			t.Errorf("the delivery instruction was allowed into the script: %q", bad)
		}
	}

	// The words that never appear in copy by accident still work.
	dead := AdBrief{Brand: "Stillwater", About: "mugs", Delivery: "deadpan"}
	if err := checkAgainstBrief("Stillwater. Voice: deadpan. Mugs.", dead); err == nil {
		t.Error("a labelled deadpan instruction was allowed through")
	}
	if err := checkAgainstBrief("Stillwater. Mugs, thrown by hand, and still here.", dead); err != nil {
		t.Errorf("clean copy refused: %v", err)
	}
	// And a late-night swim is a swim, not an instruction.
	late := AdBrief{Brand: "The Ninth Wave", About: "a swimming club", Delivery: "late-night"}
	if err := checkAgainstBrief(
		"The Ninth Wave. Come for the late-night swim, every night of the year.", late); err != nil {
		t.Errorf("good copy refused: %v", err)
	}
}
