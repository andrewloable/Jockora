// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"encoding/json"
	"os"
	"testing"
)

// TestRepeatShareAgainstTheRealCorpus runs the SHIPPED rule over breaks that
// actually aired on the deployed station, read out of its said_lines table.
//
// Jockora-yex says the answer cannot be reasoned about and forbids invented
// fixtures, because two rules in this file were already wrong from being argued
// rather than measured. This is the measurement. It asserts nothing on its own
// -- point it at a corpus and read what it prints.
//
//	JOCKORA_CORPUS=/tmp/corpus.json go test -v ./internal/dj/ -run TestRepeatShareAgainstTheRealCorpus
func TestRepeatShareAgainstTheRealCorpus(t *testing.T) {
	path := os.Getenv("JOCKORA_CORPUS")
	if path == "" {
		t.Skip("set JOCKORA_CORPUS to a JSON array of {jock, text} read from said_lines")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the corpus: %v", err)
	}
	var corpus []struct{ Jock, Text string }
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decoding the corpus: %v", err)
	}

	var short, shortDropped int
	for _, b := range corpus {
		words := len(spokenWords(b.Text))
		gram, dropped := repeatsItself(b.Text, nil)
		mark := "     "
		if dropped {
			mark = "DROP "
		}
		if words < 30 {
			short++
			if dropped {
				shortDropped++
			}
		}
		t.Logf("%s%3dw %-16s %q  %q", mark, words, b.Jock, trunc(b.Text, 64), gram)
	}
	t.Logf("\n%d breaks, %d under 30 words, %d of those the rule would drop",
		len(corpus), short, shortDropped)
}

// TestRepeatShareOnTheTicketsExamples runs the two hand-written lines
// Jockora-yex is argued from. The ticket says to treat them as illustration
// rather than evidence, and this is what the shipped rule actually does with
// them -- which is not what the ticket assumed.
func TestRepeatShareOnTheTicketsExamples(t *testing.T) {
	// BOTH ARE DROPPED, which is what the ticket predicted and why it said no
	// threshold separates them. What the ticket could not know without the
	// corpus is that the first line is not how the model actually writes a
	// triple: measured over 28 real breaks, every genuine device is a repeated
	// WORD or BIGRAM -- "HEY HEY HEY", "Welcome, welcome, welcome", "What a
	// record! What a RECORD!" -- which never forms a repeated four-gram and
	// scores zero. The hand-written line repeats a four-gram because it was
	// written to.
	for _, tc := range []struct{ name, text string }{
		{"the hand-written device the ticket argues from",
			"You are a LEGEND, a legend, a legend, and that is the whole report tonight friends"},
		{"the loop the ticket says really aired and really was degenerate",
			"DUTCH. The Hammer. Listen. Foo Fighters. Everlong. Boom. Now we ride. " +
				"Marilyn Manson. Sweet Dreams. Boom. Now we ride."},
	} {
		gram, dropped := repeatsItself(tc.text, nil)
		if !dropped {
			t.Errorf("%s is no longer dropped (gram %q); the corpus finding in "+
				"repeatsItself was measured against both of these being dropped", tc.name, gram)
		}
		t.Logf("%-60s words=%d dropped=%v gram=%q",
			tc.name, len(spokenWords(tc.text)), dropped, gram)
	}

	// AND THE REAL DEVICES SURVIVE, which is the half that matters. These three
	// aired on the deployed station and are the reason the rule was loosened in
	// Jockora-hkq; they must not become drops again.
	for _, real := range []string{
		"HEY HEY HEY! You just tuned in! GOOD! You absolute legend, you!",
		"Hey, hey! You're here! You made it! Welcome, welcome, welcome!",
		"HOLY SMOKE! What a record! What a RECORD! We just rode the tail of it.",
	} {
		if gram, dropped := repeatsItself(real, nil); dropped {
			t.Errorf("a device that really aired is now dropped on %q: %q", real, gram)
		}
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
