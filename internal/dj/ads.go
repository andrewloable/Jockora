// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// AdPoolSize is how many adverts a station carries.
//
// Ten is enough that the rotation does not feel like a loop and few enough that
// a person can read every one before it airs, which is the v0.1 rule.
const AdPoolSize = 10

// AdCooldown is the minimum gap before the same advert airs again.
//
// ADVERTS ARE THE ONE THING IN JOCKORA THAT IS SUPPOSED TO REPEAT. Everything
// else is policed by the said-lines index because a listener forgives a dull
// line and never forgives the same line twice -- but an advert recurring is
// what makes it an advert, and it is exactly why the GTA ads are quotable.
const AdCooldown = 90 * time.Minute

// adAttempts is how many times one advert may be rewritten.
//
// More than a break gets, because this runs ONCE at station setup rather than
// inside a lookahead window, so there is no airtime budget to blow -- and a
// short pool is a rotation a listener notices.
const adAttempts = 4

// MaxAdChars caps an advert. One slot, never a block. At the writer's speaking
// rate that is roughly twenty-five seconds, which is a real radio advert.
//
// CHARACTERS, NOT WORDS, and that is the whole point. A JSON-schema grammar can
// only bound a string by length, so a word cap and a character cap can never
// agree: with the schema at 360 characters and the validator at 60 words, the
// sampler filled the string exactly and produced 62 words EVERY TIME, four
// attempts running, and the pool came back empty. Two limits measuring
// different things is a machine for rejecting your own output. There is now one
// limit, the sampler enforces it exactly, and Validate confirms it.
const MaxAdChars = 360

// adCategories give each request a materially different subject.
//
// Telling a model "invent something different from the brands above" does not
// work: measured live, forty attempts produced ONE distinct brand, because the
// prompt was otherwise identical every time and a small model settles on a name
// it likes. Naming the category changes the prompt itself, which is the only
// instruction a model of this size reliably follows.
//
// They are also just what a real station's rotation looks like -- a mattress
// shop, a haulage firm, a late-night diner -- rather than ten variations on one
// idea.
var adCategories = []string{
	"a mattress and bedding shop",
	"a 24-hour diner",
	"a used car dealership",
	"a driving school",
	"a hardware store",
	"a taxi or minicab firm",
	"a funeral director",
	"an energy drink",
	"a gym or fitness studio",
	"a locksmith",
	"a pest control service",
	"a discount furniture warehouse",
	"a dental practice",
	"a self-storage facility",
	"a pizza delivery place",
}

// MinAdWords is the floor. Below this it is a fragment, not an advert.
const MinAdWords = 8

// AdTargetWords is what the PROMPT asks for. Advisory, and well under the cap:
// every model overshoots a stated limit, so asking for the ceiling lands past
// it. This is how you talk to a writer; MaxAdChars is what actually binds.
const AdTargetWords = 35

// Ad is one invented advertisement.
//
// It has NOWHERE to hold a fact, deliberately and structurally -- the same
// reasoning that gives the Ramp type nowhere to hold a lyric. An invented
// product must never become something the DJ can assert, and a field that could
// carry one would eventually be filled.
type Ad struct {
	ID     string `json:"id"`
	Brand  string `json:"brand"`
	Script string `json:"script"`
}

// Break renders the advert as something the airing path can carry.
//
// AssertedFacts is always nil. An advert is fiction; the jock may remember
// RUNNING it, never that the product exists.
func (a Ad) Break() *Break {
	return &Break{Body: a.Script}
}

// realBrands is a backstop, not the control.
//
// The control is the prompt: invented compounds only, no real brand, product,
// company or person, and no parody -- fair-use cover for parody is US-shaped
// and JockPacks ship worldwide. This list catches the most likely slips.
//
// It is deliberately, unavoidably incomplete, and it does not scale: before
// adverts are ever generated without a person reading them, this needs to
// become a real check rather than a word list.
var realBrands = []string{
	"coca-cola", "coca cola", "pepsi", "mcdonald", "burger king", "kfc", "starbucks",
	"nike", "adidas", "puma", "reebok", "gucci", "prada", "rolex",
	"apple", "iphone", "ipad", "macbook", "android", "google", "samsung", "microsoft",
	"windows", "xbox", "playstation", "nintendo", "sony", "amazon", "netflix", "spotify",
	"facebook", "instagram", "tiktok", "twitter", "youtube", "uber", "airbnb", "tesla",
	"toyota", "honda", "ford", "bmw", "mercedes", "volkswagen", "ferrari",
	"walmart", "ikea", "lego", "disney", "marvel", "budweiser", "heineken", "guinness",
	"red bull", "nestle", "unilever", "gillette", "colgate", "pfizer", "visa", "mastercard",
}

// ErrAdNamesRealBrand means an advert mentions something that exists.
var errAdNamesRealBrand = fmt.Errorf("dj: advert names a real brand")

var wordish = regexp.MustCompile(`[^a-z0-9]+`)

// Validate checks an advert against the brand policy and the slot length.
//
// It does NOT check the said-lines index. That is the deliberate carve-out:
// running an advert through the anti-repetition validator would reject it the
// second time it aired, which is the first time it starts working.
func (a Ad) Validate() error {
	brand := strings.TrimSpace(a.Brand)
	script := strings.TrimSpace(a.Script)
	if brand == "" {
		return fmt.Errorf("dj: advert has no brand")
	}
	if script == "" {
		return fmt.Errorf("dj: advert has no script")
	}
	if n := len(script); n > MaxAdChars {
		return fmt.Errorf("dj: advert is %d characters, over the %d-character slot", n, MaxAdChars)
	}
	// The same guard breaks get. A script that recites its own prompt is not an
	// advert, and the brand denylist cannot tell the difference -- with the
	// persona block in the prompt, ten adverts came back carrying the persona's
	// speech style as their script and every one of them validated.
	if phrase, echoed := echoesInstructions(brand+" "+script, []string{brand}); echoed {
		return fmt.Errorf("dj: advert recites its prompt: %q", phrase)
	}

	// An advert names its business. This is not a hack to catch a bad model --
	// it is what an advert IS, and it is the cheapest check that separates one
	// from a fragment of the prompt that happened to land in the field. It
	// rejects "Comfort Zone" / "The station plays synthwave, alternative,
	// electronic, ambient." on the only ground that matters: nobody hearing it
	// would learn the name of anything.
	if !strings.Contains(strings.ToLower(script), strings.ToLower(firstWord(brand))) {
		return fmt.Errorf("dj: the advert never says the name %q", brand)
	}
	if n := len(strings.Fields(script)); n < MinAdWords {
		return fmt.Errorf("dj: advert is %d words, too short to be one", n)
	}
	if run, degenerate := degenerateRun(script); degenerate {
		return fmt.Errorf("dj: advert degenerates into a repeated %q", run)
	}

	// Padded with spaces so a substring match cannot fire inside a longer
	// invented word: "Nordhaven" must not trip on "haven", and an invented
	// "Applewick Cider" is not Apple.
	haystack := " " + wordish.ReplaceAllString(strings.ToLower(brand+" "+script), " ") + " "
	for _, b := range realBrands {
		if strings.Contains(haystack, " "+wordish.ReplaceAllString(b, " ")+" ") {
			return fmt.Errorf("%w: %q", errAdNamesRealBrand, b)
		}
	}
	return nil
}

// AdRotation picks which advert airs next.
type AdRotation struct {
	ads       []Ad
	lastAired map[string]time.Time
}

// NewAdRotation returns a rotation over a pool.
func NewAdRotation(ads []Ad) *AdRotation {
	return &AdRotation{ads: ads, lastAired: make(map[string]time.Time, len(ads))}
}

// Aired records that an advert went out.
func (r *AdRotation) Aired(id string, at time.Time) { r.lastAired[id] = at }

// Next returns the advert to air.
//
// Eligible adverts first, oldest-aired among them. When every advert is inside
// its cooldown the LEAST RECENTLY AIRED is reused rather than returning an
// error: running out of fresh adverts is not a reason to interrupt a station,
// and the oldest one is the repeat a listener is least likely to notice.
func (r *AdRotation) Next(now time.Time) (*Ad, error) {
	if len(r.ads) == 0 {
		return nil, fmt.Errorf("dj: advert rotation is empty")
	}

	ordered := make([]Ad, len(r.ads))
	copy(ordered, r.ads)
	sort.SliceStable(ordered, func(i, j int) bool {
		return r.lastAired[ordered[i].ID].Before(r.lastAired[ordered[j].ID])
	})

	for _, ad := range ordered {
		last, aired := r.lastAired[ad.ID]
		if !aired || now.Sub(last) >= AdCooldown {
			return &ad, nil
		}
	}

	// A single-advert pool inside its cooldown is the one case where refusing
	// is right: reusing it would air the same advert twice in a row.
	if len(r.ads) == 1 {
		return nil, fmt.Errorf("dj: the only advert aired %s ago, inside its %s cooldown",
			now.Sub(r.lastAired[r.ads[0].ID]).Round(time.Minute), AdCooldown)
	}
	oldest := ordered[0]
	return &oldest, nil
}

// AdSchema constrains advert generation at the sampler.
func AdSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"brand": map[string]any{"type": "string", "minLength": 2, "maxLength": 60},
			// Kept in step with MaxAdWords. When the schema allowed more
			// characters than the validator allowed words, the sampler could
			// legally produce something Validate then had to reject -- observed
			// live at 67 words against a 60-word cap, burning both retries and
			// returning an empty pool.
			"script": map[string]any{"type": "string", "minLength": 10, "maxLength": MaxAdChars},
		},
		"required":             []any{"brand", "script"},
		"additionalProperties": false,
	}
}

// GenerateAdPool writes n adverts in the station's voice.
//
// Adverts are themed on the PERSONA in v0.1 rather than on station_tags,
// because there is one whole-library station and no tags to theme on yet.
//
// An advert that names a real brand is regenerated rather than kept, and a
// pool that comes back short is returned with whatever succeeded: fewer
// adverts is a smaller rotation, not a broken station.
func GenerateAdPool(ctx context.Context, w Writer, p *Persona, n int) ([]Ad, error) {
	if n <= 0 {
		n = AdPoolSize
	}
	var out []Ad
	seen := make(map[string]bool, n)

	// BOUNDED. The duplicate-brand check below skips without consuming an
	// attempt, so a model that keeps offering the same invented brand would
	// spin here until the caller's context expired -- a twenty-minute hang
	// presented as a timeout, with nothing saying why.
	for tries := 0; len(out) < n && tries < n*adAttempts; tries++ {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		ad, err := generateOneAd(ctx, w, p, out, adCategories[tries%len(adCategories)])
		if err != nil {
			return out, err
		}
		key := strings.ToLower(strings.TrimSpace(ad.Brand))
		if seen[key] {
			continue // a rotation of one brand under ten names is not a rotation
		}
		seen[key] = true
		ad.ID = fmt.Sprintf("ad-%02d", len(out)+1)
		out = append(out, ad)
	}
	if len(out) < n {
		// A short pool is a smaller rotation, not a broken station -- but the
		// operator must be told, because a rotation of three is noticeable.
		return out, fmt.Errorf("dj: wrote %d of %d adverts before running out of distinct brands", len(out), n)
	}
	return out, nil
}

// generateOneAd asks, retrying on a policy violation and SAYING WHAT WAS WRONG.
//
// A retry with the identical prompt gets the identical answer often enough to
// matter: the first live run produced an empty pool because every attempt ran
// over the word cap and nothing ever told the model so.
func generateOneAd(ctx context.Context, w Writer, p *Persona, existing []Ad, category string) (Ad, error) {
	var lastErr error
	for attempt := 0; attempt < adAttempts; attempt++ {
		raw, err := w.WriteBreak(ctx, buildAdPrompt(p, existing, category, lastErr), AdSchema())
		if err != nil {
			return Ad{}, err
		}
		ad, err := parseAd(raw)
		if err != nil {
			lastErr = err
			continue
		}
		if err := ad.Validate(); err != nil {
			lastErr = err
			continue
		}
		return ad, nil
	}
	return Ad{}, fmt.Errorf("dj: could not write a usable advert: %w", lastErr)
}

func parseAd(raw string) (Ad, error) {
	var ad Ad
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &ad); err != nil {
		return Ad{}, fmt.Errorf("dj: advert response is not valid json: %w", err)
	}
	return ad, nil
}

// buildAdPrompt asks for one advert in the station's voice.
//
// The brand rule is stated as a hard prohibition and then RESTATED as the thing
// to do, because negative instructions alone are weakly followed by every model
// -- the same reason the break prompt pairs its prohibitions with the validator
// that actually enforces them. Ad.Validate is the enforcement; this reduces the
// rate.
func buildAdPrompt(p *Persona, existing []Ad, category string, lastErr error) string {
	var b strings.Builder

	// PHRASING MATTERS MORE THAN CONTENT HERE, and it was arrived at by probing
	// rather than by taste. Earlier versions said the business was "completely
	// INVENTED" and the model returned brand "INVENTED", script "INVENTED" --
	// it read the emphasis as the answer. A later version labelled the fields
	// ("brand: the name of the business you invented") and got back exactly
	// that string as the brand. What works is an ordinary sentence with no
	// emphasised word and no field description worth copying.
	b.WriteString("You write radio adverts for made-up local businesses.\n\n")
	fmt.Fprintf(&b, "Write one advert for %s. Make up a name for the business. About %d words.\n\n",
		category, AdTargetWords)
	// THE PERSONA BLOCK IS DELIBERATELY NOT HERE. An advert is read by the
	// ANNOUNCER, not by the DJ -- the voice change is the whole signal that
	// this is an advert -- so writing it in the DJ's voice is wrong before any
	// model sees it. It is also actively dangerous with a small model: with the
	// block included, the model returned the persona's own speech_style and
	// forbidden list AS THE ADVERT SCRIPT, ten times out of ten, and every one
	// passed validation because nothing checked that a script was not a prompt.
	//
	// Themed on the persona means the station's SUBJECT, one short line with
	// nothing quotable in it.
	fmt.Fprintf(&b, "The station plays %s.\n", strings.Join(p.GoodForGenres(), ", "))
	b.WriteString("The name must not belong to a real company, and do not imitate a real advert.\n")

	if len(existing) > 0 {
		b.WriteString("\nNames already used:\n")
		for _, ad := range existing {
			fmt.Fprintf(&b, "  %s\n", ad.Brand)
		}
	}
	if lastErr != nil {
		fmt.Fprintf(&b, "\nThe last attempt was rejected: %s\nFix exactly that.\n", lastErr)
	}

	b.WriteString("\nReply with a JSON object containing \"brand\" (the name) and \"script\" (what the announcer says).\n")
	return b.String()
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

// degenerateRun catches a model that has fallen into a loop.
//
// Small models under a grammar do this: the sampler keeps a token legal, the
// model has nothing left to say, and the field fills with "} } } } }" or the
// same clause four times over. It is unmistakable to a listener and invisible
// to every other check here.
func degenerateRun(s string) (string, bool) {
	words := strings.Fields(s)
	if len(words) < 6 {
		return "", false
	}
	run, count := "", 0
	for _, w := range words {
		if w == run {
			if count++; count >= 4 {
				return run, true
			}
			continue
		}
		run, count = w, 1
	}
	return "", false
}
