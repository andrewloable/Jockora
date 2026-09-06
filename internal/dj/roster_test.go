// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"strings"
	"testing"
)

// TestRosterLoadsAndRoutes guards the whole persona directory rather than one
// card. A malformed card is only discovered when a station tries to pick it,
// which is at airtime.
func TestRosterLoadsAndRoutes(t *testing.T) {
	all, err := LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 9 {
		t.Fatalf("loaded %d personas, want the whole roster", len(all))
	}

	seen := map[string]bool{}
	for _, p := range all {
		if seen[p.ID()] {
			t.Errorf("duplicate persona id %q; SelectPersona would pick arbitrarily", p.ID())
		}
		seen[p.ID()] = true

		// A voice id that is not namespaced reaches Kokoro as a literal and
		// fails EVERY synthesis with a 503. That went undetected once, for
		// twenty breaks out of twenty, so it is checked here by name.
		if !strings.HasPrefix(p.VoiceID(), "kokoro:") {
			t.Errorf("%s: voice_id %q is not namespaced", p.ID(), p.VoiceID())
		}
		if len(p.GoodForGenres()) == 0 || len(p.GoodForMoods()) == 0 {
			t.Errorf("%s: cannot be routed to; SelectPersona matches on genres and moods", p.ID())
		}

		// A forbidden list the rubric cannot enforce at all is decoration.
		// prosper_okonkwo predates the rubric and is the documented exception.
		if c, _ := CheckableRules(p); len(c) == 0 && p.ID() != "prosper_okonkwo" {
			t.Errorf("%s: no forbidden rule quotes a phrase, so the rubric can check none of them", p.ID())
		}
	}
}

// TestRosterCoversTheLibrarysGenres: a roster is only useful if the genres a
// real library reports actually reach a jock.
func TestRosterCoversTheLibrarysGenres(t *testing.T) {
	all, err := LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}

	// Measured from the enriched dossiers of the development library.
	for _, genre := range []string{"rock", "alternative", "opm", "electronic", "pop", "soul", "metal", "punk", "classical"} {
		chosen, scores := SelectPersona(all, []string{genre}, nil)
		if chosen == nil {
			t.Errorf("%q routes to no jock", genre)
			continue
		}
		if scores[0].Score == 0 {
			t.Errorf("%q matched nothing; %s was picked only as a fallback", genre, chosen.ID())
		}
	}
}

// TestRosterLeavesNoJockUnreachable: a persona that never wins any genre can
// never be selected, so it is dead weight in the directory and nobody would
// notice. Adding four cards at once crowded roxy_sinclair out of pop, dance,
// disco and funk in one go -- every one of those routed to a NEW jock, and the
// only reason it was caught is that two older tests happened to assert rock.
func TestRosterLeavesNoJockUnreachable(t *testing.T) {
	all, err := LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}

	won := map[string]bool{}
	for _, p := range all {
		for _, g := range p.GoodForGenres() {
			if chosen, _ := SelectPersona(all, []string{g}, nil); chosen != nil {
				won[chosen.ID()] = true
			}
		}
	}
	for _, p := range all {
		if !won[p.ID()] {
			t.Errorf("%s wins no genre, not even one of its own; it can never be selected", p.ID())
		}
	}
}
