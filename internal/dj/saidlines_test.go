// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

func saidStore(t *testing.T) *SaidLines {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &SaidLines{Store: s, JockID: "midnight_vale"}
}

func TestSaidExtractsContentWord4Grams(t *testing.T) {
	got := ContentWords("This next one is a real gem from nineteen eighty four")

	for _, stop := range []string{"this", "next", "one", "is", "a", "from"} {
		for _, w := range got {
			if w == stop {
				t.Errorf("stopword %q survived into the content words: %v", stop, got)
			}
		}
	}
	for _, want := range []string{"real", "gem", "nineteen", "eighty", "four"} {
		if !contains(got, want) {
			t.Errorf("content word %q was dropped: %v", want, got)
		}
	}

	grams := NGrams(got, 4)
	if len(grams) == 0 {
		t.Fatal("no 4-grams extracted")
	}
	for _, g := range grams {
		for _, stop := range []string{" is ", " a ", " the ", " from "} {
			if strings.Contains(" "+g+" ", stop) {
				t.Errorf("4-gram %q contains a stopword", g)
			}
		}
	}
}

func TestSaidOpeningNormalisation(t *testing.T) {
	a := NormaliseOpening("This next one is a real gem from nineteen eighty four.")
	b := NormaliseOpening("this  NEXT one   is a REAL gem, from nineteen eighty four!")

	if a != b {
		t.Errorf("openings normalised differently:\n  %q\n  %q", a, b)
	}
	if a == "" {
		t.Fatal("opening normalised to empty")
	}
	if strings.Contains(a, "This") || strings.Contains(a, ",") {
		t.Errorf("opening %q is not normalised", a)
	}
}

func TestSaidCollisionDetected(t *testing.T) {
	sl := saidStore(t)
	ctx := context.Background()

	if err := sl.Record(ctx, "Here is a record about driving through empty streets at midnight."); err != nil {
		t.Fatal(err)
	}

	hit, gram, err := sl.CheckCollision(ctx, "Another one, driving through empty streets at midnight again.")
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("a repeated content-word run was not detected")
	}
	if gram == "" {
		t.Error("the collision reported no offending gram")
	}
	if !strings.Contains(gram, "driving") {
		t.Errorf("offending gram = %q, want it to name the repeated run", gram)
	}
}

// TestSaidNoFalsePositiveOnFunctionWords is the reason stopwords are removed
// BEFORE n-gram extraction. Raw 4-grams collide on ordinary English and the
// validator would chase phantom repetition forever.
func TestSaidNoFalsePositiveOnFunctionWords(t *testing.T) {
	sl := saidStore(t)
	ctx := context.Background()

	if err := sl.Record(ctx, "That was one of the best records ever pressed in Sheffield."); err != nil {
		t.Fatal(err)
	}

	hit, gram, err := sl.CheckCollision(ctx, "This is one of the best things about late nights in Manila.")
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Errorf("two unrelated lines collided on function words: %q", gram)
	}
}

// TestSaidChecksFullTable: checking only a recent window makes collisions
// outside it invisible to both the prompt and the validator.
func TestSaidChecksFullTable(t *testing.T) {
	sl := saidStore(t)
	ctx := context.Background()

	if err := sl.Record(ctx, "Recorded in a converted chapel outside Bristol during a heatwave."); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		if err := sl.Record(ctx, fmt.Sprintf(
			"Filler line number %d mentioning saxophone%d rehearsal%d tapes%d.", i, i, i, i)); err != nil {
			t.Fatal(err)
		}
	}

	hit, gram, err := sl.CheckCollision(ctx,
		"They cut it in a converted chapel outside Bristol during a heatwave.")
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("a collision with the very first line was missed after 500 more; the check is windowed")
	}
	if !strings.Contains(gram, "chapel") {
		t.Errorf("offending gram = %q", gram)
	}
}

func TestSaidShortLineNeverCollides(t *testing.T) {
	sl := saidStore(t)
	ctx := context.Background()

	if err := sl.Record(ctx, "Back after this."); err != nil {
		t.Fatal(err)
	}
	hit, _, err := sl.CheckCollision(ctx, "Back after this.")
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Error("a line too short to form a 4-gram reported a collision")
	}
}

// TestSaidIsPerJock: two DJs sharing a station must not be told they are
// repeating each other.
func TestSaidIsPerJock(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	a := &SaidLines{Store: s, JockID: "vale"}
	b := &SaidLines{Store: s, JockID: "other"}

	if err := a.Record(ctx, "Recorded in a converted chapel outside Bristol during a heatwave."); err != nil {
		t.Fatal(err)
	}

	hit, _, err := b.CheckCollision(ctx, "They cut it in a converted chapel outside Bristol during a heatwave.")
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Error("one DJ collided with another DJ's line; the index must be per jock")
	}

	hit, _, err = a.CheckCollision(ctx, "They cut it in a converted chapel outside Bristol during a heatwave.")
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("the same DJ did not collide with its own line")
	}
}

func TestSaidRecordStoresOpeningAndGrams(t *testing.T) {
	sl := saidStore(t)
	ctx := context.Background()
	line := "Recorded in a converted chapel outside Bristol during a heatwave."

	if err := sl.Record(ctx, line); err != nil {
		t.Fatal(err)
	}

	var text, opening, grams string
	if err := sl.Store.DB().QueryRow(
		`SELECT text, opening_norm, ngrams FROM said_lines WHERE jock_id = ?`, "midnight_vale").
		Scan(&text, &opening, &grams); err != nil {
		t.Fatal(err)
	}
	if text != line {
		t.Errorf("text = %q", text)
	}
	if opening != NormaliseOpening(line) {
		t.Errorf("opening_norm = %q, want %q", opening, NormaliseOpening(line))
	}
	if !strings.Contains(grams, "converted") {
		t.Errorf("ngrams = %q, want the content-word runs", grams)
	}
}

// TestSaidOpeningRepetitionIsDetected: openings are the most noticeable
// repetition of all, and two lines can share one without sharing a 4-gram.
//
// The comparison is exact on the first six CONTENT words, so it catches a
// recycled opening formula that then diverges, which is the shape the repetition
// actually takes.
func TestSaidOpeningRepetitionIsDetected(t *testing.T) {
	sl := saidStore(t)
	ctx := context.Background()

	if err := sl.Record(ctx,
		"Coming up, something special, something loud, something Belgian, recorded live."); err != nil {
		t.Fatal(err)
	}

	// Same opening formula, different ending. No shared 4-gram spans the join,
	// so only the opening check can catch this.
	used, err := sl.OpeningUsed(ctx,
		"Coming up: something special, something loud, something Belgian, and entirely new.")
	if err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Errorf("a recycled opening formula was not detected\n  stored: %q\n  new:    %q",
			NormaliseOpening("Coming up, something special, something loud, something Belgian, recorded live."),
			NormaliseOpening("Coming up: something special, something loud, something Belgian, and entirely new."))
	}

	used, err = sl.OpeningUsed(ctx, "Here is a record I have been saving for a night like this.")
	if err != nil {
		t.Fatal(err)
	}
	if used {
		t.Error("an unrelated opening was reported as used")
	}
}

// TestSaidOpeningIsNotOversensitive: two lines that merely start with the same
// couple of words are not the same opening, and flagging them would reject
// perfectly good writing.
func TestSaidOpeningIsNotOversensitive(t *testing.T) {
	sl := saidStore(t)
	ctx := context.Background()

	if err := sl.Record(ctx, "Coming up next we have something special from the archives."); err != nil {
		t.Fatal(err)
	}

	used, err := sl.OpeningUsed(ctx, "Coming up next we have something entirely different tonight.")
	if err != nil {
		t.Fatal(err)
	}
	if used {
		t.Error("two lines sharing only their first two content words were treated as the same opening")
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
