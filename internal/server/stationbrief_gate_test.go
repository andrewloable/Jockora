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

// Jockora-g1t.14. The offline half of the tempo-and-length gate. What it proves
// is what PLAYS, through station.Regenerate, not that four columns round-trip:
// reading the columns back proves storage, and storage was never the thing that
// was broken.

// rangeLibrary seeds rock tracks whose bpm and duration are known, so a brief's
// ranges can be checked against the music itself.
//
// ITS OWN TRACKS, at ids 10 and up. The first version measured gateLibrary's
// four -- and one of those is AMBIENT, so the station excluded it for its genre
// while the test believed the length range had done it. A fixture where the
// thing under test is indistinguishable from what it replaced proves nothing.
func rangeLibrary(t *testing.T, reply string) (*Server, *store.Store) {
	t.Helper()
	s, st := gateLibrary(t, reply)
	ctx := context.Background()
	for _, tr := range []struct {
		id       int64
		bpm, dur any
	}{
		{10, 170.0, 180.0}, // quick and short: inside both
		{11, 170.0, 900.0}, // quick but long
		{12, 60.0, 180.0},  // slow but short
		{13, 60.0, 900.0},  // outside both
		// NOTHING MEASURED, which is what most of a real library looks like
		// until the analyser catches up.
		{14, nil, nil},
		{15, nil, nil},
	} {
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO tracks (id, path, playable, bpm, duration_s) VALUES (?, ?, 1, ?, ?)`,
			tr.id, fmt.Sprintf("/m/range-%d.mp3", tr.id), tr.bpm, tr.dur); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{
			"station_tags": []string{"rock"}, "mood": []string{"raw"}})
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
			tr.id, string(raw), enrich.ConfidenceHigh); err != nil {
			t.Fatal(err)
		}
	}
	return s, st
}

// rockInRangeLibrary is every rock track the fixture holds: gateLibrary's three
// (its fourth is ambient) plus the six above.
const rockInRangeLibrary = 9

// playlistOf is what the station actually airs.
func playlistOf(t *testing.T, st *store.Store, id int64) []int64 {
	t.Helper()
	ids, err := st.StationTrackIDs(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestBriefGateRangesEndToEnd(t *testing.T) {
	s, st := rangeLibrary(t, `{"name":"Quick And Short","genres":["rock"],"moods":[],
		"year_min":0,"year_max":0,"tempo_min":150,"tempo_max":200,
		"duration_min_s":0,"duration_max_s":300}`)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`{"brief":"something to run to, nothing over five minutes"}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("derive = %d: %s", rec.Code, rec.Body)
	}
	var d map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}

	// THE CONSOLE SENDS BACK WHAT IT WAS SHOWN. Saving what the derive
	// answered is the whole preview-then-confirm contract.
	body, _ := json.Marshal(map[string]any{
		"name": d["name"], "genres": d["genres"], "moods": d["moods"],
		"brief":     "something to run to, nothing over five minutes",
		"tempo_min": d["tempo_min"], "tempo_max": d["tempo_max"],
		"duration_min_s": d["duration_min_s"], "duration_max_s": d["duration_max_s"],
	})
	made := as(t, s, http.MethodPost, "/admin/stations", string(body), admin)
	if made.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", made.Code, made.Body)
	}
	var out struct{ ID int64 }
	if err := json.Unmarshal(made.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	// WHAT PLAYS, through the real Regenerate the create already ran.
	got := playlistOf(t, st, out.ID)
	has := map[int64]bool{}
	for _, id := range got {
		has[id] = true
	}
	if !has[10] {
		t.Error("the quick short track is not in the station the brief asked for")
	}
	for _, id := range []int64{11, 12, 13} {
		if has[id] {
			t.Errorf("track %d is outside the ranges and is airing anyway: %v", id, got)
		}
	}
	// AND THE UNMEASURED ONES ARE STILL IN. Excluding them is the failure that
	// empties a fresh library's dial.
	for _, id := range []int64{14, 15} {
		if !has[id] {
			t.Errorf("track %d has nothing measured and was excluded", id)
		}
	}
	// AND THE PREVIEWED COUNT IS THE COUNT THEY GOT. A number that changes
	// between the preview and the save is worse than no number.
	if n, ok := d["tracks"].(float64); !ok || int(n) != len(got) {
		t.Errorf("previewed %v tracks and got %d", d["tracks"], len(got))
	}
}

// TestBriefGateRangesSilentBriefIsUnbounded: the failure that would be
// invisible. A guessed bound quietly halves a station and nothing on screen
// explains it -- which is exactly what the year rule did live before it was
// told to leave dates alone.
func TestBriefGateRangesSilentBriefIsUnbounded(t *testing.T) {
	s, st := rangeLibrary(t, `{"name":"Angry Guitars","genres":["rock"],"moods":[],
		"year_min":0,"year_max":0,"tempo_min":0,"tempo_max":0,
		"duration_min_s":0,"duration_max_s":0}`)
	admin := adminCookie(t, s)

	body, _ := json.Marshal(map[string]any{
		"name": "Angry Guitars", "genres": []string{"rock"}, "brief": "angry guitars"})
	made := as(t, s, http.MethodPost, "/admin/stations", string(body), admin)
	if made.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", made.Code, made.Body)
	}
	var out struct{ ID int64 }
	if err := json.Unmarshal(made.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// EVERY rock track, measured or not. Six of them.
	if got := playlistOf(t, st, out.ID); len(got) != rockInRangeLibrary {
		t.Errorf("a brief that mentioned neither pace nor length selected %d of %d: %v",
			len(got), rockInRangeLibrary, got)
	}
}

// TestBriefGateRangesNullNeverExcludes: bpm comes from the analyser, a slow
// background pass, so on a fresh library almost nothing has one. Dropping the
// unmeasured would empty the dial at exactly the moment an operator is setting
// it up.
func TestBriefGateRangesNullNeverExcludes(t *testing.T) {
	s, st := rangeLibrary(t, "")
	admin := adminCookie(t, s)

	body, _ := json.Marshal(map[string]any{
		"name": "Quick", "genres": []string{"rock"},
		"tempo_min": 150, "tempo_max": 200, "duration_min_s": 60, "duration_max_s": 300})
	made := as(t, s, http.MethodPost, "/admin/stations", string(body), admin)
	if made.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", made.Code, made.Body)
	}
	var out struct{ ID int64 }
	if err := json.Unmarshal(made.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	has := map[int64]bool{}
	for _, id := range playlistOf(t, st, out.ID) {
		has[id] = true
	}
	// Tracks 14 and 15 have NULL bpm and NULL duration_s and must both be in.
	for _, id := range []int64{14, 15} {
		if !has[id] {
			t.Errorf("track %d has nothing measured and was excluded; on a fresh library "+
				"that is the whole dial", id)
		}
	}
	// And the measured ones are still filtered.
	if has[13] {
		t.Error("the slow long track is airing on a quick short station")
	}
}
