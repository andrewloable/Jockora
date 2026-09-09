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
	for _, table := range []string{"[data-playlist]", "[data-stations]", "[data-jocks]",
		"[data-overview]", "[data-logs]", "[data-ads]"} {
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
// TestConsoleCSSPutsATickBesideItsLabel: Jockora-e9a.56. "label {display:
// block}" plus "label input {display: block; width: 100%}" is right for a text
// field and exactly wrong for a tick. On the model page all three radios
// rendered at x 113 with their bold label 14px BELOW, so a lone circle floated
// above each heading -- most obvious at 390px.
//
// The vocabulary pickers were given a flex row when they were built; every
// other tick in the console was left on the block rule. This is the general
// case of that fix, so a checkbox added tomorrow is right without anybody
// remembering.
func TestConsoleCSSPutsATickBesideItsLabel(t *testing.T) {
	css := styles(t)
	rule := ruleFor(t, css, `label:has(> input[type='radio'])`)
	if !strings.Contains(rule, "display: flex") {
		t.Error("a label holding a tick is still a block, so the control stacks above its text")
	}
	// The width:100% from the text-field rule is what made the radio take a
	// line of its own. Undoing display alone is not enough.
	back := ruleFor(t, css, `label:has(> input[type='radio']) > input`)
	if !strings.Contains(back, "width: auto") {
		t.Error("a tick still stretches to the full width of its label")
	}
}

// TestConsoleCSSDrawsATickInTheConsolePalette: the radio computed to
// accent-color auto, so the selected Cloudflare option drew in the browser's
// default blue in a console whose palette is warm rust and cream. Same family
// as the Overview file input and the checkboxes before Jockora-e9a.44.
func TestConsoleCSSDrawsATickInTheConsolePalette(t *testing.T) {
	rule := ruleFor(t, styles(t), "input[type='radio']")
	if !strings.Contains(rule, "accent-color: var(--ember)") {
		t.Error("a tick draws in the browser default blue, not the console's own ember")
	}
}

// TestConsoleCSSKeepsADescriptionOffItsLabel: Angular drops the whitespace-only
// text node between two inline siblings, so <strong>Name</strong>
// <small>what it is</small> renders as one run of text. Reported on three
// separate screens -- the Overview download link (Jockora-e9a.46), the advert
// form (Jockora-e9a.55) and the model page (Jockora-e9a.56) -- which makes it
// one rule the sheet owes every screen rather than three markup fixes.
//
// The page read "Local llama.cppA model running on your own hardware".
func TestConsoleCSSKeepsADescriptionOffItsLabel(t *testing.T) {
	css := styles(t)
	rule := ruleFor(t, css, "small")
	if !strings.Contains(rule, "display: block") {
		t.Error("a description is still inline, so it touches whatever element precedes it")
	}
	// One deliberate exception, MARKED rather than guessed: the feedback line
	// names its jock on the same line as the sentence it belongs to.
	if !strings.Contains(css, "small[data-inline]") {
		t.Error("there is no way to opt a description back onto its line")
	}
}

// TestConsoleCSSStylesATextarea: THE STYLESHEET HAD NO TEXTAREA RULE AT ALL,
// in 1386 lines. That single omission is three reported defects.
//
// "label input, label select {display: block}" is what puts a label above its
// field, and a textarea was in neither list -- so it stayed inline and sat
// BESIDE its own label while the inputs beside it had theirs above. Measured on
// the ads form: "What it is about" label at x472 y455 and its box at x580 y455,
// the same baseline, while "Delivery" had its label at y466 and its box at
// y492. Three label placements in one fieldset, which is what Jockora-e9a.53
// reports for Stations and Jockora-e9a.55 for Ads -- one cause, two screens.
//
// It also left the textarea on the browser's monospace default, in a console
// that is Avenir Next everywhere else.
func TestConsoleCSSStylesATextarea(t *testing.T) {
	css := styles(t)

	block := ruleFor(t, css, "label textarea")
	if !strings.Contains(block, "display: block") {
		t.Error("a textarea is still inline, so its label sits beside it and not above it")
	}

	// It has to LOOK like the field beside it, or a form reads as two designs.
	// Newline before and space after, so this is the BARE textarea rule and not
	// "label textarea", "textarea:hover" or "fieldset textarea" -- ruleFor
	// matches anywhere in a selector list and returns the first rule that hits,
	// and the hover rule sits earlier in the sheet.
	field := ruleFor(t, css, "\ntextarea ")
	if !strings.Contains(field, "font: inherit") && !strings.Contains(field, "font-family") {
		t.Error("a textarea still draws in the browser's monospace default")
	}
	// Room for the sentence it holds. The about box was 36px tall around
	// content whose scrollHeight was 49.
	if !strings.Contains(field, "min-height") {
		t.Error("a textarea has no minimum height, so it clips the sentence it was sized for")
	}
}

// TestConsoleCSSLetsASliderBeASlider: Jockora-b7q, reported as "i cannot make
// the volume 100%".
//
// The volume IS 100%. Measured on a real browser the input reads value 1,
// valueAsNumber 1, max 1, and the audio element's volume is 1 -- and a
// screenshot of the element at that value shows UNFILLED TRACK to the right of
// the thumb. The control was lying about its own state, which is worse than one
// that does not work: the listener drags it, sees track left over, and
// concludes the app is broken.
//
// The entry-field rule matched input[type=range], so the slider was wearing a
// text box: min-height 44px, padding 8px 12px, a border and a surface fill.
// That padding insets the native track inside the 128px box.
//
// Exactly the defect the sheet already fixed for checkboxes one rule below --
// "A CHECKBOX IS NOT AN ENTRY BOX" -- with range never added to the exclusion.
func TestConsoleCSSLetsASliderBeASlider(t *testing.T) {
	css := styles(t)
	sizing := regexp.MustCompile(`(?m)^input:not\(\[type='checkbox'\]\)[^,{]*`)
	sel := sizing.FindString(css)
	if sel == "" {
		t.Fatal("the entry-field sizing rule is gone; if it moved, move this test with it")
	}
	if !strings.Contains(sel, `:not([type='range'])`) {
		t.Errorf("a range still gets the entry-field box, so its track is inset: %q",
			strings.TrimSpace(sel))
	}
}

// TestConsoleCSSShapesAFieldLikeALabel: Jockora-lph. The markup contract that a
// form row is a run of same-shaped boxes is only half the fix -- the sheet has
// to give [data-field] the same box a label gets, or the Angular spec passes
// while the row still misaligns on screen.
//
// A bare button in a form row has no label header above it, so under the
// fieldset's bottom alignment it sat below the controls it belongs beside. It
// cannot be wrapped in a real label: a button is itself labelable, so that
// markup is invalid. A box of the same SHAPE needs no offset to maintain.
func TestConsoleCSSShapesAFieldLikeALabel(t *testing.T) {
	css := styles(t)
	// THE WHOLE SELECTOR LIST, not just "[data-field]": the rule that lays out
	// the button inside the box also names it and also says display:block, so a
	// looser anchor matched that one instead and the mutation survived.
	if !strings.Contains(ruleFor(t, css, "label,\n[data-field]"), "display: block") {
		t.Error("[data-field] does not get the label box, so an action sits below its row")
	}
	// And its button is laid out like a control inside a label, or the box is
	// the right height and the thing inside it still is not.
	if !strings.Contains(ruleFor(t, css, "[data-field] > button"), "margin-top: var(--s1)") {
		t.Error("the action inside a field box is not spaced like a control")
	}
}

// TestConsoleCSSGivesTheStationFormItsRows: reported with a screenshot
// 2026-09-09. The station dialog's fieldset is a wrapping flex row, so anything
// without a width of its own packed into whatever gap was left -- and three
// things that each need a row of their own did not have one.
//
// MEASURED BEFORE AND AFTER at 1280px. Before: the year group started at x390
// and the length group at x571, because the Name box and the track count had
// taken the space to their left; the by-hand picker was 243px wide wedged
// beside "Longest (minutes)", so its summary read as that field's caption. The
// count itself drew to the LEFT of the Name box and read as its label. After,
// every one of them starts at x209 with the rest of the form.
func TestConsoleCSSGivesTheStationFormItsRows(t *testing.T) {
	css := styles(t)
	for _, c := range []struct{ sel, why string }{
		{"[data-derived-status]", "the model's answer packs in beside the Name box and reads as its caption"},
		{"[data-name-field]", "the station's name floats wherever the wrap leaves room"},
		{"[data-manual]", "two checkbox grids sit beside a number field and read as its caption"},
	} {
		if !strings.Contains(ruleFor(t, css, c.sel), "flex: 1 0 100%") {
			t.Errorf("%s does not take a row of its own, so %s", c.sel, c.why)
		}
	}
	// THE COUNT AND THE WARNING STACK. They are two sentences about one answer
	// and they ran together on one line.
	if !strings.Contains(ruleFor(t, css, "[data-derived-status] > span"), "display: block") {
		t.Error("the track count and the low-track warning share a line")
	}
	// AND THE DESCRIBE BUTTON DOES NOT STRETCH. It sits beside a textarea that
	// is already the full width of the row, so a bare flex item would grow to
	// fill whatever is left rather than staying a button.
	if !strings.Contains(ruleFor(t, css, "[data-brief-group] > button"), "flex: 0 0 auto") {
		t.Error("the describe button stretches across the row beside the description")
	}
}

// TestConsoleCSSMakesASortableHeaderAControl: Jockora-9g7. A th with a click
// handler is not reachable by keyboard and is announced as a cell, so the
// sortable headers are real buttons -- which then have to be stripped back to
// header typography, or every table grows a row of grey chrome.
func TestConsoleCSSMakesASortableHeaderAControl(t *testing.T) {
	css := styles(t)
	rule := ruleFor(t, css, "[data-sort]")
	for _, want := range []string{"font: inherit", "background: none", "border: none", "text-align: left"} {
		if !strings.Contains(rule, want) {
			t.Errorf("a sortable header is missing %q, so it draws as a button rather than a header", want)
		}
	}
	// THE ARROW KEEPS ITS SPACE. Without a reserved width, clicking a header
	// shoves every column beside it sideways as the marker appears.
	if !strings.Contains(ruleFor(t, css, "[data-sort-marker]"), "min-width") {
		t.Error("the sort arrow has no reserved width, so sorting shifts the columns")
	}
}

// TestConsoleCSSGivesADialogAPhoneShape: Jockora-e9a.60. Every add and edit
// form used to be an in-page fieldset below the list it belonged to. Measured
// on a 390x844 phone: tapping Edit on the first station left scrollY at 0 while
// the form opened at viewport y 868 -- entirely below the fold on a page 2664px
// tall, with nothing scrolling to it.
//
// The dialog element solves WHERE the form is; these rules decide what shape it
// takes, and a small centred box on a 390px screen is a form in a letterbox.
func TestConsoleCSSGivesADialogAPhoneShape(t *testing.T) {
	css := styles(t)

	box := ruleFor(t, css, "[data-form-dialog]")
	if !strings.Contains(box, "width: min(") {
		t.Error("a dialog takes whatever width it likes, rather than a readable column")
	}
	// The backdrop is the platform's, and dimming it is what makes the page
	// behind read as out of reach.
	if !strings.Contains(css, "[data-form-dialog]::backdrop") {
		t.Error("the dialog has no backdrop, so the page behind it looks live")
	}
	// A FULL-HEIGHT SHEET BELOW 48rem, the same breakpoint every admin table
	// already becomes a card at.
	// A FULL-HEIGHT SHEET BELOW 48rem, the same breakpoint every admin table
	// already becomes a card at. Read from the narrow block rather than through
	// ruleFor, which splits on braces and so cannot see inside a media query --
	// the same reason TestConsoleCSSStacksThePlaylistOnAPhone reads it this way.
	narrow := strings.Index(css, "@media (max-width: 47.99rem)")
	if narrow < 0 {
		t.Fatal("there are no narrow-viewport rules at all")
	}
	sheet := css[narrow:]
	if !strings.Contains(sheet, "[data-form-dialog]") || !strings.Contains(sheet, "100dvh") {
		t.Error("a dialog is still a centred box on a phone, which is a form in a letterbox")
	}
}

// TestConsoleCSSKeepsARangePairTogether: Jockora-e9a.61. A range is two boxes
// describing ONE bound, and as loose flex items the wrap could put them
// anywhere -- measured at 1280px, Shortest sat at x951 on one row and Longest
// at x113 on the next, which is the worst possible place for the two halves of
// one number.
func TestConsoleCSSKeepsARangePairTogether(t *testing.T) {
	css := styles(t)
	pair := ruleFor(t, css, "[data-range]")
	if !strings.Contains(pair, "flex-wrap: nowrap") {
		t.Error("a range pair may still wrap, so its two ends can land on different rows")
	}
	// And the field a generate button acts on travels with it, or the button
	// starts the next row and reads as part of whatever it lands beside.
	group := ruleFor(t, css, "[data-brief-group]")
	if !strings.Contains(group, "flex: 1 0 100%") {
		t.Error("the description and its button are not one row of their own")
	}
}

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

// ---------------------------------------------------------- listener page --
//
// Jockora-e9a.47. At 390px the listener page is the best screen in the product:
// zero overflowing elements, the station card full width, its metadata on two
// comfortable lines. At 1280px the SAME card is 208px wide and its metadata
// wraps to five cramped monospace lines, with 610px -- 48 percent of the
// viewport -- empty. The desktop layout was the mobile layout left-aligned.

// TestListenerPageIsAColumnNotALeftEdge: e9a.49 decided 68rem centred for the
// console and said this task would read it as "centre the dial too". A listener
// page is a player and a dial, not a data console, so it takes a narrower
// measure -- but it is CHOSEN and centred rather than whatever is left over.
func TestListenerPageIsAColumnNotALeftEdge(t *testing.T) {
	rule := ruleFor(t, styles(t), "app-listener")
	if !strings.Contains(rule, "max-width") {
		t.Error("the listener page has no measure; it is as wide as the screen it is on")
	}
	if !strings.Contains(rule, "margin: 0 auto") {
		t.Error("the listener page is not centred, so it hugs the left edge of a wide screen")
	}
}

// TestListenerPageGivesAStationCardRoomToRead: at minmax(13rem, 1fr) a card was
// 208px and its metadata -- tracks, jock, moods, listeners -- wrapped to five
// lines in a monospace face.
func TestListenerPageGivesAStationCardRoomToRead(t *testing.T) {
	// COMMENTS STRIPPED FROM THE BODY, not only from the selector. The rule's
	// own comment explains why min(100%, ...) is there, so a Contains check
	// matched the explanation and passed with the guard deleted. Third time
	// today a comment has defeated an assertion in this file.
	rule := stripComments(ruleFor(t, styles(t), "[data-dial]"))
	m := regexp.MustCompile(`minmax\(\s*(?:min\(100%,\s*)?([0-9.]+)rem`).FindStringSubmatch(rule)
	if m == nil {
		t.Fatalf("the dial's track size is not a rem minimum any more:\n%s", rule)
	}
	var min float64
	if _, err := fmt.Sscanf(m[1], "%g", &min); err != nil {
		t.Fatalf("track minimum %q is not a number", m[1])
	}
	if min < 16 {
		t.Errorf("a station card is at least %grem; it was 13rem and drew five cramped lines", min)
	}
	// AND THE PHONE IS NOT REGRESSED. Without min(100%, ...) a track minimum
	// wider than the viewport overflows the page sideways, which is the one
	// thing the 390px layout measured clean.
	if !strings.Contains(rule, "min(100%") {
		t.Error("a track wider than a phone's viewport can now push the page sideways")
	}
}

// TestListenerPageSettlesLiveWithTheTransport: margin-left auto pushed LIVE to
// the far end of a full-width bar, so it sat alone with empty space either side
// while Listen and Volume clustered at the left. It belongs to the transport.
func TestListenerPageSettlesLiveWithTheTransport(t *testing.T) {
	if strings.Contains(ruleFor(t, styles(t), "[data-live]"), "margin-left: auto") {
		t.Error("LIVE is still pushed away from the controls it belongs to")
	}
}
