// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/andrewloable/jockora/internal/enrich"
)

// ErrBreakRepetitive means the writer could not produce a fresh break.
//
// It is not an error to surface to the listener. A dropped break is a designed
// outcome: the music continues, the mixer extends the crossfade, and nothing
// about it is audible as a fault.
var ErrBreakRepetitive = errors.New("dj: break repeated something already said")

// ErrBreakDropped means a break could not be produced, for a reason the wrapped
// error names.
//
// It exists because Generate used to report EVERY failure as ErrBreakRepetitive,
// including malformed JSON and model errors. A caller asking errors.Is(err,
// ErrBreakRepetitive) would then treat a broken model as a chatty one, and the
// log line said "break repeated something already said: not valid json" -- an
// answer that points at the wrong half of the system.
var ErrBreakDropped = errors.New("dj: break dropped")

// DefaultRefusalBackoff is the pause before a second attempt at a model that
// refused the first.
//
// A hosted endpoint allows a fixed number of requests a MINUTE, and the
// enrichment queue runs continuously against the same allowance -- so the DJ's
// occasional request is the one that loses a burst. Retrying such a refusal
// immediately spends the second attempt inside the same window and fails for
// the identical reason, which is what silence at a cadence of one break per
// track turned out to be.
//
// Fifteen seconds against a lookahead budget measured at 148 seconds, of which
// generation itself spent six. Long enough to leave a burst behind, short
// enough that two attempts and a wait still air on time.
const DefaultRefusalBackoff = 15 * time.Second

// MaxAttempts is the ceiling on GENERATIONS per break.
//
// ONE. Whatever the model writes is what airs, or the break is dropped and
// music plays -- which is the designed outcome and is not audible as a fault.
// It was two, spent rewriting a break that came out too long; a break that
// does not fit is now re-placed into the wider gap instead, which costs no
// call at all.
const MaxAttempts = 1

// MaxRefusals is how many times a REFUSAL is waited out.
//
// A refusal is not a generation. A rate limit produces no output -- the model
// never wrote anything -- so retrying one is not a second attempt at writing,
// it is the first attempt finally happening, and counting it against the
// generation budget would silence the DJ for a minute every time a burst of
// enrichment traffic landed on it. Bounded, because a model that refuses three
// times running is not busy, it is gone.
const MaxRefusals = 3

// DropReason records why a break did not air.
type DropReason string

const (
	DropRepetitive DropReason = "repetitive"
	// DropInstructionEcho means the model spoke the prompt back instead of
	// writing a break.
	DropInstructionEcho DropReason = "instruction_echo"
	DropUngrounded      DropReason = "ungrounded"
	// DropSelfRepetitive means the break repeated a phrase inside itself.
	DropSelfRepetitive DropReason = "self_repetitive"
	DropBadJSON        DropReason = "bad_json"
	DropLLMError       DropReason = "llm_error"
)

// Writer produces one break from an assembled prompt and schema.
type Writer interface {
	WriteBreak(ctx context.Context, prompt string, schema map[string]any, maxTokens int) (string, error)
}

// Validator enforces non-repetition and groundedness AFTER generation.
//
// The prompt asks; this enforces. Negative instructions are weakly followed by
// every model, so the prohibition list in the prompt reduces the rate and the
// validator sets the floor.
type Validator struct {
	Writer Writer
	Said   *SaidLines

	// Backoff is how long to wait before retrying a REFUSAL, as opposed to a
	// bad answer. Zero means DefaultRefusalBackoff.
	Backoff time.Duration

	mu         sync.Mutex
	attempts   int
	breaks     int
	drops      int
	ungrounded int
	reasons    map[DropReason]int

	// trackNames are the artist and title of the records this break is about.
	// Grams made entirely of these words are not collisions: naming the record
	// is the job, and consecutive breaks share tracks by design.
	trackNames []string

	// recentFacts maps a fact id to the break number it was last asserted in.
	// A fact used inside FactCooldown breaks is withheld from the next
	// schema, so the DJ cannot recite it twice in quick succession.
	recentFacts map[string]int
}

// FactCooldown is how many breaks must pass before a fact may be asserted
// again.
//
// Ten, which at the default cadence is roughly forty tracks. Long enough that a
// listener will not hear the same sentence about a band twice in a session,
// short enough that a small library does not run out of things to say.
const FactCooldown = 10

// Stats is a snapshot of validator counters.
type Stats struct {
	Breaks   int
	Drops    int
	Attempts int
	Reasons  map[DropReason]int
	// Ungrounded counts fact declarations that named nothing and were removed.
	// The breaks carrying them still aired.
	Ungrounded int
}

// DropRate is the fraction of requested breaks that did not air.
//
// It feeds the status endpoint and GATE 5. A rising drop rate means the DJ is
// running out of things to say, which is the failure mode that arrives slowly.
func (v *Validator) DropRate() float64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	total := v.breaks + v.drops
	if total == 0 {
		return 0
	}
	return float64(v.drops) / float64(total)
}

// Stats returns the counters.
func (v *Validator) Stats() Stats {
	v.mu.Lock()
	defer v.mu.Unlock()
	reasons := make(map[DropReason]int, len(v.reasons))
	for k, n := range v.reasons {
		reasons[k] = n
	}
	return Stats{Breaks: v.breaks, Drops: v.drops, Attempts: v.attempts,
		Reasons: reasons, Ungrounded: v.ungrounded}
}

func (v *Validator) record(reason DropReason, aired bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.reasons == nil {
		v.reasons = map[DropReason]int{}
	}
	if aired {
		v.breaks++
		return
	}
	v.drops++
	v.reasons[reason]++
}

// backoff is the configured refusal wait, or the default.
func (v *Validator) backoff() time.Duration {
	if v.Backoff > 0 {
		return v.Backoff
	}
	return DefaultRefusalBackoff
}

// countUngrounded records declarations that named nothing. The break still
// aired; this says the model got the labels wrong, which is a prompt problem
// and needs to be visible as one.
func (v *Validator) countUngrounded(n int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.ungrounded += n
}

func (v *Validator) countAttempt() {
	v.mu.Lock()
	v.attempts++
	v.mu.Unlock()
}

// Generate writes one break, regenerating at most once, and records it only if
// it passes.
//
// A rejected break is NEVER written to said_lines: recording rejects would
// poison the index against future good breaks that happen to reuse a phrase the
// DJ never actually aired.
// words is the length the prompt asked for, which is what the token budget is
// derived from: a flat cap truncated the longest windows and those breaks were
// dropped. Zero means the default.
func (v *Validator) Generate(ctx context.Context, prompt string, words int, prev, cur, next *enrich.Dossier) (*Break, error) {
	schema := BreakSchemaExcluding(prev, cur, next, v.cooledFacts())
	lastReason := DropRepetitive
	var lastErr error

	// Two budgets, deliberately separate: generations, and refusals that
	// produced no generation at all.
	for attempt, refusals := 0, 0; attempt < MaxAttempts; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		raw, err := v.Writer.WriteBreak(ctx, prompt, schema, TokensFor(words))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastReason, lastErr = DropLLMError, err

			// A REFUSAL IS NOT A BAD ANSWER, and it is not a generation either.
			// A bad answer would be different next time; a rate limit will not
			// clear until its window does, so the retry has to WAIT or it is
			// not a retry at all -- and it must not spend the one generation
			// this break is allowed, because nothing was generated.
			if errors.Is(err, enrich.ErrLLMUnavailable) && refusals+1 < MaxRefusals {
				refusals++
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(v.backoff()):
				}
				continue
			}
			break
		}

		// The model answered. THAT is the generation.
		attempt++
		v.countAttempt()

		b, err := ParseBreak(raw)
		if err != nil {
			lastReason, lastErr = DropBadJSON, err
			continue
		}

		// UNRESOLVABLE DECLARATIONS ARE DROPPED, THE BREAK IS NOT.
		//
		// This was fatal, on the reasoning that the schema enum makes an
		// ungrounded id unrepresentable -- true of a grammar-constrained
		// backend and not of a hosted one, which treats the schema as a
		// request. Measured live: the model declared the fact TEXT and every
		// fact-bearing break was thrown away.
		//
		// Dropping the declaration leaves a break that declares nothing, which
		// is already an accepted outcome -- a personality-only break -- so this
		// is no weaker than what the model could legally have returned. The
		// prose is untouched either way, and one generation means there is no
		// second chance to spend on a malformed label.
		if dropped := KeepResolvableFacts(b, prev, cur, next); len(dropped) > 0 {
			// Counted, not logged: this type has no logger, and a count under
			// its own reason is what the console reads. It shows up as
			// "ungrounded" in the break stats without the break being lost.
			v.countUngrounded(len(dropped))
		}

		// The model sometimes recites the instructions instead of following
		// them. The said-lines index catches the SECOND occurrence as
		// repetition, which means the first one airs -- a listener hearing
		// "state only facts listed above" in a DJ voice. Measured live, this
		// was the single largest source of drops.
		if phrase, echoed := echoesInstructions(b.Text(), v.names()); echoed {
			lastReason = DropInstructionEcho
			lastErr = fmt.Errorf("%w: the break recites the prompt: %q", ErrBreakDropped, phrase)
			continue
		}

		// Before the said-lines index, because the index cannot see this: it
		// compares against breaks that ALREADY AIRED, so a break that repeats
		// itself collides with nothing and goes out. Checked here it is caught
		// on the attempt that produced it.
		if phrase, looped := repeatsItself(b.Text(), v.names()); looped {
			lastReason = DropSelfRepetitive
			lastErr = fmt.Errorf("%w: the break says %q more than once", ErrBreakDropped, phrase)
			continue
		}

		hit, gram, err := v.Said.CheckCollisionIgnoring(ctx, b.Text(), v.names())
		if err != nil {
			return nil, err
		}
		if hit {
			lastReason = DropRepetitive
			lastErr = fmt.Errorf("%w: %q", ErrBreakRepetitive, gram)
			continue
		}

		if err := v.Said.Record(ctx, b.Text()); err != nil {
			return nil, err
		}
		v.rememberFacts(b.AssertedFacts)
		v.record("", true)
		return b, nil
	}

	v.record(lastReason, false)
	if lastReason == DropRepetitive {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, ErrBreakRepetitive
	}
	return nil, fmt.Errorf("%w (%s): %v", ErrBreakDropped, lastReason, lastErr)
}

// instructionPhrases are fragments of the writing prompt that must never be
// spoken on air. Kept deliberately short and specific: a phrase here is one no
// DJ would say and every prompt does.
var instructionPhrases = []string{
	"only facts listed",
	"facts listed above",
	"state only",
	"do not invent",
	"asserted_facts",
	"the following facts",
	"return only the json",
	"json object",
	"word target",
	"speaking window",
	// Persona-card scaffolding. A small model hands these back as content when
	// the card is in the prompt at all.
	"you never do these things",
	"never mentions the weather",
	"backsells like a person",
	"like someone talking to one person",
	"announcer's script",
	"brand name",
	"the name of the business",
	// Model scaffolding that is not English at all. Observed live: a break came
	// back as "}<tool_call|>```json }<tool_call|>```json" and aired, because
	// nothing checked that a break was made of words.
	"<tool_call",
	"</tool_call",
	"```",
	"<|im_start|",
	"<|im_end|",
	"<start_of_turn>",
	"<end_of_turn>",
	// Template placeholders. The model is filling a form rather than talking,
	// and every one of these AIRED in a live run: they are grammatical, the
	// right length, and collide with nothing in the said-lines index.
	"goes here",
	"your radio break",
	"your spoken",
	"your dialogue",
	"text of your",
	"your words here",
	"insert ",
	"must fit within",
	"word limit",
	"[your",
	// The FIELD NAMES themselves. Describing the fields in the prompt stopped
	// the model inventing placeholders and started it copying the
	// descriptions -- "Opening line, if any. Up to 15 words." -- which is the
	// same failure wearing the new wording.
	//
	// No DJ says "handoff" into a microphone. This is a structural rule, not
	// another entry in a list of phrases: a break that names the parts of a
	// break is describing itself rather than being one.
	"handoff",
	"opening line",
	"opening text",
	"body text",
	"if any.",
	"if any,",
	"up to 15 words",
	"line to greet",
	// Stage directions. The model describes the DELIVERY instead of
	// performing it: "Loudly and enthusiastically." aired as a break.
	"loudly and",
	"enthusiastically.",
	"excitedly.",
	"with enthusiasm",
	"in a booming voice",
	"[shouting",
	"(shouting",
	"line that transitions",
	"fits the context",
}

// Structural placeholder signatures.
//
// A phrase list is whack-a-mole and it lost: blocking "your radio break
// sentence goes here" produced "YOUR SPEECH HERE", then "YOUR 30 WORDS HERE",
// then "YOUR LINES HERE". These match the SHAPE instead, which is what a
// placeholder actually is.
var (
	// "your <anything> here" -- the possessive-you placeholder, in any casing
	// and with any filler between the two words.
	placeholderYourHere = regexp.MustCompile(`(?i)\byour\b[^.!?]{0,40}\bhere\b`)

	// A run of three or more SHOUTED words. A DJ does not speak in capitals;
	// template markers are written that way. Two is allowed so a real break can
	// say "OK" or name a band like "AC DC".
	placeholderShouting = regexp.MustCompile(`\b[A-Z][A-Z]+\b(?:[^\w\n]+\b[A-Z][A-Z]+\b){2,}`)

	// A dotted schema reference that leaked into speech: next.genre,
	// cur.artist_facts, prev.title.
	placeholderFieldRef = regexp.MustCompile(`(?i)\b(prev|cur|next)\.[a-z_]+`)

	// A snake_case identifier. Nobody says "3am_listening" or
	// "radio_intro_music" out loud; both aired, lifted from tag vocabularies.
	placeholderSnakeCase = regexp.MustCompile(`\b[a-z0-9]+_[a-z0-9_]+\b`)
)

// echoesInstructions reports whether a break is reciting its own prompt.
//
// Two layers, and the order matters only for the message: the structural rules
// catch the shape of a placeholder, and the phrase list catches the specific
// wordings that have actually been observed on air.
func echoesInstructions(text string, names []string) (string, bool) {
	if m := placeholderYourHere.FindString(text); m != "" {
		return m, true
	}
	// SHOUTING IS A MARKER ONLY WHEN IT IS MOST OF THE BREAK.
	//
	// Three consecutive capitalised words was the rule, because "YOUR SPEECH
	// HERE" once aired. It has since refused three good breaks: a shouted track
	// title twice, and "YOU ARE HERE" inside ninety words of prose. The
	// deployed model writes emphatically -- "I am DEAD, I am BURIED", "What a
	// TITLE" -- and with one generation per break, every false positive here is
	// silence.
	//
	// A placeholder IS the field: it fills the whole line. A flourish sits
	// inside real sentences. That is the difference worth measuring, and the
	// specific wording that started this is caught by placeholderYourHere in
	// any casing regardless.
	if m := placeholderShouting.FindString(text); m != "" &&
		!allNameWords(strings.ToLower(m), nameWordSet(names)) &&
		dominates(m, text) {
		return m, true
	}
	if m := placeholderFieldRef.FindString(text); m != "" {
		return m, true
	}
	if m := placeholderSnakeCase.FindString(text); m != "" {
		return m, true
	}

	lower := strings.ToLower(text)
	for _, phrase := range instructionPhrases {
		if strings.Contains(lower, phrase) {
			return phrase, true
		}
	}
	return "", false
}

// cooledFacts lists the fact ids still inside their cooldown.
func (v *Validator) cooledFacts() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []string
	for id, at := range v.recentFacts {
		if v.breaks-at < FactCooldown {
			out = append(out, id)
		}
	}
	return out
}

// rememberFacts records what a break asserted, so the next one cannot repeat it.
func (v *Validator) rememberFacts(ids []string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.recentFacts == nil {
		v.recentFacts = make(map[string]int, len(ids))
	}
	for _, id := range ids {
		v.recentFacts[id] = v.breaks
	}
}

// SetTrackNames tells the validator which proper nouns this break is allowed to
// repeat. Call it before Generate, once per boundary.
func (v *Validator) SetTrackNames(names ...string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.trackNames = append(v.trackNames[:0], names...)
}

func (v *Validator) names() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.trackNames...)
}

// repeatsItself reports a phrase the break says more than once, inside itself.
//
// THE SAID-LINES INDEX CANNOT SEE THIS. It compares a break against breaks that
// already aired, so the FIRST occurrence of a phrase is what it records -- and
// a break built entirely out of one repeated phrase collides with nothing and
// goes to air. Every one of the first eight breaks the deployed station
// produced was this shape: "nobody nobody, nobody, nobody, nobody, nobody,
// nobody nobody", "cure. mood cure. mood cure. mood".
//
// Same 4-gram run length as the collision index, but over EVERY word rather
// than content words only. Stopwords are noise when comparing two breaks and
// they are not noise inside one: "Boom. Now we ride. ... Boom. Now we ride."
// keeps just "boom ride" once the stopwords go, which is two words and below
// any sane threshold -- while a listener hears the whole sentence twice.
//
// TRACK NAMES ARE EXEMPT, for the same reason the collision index exempts them:
// naming the record is the job. Four consecutive words was supposed to be long
// enough that a backsell naming a record twice could not trip it, and the live
// probe showed that reasoning was simply wrong -- "Have Fun Storming the
// Castle" is a five-word title, so saying it twice is a repeated 4-gram. A
// break was rejected for doing its job.
//
// Matched over spoken words on BOTH sides, so the stopword inside a title
// counts: names reduced with ContentWords would drop the "the" and leave the
// gram "have fun storming the" unexempted, which is the gram that fired.
// A DEVICE IS NOT A LOOP, and the difference is HOW MUCH OF THE BREAK it is.
//
// One repeated four-gram was a drop, and Dutch Mahoney's card says he "repeats
// a word three times when twice would do" -- so the rule rejected the trait the
// card exists to produce. Measured live against minimax-m3 on 2026-09-07: the
// rhetorical repeats ("you are a LEGEND, a legend, a legend", "that is a TITLE,
// that is a title that demands respect") covered 6 to 11 per cent of their
// breaks, while every loop that actually aired covered 38 to 100 per cent --
// "nobody nobody nobody" is the whole break, "boom now we ride" twice in
// twenty-one words is more than a third of it.
//
// So the answer is the SHARE of the break sitting inside a repeated run, which
// separates the two with room to spare. It does not weaken the across-break
// index: a phrase reused in a LATER break is still refused outright by
// said_lines, because saying the same thing twice on one station is a different
// failure from a stuck model.
func repeatsItself(text string, names []string) (string, bool) {
	words := spokenWords(text)
	nameWords := nameWordSet(names)

	first := map[string]int{}
	seen := map[string]int{}
	// Word POSITIONS inside a repeated run, deduplicated: a phrase repeated
	// once produces several overlapping grams, and counting the grams would
	// make one device look like four.
	covered := map[int]bool{}
	worst := ""

	for i, g := range NGrams(words, GramSize) {
		if len(nameWords) > 0 && allNameWords(g, nameWords) {
			continue
		}
		seen[g]++
		at, repeat := first[g]
		if !repeat {
			first[g] = i
			continue
		}
		if worst == "" {
			worst = g
		}
		// SAID THREE TIMES IS A LOOP whatever the share says. Share alone had a
		// hole: a phrase repeated five times covers twenty words, which in a
		// long break is under the threshold, so the more a stuck model wrote
		// around its loop the safer the loop became. Three is measured, not
		// picked -- across every device caught live, no gram appears twice
		// over.
		if seen[g] >= 3 {
			return g, true
		}
		for k := 0; k < GramSize; k++ {
			covered[at+k] = true
			covered[i+k] = true
		}
	}
	if worst == "" {
		return "", false
	}
	if float64(len(covered))/float64(len(words)) < RepeatShare {
		return "", false
	}
	return worst, true
}

// RepeatShare is how much of a break must sit inside a repeated run before it
// reads as a loop rather than a turn of phrase.
//
// A quarter. The measured gap either side of it is wide -- 11 per cent for the
// worst device, 38 per cent for the mildest real loop -- and the consequence of
// being wrong is asymmetric: a dropped break is silence a listener notices, and
// an aired one that repeats itself is a listener noticing something else.
const RepeatShare = 0.25

// spokenWords is every word as it will be heard: lowercased, punctuation gone,
// nothing dropped.
func spokenWords(text string) []string {
	return strings.Fields(nonWordRE.ReplaceAllString(strings.ToLower(text), " "))
}

// nameWordSet is every word of every name this break is allowed to say, as the
// listener hears them: lowercased, punctuation gone, stopwords KEPT. Dropping
// the stopwords would leave "the" out of "Have Fun Storming the Castle" and
// unexempt the one gram that matters.
func nameWordSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, n := range names {
		for _, w := range spokenWords(n) {
			out[w] = true
		}
	}
	return out
}

// ShoutingShare is how much of a break must be one shouted run before it reads
// as a template marker rather than emphasis.
//
// Half. A marker fills the field it was meant to replace; a DJ shouting for
// effect does it inside sentences that carry the rest of the meaning.
const ShoutingShare = 0.5

// dominates reports whether a matched run is most of the text it came from.
func dominates(match, text string) bool {
	whole := len(spokenWords(text))
	if whole == 0 {
		return false
	}
	return float64(len(spokenWords(match)))/float64(whole) >= ShoutingShare
}
