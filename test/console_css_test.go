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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
		// The selector only. A comment ABOVE a rule sits in the same chunk, and
		// the decisions block at the top of the sheet names several selectors
		// in prose -- without this, ruleFor("button[data-danger]") returned the
		// body of the box-sizing rule that happened to follow it.
		if strings.Contains(stripComments(block[:open]), sel) {
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

// TestConsoleCSSStacksEveryAdminTable: the Playlist got the stacked-card
// treatment and the others did not, so on a 390px phone the Jocks table drew
// its actions to 469px, Stations to 559px, and NOTHING scrolled -- every
// ancestor had overflow-x visible and scrollWidth equal to clientWidth. The
// buttons were rendered and unreachable: a station could not be edited,
// disabled, deleted or have its playlist opened from a phone at all.
func TestConsoleCSSStacksEveryAdminTable(t *testing.T) {
	css := styles(t)
	narrow := strings.Index(css, "@media (max-width: 47.99rem)")
	if narrow < 0 {
		t.Fatal("the narrow-viewport rules are gone")
	}
	block := css[narrow:]
	for _, table := range []string{"[data-playlist]", "[data-stations]", "[data-jocks]", "[data-overview]"} {
		if !strings.Contains(block, table+" tr") {
			t.Errorf("%s does not become a card on a phone; its buttons are unreachable", table)
		}
	}
	// The long values wrap rather than being clipped mid-word: "adult
	// contemporary" read as "adult contemporar".
	if !strings.Contains(block, "overflow-wrap: anywhere") {
		t.Error("a long value can still be clipped mid-word in a card")
	}
}

// TestConsoleCSSLetsACheckboxBeACheckbox: the fieldset sizing rule was fixed
// and the GLOBAL one was not, so every option in the vocabulary still rendered
// as a 44px-tall box whose WIDTH depended on the length of its own label --
// rock 128px, alternative 88px, metal 120px, because the box shrank by however
// much the text beside it needed. That is what made the picker zig-zag.
func TestConsoleCSSLetsACheckboxBeACheckbox(t *testing.T) {
	css := styles(t)

	// Every rule that imposes an ENTRY BOX's shape has to exclude checkboxes.
	// A bare "input" selector that only inherits a font is fine and is not what
	// this is about.
	for _, decl := range []string{"min-height: 44px", "border-radius: var(--r-control)"} {
		for _, sel := range selectorsSetting(css, decl) {
			if strings.Contains(sel, "input") && !strings.Contains(sel, ":not([type='checkbox'])") &&
				!strings.Contains(sel, "[type=") {
				t.Errorf("%q shapes a checkbox like an entry box", sel)
			}
		}
	}
	if !strings.Contains(css, "input[type='checkbox'],") {
		t.Error("nothing gives a checkbox its intrinsic size back")
	}
}

// selectorsSetting returns the selector of every rule whose body contains decl.
func selectorsSetting(css, decl string) []string {
	var out []string
	for _, block := range strings.Split(css, "}") {
		open := strings.Index(block, "{")
		if open < 0 || !strings.Contains(block[open:], decl) {
			continue
		}
		// Comments stripped for the same reason ruleFor strips them: the text
		// before the brace includes the comment ABOVE the rule, and a comment
		// that mentions "input" is not a selector that matches one.
		out = append(out, strings.TrimSpace(stripComments(block[:open])))
	}
	return out
}

// TestConsoleCSSMarksDestructiveActions: every button in the console had ONE
// background -- a preview, an edit and an irreversible delete were byte
// identical and sat flush against each other. --bad was defined and used on
// nothing.
func TestConsoleCSSMarksDestructiveActions(t *testing.T) {
	rule := ruleFor(t, styles(t), "button[data-danger]")
	if !strings.Contains(rule, "var(--bad)") {
		t.Error("a destructive button does not use the colour reserved for it")
	}
}

// ------------------------------------------------------- the design system --
//
// Jockora-e9a.49 made five decisions in numbers. These pin them. Every name
// starts with TestDesignSystem so `-run TestDesignSystem` covers the set.

// spacing parses the --sN scale into rem, so a test can compare two steps
// rather than hard-coding the pixels they happen to resolve to today.
func spacing(t *testing.T, css string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	re := regexp.MustCompile(`--(s[1-8]):\s*([0-9.]+)rem`)
	for _, m := range re.FindAllStringSubmatch(css, -1) {
		var v float64
		if _, err := fmt.Sscanf(m[2], "%g", &v); err != nil {
			t.Fatalf("the %s step is not a number: %q", m[1], m[2])
		}
		out[m[1]] = v
	}
	if len(out) != 8 {
		t.Fatalf("the spacing scale has %d steps, want 8", len(out))
	}
	return out
}

// step returns the rem value of the var(--sN) that decl sets in the first rule
// selecting sel.
func step(t *testing.T, css, sel, decl string) float64 {
	t.Helper()
	rule := ruleFor(t, css, sel)
	re := regexp.MustCompile(decl + `:[^;]*var\(--(s[1-8])\)`)
	m := re.FindStringSubmatch(rule)
	if m == nil {
		t.Fatalf("%q does not set %s from the spacing scale:\n%s", sel, decl, rule)
	}
	return spacing(t, css)[m[1]]
}

// TestDesignSystemSeparatesGroupsMoreThanTheirContents: measured on the Overview
// at 1280px, consecutive sections were 12px apart and a heading sat 12px from
// its own body text -- the gap BETWEEN groups equalled the gap WITHIN one, so
// six tabs read as one wall. Decision 1: at least 2.5 to 1.
func TestDesignSystemSeparatesGroupsMoreThanTheirContents(t *testing.T) {
	css := styles(t)
	between := step(t, css, "\nsection", "margin-top")
	within := step(t, css, "\np ", "margin")
	if within == 0 {
		t.Fatal("prose sets no bottom margin, so there is no within-group step to compare")
	}
	if ratio := between / within; ratio < 2.5 {
		t.Errorf("between-group %grem over within-group %grem is %.2f to 1, want at least 2.5",
			between, within, ratio)
	}
}

// TestDesignSystemGivesAHeadingItsSpaceAbove: a heading belongs to what follows
// it. Space split evenly above and below makes it float between two groups and
// belong to neither.
func TestDesignSystemGivesAHeadingItsSpaceAbove(t *testing.T) {
	css := styles(t)
	for _, h := range []struct{ sel, above, below string }{
		{"\nh2 ", "margin-top", "margin-bottom"},
		{"section > h3", "margin-top", "margin-bottom"},
	} {
		above := step(t, css, h.sel, h.above)
		below := step(t, css, h.sel, h.below)
		if above <= below {
			t.Errorf("%s takes %grem above and %grem below; a heading takes its space above",
				strings.TrimSpace(h.sel), above, below)
		}
	}
}

// TestDesignSystemHasExactlyThreeButtonTiers: every action button in the console
// had ONE background -- a preview, an edit and an irreversible delete were byte
// identical. Decision 3 is three tiers and no more: primary, secondary,
// destructive. Navigation is not an action tier and is excluded by name.
func TestDesignSystemHasExactlyThreeButtonTiers(t *testing.T) {
	css := styles(t)
	tiers := map[string]string{}
	for _, block := range strings.Split(css, "}") {
		open := strings.Index(block, "{")
		if open < 0 {
			continue
		}
		sel := strings.TrimSpace(stripComments(block[:open]))
		body := block[open:]
		if !strings.Contains(sel, "button") || strings.Contains(sel, "nav ") {
			continue
		}
		// A tier is an identity at rest. Hover and disabled are states of one.
		if strings.ContainsAny(sel, ":") {
			continue
		}
		if !strings.Contains(body, "background:") {
			continue
		}
		tiers[sel] = body
	}
	if len(tiers) != 3 {
		got := make([]string, 0, len(tiers))
		for sel := range tiers {
			got = append(got, sel)
		}
		sort.Strings(got)
		t.Fatalf("%d button tiers, want exactly 3 (primary, secondary, destructive): %v",
			len(tiers), got)
	}
	var destructive string
	for sel, body := range tiers {
		if strings.Contains(sel, "data-danger") {
			destructive = body
		}
	}
	if destructive == "" {
		t.Fatal("none of the three tiers is the destructive one")
	}
	if !strings.Contains(destructive, "var(--bad)") {
		t.Error("the destructive tier does not resolve to --bad")
	}
}

// stripComments removes /* ... */ from a selector fragment, so a rule preceded
// by a comment mentioning "button" is not counted as one.
func stripComments(s string) string {
	for {
		open := strings.Index(s, "/*")
		if open < 0 {
			return s
		}
		close := strings.Index(s[open:], "*/")
		if close < 0 {
			return s[:open]
		}
		s = s[:open] + s[open+close+2:]
	}
}

// TestDesignSystemUsesTheStatusColoursItDefines: --ok appeared 0 times and
// --warn 0 times in a 1032-line sheet. A defined-and-unused token is a decision
// nobody made. Decision 4 puts both on the things that have status.
func TestDesignSystemUsesTheStatusColoursItDefines(t *testing.T) {
	css := styles(t)
	for _, token := range []string{"--ok", "--warn"} {
		uses := strings.Count(css, "var("+token+")")
		defined := strings.Count(css, token+":")
		if defined > 0 && uses == 0 {
			t.Errorf("%s is defined %d times and used on nothing; use it or delete it",
				token, defined)
		}
	}
}

// TestDesignSystemCapsTheReadingMeasure: p was capped at 62ch and nothing else
// was, so every message the console writes back -- the one prose a saving
// operator actually reads -- ran the full 1088px column.
func TestDesignSystemCapsTheReadingMeasure(t *testing.T) {
	css := styles(t)
	if !strings.Contains(ruleFor(t, css, "app-root"), "max-width: 68rem") {
		t.Error("the console column is not held at the 68rem decided in Jockora-e9a.49")
	}
	for _, sel := range []string{"\np ", "[data-said]:not(:empty)"} {
		if !strings.Contains(ruleFor(t, css, sel), "ch") {
			t.Errorf("%s is not held to a reading measure", strings.TrimSpace(sel))
		}
	}
}

// ----------------------------------------------------------- console rhythm --
//
// Jockora-e9a.45 is the same finding as e9a.49's decision 1, stated per TAB.
// Only the Overview uses <section>; the other five tabs group with a heading, a
// table and a fieldset, and a ratio proved on <section> alone proves nothing
// about them. These pin the rhythm on the elements those tabs actually use.

// TestConsoleRhythmSeparatesGroupsOnEveryTab: measured on the Overview at
// 1280px, consecutive sections were 12px apart and a heading sat 12px from its
// own body text. Five tabs never had a <section> to fix.
func TestConsoleRhythmSeparatesGroupsOnEveryTab(t *testing.T) {
	css := styles(t)
	within := step(t, css, "\np ", "margin")

	// Every element that STARTS a group in the console. A tab is a heading, a
	// table and a form; the Overview is a stack of sections.
	for _, group := range []struct{ sel, prop string }{
		{"\nsection", "margin-top"},
		{"\nfieldset", "margin"},
	} {
		between := step(t, css, group.sel, group.prop)
		if ratio := between / within; ratio < 2.5 {
			t.Errorf("%s opens %grem from what came before against %grem inside a group: %.2f to 1, want 2.5",
				strings.TrimSpace(group.sel), between, within, ratio)
		}
	}

	// A picker inside the station editor is CONTENTS of one group, not a group.
	// Without this the vocabulary fieldsets take the between-group step and the
	// edit row grows 32px of air between Genre and Mood.
	if nested := step(t, css, "fieldset fieldset", "margin"); nested >= within*2.5 {
		t.Errorf("a nested fieldset takes the between-group step (%grem); it is contents", nested)
	}
}

// TestConsoleRhythmBindsAHeadingToWhatFollows: every h2 and h3 in the console
// had margin-top 0 and margin-bottom 12px -- the exact inverse of what binds a
// heading to the content it introduces.
func TestConsoleRhythmBindsAHeadingToWhatFollows(t *testing.T) {
	css := styles(t)
	for _, sel := range []string{"\nh2 ", "section > h3"} {
		above, below := step(t, css, sel, "margin-top"), step(t, css, sel, "margin-bottom")
		if above <= below {
			t.Errorf("%s: %grem above, %grem below -- it floats between two groups and binds to neither",
				strings.TrimSpace(sel), above, below)
		}
	}
}

// ---------------------------------------------------------- enrichment panel --
//
// Jockora-e9a.46. Two of its four defects are stylesheet facts.

// TestEnrichmentPanelDoesNotUseTheBrowsersBlue: the download link computed to
// rgb(0, 0, 238) with an underline -- the browser default, and the only blue
// pixel in a console whose palette is warm rust and cream. Every other action
// on the page is a styled button.
func TestEnrichmentPanelDoesNotUseTheBrowsersBlue(t *testing.T) {
	// The whole declaration. "color:" alone matched text-decoration-color and
	// let a mutation that removed the colour entirely pass.
	rule := ruleFor(t, styles(t), "[data-export]")
	if !strings.Contains(rule, "color: var(--ember)") {
		t.Errorf("the download link takes the browser's blue instead of the console's own token:\n%s", rule)
	}
}

// TestEnrichmentPanelHidesTheNativeFileWidget: a raw input type=file renders
// the browser's "Choose File / No file chosen" -- the same defect as the
// vocabulary checkboxes, an unstyled native control in a styled console. The
// label becomes the control and the input hides behind it.
func TestEnrichmentPanelHidesTheNativeFileWidget(t *testing.T) {
	css := styles(t)
	rule := ruleFor(t, css, "[data-import-label] input[type='file']")
	if !strings.Contains(rule, "position: absolute") || !strings.Contains(rule, "opacity: 0") {
		t.Errorf("the native file widget is still on screen:\n%s", rule)
	}
	// Hidden is not gone: it stays focusable, so the ring has to move to the
	// thing that IS on screen or a keyboard user loses the control entirely.
	if !strings.Contains(css, "[data-import-label]:focus-within") {
		t.Error("hiding the input took the keyboard focus ring with it")
	}
	if !strings.Contains(css, "[data-import-button]") {
		t.Error("nothing stands in for the widget that was hidden")
	}
}

// -------------------------------------------------------- console states --
//
// Jockora-e9a.50. The stylesheet half: the three states have to LOOK like
// three states, and focus has to survive every rule that turns an outline off.

// TestConsoleStatesLooksLikeThreeStates: an empty state that renders as body
// text in the middle of a table is a row the operator scrolls past. It takes
// the console's own quiet treatment, and it says so in --ink-faint rather than
// in a colour that reads as an error.
func TestConsoleStatesLooksLikeThreeStates(t *testing.T) {
	css := styles(t)
	for _, sel := range []string{"[data-empty]", "[data-loading]"} {
		rule := ruleFor(t, css, sel)
		if !strings.Contains(rule, "var(--") {
			t.Errorf("%s is unstyled; it renders as a bare table cell", sel)
		}
	}
	// The empty state is the one that has to be read, so it is not the fainter
	// of the two. Loading is transient and should not shout.
	if !strings.Contains(ruleFor(t, css, "[data-loading]"), "--ink-faint") {
		t.Error("the loading line is as loud as the content it is standing in for")
	}
}

// TestConsoleStatesNeverTurnsOffFocusWithoutReplacingIt: outline:none is how a
// control becomes invisible to a keyboard. Every rule that sets it has to put
// something back IN THE SAME BLOCK, because a later :focus-visible rule loses
// to a more specific selector and silently does nothing.
func TestConsoleStatesNeverTurnsOffFocusWithoutReplacingIt(t *testing.T) {
	css := styles(t)
	for _, block := range strings.Split(css, "}") {
		open := strings.Index(block, "{")
		if open < 0 {
			continue
		}
		body := block[open:]
		if !strings.Contains(body, "outline: none") {
			continue
		}
		sel := strings.TrimSpace(stripComments(block[:open]))
		if !strings.Contains(body, "box-shadow:") {
			t.Errorf("%q turns focus off and puts nothing back", sel)
		}
	}
	// And the backstop itself is still there.
	if !strings.Contains(ruleFor(t, css, ":focus-visible"), "outline: 2px solid var(--ember)") {
		t.Error("the global keyboard focus ring is gone")
	}
}

// TestConsoleStatesPutsEveryClickOnARealControl: the console is operable by
// keyboard because every (click) in it sits on a <button> or an <a>, which
// take focus and fire on Enter for free. A div with a click handler does
// neither, and nothing about the rendered page says so -- it is invisible
// until somebody without a mouse tries. Audited at 44 handlers, all of them on
// a control; this is what keeps the 45th honest.
func TestConsoleStatesPutsEveryClickOnARealControl(t *testing.T) {
	var checked int
	tag := regexp.MustCompile(`(?s)<([a-zA-Z][\w-]*)((?:[^<>])*?)\(click\)=`)
	err := filepath.WalkDir("../web/app/src/app", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".component.ts") ||
			strings.HasSuffix(path, ".spec.ts") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range tag.FindAllStringSubmatch(string(b), -1) {
			checked++
			if m[1] != "button" && m[1] != "a" {
				t.Errorf("%s: <%s> carries a click handler; it takes no focus and does not fire on Enter",
					filepath.Base(path), m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the console templates: %v", err)
	}
	if checked < 40 {
		t.Fatalf("only %d click handlers found; the scan is not reading the templates", checked)
	}
}

// TestModelOutageLooksLikeTrouble: the model-refusing line is the one thing on
// the Overview that has to reach an operator who came to read a different
// number. It takes --bad, which decision 4 reserved for exactly this.
func TestModelOutageLooksLikeTrouble(t *testing.T) {
	css := styles(t)
	for _, sel := range []string{"[data-model-health]", "[data-llm-health]"} {
		rule := ruleFor(t, css, sel)
		if !strings.Contains(rule, "var(--bad)") {
			t.Errorf("%s does not use the colour reserved for something being wrong", sel)
		}
	}
}
