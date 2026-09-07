// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrewloable/jockora/internal/enrich"
)

// TokenBudget caps the assembled prompt AND the schema sent with it.
//
// Under llama.cpp's native /completion there is no chat template and, because a
// grammar forces JSON from the first token, no reasoning preamble either. What
// the original budget did not anticipate is that the json_schema travels with
// the request, and a schema whose asserted_facts enum lists every resolvable
// fact can run to several hundred tokens on a well-enriched pair of tracks. So
// the cap covers both.
const TokenBudget = 2000

// WordsPerSecond converts a speaking window into a word target. Roughly 150
// words per minute, which is unhurried broadcast delivery rather than a
// newsreader.
const WordsPerSecond = 2.5

// MaxFactsInPrompt is how many dossier facts the writer is shown. More than
// three and a thirty-second break turns into a recitation.
const MaxFactsInPrompt = 3

// Prohibitions is what the DJ has already said and must not repeat.
//
// NOTHING POPULATES THIS, AND THAT IS DELIBERATE. Wiring it was tried on
// 2026-09-06, measured, and reverted. Filling it from the said-lines index --
// the twenty most recent openings and a hundred recent 4-grams -- run at an
// identical seed against the same library:
//
//	                 breaks  collisions  truncated  would air
//	not populated        40           7         10   33 of 50  (66%, CI 52-78)
//	populated            38          23         12   15 of 50  (30%, CI 19-44)
//
// COLLISIONS MORE THAN TRIPLED. The intervals do not overlap, so this is an
// effect rather than noise, and the mechanism is visible in the failures: the
// colliding phrases were the greetings themselves -- "hey tuned welcome folks",
// "keep tight folks hey" -- which is to say the model used the very phrases it
// had just been shown and told to avoid.
//
// This is the same failure already recorded below about worked examples, and it
// is the strongest form of the rule this file keeps learning: WHATEVER THE
// PROMPT SHOWS, THE MODEL SAYS. Prohibition belongs in the sampler and the
// validator, where it is enforced rather than suggested.
//
// The type and its rendering are kept because they cost nothing and because the
// numbers above need somewhere to live. Do not wire it again without beating
// 33 of 50.
//
// It is also the SOFTEST input in the prompt and the least individually
// load-bearing, which is why it is the first thing trimmed when the budget
// binds.
type Prohibitions struct {
	Openings []string
	NGrams   []string
}

// PromptInput is everything needed to write one break.
//
// A struct rather than eight positional parameters, and it carries the schema
// because round 12 made the schema part of the budget.
type PromptInput struct {
	Persona *Persona

	// Previous is the track that just ENDED. It may be nil at session start, and
	// it is what a backsell names.
	Previous *enrich.Dossier
	Current  *enrich.Dossier
	Next     *enrich.Dossier

	// PreviousArtist and PreviousTitle name the track that just finished. A
	// backsell needs the name itself, which the dossier does not carry.
	PreviousArtist string
	PreviousTitle  string

	// CurrentArtist, CurrentTitle, NextArtist and NextTitle name the records on
	// either side of the break.
	//
	// These were missing entirely, and the DJ was never told what it was about
	// to play. With a thin dossier it filled the gap the only way it could:
	// asked to introduce an unknown record it announced "Unknown Territory by
	// The Unseen", a song and a band that do not exist. A name is the one thing
	// a station always knows -- it comes from the file's tags, or failing that
	// from the filename -- so withholding it manufactured the exact invention
	// the whole design exists to prevent.
	CurrentArtist string
	CurrentTitle  string
	NextArtist    string
	NextTitle     string

	// CurrentAlbum, CurrentYear, NextAlbum and NextYear are the file's OWN
	// tags, and they are rendered only where there is no dossier.
	//
	// The scanner has always read them and the DJ was never shown either, so an
	// unenriched record left the jock a bare title to be interesting about. A
	// track that HAS a dossier already carries a release field somebody
	// reasoned about, and a raw tag beside it would sooner or later contradict
	// it -- so these fill a hole rather than adding to a full page.
	//
	// CONTEXT, NOT FACT. They are spoken from, exactly as the title is, and
	// never declared: asserted_facts stays an enum built from resolvable
	// dossier facts and BreakSchema does not move.
	CurrentAlbum string
	CurrentYear  int
	NextAlbum    string
	NextYear     int

	Prohibitions  Prohibitions
	Placement     string
	WindowSeconds float64
	Schema        map[string]any

	// IsColdOpen marks the first break after the stream starts.
	//
	// The first thing said after someone presses play should acknowledge that
	// they just arrived, rather than continuing a conversation they were not
	// part of. v0.1 serves one shared session, so this means the STREAM
	// starting, not a listener connecting.
	IsColdOpen bool
}

// EstimateTokens approximates a token count from character length.
//
// Four characters per token is the usual rule of thumb for English. It is an
// estimate on purpose: the alternative is shipping a tokeniser for the model of
// the day, and the budget has 500 tokens of headroom precisely so the estimate
// does not have to be exact.
func EstimateTokens(s string) int { return len(s) / 4 }

// WordTarget is how many words fit the speaking window.
func WordTarget(windowSeconds float64) int {
	if windowSeconds <= 0 {
		return 0
	}
	return int(windowSeconds*WordsPerSecond + 0.5)
}

// BuildBreakPrompt assembles the writing prompt, enforcing the budget BEFORE the
// call rather than truncating the response afterwards.
//
// When the budget binds, prohibitions are trimmed first and the second track's
// dossier next. The persona and the instructions are never trimmed: the persona
// is ground truth, and the instructions are what make the response parseable.
func BuildBreakPrompt(in PromptInput) (string, error) {
	if in.Persona == nil {
		return "", fmt.Errorf("dj: no persona")
	}

	schemaTokens := 0
	if in.Schema != nil {
		raw, err := json.Marshal(in.Schema)
		if err != nil {
			return "", fmt.Errorf("dj: encoding break schema: %w", err)
		}
		schemaTokens = EstimateTokens(string(raw))
	}

	prohib := in.Prohibitions
	next := in.Next

	// Trim the softest input until it fits, then the second dossier, then give
	// up and return what is left rather than silently overrunning.
	for {
		prompt := assemble(in, next, prohib)
		if EstimateTokens(prompt)+schemaTokens <= TokenBudget {
			return prompt, nil
		}

		switch {
		case len(prohib.NGrams) > 0:
			prohib.NGrams = halve(prohib.NGrams)
		case len(prohib.Openings) > 0:
			prohib.Openings = halve(prohib.Openings)
		case next != nil:
			next = nil
		default:
			// Persona plus instructions alone exceed the budget. That is a
			// persona-file problem, not something to paper over by cutting the
			// instructions that make the output parseable.
			return prompt, fmt.Errorf("dj: prompt is %d tokens over budget with nothing left to trim",
				EstimateTokens(prompt)+schemaTokens-TokenBudget)
		}
	}
}

func halve(s []string) []string {
	if len(s) <= 1 {
		return nil
	}
	return s[:len(s)/2]
}

// assemble renders the prompt for a given set of trimmed inputs.
func assemble(in PromptInput, next *enrich.Dossier, prohib Prohibitions) string {
	var b strings.Builder

	b.WriteString(in.Persona.PromptBlock())
	b.WriteString("\n\nYOU ARE WRITING ONE SPOKEN RADIO BREAK.\n\n")

	// What the three fields ARE. Without this the model is asked for a JSON
	// object whose keys it has never been told the meaning of, and it fills
	// them with plausible placeholders -- "Your radio break sentence goes
	// here", "TEXT OF YOUR RADIO BREAK HERE" -- which then AIR, because they
	// are grammatical, the right length, and collide with nothing.
	// Kept SHORT and free of anything that reads like content. Describing the
	// three fields in prose made the model copy the descriptions -- "Opening
	// line, if any. Up to 15 words." -- exactly as an earlier version, which
	// described nothing, made it invent "Your radio break sentence goes here".
	// Whatever describes the output gets lifted into it, so the prompt says as
	// little as it can and the validator is the floor.
	b.WriteString("Every field holds words you say ALOUD, into a microphone.\n")
	b.WriteString("Never describe a line instead of saying it, and never say the\n")
	b.WriteString("name of a field or a fact: those are plumbing, not speech.\n\n")

	// "about N words" reads as permission to overshoot, and it was taken:
	// measured over 49 breaks, ramps ran 1.5x their target and outros 2.1x.
	// A ceiling with a number to count against is a different instruction from
	// an approximation.
	words := WordTarget(in.WindowSeconds)
	fmt.Fprintf(&b, "LENGTH: %d words MAXIMUM. Count them. Fewer is better.\n", words)
	fmt.Fprintf(&b, "You are speaking over %.1f seconds of music. Word %d lands as\n",
		in.WindowSeconds, words+1)
	b.WriteString("the vocal starts, and the listener hears you talk over the song.\n\n")

	// Padding to a word count by repeating a name is what a model does when it
	// has run out of things to say. Measured: "Howard Shore, Howard Shore,
	// Howard Shore, born in 1946, born in 1946, born in 1946." Saying less is
	// always available and always better.
	b.WriteString("Say each name, title and year ONCE. If you find yourself repeating\n")
	b.WriteString("one to fill the time, stop talking instead.\n\n")

	if in.Placement != "" {
		fmt.Fprintf(&b, "PLACEMENT: %s\n", placementGuidance(in.Placement))
	}

	if in.IsColdOpen {
		b.WriteString("\nTHIS IS THE FIRST THING YOU SAY AFTER THE STREAM STARTS.\n")
		b.WriteString("Someone has just tuned in. Acknowledge that they have arrived,\n")
		b.WriteString("in your own way, without a scripted greeting and without\n")
		b.WriteString("pretending they have been here all along.\n")
	}

	if in.Placement == "outro" && in.PreviousTitle != "" {
		// Naming what just finished is most of what makes a host feel like a
		// host rather than an announcer.
		fmt.Fprintf(&b, "\nBACKSELL: name the track that just finished -- %q by %s --\n",
			in.PreviousTitle, in.PreviousArtist)
		b.WriteString("then hand off. You are talking over its tail, so it is still\n")
		b.WriteString("in the listener's ear.\n")
		// No sleeve for the backsell. The track has already played, so the
		// jock is naming it rather than introducing it, and the writer does
		// not carry tags for it -- adding them here would print an empty line
		// and a zero year.
		writeDossier(&b, "THE TRACK THAT JUST FINISHED", "prev", in.Previous,
			in.PreviousArtist, in.PreviousTitle, "", 0)
	}

	writeDossier(&b, "THE TRACK NOW ENDING", "cur", in.Current,
		in.CurrentArtist, in.CurrentTitle, in.CurrentAlbum, in.CurrentYear)
	writeDossier(&b, "THE TRACK COMING UP", "next", next,
		in.NextArtist, in.NextTitle, in.NextAlbum, in.NextYear)

	if in.Current == nil && next == nil && in.Previous == nil {
		// An empty dossier is a designed outcome, not a failure. The DJ has
		// personality and nothing else, and must not fill the gap by inventing.
		b.WriteString("\nYOU HAVE NO FACTS ABOUT EITHER TRACK.\n")
		b.WriteString("This is not a problem and it is not an apology. Not knowing is\n")
		b.WriteString("one of the oldest things a radio host does, and it is usually\n")
		b.WriteString("funnier than knowing. Be entertaining ABOUT the not knowing:\n")
		b.WriteString("react to the title, to the sound, to what the name makes you\n")
		b.WriteString("picture, to your own week. Wonder aloud. Be wrong out loud, as\n")
		b.WriteString("long as everybody can hear that you are guessing.\n\n")
		b.WriteString("What you may NOT do is state something as though you knew it.\n")
		// THE YEAR IS THE ONE EXCEPTION NOW. Forbidding it outright while an
		// album year is printed above is a contradiction, and a model handed
		// one contradiction discounts the rest of the paragraph with it.
		b.WriteString("Do not name a place, a label or a band member as fact. The\n")
		b.WriteString("only year you may say is one printed above, and it is a sleeve\n")
		b.WriteString("date rather than research, so it may be a reissue.\n")
		b.WriteString("A guess the listener can hear is a guess is entertainment; the\n")
		b.WriteString("same sentence said flatly is a lie they will repeat.\n")
	} else {
		b.WriteString("\nState ONLY the facts listed above. Anything else you think you\n")
		b.WriteString("know about these records is not available to you.\n")
		// The facts arrive as finished sentences and a model will happily read
		// one out word for word. GATE 5 measured the result: every remaining
		// collision was a fact recited verbatim in two different breaks --
		// "howard shore born 1946", "nickelback canadian group formed".
		//
		// The n-gram index then correctly refuses the second break, so this is
		// not cosmetic: it is the difference between a break airing and being
		// dropped.
		b.WriteString("Say them in YOUR OWN WORDS. Reading a fact out as written is how\n")
		b.WriteString("two breaks end up sharing a sentence, and the second one is cut.\n")
		// The bracketed labels, named once. Without this the model has seen the
		// ids but not been told what they are for, and declares the fact text.
		b.WriteString("In asserted_facts put the bracketed LABELS of the facts you used,\n")
		b.WriteString("exactly as written, never the sentences themselves. Never say a\n")
		b.WriteString("label out loud.\n")
		// TRIED AND REVERTED, 2026-09-06. Four more lines here told the writer
		// that a year is never a sentence of its own. Run against an identical
		// seed, identical windows and an identical library, it changed the
		// recited-fact count from 29 to 28 and the collision count from 9 to 9,
		// while tripling the responses that hit the 200-token output budget and
		// came back truncated: 3 breaks lost before, 9 after. The pass rate
		// appeared to RISE, from 45% to 49%, purely because the denominator
		// shrank. Longer instructions made the writer write longer.
		//
		// Recitation is therefore not a wording problem and not fixable by
		// adding prose. Leave it to a listener at room test B to say whether it
		// even matters, and if it does, spend the budget rather than the prompt.
	}

	if len(prohib.Openings) > 0 {
		b.WriteString("\nYOU HAVE ALREADY OPENED WITH THESE. Do not reuse them:\n")
		for _, o := range prohib.Openings {
			b.WriteString("  - " + o + "\n")
		}
	}
	if len(prohib.NGrams) > 0 {
		b.WriteString("\nYOU HAVE ALREADY USED THESE PHRASES. Do not reuse them:\n")
		for _, g := range prohib.NGrams {
			b.WriteString("  - " + g + "\n")
		}
	}

	// Spelled out, not just left to the schema parameter: measured against a
	// hosted free model (OpenRouter, minimax-m3) that does not reliably honour
	// response_format for a creative request. Two failures, fixed in two
	// steps:
	//
	//  1. Left as "return the JSON object described by the schema", it
	//     answered in prose, with a trailing "Fact ids used: f1, f2" line,
	//     0 of 6 trials valid. Naming the specific shapes that leaked (a
	//     sentence before the JSON, a fence around it) fixed that part.
	//  2. Even then, against the real four-field schema, it invented its own
	//     keys instead -- "text", "script", "spoken_break" -- 0 of 3 trials,
	//     because the schema parameter communicates a shape to some hosted
	//     backends but not the field NAMES. Only spelling the keys out in the
	//     prompt text fixed it: 2 of 2 clean trials once naming was added.
	//
	// Grammar-constrained backends (llama.cpp) already cannot emit a wrong
	// key or extra prose, so this costs them nothing.
	b.WriteString("\nReturn ONLY the JSON object, with exactly these keys: \"opening\",\n")
	b.WriteString("\"body\", \"handoff\", \"asserted_facts\". No prose, no commentary,\n")
	b.WriteString("no markdown fences, before or after it.\n")
	return b.String()
}

func placementGuidance(placement string) string {
	switch placement {
	case "ramp":
		return "you are talking over the instrumental INTRO of the next track. " +
			"Talk up what is STARTING. Do not name the previous track here: the " +
			"song is beginning and naming what just ended is confusing. " +
			"Finish before the singing starts."
	case "outro":
		return "you are talking over the instrumental TAIL of the track that is ending."
	default:
		return "you are talking in the gap between two tracks."
	}
}

// writeDossier renders at most MaxFactsInPrompt facts. More than three turns a
// thirty-second break into a recitation.
// TRIED AND REVERTED, 2026-09-06. This withheld recently-used facts from the
// dossier block, not just from the schema, on the correct observation that the
// fact cooldown only stops a writer DECLARING a fact and does nothing about it
// reading one out: three collisions on "howard shore born 1946" came from
// breaks that declared station_tags and mood and never asserted a year at all.
//
// The mechanism fix worked and did not help. Measured over three runs at an
// identical seed, 50 requests each:
//
//	                            breaks  collisions  recited  truncated  would air
//	schema cooldown only            47           9        9          3         38
//	also hidden for 10 breaks       38           9        2         12         29
//	also hidden for 3 breaks        43          11        7          7         32
//
// Recitation fell from 9 to 2, exactly as intended, and the collisions simply
// became generic filler instead -- "rock roll stay tuned", "don t miss beat".
// Fewer breaks survived, because a writer with less to say pads until it hits
// the token budget. Adding INSTRUCTIONS did the same thing, in the same
// direction, for the same reason.
//
// THE WRITER'S PROBLEM IS NOT THAT IT REPEATS FACTS. It is that it does not
// have enough to say. Repetition is the symptom of a thin dossier, so the fix
// is richer enrichment, not a stricter cooldown.
func writeDossier(b *strings.Builder, heading, prefix string, d *enrich.Dossier,
	artist, title, album string, year int) {
	// The NAME is printed even with no dossier at all. Knowing what is playing
	// is not the same as knowing anything about it, and a jock that knows the
	// title has something true to be funny about instead of inventing one.
	if d == nil && artist == "" && title == "" {
		return
	}

	fmt.Fprintf(b, "\n%s\n", heading)
	if title != "" {
		b.WriteString("  title: " + title + "\n")
	}
	if artist != "" {
		b.WriteString("  artist: " + artist + "\n")
	}
	if d == nil {
		// THE SLEEVE, where there is nothing else. One line, and the year rides
		// on it rather than standing alone: a bare year reads as a fact stated
		// flatly, and a tag year is frequently the REISSUE -- confidently wrong
		// in a way a dossier release date is not.
		if album == "" {
			b.WriteString("  (nothing else is known about this record)\n")
			return
		}
		b.WriteString("  album: " + album)
		if year > 0 {
			fmt.Fprintf(b, " (%d)", year)
		}
		b.WriteString("\n")
		// ONE LINE, NO CAVEAT UNDER IT. A second line saying the sleeve is not
		// research invited the model to talk ABOUT the sleeve -- measured live:
		// "the sleeve is usually telling the truth, sometimes, mostly" -- and
		// breaks then ran past the 200-token budget and were truncated, which
		// drops them. The no-facts block below already says what the year is
		// worth, once, where it belongs.
		b.WriteString("  (nothing else is known about this record)\n")
		return
	}
	if d.SubjectSummary != "" {
		fmt.Fprintf(b, "  about [%s.subject_summary]: %s\n", prefix, d.SubjectSummary)
	}
	if d.Release != "" {
		fmt.Fprintf(b, "  release [%s.release]: %s\n", prefix, d.Release)
	}
	if len(d.StationTags) > 0 {
		b.WriteString("  genre: " + strings.Join(d.StationTags, ", ") + "\n")
	}
	if len(d.Mood) > 0 {
		b.WriteString("  mood: " + strings.Join(d.Mood, ", ") + "\n")
	}

	// EACH FACT CARRIES THE ID THE SCHEMA WILL ACCEPT FOR IT.
	//
	// asserted_facts is an enum of ids like "cur.artist_facts[0]", and under
	// llama.cpp a grammar makes those the only emittable values -- so the model
	// cannot get it wrong and never needed to be told. A hosted endpoint treats
	// the schema as a REQUEST, and a model that has never seen an id declares
	// the fact TEXT instead. Measured live on the first break after switching
	// host: every fact-bearing break dropped as ungrounded.
	//
	// Showing them is only safe because saying one is caught:
	// placeholderFieldRef refuses a break that speaks prev/cur/next dot
	// anything, and the instructions already say a field name is plumbing
	// rather than speech.
	facts := d.ArtistFacts
	if len(facts) > MaxFactsInPrompt {
		facts = facts[:MaxFactsInPrompt]
	}
	for i, f := range facts {
		fmt.Fprintf(b, "  fact [%s.artist_facts[%d]]: %s\n", prefix, i, f)
	}
}
