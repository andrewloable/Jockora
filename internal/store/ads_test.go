// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Jockora-iyw.1. The ads table has existed since migration 1 and HAS NEVER HAD
// A READER OR A WRITER -- grep the repository and there is no INSERT INTO ads
// and no SELECT FROM ads anywhere. Adverts come from JockPack TOML loaded at
// station start. This is the store layer that never existed.

// adsSchemaVersion is the schema BEFORE this migration, written as the number
// it is. migrate_v2_test.go records what CurrentSchemaVersion-1 cost the last
// time: it silently stopped meaning what it meant the moment another migration
// landed, and the columns under test were simply absent.
const adsSchemaVersion = 10

func adFixture() Ad {
	return Ad{
		Brand:    "Stillwater Ceramics",
		Brief:    "local pottery, makes exactly one mug, deadpan about it",
		Delivery: "flat, unhurried, no exclamation marks",
		Script:   "Stillwater Ceramics have been making one mug for eleven years. One mug, still.",
	}
}

func TestAdStoreRoundTrip(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "ads.db"))
	ctx := context.Background()

	want := adFixture()
	id, err := s.CreateAd(ctx, want)
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	got, err := s.GetAd(ctx, id)
	if err != nil {
		t.Fatalf("GetAd: %v", err)
	}
	if got.Brand != want.Brand || got.Brief != want.Brief ||
		got.Delivery != want.Delivery || got.Script != want.Script {
		t.Errorf("round trip lost a field: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt was not set")
	}
	// NEVER AIRED is the zero time, not the epoch: an advert written a minute
	// ago and one aired in 1970 must not sort together in the rotation.
	if !got.LastAiredAt.IsZero() {
		t.Errorf("LastAiredAt = %v on a new advert, want the zero time", got.LastAiredAt)
	}
}

// TestAdStoreList: the console shows this list on a poll, so an unordered one
// reshuffles under the operator's cursor every few seconds.
func TestAdStoreList(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "list.db"))
	ctx := context.Background()

	for _, brand := range []string{"First", "Second", "Third"} {
		ad := adFixture()
		ad.Brand = brand
		if _, err := s.CreateAd(ctx, ad); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListAds(ctx)
	if err != nil {
		t.Fatalf("ListAds: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ListAds returned %d, want 3", len(list))
	}
	// Newest first: the advert an operator just wrote is the one they are
	// looking for.
	if list[0].Brand != "Third" || list[2].Brand != "First" {
		t.Errorf("order = %q, %q, %q; want newest first",
			list[0].Brand, list[1].Brand, list[2].Brand)
	}
	// Stable across calls, which an unordered query is not.
	again, err := s.ListAds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range list {
		if again[i].ID != list[i].ID {
			t.Errorf("the order changed between two calls at %d", i)
		}
	}
}

func TestAdStoreUpdate(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "update.db"))
	ctx := context.Background()

	id, err := s.CreateAd(ctx, adFixture())
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	// A DISTINCTLY OLD created_at. Compared against a value written in the same
	// second, a mutation that rewrites it to unixepoch() is indistinguishable
	// from one that leaves it alone -- which is exactly what happened.
	const wasWritten = 1700000000
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE ads SET created_at = ? WHERE id = ?`, wasWritten, id); err != nil {
		t.Fatal(err)
	}
	if before, err = s.GetAd(ctx, id); err != nil {
		t.Fatal(err)
	}

	before.Brand = "Stillwater Pottery"
	before.Brief = "they finally made a second mug"
	before.Delivery = "faintly scandalised"
	before.Script = "Stillwater Pottery. Two mugs now. We are as surprised as you are."
	if err := s.UpdateAd(ctx, before); err != nil {
		t.Fatalf("UpdateAd: %v", err)
	}
	after, err := s.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Brand != before.Brand || after.Brief != before.Brief ||
		after.Delivery != before.Delivery || after.Script != before.Script {
		t.Errorf("update did not stick: %+v", after)
	}
	// CREATED_AT IS NOT AN EDITABLE FIELD. Rewriting it on every save would
	// reshuffle the newest-first list every time somebody fixed a typo.
	if after.CreatedAt.Unix() != wasWritten {
		t.Errorf("created_at moved from %v to %v", before.CreatedAt, after.CreatedAt)
	}
	if err := s.UpdateAd(ctx, Ad{ID: 9999}); !errors.Is(err, ErrNotFound) {
		t.Errorf("updating a missing advert = %v, want ErrNotFound", err)
	}
}

func TestAdStoreDelete(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "delete.db"))
	ctx := context.Background()

	id, err := s.CreateAd(ctx, adFixture())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAd(ctx, id); err != nil {
		t.Fatalf("DeleteAd: %v", err)
	}
	if _, err := s.GetAd(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAd after delete = %v, want ErrNotFound", err)
	}
	// Silently succeeding on a missing row is how a console reports a delete
	// that deleted nothing.
	if err := s.DeleteAd(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting again = %v, want ErrNotFound", err)
	}
}

func TestAdStoreMissing(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "missing.db"))
	if _, err := s.GetAd(context.Background(), 404); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetAd on a missing id = %v, want ErrNotFound", err)
	}
}

// TestAdStoreMarkAired: SEPARATE FROM UpdateAd on purpose. It is called from
// the airing path on a station goroutine while an operator may be editing the
// same row in the console, and an airing must never write back a script the
// operator has just changed.
func TestAdStoreMarkAired(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "aired.db"))
	ctx := context.Background()

	id, err := s.CreateAd(ctx, adFixture())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1788840000, 0)
	if err := s.MarkAdAired(ctx, id, at); err != nil {
		t.Fatalf("MarkAdAired: %v", err)
	}
	got, err := s.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastAiredAt.Equal(at) {
		t.Errorf("LastAiredAt = %v, want %v", got.LastAiredAt, at)
	}
	// NOTHING ELSE MOVED, which is the whole reason this is not UpdateAd.
	if got.Script != adFixture().Script || got.Brand != adFixture().Brand {
		t.Errorf("airing rewrote the advert: %+v", got)
	}
	if err := s.MarkAdAired(ctx, 9999, at); !errors.Is(err, ErrNotFound) {
		t.Errorf("marking a missing advert = %v, want ErrNotFound", err)
	}
}

// TestAdStoreMigrates: rows that predate this migration survive it with NULL in
// the four new columns, which is what a JockPack advert imported later becomes.
func TestAdStoreMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	old := openAt(t, path)
	ctx := context.Background()

	if _, err := old.DB().ExecContext(ctx,
		`INSERT INTO ads (text, last_aired_at) VALUES (?, ?)`,
		"An advert from before there was a console.", 1788000000); err != nil {
		t.Fatal(err)
	}
	// Undo everything above version 10 from the ONE list every fixture in this
	// package shares.
	rewindTo(t, old, adsSchemaVersion)
	old.Close() //nolint:errcheck // reopening to migrate

	up := openAt(t, path)
	list, err := up.ListAds(ctx)
	if err != nil {
		t.Fatalf("ListAds after migrating: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("%d ads survived, want 1", len(list))
	}
	if list[0].Script != "An advert from before there was a console." {
		t.Errorf("the script was lost: %+v", list[0])
	}
	if list[0].Brand != "" || list[0].Brief != "" || list[0].Delivery != "" {
		t.Errorf("the migration invented values: %+v", list[0])
	}
	if !list[0].CreatedAt.IsZero() {
		t.Errorf("CreatedAt = %v on a row that predates the column", list[0].CreatedAt)
	}
	// And the four columns really are NULL rather than empty strings.
	var brand sql.NullString
	if err := up.DB().QueryRowContext(ctx,
		`SELECT brand FROM ads WHERE id = ?`, list[0].ID).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if brand.Valid {
		t.Errorf("brand = %q, want NULL", brand.String)
	}
}

// TestAdStoreReportsADatabaseThatWillNotAnswer: every method says so rather
// than reporting success, which is the difference between an operator seeing
// "could not save" and an operator seeing a saved advert that is not there.
//
// The same shape jocks_test.go and sources_test.go use: read-only for the
// writes, then closed for everything.
func TestAdStoreReportsADatabaseThatWillNotAnswer(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "broken.db"))
	ctx := context.Background()

	id, err := s.CreateAd(ctx, adFixture())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAd(ctx, adFixture()); err == nil {
		t.Error("CreateAd reported success against a read-only database")
	}
	if err := s.UpdateAd(ctx, Ad{ID: id, Script: "x"}); err == nil {
		t.Error("UpdateAd reported success against a read-only database")
	}
	if err := s.MarkAdAired(ctx, id, time.Now()); err == nil {
		t.Error("MarkAdAired reported success against a read-only database")
	}
	if err := s.DeleteAd(ctx, id); err == nil {
		t.Error("DeleteAd reported success against a read-only database")
	}
	if err := s.SetAdEnabled(ctx, id, false); err == nil {
		t.Error("SetAdEnabled reported success against a read-only database")
	}
	if _, err := s.DB().Exec(`PRAGMA query_only = 0`); err != nil {
		t.Fatal(err)
	}

	// A row this build cannot scan. text is NOT NULL in the schema, so the
	// only way to reach a bad scan is a hand-edited database -- which is
	// exactly when an operator most needs an error rather than a blank advert.
	if _, err := s.DB().Exec(`UPDATE ads SET last_aired_at = 'not a number' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAd(ctx, id); err == nil {
		t.Error("GetAd returned an advert whose last_aired_at is not a time")
	}
	if _, err := s.ListAds(ctx); err == nil {
		t.Error("ListAds silently dropped a row it could not scan")
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAd(ctx, id); err == nil {
		t.Error("GetAd reported success against a closed database")
	}
	if _, err := s.ListAds(ctx); err == nil {
		t.Error("ListAds reported success against a closed database")
	}
}

// TestAdStoreNeverAiredIsNotTheEpoch: a row carrying a literal 0 -- which is
// what a JockPack import and any pre-console advert leave behind -- must read
// back as the ZERO TIME, not as midnight in 1970.
//
// Statement coverage cannot see this: the branch executes either way, and only
// the sub-condition differs. It took a mutation to find, and the mutation is
// the reason this test exists.
func TestAdStoreNeverAiredIsNotTheEpoch(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "epoch.db"))
	ctx := context.Background()

	id, err := s.CreateAd(ctx, adFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range []any{int64(0), nil} {
		if _, err := s.DB().ExecContext(ctx,
			`UPDATE ads SET last_aired_at = ? WHERE id = ?`, stored, id); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetAd(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !got.LastAiredAt.IsZero() {
			t.Errorf("last_aired_at %v read back as %v, want the zero time",
				stored, got.LastAiredAt)
		}
	}

	// The same for created_at, which every advert older than migration 11 has.
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE ads SET created_at = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.IsZero() {
		t.Errorf("created_at 0 read back as %v, want the zero time", got.CreatedAt)
	}
}
