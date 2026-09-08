// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Jockora-iyw.3. adRotation() ran ONCE per station start, and a station runs
// while it has a listener -- so a busy station runs for days. An operator who
// deletes an advert and keeps hearing it all afternoon reports it as broken,
// and they are right.

func ad(id, brand string) Ad {
	return Ad{ID: id, Brand: brand,
		Script: brand + ". A thing that exists and is worth your attention tonight."}
}

// TestAdRotationLiveReadsFresh: READING PER PICK IS THE POINT. One small SELECT
// roughly every fourth break is nothing, and it is the only way an edit in the
// console is heard without a restart.
func TestAdRotationLiveReadsFresh(t *testing.T) {
	pool := []Ad{ad("1", "Stillwater"), ad("2", "Harrow")}
	r := NewAdRotation(nil)
	r.Source = func(context.Context) ([]Ad, error) { return pool, nil }

	now := time.Unix(1788840000, 0)
	if _, err := r.Next(context.Background(), now); err != nil {
		t.Fatalf("Next: %v", err)
	}

	// The operator adds one. NOTHING IS REBUILT.
	pool = append(pool, ad("3", "Marchetti"))

	// Airing is what ages an advert, and with a Source the age lives on the
	// ROW -- so the fixture updates itself the way the database would.
	r.Aired = func(_ context.Context, id string, at time.Time) error {
		for i := range pool {
			if pool[i].ID == id {
				pool[i].LastAiredAt = at
			}
		}
		return nil
	}

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		got, err := r.Next(context.Background(), at)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		seen[got.ID] = true
		if err := r.MarkAired(context.Background(), got.ID, at); err != nil {
			t.Fatal(err)
		}
	}
	if !seen["3"] {
		t.Errorf("the advert added after the station started was never picked: %v", seen)
	}
}

func TestAdRotationLiveDeleted(t *testing.T) {
	pool := []Ad{ad("1", "Stillwater"), ad("2", "Harrow")}
	r := NewAdRotation(nil)
	r.Source = func(context.Context) ([]Ad, error) { return pool, nil }
	now := time.Unix(1788840000, 0)

	// Both are eligible until one is deleted, so pick once first to prove "2"
	// really was reachable -- otherwise this test passes against a rotation
	// that never returns anything but the first advert.
	if got, err := r.Next(context.Background(), now); err != nil || got == nil {
		t.Fatalf("Next: %v", err)
	}
	pool[0].LastAiredAt = now
	if got, err := r.Next(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	} else if got.ID != "2" {
		t.Fatalf("the second advert is unreachable even before deleting it: picked %q", got.ID)
	}

	pool = pool[:1]
	// From i=1, so the survivor is past the cooldown it just entered -- a
	// one-advert pool inside its cooldown refusing is correct and is a
	// different test.
	for i := 1; i < 6; i++ {
		got, err := r.Next(context.Background(), now.Add(time.Duration(i)*AdCooldown))
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got.ID == "2" {
			t.Fatal("a deleted advert was still aired")
		}
	}
}

// TestAdRotationLiveCooldownFromStore: ads.last_aired_at is a column that has
// never been written, and it is exactly where the cooldown belongs now that ads
// are GLOBAL -- two stations sharing a pool must share the cooldown, or a
// listener flipping stations hears the same advert twice.
func TestAdRotationLiveCooldownFromStore(t *testing.T) {
	now := time.Unix(1788840000, 0)
	pool := []Ad{
		{ID: "1", Brand: "Stillwater", Script: "Stillwater. One mug, still, and always was.",
			LastAiredAt: now.Add(-time.Minute)}, // inside its cooldown
		{ID: "2", Brand: "Harrow", Script: "Harrow. Books and a cat and no computer at all."},
	}
	var wrote []string
	r := NewAdRotation(nil)
	r.Source = func(context.Context) ([]Ad, error) { return pool, nil }
	r.Aired = func(_ context.Context, id string, _ time.Time) error {
		wrote = append(wrote, id)
		return nil
	}

	got, err := r.Next(context.Background(), now)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	// THE ROW DRIVES ELIGIBILITY, not an in-memory map that a restart empties.
	if got.ID != "2" {
		t.Errorf("picked %q, want the one that is not cooling down", got.ID)
	}

	if err := r.MarkAired(context.Background(), got.ID, now); err != nil {
		t.Fatalf("MarkAired: %v", err)
	}
	if len(wrote) != 1 || wrote[0] != "2" {
		t.Errorf("wrote %v, want the row that just aired", wrote)
	}
}

// TestAdRotationLiveSourceError: AdWriter already gives the slot back to the DJ
// when the rotation cannot produce one. Breaks are optional, music is not.
func TestAdRotationLiveSourceError(t *testing.T) {
	r := NewAdRotation(nil)
	r.Source = func(context.Context) ([]Ad, error) {
		return nil, errors.New("the database is closed")
	}
	if _, err := r.Next(context.Background(), time.Now()); err == nil {
		t.Error("a source that failed produced an advert")
	}
}

// TestAdRotationLiveNilSourceUnchanged: with no Source it is exactly what it
// was, over its own slice and its own in-memory map.
func TestAdRotationLiveNilSourceUnchanged(t *testing.T) {
	r := NewAdRotation([]Ad{ad("1", "Stillwater"), ad("2", "Harrow")})
	now := time.Unix(1788840000, 0)
	ctx := context.Background()

	first, err := r.Next(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.MarkAired(ctx, first.ID, now); err != nil {
		t.Fatal(err)
	}

	second, err := r.Next(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Error("the in-memory cooldown stopped working")
	}
	// And MarkAired with no Aired function set is a no-op rather than a panic.
	if err := r.MarkAired(ctx, first.ID, now); err != nil {
		t.Errorf("MarkAired with no writer: %v", err)
	}
}

// TestAdRotationLiveIDsAreRowIDs: dj.Ad.ID is a string and the row id is an
// integer. A silent mismatch means the cooldown never matches a row and every
// advert looks eligible for ever -- which reads as a rotation that ignores its
// own cooldown.
func TestAdRotationLiveIDsAreRowIDs(t *testing.T) {
	now := time.Unix(1788840000, 0)
	var marked string
	r := NewAdRotation(nil)
	r.Source = func(context.Context) ([]Ad, error) {
		return []Ad{{ID: "42", Brand: "Stillwater",
			Script: "Stillwater. One mug, still, and it always has been."}}, nil
	}
	r.Aired = func(_ context.Context, id string, _ time.Time) error {
		marked = id
		return nil
	}

	got, err := r.Next(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.MarkAired(context.Background(), got.ID, now); err != nil {
		t.Fatal(err)
	}
	if marked != "42" {
		t.Errorf("marked %q, want the row id the source gave", marked)
	}
}
