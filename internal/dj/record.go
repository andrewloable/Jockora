// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import "github.com/andrewloable/jockora/internal/store"

// Record renders a persona as the row the operator console edits.
//
// A METHOD RATHER THAN PUBLIC FIELDS. Persona's fields stay private because the
// card is immutable ground truth at runtime -- distilled memory is a separate
// overlay that may never contradict it, and opening the type would make that
// rule a convention instead of a compiler guarantee. Conversion is one-way
// traffic through here, in both directions.
func (p *Persona) Record() store.Jock {
	return store.Jock{
		ID:            p.ID(),
		Name:          p.Name(),
		VoiceID:       p.VoiceID(),
		GoodForGenres: p.GoodForGenres(),
		GoodForMoods:  p.GoodForMoods(),
		SpeechStyle:   p.SpeechStyle(),
		Personality:   p.Personality(),
		Forbidden:     p.Forbidden(),
	}
}

// FromRecord builds the immutable runtime persona from a row.
//
// The slices are COPIED rather than aliased: a Persona that shared backing
// arrays with the row it came from could have its boundaries rewritten by
// whoever still held that row, which is exactly the mutation the private
// fields exist to prevent.
func FromRecord(j store.Jock) *Persona {
	return &Persona{
		id:            j.ID,
		name:          j.Name,
		voiceID:       j.VoiceID,
		goodForGenres: append([]string(nil), j.GoodForGenres...),
		goodForMoods:  append([]string(nil), j.GoodForMoods...),
		speechStyle:   j.SpeechStyle,
		personality:   j.Personality,
		forbidden:     append([]string(nil), j.Forbidden...),
	}
}
