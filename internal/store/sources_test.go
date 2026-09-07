// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSourcesCreateListDelete(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()

	folder, err := s.CreateSource(ctx, Source{
		Kind: SourceFolder, Locator: "/music", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSource(ctx, Source{
		Kind: SourceSubsonic, Locator: "http://nas:4533",
		Username: "andrew", Password: "hunter2", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListSources returned %d, want 2", len(list))
	}
	if list[0].Kind != SourceFolder || list[0].Locator != "/music" {
		t.Errorf("folder source did not round-trip: %+v", list[0])
	}
	// A folder has no credentials, and NULL must come back as "" rather than
	// as a scan error.
	if list[0].Username != "" || list[0].Password != "" {
		t.Errorf("folder source carries credentials: %+v", list[0])
	}
	if list[1].Username != "andrew" || list[1].Password != "hunter2" {
		t.Errorf("subsonic credentials did not round-trip: %+v", list[1])
	}
	if list[1].CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	// GetSource is what play time uses to turn a track locator back into a
	// signed stream URL, so it has to return the credentials.
	one, err := s.GetSource(ctx, list[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.Locator != "http://nas:4533" || one.Password != "hunter2" {
		t.Errorf("GetSource returned %+v", one)
	}
	if _, err := s.GetSource(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSource of a missing id: %v, want ErrNotFound", err)
	}

	if err := s.DeleteSource(ctx, folder); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListSources(ctx); len(list) != 1 {
		t.Errorf("after delete: %d sources, want 1", len(list))
	}
	if err := s.DeleteSource(ctx, folder); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v, want ErrNotFound", err)
	}
}

// TestSourcesPasswordNeverSerialises: the sources list is what the admin
// console renders, and a Subsonic password cannot be hashed because every
// request has to replay it. The one thing that keeps it out of a response body
// is that it never leaves the struct.
func TestSourcesPasswordNeverSerialises(t *testing.T) {
	raw, err := json.Marshal(Source{
		Kind: SourceSubsonic, Locator: "http://nas:4533",
		Username: "andrew", Password: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Errorf("the password reached JSON: %s", raw)
	}
	if !strings.Contains(string(raw), "andrew") {
		t.Errorf("the username should still be shown: %s", raw)
	}
}

// TestSourcesKindCheck: a typo must be rejected by the DATABASE. A source with
// a kind nothing can scan is a library that silently never appears, and the
// operator's only symptom is an empty dial.
func TestSourcesKindCheck(t *testing.T) {
	s := stationStore(t)
	if _, err := s.CreateSource(context.Background(), Source{
		Kind: "spotify", Locator: "spotify:playlist:x", Enabled: true}); err == nil {
		t.Error("CreateSource accepted kind spotify")
	}
}

func TestSourcesEnableDisable(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	id, err := s.CreateSource(ctx, Source{Kind: SourceFolder, Locator: "/music", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSource(ctx, Source{
		Kind: SourceFolder, Locator: "/other", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetSourceEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	on, err := s.ListEnabledSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(on) != 1 || on[0].Locator != "/other" {
		t.Errorf("ListEnabledSources = %+v, want only /other", on)
	}
	// Disabling is not deleting: the operator has to be able to find it again,
	// and its tracks stay in the library rather than being rescanned away.
	all, err := s.ListSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListSources returned %d, want both", len(all))
	}
	if all[0].Enabled {
		t.Error("the disabled source still reads as enabled")
	}

	if err := s.SetSourceEnabled(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if on, _ := s.ListEnabledSources(ctx); len(on) != 2 {
		t.Errorf("re-enabling did not bring it back: %+v", on)
	}
	if err := s.SetSourceEnabled(ctx, 9999, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("disabling a missing source: %v, want ErrNotFound", err)
	}
}

// TestSourcesErrorsSurface covers what a working database never does.
func TestSourcesErrorsSurface(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	id, err := s.CreateSource(ctx, Source{Kind: SourceFolder, Locator: "/music", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.DB().Exec(`UPDATE sources SET enabled = 'yes'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListSources(ctx); err == nil {
		t.Error("ListSources accepted a corrupt enabled flag")
	}
	if _, err := s.DB().Exec(`UPDATE sources SET enabled = 1`); err != nil {
		t.Fatal(err)
	}

	readOnly(t, s)
	if _, err := s.CreateSource(ctx, Source{Kind: SourceFolder, Locator: "/x"}); err == nil {
		t.Error("CreateSource succeeded against a read-only database")
	}
	if err := s.SetSourceEnabled(ctx, id, false); err == nil {
		t.Error("SetSourceEnabled succeeded against a read-only database")
	}
	if err := s.DeleteSource(ctx, id); err == nil {
		t.Error("DeleteSource succeeded against a read-only database")
	}
}

func TestSourcesClosedStore(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListSources(ctx); err == nil {
		t.Error("ListSources succeeded on a closed store")
	}
	if _, err := s.ListEnabledSources(ctx); err == nil {
		t.Error("ListEnabledSources succeeded on a closed store")
	}
	if _, err := s.GetSource(ctx, 1); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("GetSource on a closed store: %v, want a real error", err)
	}
}

// TestSourcesRetireTracks: removing a source must not remove its music. Those
// rows carry dossiers worth minutes of model time each and are referenced by
// said_lines; a source added back should find them waiting rather than have to
// enrich the whole library again.
func TestSourcesRetireTracks(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()

	keep, err := s.CreateSource(ctx, Source{Kind: SourceFolder, Locator: "/a", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	going, err := s.CreateSource(ctx, Source{Kind: SourceFolder, Locator: "/b", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, src := range []int64{keep, going, going} {
		if _, err := s.DB().Exec(
			`INSERT INTO tracks (id, path, playable, source_id) VALUES (?, ?, 1, ?)`,
			i+1, fmt.Sprintf("/m/%d.mp3", i+1), src); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.RetireSourceTracks(ctx, going)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("retired %d tracks, want 2", n)
	}

	var rows, marked int
	if err := s.DB().QueryRow(`SELECT count(*) FROM tracks`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM tracks WHERE missing_at IS NOT NULL`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if rows != 3 || marked != 2 {
		t.Errorf("%d rows with %d marked, want 3 and 2", rows, marked)
	}
	// The other source is untouched.
	var other any
	if err := s.DB().QueryRow(`SELECT missing_at FROM tracks WHERE id = 1`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if other != nil {
		t.Errorf("another source's track was retired: %v", other)
	}

	// Retiring twice does nothing more, and a source with no tracks is a
	// no-op rather than a write.
	if n, err := s.RetireSourceTracks(ctx, going); err != nil || n != 0 {
		t.Errorf("retiring twice = %d, %v", n, err)
	}
	if n, err := s.RetireSourceTracks(ctx, 9999); err != nil || n != 0 {
		t.Errorf("retiring a source with no tracks = %d, %v", n, err)
	}
}

func TestSourcesRetireSurfacesFailures(t *testing.T) {
	s := stationStore(t)
	ctx := context.Background()
	id, err := s.CreateSource(ctx, Source{Kind: SourceFolder, Locator: "/a", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO tracks (id, path, playable, source_id) VALUES (1, '/m/1.mp3', 1, ?)`,
		id); err != nil {
		t.Fatal(err)
	}

	readOnly(t, s)
	if _, err := s.RetireSourceTracks(ctx, id); err == nil {
		t.Error("retiring reported success against a read-only database")
	}

	closed := stationStore(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.RetireSourceTracks(ctx, 1); err == nil {
		t.Error("retiring succeeded on a closed store")
	}
}
