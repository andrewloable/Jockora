// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"strings"
	"testing"
	"time"
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
	ad, err := r.Next(now)
	if err != nil {
		t.Fatal(err)
	}
	r.Aired(ad.ID, now)

	if got := saidLineCount(t, s); got != before {
		t.Errorf("said_lines grew from %d to %d when an ad aired; the ad would then block itself forever", before, got)
	}
}

func TestAdsRotationRespectsNinetyMinutes(t *testing.T) {
	r := NewAdRotation(testPool(t, 10))
	now := time.Unix(1_700_000_000, 0)

	first, err := r.Next(now)
	if err != nil {
		t.Fatal(err)
	}
	r.Aired(first.ID, now)

	second, err := r.Next(now.Add(10 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Errorf("aired %s again after 10 minutes; the cooldown is %s", first.ID, AdCooldown)
	}

	// After the cooldown it is eligible again.
	r2 := NewAdRotation(testPool(t, 1))
	r2.Aired("ad-a", now)
	if _, err := r2.Next(now.Add(AdCooldown - time.Minute)); err == nil {
		t.Error("a single-ad pool returned an ad inside its cooldown")
	}
	if _, err := r2.Next(now.Add(AdCooldown + time.Minute)); err != nil {
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
		r.Aired("ad-"+string(rune('a'+i)), now.Add(time.Duration(i)*time.Minute))
	}

	got, err := r.Next(now.Add(11 * time.Minute))
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
func TestAdsRealBrandIsRejected(t *testing.T) {
	for _, ad := range []Ad{
		{ID: "a", Brand: "Coca-Cola", Script: "Coca-Cola is refreshing and cold and exactly what you wanted tonight."},
		{ID: "b", Brand: "Nordhaven", Script: "Like a McDonald's for sleep."},
		{ID: "c", Brand: "Nordhaven", Script: "Nordhaven is better than an iPhone and it will still work tomorrow."},
		{ID: "d", Brand: "nike shoes", Script: "nike shoes will carry you further than you meant to go tonight."},
	} {
		if err := ad.Validate(); err == nil {
			t.Errorf("accepted an ad naming a real brand: brand=%q script=%q", ad.Brand, ad.Script)
		}
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
	if _, err := NewAdRotation(nil).Next(time.Unix(0, 0)); err == nil {
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

// TestAdsPoolGenerationIsBounded: the duplicate-brand skip does not consume a
// retry, so an unbounded loop here hangs until the caller's context expires and
// reports a timeout with nothing saying why.
func TestAdsPoolGenerationIsBounded(t *testing.T) {
	w := &repeatWriter{}
	done := make(chan struct{})
	var ads []Ad
	var err error
	go func() {
		ads, err = GenerateAdPool(context.Background(), w, testPersona(t), 10)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("GenerateAdPool did not return; a model repeating one brand hangs it forever")
	}

	if err == nil {
		t.Error("returned a full pool from a writer that only ever names one brand")
	}
	if len(ads) != 1 {
		t.Errorf("kept %d adverts, want 1 distinct brand", len(ads))
	}
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
