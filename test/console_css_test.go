// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// The console's stylesheet, where three defects have now lived that no unit
// test could see.
//
// Angular's test environment does not apply the stylesheet, so nothing in
// web/app can assert on what a rule does; the components render correct markup
// and the operator still meets 44px checkboxes in a box showing three of 42
// options. These tests read the rules themselves. They cannot prove a screen
// looks right -- only a person can -- but they pin the specific rules whose
// absence caused a reported bug, so a revert fails here instead of on the box.
package test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const stylesheet = "../web/app/src/styles.css"

func styles(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(stylesheet)
	if err != nil {
		t.Fatalf("reading the console stylesheet: %v", err)
	}
	return string(b)
}

// ruleFor returns the declarations of the first rule whose selector list
// contains sel, so a test asserts on one block rather than on the whole file.
func ruleFor(t *testing.T, css, sel string) string {
	t.Helper()
	for _, block := range strings.Split(css, "}") {
		open := strings.Index(block, "{")
		if open < 0 {
			continue
		}
		if strings.Contains(block[:open], sel) {
			return block[open+1:]
		}
	}
	t.Fatalf("no rule in the stylesheet selects %q", sel)
	return ""
}

// TestConsoleCSSKeepsCheckboxesOutOfFieldSizing: "fieldset input" matched every
// input in a fieldset, including the vocabulary's checkboxes, and sized them
// like entry boxes -- measured at 44px tall and up to 111px wide each.
func TestConsoleCSSKeepsCheckboxesOutOfFieldSizing(t *testing.T) {
	css := styles(t)
	sizing := regexp.MustCompile(`(?m)^fieldset input[^,{]*`)
	sel := sizing.FindString(css)
	if sel == "" {
		t.Fatal("the fieldset input sizing rule is gone; if it moved, move this test with it")
	}
	if !strings.Contains(sel, `:not([type='checkbox'])`) && !strings.Contains(sel, `:not([type="checkbox"])`) {
		t.Errorf("fieldset input sizing still matches checkboxes: %q", strings.TrimSpace(sel))
	}
}

// TestConsoleCSSGivesTheTagPickerItsOwnRow: as an ordinary flex item beside the
// name field each picker collapsed to ONE column, so auto-fill had one column
// to fill and 42 options became a 1550px scroller inside a 174px box.
func TestConsoleCSSGivesTheTagPickerItsOwnRow(t *testing.T) {
	rule := ruleFor(t, styles(t), "[data-genre]")
	if !strings.Contains(rule, "flex: 1 1 100%") {
		t.Error("the tag picker is not on a row of its own, so its grid gets one column")
	}
	// A box that scrolls with no scrollbar is a box that looks complete. The
	// stable gutter is the only sign the old one gave that there was more.
	if !strings.Contains(rule, "scrollbar-gutter: stable") {
		t.Error("the tag picker scrolls with nothing on screen to say so")
	}
	if strings.Contains(rule, "max-height: 11rem") {
		t.Error("the tag picker is back to showing three rows of a 42-option vocabulary")
	}
}

// TestConsoleCSSStylesEveryTagPicker: the playlist's own pickers are named in
// the plural and were styled by nothing at all when they were added.
func TestConsoleCSSStylesEveryTagPicker(t *testing.T) {
	css := styles(t)
	for _, sel := range []string{"[data-edit-genres]", "[data-edit-moods]"} {
		if !strings.Contains(css, sel) {
			t.Errorf("%s has no styling; it renders as a bare fieldset", sel)
		}
	}
}

// TestConsoleCSSStacksThePlaylistOnAPhone: seven columns on a 390px phone put
// Pin and Exclude 654px in, off the right edge of a table that scrolls with
// nothing on screen to say so -- which is the same as not being there.
func TestConsoleCSSStacksThePlaylistOnAPhone(t *testing.T) {
	css := styles(t)
	narrow := strings.Index(css, "@media (max-width: 47.99rem)")
	if narrow < 0 {
		t.Fatal("no narrow-viewport rules for the playlist; the actions are off-screen again")
	}
	block := css[narrow:]
	for _, want := range []string{
		"[data-playlist] thead",
		"data-label",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the narrow playlist rules do not mention %q", want)
		}
	}
	// The label is what a card has instead of the heading row it hides.
	if !strings.Contains(block, `content: attr(data-label)`) {
		t.Error("a stacked row hides the headings and never names its fields")
	}
}

// TestConsoleCSSGivesTheLiveStreamNoClock: the transport replaced the browser's
// native widget, which insisted on a scrub bar and a running duration for a
// stream that has no position.
func TestConsoleCSSGivesTheLiveStreamNoClock(t *testing.T) {
	css := styles(t)
	for _, sel := range []string{"[data-transport]", "[data-volume]", "[data-live]"} {
		if !strings.Contains(css, sel) {
			t.Errorf("%s is unstyled; the transport renders as raw controls", sel)
		}
	}
}

// TestConsoleCSSKeepsTheStationCardInsideItself: a 51-character mood string
// drew 377px of text inside a 206px card and scrolled the listener page
// sideways by 20px on a phone. Spacing the moods is the fix; this is the
// backstop for every other token a card can hold.
func TestConsoleCSSKeepsTheStationCardInsideItself(t *testing.T) {
	rule := ruleFor(t, styles(t), "[data-station] small")
	if !strings.Contains(rule, "overflow-wrap: anywhere") {
		t.Error("a long unbroken name can still run outside the station card")
	}
}
