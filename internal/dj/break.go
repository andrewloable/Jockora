// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/andrewloable/jockora/internal/enrich"
)

// ErrBreakUngrounded means a break asserts something no dossier supports.
//
// Under the per-request fact enum this should never fire: an ungrounded id is
// unrepresentable at the sampler. It is kept because a schema constrains the
// output of the model, not the input to this function, and because an assertion
// that never fires is exactly the one worth keeping.
var ErrBreakUngrounded = errors.New("dj: break asserts an unresolvable fact")

// ErrBreakBadJSON means the response was not a break.
var ErrBreakBadJSON = errors.New("dj: break response is not valid json")

// FactConfidenceThreshold is the minimum confidence a dossier must carry before
// the DJ may state anything from it on air.
const FactConfidenceThreshold = 0.6

// ConfidenceScore converts a dossier's confidence label into a number.
//
// The dossier vocabulary is high/low/none, and this threshold is expressed as
// 0.6, so the mapping is stated here rather than left implicit: only "high"
// clears the bar. "low" means the model was not adequately grounded, and a DJ
// asserting that on air is the failure the dossier design exists to prevent.
func ConfidenceScore(label string) float64 {
	switch label {
	case enrich.ConfidenceHigh:
		return 1.0
	case enrich.ConfidenceLow:
		return 0.5
	default:
		return 0.0
	}
}

// Break is one written radio break.
type Break struct {
	Opening       string   `json:"opening"`
	Body          string   `json:"body"`
	Handoff       string   `json:"handoff"`
	AssertedFacts []string `json:"asserted_facts"`
}

// Text is the whole break as it will be spoken.
//
// A repeated leading phrase is collapsed. Models routinely restate the opening
// at the start of the body -- "Next up, Next up, a powerful declaration...",
// "Good evening. Good evening." -- because each field is generated knowing what
// the break is meant to sound like and not what the neighbouring field already
// said. It is the single most audible flaw in otherwise good output, and it is
// cheaper to fix here than to keep asking a model not to do it.
func (b *Break) Text() string {
	parts := nonEmpty(b.Opening, b.Body, b.Handoff)
	for i := range parts {
		parts[i] = stripSpeakerLabel(parts[i])
	}
	for i := 1; i < len(parts); i++ {
		parts[i] = dropRepeatedPrefix(parts[i-1], parts[i])
	}
	return strings.TrimSpace(strings.Join(nonEmpty(parts...), " "))
}

// speakerLabel matches a script-style speaker tag at the start of a line:
// "Dutch:", "Dutch 'The Hammer' Mahoney:", "DJ:".
//
// The model is writing a SCRIPT rather than speaking, and the label is not part
// of the break -- it is the stage direction around it. Stripped rather than
// rejected, because everything after the colon is usually a perfectly good
// break and throwing it away would cost a good line to fix a prefix.
var speakerLabel = regexp.MustCompile(`^[A-Z][\w'". ]{0,40}:\s+`)

// stripSpeakerLabel removes a leading speaker tag.
func stripSpeakerLabel(s string) string {
	// Only when what follows is real speech; "Coming up:" is a legitimate
	// opening and must survive.
	trimmed := speakerLabel.ReplaceAllString(s, "")
	if trimmed == s || len(strings.Fields(trimmed)) < 3 {
		return s
	}
	// A label is a NAME, and a name has no lowercase words in it. That is what
	// separates "Dutch 'The Hammer' Mahoney:" from "Coming up:", which is a
	// perfectly good opening and must survive untouched.
	label := strings.TrimSuffix(strings.TrimSpace(s[:len(s)-len(trimmed)]), ":")
	words := strings.Fields(label)
	if len(words) == 0 || len(words) > 4 {
		return s
	}
	for _, w := range words {
		if !looksLikeName(w) {
			return s
		}
	}
	return trimmed
}

// looksLikeName reports whether a word could be part of a speaker's name:
// capitalised, or a quoted nickname, or an initialism.
func looksLikeName(w string) bool {
	w = strings.Trim(w, `'"`)
	if w == "" {
		return false
	}
	for _, r := range w {
		if unicode.IsLower(r) {
			return false
		}
		break // only the first rune decides
	}
	return unicode.IsUpper([]rune(w)[0])
}

// dropRepeatedPrefix removes the start of next when it repeats the end of prev.
//
// Compared on normalised words so "Next up," and "Next up" match, and only for
// runs of at least two words: a single shared word is ordinary English.
func dropRepeatedPrefix(prev, next string) string {
	prevWords, nextWords := strings.Fields(prev), strings.Fields(next)
	if len(prevWords) == 0 || len(nextWords) < 2 {
		return next
	}

	// Longest first, so "Next up, a" is preferred over "Next".
	maxRun := min(len(prevWords), len(nextWords)-1)
	for n := maxRun; n >= 2; n-- {
		if sameWords(prevWords[len(prevWords)-n:], nextWords[:n]) {
			return strings.TrimSpace(strings.Join(nextWords[n:], " "))
		}
	}
	// A whole short opening repeated verbatim: "Good evening." / "Good evening."
	if len(prevWords) <= 3 && len(nextWords) > len(prevWords) && sameWords(prevWords, nextWords[:len(prevWords)]) {
		return strings.TrimSpace(strings.Join(nextWords[len(prevWords):], " "))
	}
	return next
}

func sameWords(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if normaliseWord(a[i]) != normaliseWord(b[i]) {
			return false
		}
	}
	return true
}

// normaliseWord strips punctuation and case so "up," equals "Up".
func normaliseWord(w string) string {
	return strings.ToLower(strings.TrimFunc(w, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}))
}

func nonEmpty(parts ...string) []string {
	var out []string
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// A break must be made of words rather than punctuation. Two words and eight
// letters, NOT a word count on its own: "Midnight Vale." is a legitimate
// two-word station ID and a rule that rejected it would be measuring the wrong
// thing. What it does reject is "... ... ...", which has no letters at all.
const (
	MinBreakWords   = 2
	MinBreakLetters = 8
)

// contentWordsIn returns the runs of letters in a string, which is what
// separates speech from punctuation a model emitted to fill a schema.
func contentWordsIn(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// ParseBreak decodes a model response into a Break.
func ParseBreak(raw string) (*Break, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty response", ErrBreakBadJSON)
	}

	var b Break
	if err := json.Unmarshal([]byte(trimmed), &b); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBreakBadJSON, err)
	}

	// A break with nothing to say is schema-valid and useless. Every field is
	// optional in the grammar, so {"opening":"","body":"","handoff":""} parses,
	// resolves no facts, collides with nothing in the said-lines index, gets
	// RECORDED as aired, and then fails at the TTS sidecar with a 400 on empty
	// text. Measured live, that was 4 breaks in 20 -- each one reported as an
	// unavailable sidecar, which is the wrong diagnosis entirely.
	if strings.TrimSpace(b.Text()) == "" {
		return nil, fmt.Errorf("%w: the break has no spoken text", ErrBreakBadJSON)
	}

	// A break must be made of WORDS. minLength on the schema stops an empty
	// string; it cannot stop "... ... ...", which is what actually aired in a
	// live run -- schema-valid, non-empty, the right length, colliding with
	// nothing, and completely content-free. Four real words is a low bar that
	// no genuine break fails.
	words := contentWordsIn(b.Text())
	letters := 0
	for _, w := range words {
		letters += len(w)
	}
	if len(words) < MinBreakWords || letters < MinBreakLetters {
		return nil, fmt.Errorf("%w: the break is %d words and %d letters, which is punctuation rather than speech",
			ErrBreakBadJSON, len(words), letters)
	}

	// GBNF enforces set membership but not uniqueness, so the same fact can come
	// back twice. Dedupe before anything counts them.
	b.AssertedFacts = dedupe(b.AssertedFacts)
	return &b, nil
}

func dedupe(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// indexedFactRE matches artist_facts[N].
var indexedFactRE = regexp.MustCompile(`^artist_facts\[(\d+)\]$`)

// ResolvableFactIDs lists every fact id the DJ may legitimately assert for this
// pair of dossiers.
//
// This is what the schema's asserted_facts enum is built from, computed PER
// REQUEST. With it, an ungrounded id is not merely rejected after generation, it
// is unrepresentable: the sampler cannot produce a token sequence outside the
// enum.
func ResolvableFactIDs(prev, cur, next *enrich.Dossier) []string {
	var out []string
	for _, side := range []struct {
		prefix string
		d      *enrich.Dossier
	}{{"prev", prev}, {"cur", cur}, {"next", next}} {
		if side.d == nil || ConfidenceScore(side.d.Confidence) < FactConfidenceThreshold {
			continue
		}
		if side.d.SubjectSummary != "" {
			out = append(out, side.prefix+".subject_summary")
		}
		if side.d.Release != "" {
			out = append(out, side.prefix+".release")
		}
		for i := range side.d.ArtistFacts {
			if i >= MaxFactsInPrompt {
				break
			}
			out = append(out, fmt.Sprintf("%s.artist_facts[%d]", side.prefix, i))
		}
		for _, tag := range side.d.StationTags {
			_ = tag
			out = append(out, side.prefix+".station_tags")
			break
		}
		for _, m := range side.d.Mood {
			_ = m
			out = append(out, side.prefix+".mood")
			break
		}
	}
	sort.Strings(out)
	return out
}

// ResolveFacts verifies every asserted id points at real, sufficiently confident
// dossier content.
//
// Resolution is a LOOKUP against field ids, never a fuzzy comparison against
// dossier text. Fuzzy matching is how an ungrounded claim sneaks through: a
// sentence that merely resembles a stored fact is not the stored fact.
func ResolveFacts(b *Break, prev, cur, next *enrich.Dossier) error {
	allowed := make(map[string]bool)
	for _, id := range ResolvableFactIDs(prev, cur, next) {
		allowed[id] = true
	}

	for _, id := range b.AssertedFacts {
		if !allowed[id] {
			return fmt.Errorf("%w: %q", ErrBreakUngrounded, id)
		}
	}
	return nil
}

// ResolveFactText returns the text behind a fact id, for logging and for the
// rubric review. It is not used to validate: validation is set membership.
func ResolveFactText(id string, prev, cur, next *enrich.Dossier) (string, bool) {
	prefix, field, ok := strings.Cut(id, ".")
	if !ok {
		return "", false
	}

	var d *enrich.Dossier
	switch prefix {
	case "prev":
		d = prev
	case "cur":
		d = cur
	case "next":
		d = next
	default:
		return "", false
	}
	if d == nil || ConfidenceScore(d.Confidence) < FactConfidenceThreshold {
		return "", false
	}

	switch {
	case field == "subject_summary":
		return d.SubjectSummary, d.SubjectSummary != ""
	case field == "release":
		return d.Release, d.Release != ""
	case field == "station_tags":
		return strings.Join(d.StationTags, ", "), len(d.StationTags) > 0
	case field == "mood":
		return strings.Join(d.Mood, ", "), len(d.Mood) > 0
	default:
		m := indexedFactRE.FindStringSubmatch(field)
		if m == nil {
			return "", false
		}
		i, err := strconv.Atoi(m[1])
		if err != nil || i < 0 || i >= len(d.ArtistFacts) || i >= MaxFactsInPrompt {
			return "", false
		}
		return d.ArtistFacts[i], true
	}
}

// BreakSchema builds the json_schema for one break request.
//
// The asserted_facts enum is computed from the ids that actually resolve for
// THIS pair of dossiers. When nothing resolves, asserted_facts is dropped from
// required and given an empty-only shape, so a personality-only break is still
// generable rather than impossible.
func BreakSchema(prev, cur, next *enrich.Dossier) map[string]any {
	return BreakSchemaExcluding(prev, cur, next, nil)
}

// BreakSchemaExcluding is BreakSchema with some fact ids withheld.
//
// Withheld, not merely discouraged: the enum IS the vocabulary the sampler can
// emit, so a fact left out of it cannot be asserted at all. That is the same
// move that makes an ungrounded fact impossible, applied to a fact the DJ has
// just used.
//
// It exists because GATE 5 measured the failure precisely. Nine shared 4-grams
// across twenty raw breaks, and EVERY ONE of them was an artist fact recited
// twice -- "linkin park formed 1996", "lenny kravitz born 1964". The writer had
// no way to know it had already said them. The gate's prescribed remedy was to
// escalate the collision index from n-grams to embeddings, which would have
// detected the repetition harder without preventing any of it.
func BreakSchemaExcluding(prev, cur, next *enrich.Dossier, exclude []string) map[string]any {
	ids := ResolvableFactIDs(prev, cur, next)
	if len(exclude) > 0 {
		skip := make(map[string]bool, len(exclude))
		for _, id := range exclude {
			skip[id] = true
		}
		kept := ids[:0]
		for _, id := range ids {
			if !skip[id] {
				kept = append(kept, id)
			}
		}
		ids = kept
	}

	props := map[string]any{
		"opening": map[string]any{"type": "string", "maxLength": 200},
		// minLength makes an EMPTY break unrepresentable at the sampler rather
		// than merely rejected afterwards -- the same move that makes an
		// ungrounded fact id impossible. Required-but-empty is what the schema
		// allowed before, and measured live the model took that option on 12 of
		// 35 attempts: a perfectly valid break with nothing in it.
		"body":    map[string]any{"type": "string", "minLength": 1, "maxLength": 600},
		"handoff": map[string]any{"type": "string", "maxLength": 200},
	}
	required := []any{"opening", "body", "handoff"}

	if len(ids) > 0 {
		enum := make([]any, len(ids))
		for i, id := range ids {
			enum[i] = id
		}
		props["asserted_facts"] = map[string]any{
			"type": "array", "maxItems": 4,
			"items": map[string]any{"type": "string", "enum": enum},
		}
		required = append(required, "asserted_facts")
	} else {
		// Nothing is assertable, so the only legal value is the empty array.
		props["asserted_facts"] = map[string]any{
			"type": "array", "maxItems": 0,
			"items": map[string]any{"type": "string"},
		}
	}

	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}
