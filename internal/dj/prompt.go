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
// It is the SOFTEST input in the prompt and the least individually
// load-bearing, which is why it is the first thing trimmed when the budget
// binds: losing one of a hundred prohibitions risks one repeat, while losing
// persona text changes who is talking.
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
	b.WriteString("Never describe a line instead of saying it.\n\n")

	words := WordTarget(in.WindowSeconds)
	fmt.Fprintf(&b, "LENGTH: about %d words. This is spoken over %.1f seconds of\n",
		words, in.WindowSeconds)
	b.WriteString("music and MUST fit. Going long means talking over a vocal.\n\n")

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
		writeDossier(&b, "THE TRACK THAT JUST FINISHED", in.Previous)
	}

	writeDossier(&b, "THE TRACK NOW ENDING", in.Current)
	writeDossier(&b, "THE TRACK COMING UP", next)

	if in.Current == nil && next == nil && in.Previous == nil {
		// An empty dossier is a designed outcome, not a failure. The DJ has
		// personality and nothing else, and must not fill the gap by inventing.
		b.WriteString("\nYOU HAVE NO FACTS ABOUT EITHER TRACK.\n")
		b.WriteString("Talk from personality only. Do NOT name a year, a place, a\n")
		b.WriteString("label, a band member or any other detail: you do not know any,\n")
		b.WriteString("and inventing one is worse than saying less.\n")
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

	b.WriteString("\nReturn ONLY the JSON object described by the schema.\n")
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
func writeDossier(b *strings.Builder, heading string, d *enrich.Dossier) {
	if d == nil {
		return
	}

	fmt.Fprintf(b, "\n%s\n", heading)
	if d.SubjectSummary != "" {
		b.WriteString("  about: " + d.SubjectSummary + "\n")
	}
	if len(d.StationTags) > 0 {
		b.WriteString("  genre: " + strings.Join(d.StationTags, ", ") + "\n")
	}
	if len(d.Mood) > 0 {
		b.WriteString("  mood: " + strings.Join(d.Mood, ", ") + "\n")
	}

	facts := d.ArtistFacts
	if len(facts) > MaxFactsInPrompt {
		facts = facts[:MaxFactsInPrompt]
	}
	for _, f := range facts {
		b.WriteString("  fact: " + f + "\n")
	}
}
