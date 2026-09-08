// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"path/filepath"
	"testing"
)

// Jockora-e9a.55. An advert could only be created or destroyed, never paused.
// Every other manageable thing in this console turns off without being lost --
// sources have Disable, stations have Disable, accounts have Disable -- and
// adverts alone had to be DELETED, taking hand-written copy with them.
//
// A seasonal or paused advertiser is the ordinary case, not an edge case.

func TestAdEnabledDefaultsToOn(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "ads.db"))
	ctx := context.Background()

	id, err := s.CreateAd(ctx, adFixture())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// AN ADVERT SOMEBODY JUST WROTE IS ON AIR. Anything else means every new
	// advert is silently off and the operator concludes the feature is broken.
	if !got.Enabled {
		t.Error("a new advert is disabled")
	}
}

func TestAdEnabledTogglesWithoutTouchingTheCopy(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "ads.db"))
	ctx := context.Background()
	id, err := s.CreateAd(ctx, adFixture())
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetAdEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	off, err := s.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if off.Enabled {
		t.Error("the advert is still enabled after being disabled")
	}
	// DISABLING IS NOT DELETING. The copy is hand-written prose an operator
	// would not want to retype, which is the whole reason this exists.
	if off.Script != adFixture().Script || off.Brand != adFixture().Brand {
		t.Errorf("disabling rewrote the advert: %+v", off)
	}

	if err := s.SetAdEnabled(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	on, err := s.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !on.Enabled {
		t.Error("the advert did not come back on")
	}
}

// TestAdEnabledSurvivesAnEdit: Update writes the whole card back from a console
// form. If it carried the flag it would re-enable a paused advert the moment
// somebody fixed a typo in it -- and a form that silently turns an advert back
// on is worse than no pause at all.
func TestAdEnabledSurvivesAnEdit(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "ads.db"))
	ctx := context.Background()
	id, err := s.CreateAd(ctx, adFixture())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAdEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}

	edited := adFixture()
	edited.ID = id
	edited.Script = "Stillwater Ceramics. Twelve years now, and still the one mug."
	if err := s.UpdateAd(ctx, edited); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Error("editing an advert turned it back on")
	}
	if got.Script != edited.Script {
		t.Errorf("the edit did not land: %q", got.Script)
	}
}

func TestAdEnabledIsMissingRow(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "ads.db"))
	if err := s.SetAdEnabled(context.Background(), 404, false); err == nil {
		t.Error("disabling an advert that does not exist reported success")
	}
}

// TestAdEnabledBackfillsExistingAdverts: every advert that already exists
// predates the column. A migration that left them NULL would read as disabled
// and take a working rotation off air on upgrade.
func TestAdEnabledBackfillsExistingAdverts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	ctx := context.Background()

	old := openAt(t, path)
	rewindTo(t, old, CurrentSchemaVersion-1)
	if _, err := old.DB().ExecContext(ctx,
		`INSERT INTO ads (id, text) VALUES (7, 'an advert from before the column')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	// Reopened, so the migration runs for real over a row that already existed.
	s := openAt(t, path)
	got, err := s.GetAd(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Error("an advert that predates the column came back disabled")
	}
}
