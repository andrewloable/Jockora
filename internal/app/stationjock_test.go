// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// THE JOCK AN OPERATOR ASSIGNS IS THE JOCK ON AIR.
//
// It was not. station.jock_id was written by the console, read back by the
// console, shown on the dial -- and never once reached the microphone. The
// persona came from a genre vote over personas/ taken at boot and never
// changed again, so a station assigned Sunny Marchetti (slow, warm, af_nicole)
// aired Dutch "The Hammer" Mahoney (shouting, am_fenrir), and the dial
// captioned him with her name.

// booted is the persona a boot-time genre vote produced: the seed nobody chose.
func booted() *dj.Persona {
	return dj.FromRecord(store.Jock{ID: "dutch_mahoney", Name: "Dutch Mahoney",
		VoiceID: "kokoro:am_fenrir"})
}

func jockApp(t *testing.T) (*App, *store.Store) {
	t.Helper()
	s := openUpgraded(t, filepath.Join(t.TempDir(), "jocks.db"))
	a := &App{
		cfg: testConfig(t),
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		opts: Options{
			Library: &Library{Store: s},
			// A pipeline as well as a writer: they are built together and a
			// station with one and not the other is not a shape production
			// can produce.
			Breaks: &station.Pipeline{},
			Writer: &station.BreakWriter{
				Persona: booted(),
				// The said-lines index has to follow the jock, or the new
				// persona inherits somebody else's phrase history.
				Validator: &dj.Validator{Said: &dj.SaidLines{Store: s, JockID: "dutch_mahoney"}},
			},
		},
	}
	return a, s
}

// station with a jock assigned, and the jock row it points at.
func stationWithJock(t *testing.T, s *store.Store, jockID string) int64 {
	t.Helper()
	ctx := context.Background()
	if jockID != "" {
		if err := s.UpsertJock(ctx, store.Jock{ID: jockID, Name: "Sunny Marchetti",
			VoiceID: "kokoro:af_nicole", SpeechStyle: "Slow, warm and very close to the microphone."}); err != nil {
			t.Fatal(err)
		}
	}
	id, err := s.CreateStation(ctx, store.Station{Name: "Night Rock", Genre: "rock",
		JockID: jockID, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestStationJockOnAir(t *testing.T) {
	a, s := jockApp(t)
	id := stationWithJock(t, s, "sunny_marchetti")

	a.applyStationJock(context.Background(), id)

	if got := a.breaksFor(id).writer.CurrentPersona().ID(); got != "sunny_marchetti" {
		t.Errorf("persona on air = %q, want the station's own jock", got)
	}
	// The VOICE is the half the operator previewed, and it travels with the
	// persona: LengthCheck.VoiceOf asks the writer at every render.
	if got := a.opts.Writer.Voice(); got != "kokoro:af_nicole" {
		t.Errorf("voice on air = %q, want the assigned jock's", got)
	}
	if got := a.opts.Writer.Validator.Said.JockID; got != "sunny_marchetti" {
		t.Errorf("said-lines jock = %q, want it to follow the persona", got)
	}
}

func TestStationJockOnAirKeepsSeedWhenUnassigned(t *testing.T) {
	// A station with no jock is a normal state -- the catch-all still plays
	// music -- and the boot-time pick is the seed it falls back to.
	a, s := jockApp(t)
	id := stationWithJock(t, s, "")

	a.applyStationJock(context.Background(), id)

	if got := a.breaksFor(id).writer.CurrentPersona().ID(); got != "dutch_mahoney" {
		t.Errorf("persona on air = %q, want the boot-time seed kept", got)
	}
}

func TestStationJockOnAirSurvivesAMissingStation(t *testing.T) {
	a, _ := jockApp(t)
	a.applyStationJock(context.Background(), 404)
	if got := a.breaksFor(404).writer.CurrentPersona().ID(); got != "dutch_mahoney" {
		t.Errorf("persona on air = %q, want it left alone", got)
	}
}

func TestStationJockOnAirWithoutAWriterDoesNothing(t *testing.T) {
	// No DJ at all -- no persona, no LLM, no sidecar -- is a supported way to
	// run, so this must not panic on the feed path.
	a, s := jockApp(t)
	id := stationWithJock(t, s, "sunny_marchetti")
	a.opts.Writer = nil
	a.applyStationJock(context.Background(), id)
}

func TestStationJockOnAirLeavesTheSameJockAlone(t *testing.T) {
	// Re-reading the same jock out of the database every time a station starts
	// would rebuild the persona under a writer that already had it, for nothing.
	a, s := jockApp(t)
	id := stationWithJock(t, s, "sunny_marchetti")
	a.applyStationJock(context.Background(), id)

	was := a.breaksFor(id).writer.CurrentPersona()
	a.applyStationJock(context.Background(), id)
	if a.breaksFor(id).writer.CurrentPersona() != was {
		t.Error("the persona was replaced with an identical one")
	}
}
