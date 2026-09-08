// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// THE GATE for Jockora-g1t. Tasks 1 to 6 are the feature; this proves them
// together. Nothing under test is stubbed except the model itself -- a gate
// that mocks the store or the filter proves nothing the unit tests did not.

// briefGate stands a real store, a real filter and a real server behind one
// scripted model answer.
type briefGate struct {
	llm *enrich.Switchable
	st  *store.Store
}

func (g *briefGate) ResolveStationBrief(ctx context.Context, brief string) (enrich.StationParams, error) {
	if !g.llm.Configured() {
		return enrich.StationParams{}, enrich.ErrNoModel
	}
	return enrich.DeriveStationParams(ctx, g.llm, brief)
}

func (g *briefGate) CountMatching(ctx context.Context, f station.Filter) (int, error) {
	ids, err := f.TrackIDs(ctx, g.st)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

type scriptedModel struct{ reply string }

func (s scriptedModel) Complete(context.Context, enrich.CompletionRequest) (enrich.Completion, error) {
	return enrich.Completion{Content: s.reply, StopType: "eos"}, nil
}

// gateLibrary seeds tracks whose genre, mood and year are known, so the
// playlist a brief produces can be checked against the music itself.
func gateLibrary(t *testing.T, reply string) (*Server, *store.Store) {
	t.Helper()
	s, _, _ := authServer(t)

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	for i, tr := range []struct {
		tags []string
		mood string
		year int
	}{
		{[]string{"rock"}, "raw", 1985},     // 1: in the decade
		{[]string{"rock"}, "raw", 1988},     // 2: in the decade
		{[]string{"rock"}, "raw", 1995},     // 3: too late
		{[]string{"ambient"}, "calm", 1986}, // 4: wrong genre
	} {
		id := int64(i + 1)
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO tracks (id, path, playable, year) VALUES (?, ?, 1, ?)`,
			id, fmt.Sprintf("/m/%d.mp3", id), tr.year); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"station_tags": tr.tags, "mood": []string{tr.mood}})
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
			id, string(raw), enrich.ConfidenceHigh); err != nil {
			t.Fatal(err)
		}
	}

	var llm *enrich.Switchable
	if reply == "" {
		llm = enrich.NewSwitchable(nil)
	} else {
		llm = enrich.NewSwitchable(scriptedModel{reply: reply})
	}
	s.SetStations(st, func(ctx context.Context, sid int64) (station.Diff, error) {
		return station.Regenerate(ctx, st, sid)
	}, &stopLog{})
	s.SetStationBriefs(&briefGate{llm: llm, st: st})
	return s, st
}

// TestBriefGateEndToEnd: a brief becomes a station with music in it, and the
// number the operator was shown is the number they got.
func TestBriefGateEndToEnd(t *testing.T) {
	s, st := gateLibrary(t, `{"name":"Eighties Rock","genres":["rock"],"moods":["raw"],
		"year_min":1980,"year_max":1989}`)
	admin := adminCookie(t, s)
	const brief = "Loud eighties rock, nothing after the decade ended."

	rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`{"brief":`+quote(brief)+`}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("derive = %d: %s", rec.Code, rec.Body)
	}
	var preview struct {
		Name    string   `json:"name"`
		Genres  []string `json:"genres"`
		Moods   []string `json:"moods"`
		YearMin int      `json:"year_min"`
		YearMax int      `json:"year_max"`
		Tracks  int      `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	// Two of the four tracks: rock, raw, inside the decade.
	if preview.Tracks != 2 {
		t.Fatalf("the preview counted %d tracks, want 2", preview.Tracks)
	}

	// SAVE WHAT WAS SHOWN, exactly as the console does.
	body, _ := json.Marshal(map[string]any{
		"name": preview.Name, "genres": preview.Genres, "moods": preview.Moods,
		"brief": brief, "year_min": preview.YearMin, "year_max": preview.YearMax,
	})
	made := as(t, s, http.MethodPost, "/admin/stations", string(body), admin)
	if made.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", made.Code, made.Body)
	}
	var created struct {
		ID     int64 `json:"id"`
		Tracks int   `json:"tracks"`
	}
	if err := json.Unmarshal(made.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	// THE NUMBER THE OPERATOR WAS SHOWN IS THE NUMBER THEY GOT. If these ever
	// diverge, the preview is a promise rather than an answer.
	if created.Tracks != preview.Tracks {
		t.Errorf("the preview said %d tracks and the station got %d",
			preview.Tracks, created.Tracks)
	}

	// And the playlist holds exactly the tracks those parameters select.
	ids, err := st.StationTrackIDs(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Errorf("playlist = %v, want the two eighties rock tracks", ids)
	}

	// The station carries the brief and the parameters, so an operator can
	// rewrite the sentence rather than reverse-engineer the boxes.
	got := stationNamed(t, s, "Eighties Rock")
	if got.Brief != brief || got.YearMin != 1980 || got.YearMax != 1989 {
		t.Errorf("stored = %+v", got)
	}
}

// TestBriefGateNoModel: A LIBRARY WITH NO LLM IS A WORKING SHUFFLE, and this is
// the test that says so. The derive refuses with words; making a station from
// tags alone still works and still gets its playlist.
func TestBriefGateNoModel(t *testing.T) {
	s, st := gateLibrary(t, "")
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`{"brief":"Loud eighties rock."}`, admin)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("derive with no model = %d, want 503: %s", rec.Code, rec.Body)
	}
	var e struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("the refusal is not JSON, so the console drops it: %q", rec.Body)
	}
	if !strings.Contains(strings.ToLower(e.Error), "hand") {
		t.Errorf("the refusal does not name the way out: %q", e.Error)
	}

	// THE WAY OUT ACTUALLY WORKS.
	made := as(t, s, http.MethodPost, "/admin/stations",
		`{"name":"BY HAND","genres":["rock"],"moods":["raw"]}`, admin)
	if made.Code != http.StatusCreated {
		t.Fatalf("create by hand = %d: %s", made.Code, made.Body)
	}
	var created struct {
		ID     int64 `json:"id"`
		Tracks int   `json:"tracks"`
	}
	if err := json.Unmarshal(made.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Tracks != 3 {
		t.Errorf("the hand-made station got %d tracks, want the 3 rock ones", created.Tracks)
	}
	ids, err := st.StationTrackIDs(context.Background(), created.ID)
	if err != nil || len(ids) != 3 {
		t.Errorf("playlist = %v (%v)", ids, err)
	}
}

// quote is json.Marshal for one string, for building request bodies inline.
func quote(s string) string {
	b, _ := json.Marshal(s) //nolint:errcheck // a string always marshals
	return string(b)
}
