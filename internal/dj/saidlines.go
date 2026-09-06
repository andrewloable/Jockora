// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/andrewloable/jockora/internal/store"
)

// GramSize is the run length that counts as repetition.
const GramSize = 4

// OpeningWords is how many content words define a line's opening. Openings are
// the most noticeable repetition of all, and two lines can share one without
// sharing a single 4-gram.
const OpeningWords = 6

// stopwords are removed BEFORE n-gram extraction.
//
// This ordering is the whole trick. Raw 4-grams collide on ordinary English --
// "one of the best" appears in any two unrelated lines -- and a validator built
// on them chases phantom repetition forever while missing the real thing.
var stopwords = map[string]bool{
	"a": true, "about": true, "after": true, "all": true, "an": true, "and": true,
	"any": true, "are": true, "as": true, "at": true, "back": true, "be": true,
	"been": true, "before": true, "but": true, "by": true, "can": true, "did": true,
	"do": true, "down": true, "for": true, "from": true, "get": true, "got": true,
	"had": true, "has": true, "have": true, "he": true, "her": true, "here": true,
	"him": true, "his": true, "how": true, "i": true, "if": true, "in": true,
	"into": true, "is": true, "it": true, "its": true, "just": true, "like": true,
	"me": true, "more": true, "my": true, "next": true, "no": true, "not": true,
	"now": true, "of": true, "off": true, "on": true, "one": true, "or": true,
	"our": true, "out": true, "over": true, "own": true, "s": true, "said": true,
	"she": true, "so": true, "some": true, "still": true, "such": true, "than": true,
	"that": true, "the": true, "their": true, "them": true, "then": true, "there": true,
	"these": true, "they": true, "this": true, "those": true, "through": true,
	"to": true, "too": true, "under": true, "up": true, "us": true, "very": true,
	"was": true, "we": true, "well": true, "were": true, "what": true, "when": true,
	"where": true, "which": true, "who": true, "why": true, "will": true, "with": true,
	"would": true, "you": true, "your": true,
}

var nonWordRE = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// ContentWords lowercases, strips punctuation and removes stopwords.
func ContentWords(s string) []string {
	var out []string
	for _, w := range strings.Fields(nonWordRE.ReplaceAllString(strings.ToLower(s), " ")) {
		if !stopwords[w] {
			out = append(out, w)
		}
	}
	return out
}

// NGrams returns every run of n consecutive words.
func NGrams(words []string, n int) []string {
	if n <= 0 || len(words) < n {
		return nil
	}
	out := make([]string, 0, len(words)-n+1)
	for i := 0; i+n <= len(words); i++ {
		out = append(out, strings.Join(words[i:i+n], " "))
	}
	return out
}

// NormaliseOpening reduces a line's opening to a comparable key.
func NormaliseOpening(s string) string {
	words := ContentWords(s)
	if len(words) > OpeningWords {
		words = words[:OpeningWords]
	}
	return strings.Join(words, " ")
}

// SaidLines records everything one DJ has said.
//
// Repetition, not generation quality, is what kills AI radio: a listener
// forgives a dull line and never forgives the same line twice.
type SaidLines struct {
	Store  *store.Store
	JockID string
}

// gramSeparator delimits stored n-grams. A newline cannot appear inside one,
// since content words are letters and digits only.
const gramSeparator = "\n"

// Record stores a line the DJ actually aired.
func (s *SaidLines) Record(ctx context.Context, text string) error {
	grams := NGrams(ContentWords(text), GramSize)

	_, err := s.Store.DB().ExecContext(ctx,
		`INSERT INTO said_lines (jock_id, text, opening_norm, ngrams, aired_at) VALUES (?, ?, ?, ?, ?)`,
		s.JockID, text, NormaliseOpening(text), strings.Join(grams, gramSeparator), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("dj: recording said line: %w", err)
	}
	return nil
}

// CheckCollision reports whether text repeats a content-word run this DJ has
// already used, and which run.
//
// It scans the FULL table, not a recent window. Checking only what was injected
// into the prompt makes collisions outside that window invisible to both the
// prompt and the check, which is precisely the repetition a listener notices
// over weeks.
func (s *SaidLines) CheckCollision(ctx context.Context, text string) (bool, string, error) {
	return s.CheckCollisionIgnoring(ctx, text, nil)
}

// CheckCollisionIgnoring is CheckCollision with the words of some proper nouns
// excused.
//
// NAMING THE RECORD IS THE JOB. Consecutive breaks legitimately share tracks --
// one break's NEXT is the following break's CURRENT -- so a DJ that says what
// is coming and then says what just played will repeat the artist and title
// every single time. GATE 5 measured this directly: after the fact cooldown
// removed the recited-fact collisions, EVERY remaining one was a proper noun --
// "parokya ni edgar wag mo", "laurindo almeida bossa nova", "jack costanzo born
// 1919".
//
// The index is meant to catch a DJ reusing its own PHRASING, not to forbid it
// from saying what is playing. Grams built entirely out of name words are
// therefore not collisions.
//
// It is deliberately not the whole gram: a gram mixing a name with ordinary
// words -- "almeida is the finest" -- is still phrasing and still counts.
func (s *SaidLines) CheckCollisionIgnoring(ctx context.Context, text string, names []string) (bool, string, error) {
	nameWords := make(map[string]bool)
	for _, n := range names {
		for _, w := range ContentWords(n) {
			nameWords[w] = true
		}
	}

	grams := NGrams(ContentWords(text), GramSize)
	if len(nameWords) > 0 {
		kept := grams[:0]
		for _, g := range grams {
			if !allNameWords(g, nameWords) {
				kept = append(kept, g)
			}
		}
		grams = kept
	}
	if len(grams) == 0 {
		// Too short to form a run. A three-word sign-off is allowed to repeat.
		return false, "", nil
	}

	want := make(map[string]bool, len(grams))
	for _, g := range grams {
		want[g] = true
	}

	rows, err := s.Store.DB().QueryContext(ctx,
		`SELECT ngrams FROM said_lines WHERE jock_id = ?`, s.JockID)
	if err != nil {
		return false, "", fmt.Errorf("dj: checking collisions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var stored string
		if err := rows.Scan(&stored); err != nil {
			return false, "", fmt.Errorf("dj: checking collisions: %w", err)
		}
		for _, g := range strings.Split(stored, gramSeparator) {
			if want[g] {
				return true, g, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return false, "", fmt.Errorf("dj: checking collisions: %w", err)
	}
	return false, "", nil
}

// OpeningUsed reports whether this DJ has opened a line this way before.
func (s *SaidLines) OpeningUsed(ctx context.Context, text string) (bool, error) {
	opening := NormaliseOpening(text)
	if opening == "" {
		return false, nil
	}

	var n int
	err := s.Store.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM said_lines WHERE jock_id = ? AND opening_norm = ?`,
		s.JockID, opening).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("dj: checking opening: %w", err)
	}
	return n > 0, nil
}

// allNameWords reports whether every word in a gram came from a proper noun.
func allNameWords(gram string, nameWords map[string]bool) bool {
	for _, w := range strings.Fields(gram) {
		if !nameWords[w] {
			return false
		}
	}
	return true
}
