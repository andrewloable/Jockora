// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"errors"
	"testing"
)

// THE FLOOR UNDER A BREAK THAT SAYS THE SAME THING OVER AND OVER.
//
// The said-lines index catches a phrase reused in a LATER break. It cannot see
// a break that repeats itself, because the first occurrence is what gets
// indexed -- so a break made entirely of one repeated phrase collides with
// nothing and airs. All eight breaks the deployed station aired were of this
// shape; these are their real texts.
//
// Every test here is TestDegenerate*, which is the -run pattern for this fix.

func TestDegenerateCatchesWhatActuallyAired(t *testing.T) {
	aired := []string{
		"nobody nobody, nobody, nobody, nobody, nobody, nobody nobody",
		"cure. mood cure. mood cure. mood",
		"ne xt. mo od. next. mood. ne xt. mood. ne xt. mo od.",
		"count. crow. mr. jones. 1994. rock. alternative. start. now. waitin. for. her. " +
			"left. want. her. back. count. crow. mr. jones. 1994. rock. alternative. start. now.",
		"DUTCH. The Hammer. Listen. Foo Fighters. Everlong. Boom. Now we ride. " +
			"Marilyn Manson. Sweet Dreams. Boom. Now we ride.",
		"Sweet Dreams. Sweet Dreams. Listen, the next station tags. " +
			"Listen, the next station tags. Everlong (1997) by Foo Fighters.",
	}
	for _, text := range aired {
		if _, bad := repeatsItself(text, nil); !bad {
			t.Errorf("aired and should not have: %q", text)
		}
	}
}

// TestDegenerateExemptsTheRecordItIsAbout. Found by the live probe, not by
// reasoning: the check's own comment claimed four consecutive words was long
// enough that naming a record twice could not trip it. "Have Fun Storming the
// Castle" is a five-word title, and a real break that said it twice was
// rejected for doing exactly what a backsell is meant to do.
func TestDegenerateExemptsTheRecordItIsAbout(t *testing.T) {
	text := "WHO just finished a song called Have Fun Storming the Castle? " +
		"Bayside, that is who. Have Fun Storming the Castle, and now something else."
	names := []string{"Have Fun Storming the Castle", "Bayside"}

	if gram, bad := repeatsItself(text, names); bad {
		t.Errorf("rejected a backsell for %q", gram)
	}
	// Without the names it is indistinguishable from a loop, which is why the
	// exemption has to be passed in rather than guessed at.
	if _, bad := repeatsItself(text, nil); !bad {
		t.Error("the 4-gram rule stopped firing altogether")
	}
	// And an exemption is not a licence: a genuine loop still fails even when
	// every word of it is a track name.
	if _, bad := repeatsItself(
		"Bayside Bayside Bayside Bayside Bayside Bayside and here is something new to say",
		[]string{"Nirvana"}); !bad {
		t.Error("a loop of a word that is not a track name was allowed")
	}
}

// TestDegenerateShoutingTheTitleIsNotAPlaceholder. Also found live: the
// shouting rule exists because "YOUR SPEECH HERE" aired, and it fired on a
// good break that shouted the name of the record it was about.
func TestDegenerateShoutingTheTitleIsNotAPlaceholder(t *testing.T) {
	names := []string{"Have Fun Storming the Castle", "Bayside"}
	good := "WHOA! Bayside! HAVE FUN STORMING THE CASTLE! What a title that is."
	if phrase, echoed := echoesInstructions(good, names); echoed {
		t.Errorf("rejected a shouted title as a placeholder: %q", phrase)
	}
	// The backstop is intact: a shouted phrase that is NOT the record still
	// reads as a template marker.
	if _, echoed := echoesInstructions("AND NOW YOUR SPEECH HERE friends", names); !echoed {
		t.Error("a shouted placeholder was allowed through")
	}
	if _, echoed := echoesInstructions("THIS IS NOT A TITLE at all", names); !echoed {
		t.Error("shouting unrelated to the record was allowed through")
	}
}

func TestDegenerateLeavesRealBreaksAlone(t *testing.T) {
	// The 20-word probe output, and the shapes a good break actually takes.
	good := []string{
		"Yeah, Bayside just destroyed that castle. Barry Gibb on the floor. That's the " +
			"kind of pure, unadulterated rock and roll that got me through the 80s. And now...",
		"That was Nirvana, In Bloom, from 1991. Coming up, Green Day.",
		"Midnight Vale.",
		"",
		// Naming one record twice is not degeneracy: it is how a backsell
		// works. Four CONSECUTIVE content words have to recur before this
		// fires, and a name on its own is two.
		"Everlong. Foo Fighters, nineteen ninety-seven. Everlong, and it still lands.",
	}
	for _, text := range good {
		if gram, bad := repeatsItself(text, nil); bad {
			t.Errorf("rejected a good break for %q: %q", gram, text)
		}
	}
}

// TestDegenerateIsDroppedNotAired drives the whole path: a model that returns
// the real "nobody" break twice must produce a drop, and the drop must be
// named as self-repetition rather than blamed on the said-lines index.
func TestDegenerateIsDroppedNotAired(t *testing.T) {
	loop := `{"opening":"","body":"nobody nobody, nobody, nobody, nobody, nobody, nobody nobody","handoff":"","asserted_facts":[]}`
	v := &Validator{
		Writer: &scriptedWriter{replies: []string{loop, loop}},
		Said:   saidStore(t),
	}
	_, err := v.Generate(context.Background(), "prompt", nil, nil, nil)
	if err == nil {
		t.Fatal("the break that actually aired was accepted again")
	}
	if !errors.Is(err, ErrBreakDropped) {
		t.Errorf("error = %v, want ErrBreakDropped", err)
	}
	// One requested break, one drop -- the counter is per break, not per
	// attempt -- and it must be filed under the reason that actually happened.
	if got := v.Stats().Reasons[DropSelfRepetitive]; got != 1 {
		t.Errorf("self-repetition recorded %d times, want 1", got)
	}
}
