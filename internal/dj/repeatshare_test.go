// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import "testing"

// A DEVICE IS NOT A LOOP.
//
// Any repeated four-gram was a drop, and Dutch Mahoney's card says in as many
// words that he "repeats a word three times when twice would do". So the rule
// rejected the trait the card exists to produce. Every text below is verbatim
// from the live probe on 2026-09-07 against minimax-m3, with the gram the old
// rule fired on named beside it.
//
// The separation is SHARE, measured on these: a rhetorical repeat covers 6-11%
// of a break, and the loops that actually aired cover 38-100%.

var devices = []struct{ gram, text string }{
	{"a legend a legend",
		"OH! BAYSIDE! BAYSIDE, BAYSIDE! The Walking Wounded, two thousand seven, and a title " +
			"that sounds like a movie I watched twice on a couch that did not deserve me! You know " +
			"what, I have no idea when this band started, where they came from, or what any of them " +
			"eat for breakfast, and I do not CARE, because that song just punched the wall and the " +
			"wall is still vibrating, and if you are awake right now you are a LEGEND, a legend, a " +
			"legend! And speaking of legends! FOO FIGHTERS! Everlong! Which means forever, which " +
			"means long, which means I am going to shut up RIGHT NOW!"},
	{"a great great great",
		"BAYSIDE! Have Fun Storming the Castle! TWO THOUSAND AND SEVEN! That is a GREAT GREAT " +
			"GREAT title for a song and frankly a GREAT GREAT GREAT title for basically any Tuesday " +
			"I have ever had! Listen. I genuinely do not know a single additional thing about that " +
			"record. I have no notes. I have no clues. I have got NOTHING and I am not going to " +
			"stand here and pretend otherwise because that is the kind of low low low behaviour " +
			"that gives radio a bad name! So instead we just KEEP MOVING! Foo Fighters! Everlong! " +
			"Coming up RIGHT NOW! Hit it!"},
	{"who does not love",
		"OH MY GOODNESS what a song what a SONG! Bayside, The Walking Wounded, that is a band " +
			"name and a band album and I am HERE for it. Fun storming the castle, who does not love " +
			"fun, who does not love castles, I am both of those people personally! But now, NOW, we " +
			"are moving on. And I do not know a single thing about this next record, and that is " +
			"FINE, that is exciting, I am guessing out loud like everybody else! Foo Fighters, " +
			"Everlong, that sounds like a promise! Hit it!"},
	{"that is a title",
		"That was BAYSIDE with 'Have Fun Storming the Castle' off The Walking Wounded, two " +
			"thousand and seven! Storming the castle, my friends, storming the castle! And let me " +
			"tell you something, that is a TITLE, that is a title that demands respect. I am " +
			"storming something this afternoon, I am not telling you what. Could be a castle. Could " +
			"be the laundromat. Up next, the Foo Fighters, 'Everlong,' and I am not going to " +
			"pretend I know a single thing about this record except that I already love it. " +
			"Here we go!"},
}

func TestDegenerateAllowsARhetoricalRepeat(t *testing.T) {
	for _, d := range devices {
		if gram, bad := repeatsItself(d.text, nil); bad {
			t.Errorf("dropped a break for %q, which is the jock's own device:\n%s", gram, d.text)
		}
	}
}

func TestDegenerateStillCatchesALoopInsideAWordyBreak(t *testing.T) {
	// The share is what separates them, so the guard has to hold at LENGTH as
	// well as in the short degenerate texts: a break that says the same thing
	// over and over is a loop however many words it runs to.
	loop := "Coming up next on the station, the next station tags. " +
		"Coming up next on the station, the next station tags. " +
		"Coming up next on the station, the next station tags. " +
		"Coming up next on the station, the next station tags."
	if _, bad := repeatsItself(loop, nil); !bad {
		t.Error("a long break that is nothing but one repeated sentence was allowed")
	}
}

func TestDegenerateCountsTheShareNotTheHit(t *testing.T) {
	// One repeated four-gram in a long break is a device; the SAME gram in a
	// break with nothing else in it is the whole break.
	short := "who does not love fun who does not love fun"
	if _, bad := repeatsItself(short, nil); !bad {
		t.Error("a break that is only its own repeat was allowed")
	}
}
