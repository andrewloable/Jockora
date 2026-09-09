// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/station"
)

// Jockora-g1t.5. The operator types a description, presses a button, and sees
// what it would become AND how much music it holds -- before anything is saved.

type fakeBriefs struct {
	params   enrich.StationParams
	err      error
	count    int
	countErr error
	block    bool
	got      string
	calls    atomic.Int64
	countFor func(station.Filter) int
}

func (f *fakeBriefs) ResolveStationBrief(ctx context.Context, brief string) (enrich.StationParams, error) {
	f.calls.Add(1)
	f.got = brief
	if f.block {
		// A model that stopped answering: it returns only when the deadline
		// above it fires, which is what the handler must impose.
		<-ctx.Done()
		return enrich.StationParams{}, ctx.Err()
	}
	return f.params, f.err
}

func (f *fakeBriefs) CountMatching(_ context.Context, filter station.Filter) (int, error) {
	// countFor answers per FILTER, which is what the empty-station diagnosis
	// needs: it drops one bound at a time and asks again.
	if f.countFor != nil {
		return f.countFor(filter), f.countErr
	}
	return f.count, f.countErr
}

func briefServer(t *testing.T) (*Server, *fakeBriefs) {
	t.Helper()
	s, _, _ := stationsServer(t, 3, 2)
	b := &fakeBriefs{
		params: enrich.StationParams{
			Name: "Night Rock", Genres: []string{"rock"}, Moods: []string{"nocturnal"},
			YearMin: 1975, YearMax: 2005,
		},
		count: 412,
	}
	s.SetStationBriefs(b)
	return s, b
}

func TestStationBriefAPIDerive(t *testing.T) {
	s, b := briefServer(t)
	rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`{"brief":"late-night rock for driving, nothing after 2005"}`, adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Name    string   `json:"name"`
		Genres  []string `json:"genres"`
		Moods   []string `json:"moods"`
		YearMin int      `json:"year_min"`
		YearMax int      `json:"year_max"`
		Tracks  int      `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	if got.Name != "Night Rock" || len(got.Genres) != 1 || got.YearMax != 2005 {
		t.Errorf("params = %+v", got)
	}
	// THE COUNT IS THE POINT. Four tags and no number does not answer the
	// question the operator actually has, which is whether it has any music.
	if got.Tracks != 412 {
		t.Errorf("tracks = %d, want the count", got.Tracks)
	}
	if b.got != "late-night rock for driving, nothing after 2005" {
		t.Errorf("the model was asked %q", b.got)
	}
}

// TestStationBriefAPIDeriveSavesNothing: it is a PREVIEW. An operator pressing
// it twice must not end up with two stations.
func TestStationBriefAPIDeriveSavesNothing(t *testing.T) {
	s, _ := briefServer(t)
	before := listStationCount(t, s)
	for i := 0; i < 2; i++ {
		if rec := as(t, s, http.MethodPost, "/admin/stations/derive",
			`{"brief":"anything"}`, adminCookie(t, s)); rec.Code != http.StatusOK {
			t.Fatalf("= %d: %s", rec.Code, rec.Body)
		}
	}
	if after := listStationCount(t, s); after != before {
		t.Errorf("%d stations after two previews, want the %d there were", after, before)
	}
}

// TestStationBriefAPIDeriveNoModel: a bare 503 sends the operator to the logs
// for a thing the console can simply say, and there are TWO ways out.
func TestStationBriefAPIDeriveNoModel(t *testing.T) {
	s, b := briefServer(t)
	b.err = enrich.ErrNoModel

	rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`{"brief":"late-night rock"}`, adminCookie(t, s))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("= %d, want 503: %s", rec.Code, rec.Body)
	}
	body := strings.ToLower(rec.Body.String())
	if !strings.Contains(body, "model") {
		t.Errorf("the refusal does not mention choosing a model: %s", rec.Body)
	}
	if !strings.Contains(body, "hand") && !strings.Contains(body, "yourself") {
		t.Errorf("the refusal does not offer the other way out: %s", rec.Body)
	}
}

func TestStationBriefAPIDeriveFails(t *testing.T) {
	s, b := briefServer(t)
	b.err = errors.New("rejected the API key")

	rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`{"brief":"late-night rock"}`, adminCookie(t, s))
	// 502, not 503: the model is configured and it answered badly, which is a
	// different thing to fix from having no model at all.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("= %d, want 502: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "rejected the API key") {
		t.Errorf("the model's own reason was dropped: %s", rec.Body)
	}
}

func TestStationBriefAPIDeriveBlank(t *testing.T) {
	s, b := briefServer(t)
	for _, body := range []string{`{"brief":""}`, `{"brief":"   "}`, `{}`} {
		rec := as(t, s, http.MethodPost, "/admin/stations/derive", body, adminCookie(t, s))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", body, rec.Code, rec.Body)
		}
		var e struct{ Field, Error string }
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Field != "brief" {
			t.Errorf("%s answered %q, want a field error on brief", body, rec.Body)
		}
	}
	// WITHOUT CALLING THE MODEL: there is nothing to derive from.
	if n := b.calls.Load(); n != 0 {
		t.Errorf("the model was called %d times for a blank brief", n)
	}
}

func TestStationBriefAPIDeriveUnwired(t *testing.T) {
	s, _, _ := stationsServer(t, 1, 1)
	rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`{"brief":"anything"}`, adminCookie(t, s))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("with no briefs wired = %d, want 503", rec.Code)
	}
}

// TestStationBriefAPICreateDoesNotDerive: the console sends back the parameters
// it was SHOWN. Re-deriving would produce a different station from the one the
// operator approved -- same brief, different answer, and they would never know.
func TestStationBriefAPICreateDoesNotDerive(t *testing.T) {
	s, b := briefServer(t)
	rec := as(t, s, http.MethodPost, "/admin/stations",
		`{"name":"Night Rock","genres":["rock"],"moods":["nocturnal"],
		  "brief":"late-night rock","year_min":1975,"year_max":2005}`, adminCookie(t, s))
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if n := b.calls.Load(); n != 0 {
		t.Errorf("creating called the model %d times; the operator already approved these", n)
	}
}

func TestStationBriefAPICreate(t *testing.T) {
	s, _ := briefServer(t)
	rec := as(t, s, http.MethodPost, "/admin/stations",
		`{"name":"Night Rock","genres":["rock"],"moods":["raw"],
		  "brief":"late-night rock for driving","year_min":1975,"year_max":2005}`,
		adminCookie(t, s))
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var made struct {
		ID     int64 `json:"id"`
		Tracks int   `json:"tracks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	// STILL REGENERATED, so the operator sees a count and not a promise. Create
	// answers with the id and that count, which is the contract every existing
	// caller already reads; the stored station is checked through the list.
	if made.Tracks == 0 {
		t.Error("no playlist was built, so the operator got a promise rather than a count")
	}

	got := stationNamed(t, s, "Night Rock")
	if got.Brief != "late-night rock for driving" {
		t.Errorf("brief = %q; it is the record of what the operator asked for", got.Brief)
	}
	if got.YearMin != 1975 || got.YearMax != 2005 {
		t.Errorf("years = %d..%d", got.YearMin, got.YearMax)
	}
}

func TestStationBriefAPIUpdate(t *testing.T) {
	s, _ := briefServer(t)
	created := as(t, s, http.MethodPost, "/admin/stations",
		`{"name":"N","genres":["rock"],"moods":["raw"],"brief":"first try"}`, adminCookie(t, s))
	var made struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &made); err != nil || made.ID == 0 {
		t.Fatalf("create: %q (%v)", created.Body, err)
	}

	rec := as(t, s, http.MethodPut, fmt.Sprintf("/admin/stations/%d", made.ID),
		`{"name":"N","genres":["rock"],"moods":["raw"],
		  "brief":"second try","year_min":1990,"year_max":1999}`, adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	// CHANGING THE YEARS CHANGES WHAT THE STATION IS, so update regenerates and
	// answers with the diff -- the same rule and the same shape a genre change
	// already follows.
	var diff struct {
		Added   int `json:"added"`
		Removed int `json:"removed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &diff); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}

	got := stationNamed(t, s, "N")
	if got.Brief != "second try" || got.YearMin != 1990 || got.YearMax != 1999 {
		t.Errorf("stored = %+v", got)
	}
}

// stationNamed reads one station back through the list, which is what the
// console renders.
func stationNamed(t *testing.T, s *Server, name string) struct {
	Brief   string `json:"brief"`
	YearMin int    `json:"year_min"`
	YearMax int    `json:"year_max"`
	Name    string `json:"name"`
} {
	t.Helper()
	type view = struct {
		Brief   string `json:"brief"`
		YearMin int    `json:"year_min"`
		YearMax int    `json:"year_max"`
		Name    string `json:"name"`
	}
	rec := as(t, s, http.MethodGet, "/admin/stations", "", adminCookie(t, s))
	var views []view
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("listing: %q (%v)", rec.Body, err)
	}
	for _, v := range views {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no station named %q in %+v", name, views)
	return view{}
}

func TestStationBriefAPIView(t *testing.T) {
	s, _ := briefServer(t)
	if rec := as(t, s, http.MethodPost, "/admin/stations",
		`{"name":"N","genres":["rock"],"moods":["raw"],"brief":"a brief","year_min":1980}`,
		adminCookie(t, s)); rec.Code >= 300 {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	rec := as(t, s, http.MethodGet, "/admin/stations", "", adminCookie(t, s))
	var views []struct {
		Brief   string `json:"brief"`
		YearMin int    `json:"year_min"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	var found bool
	for _, v := range views {
		if v.Brief == "a brief" && v.YearMin == 1980 {
			found = true
		}
	}
	if !found {
		t.Errorf("the list does not carry the brief and the years: %+v", views)
	}
}

// TestStationBriefAPIBadYears: refused at the boundary, exactly as an
// out-of-vocabulary genre already is, so a hand-rolled request cannot store a
// station that will never fill.
func TestStationBriefAPIBadYears(t *testing.T) {
	s, _ := briefServer(t)
	before := listStationCount(t, s)

	rec := as(t, s, http.MethodPost, "/admin/stations",
		`{"name":"N","genres":["rock"],"year_min":1990,"year_max":1980}`, adminCookie(t, s))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("= %d, want 400: %s", rec.Code, rec.Body)
	}
	var e struct{ Field, Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Field == "" {
		t.Errorf("answered %q, want a field error", rec.Body)
	}
	if after := listStationCount(t, s); after != before {
		t.Error("an impossible station was created anyway")
	}
}

func listStationCount(t *testing.T, s *Server) int {
	t.Helper()
	rec := as(t, s, http.MethodGet, "/admin/stations", "", adminCookie(t, s))
	var views []json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("listing: %q (%v)", rec.Body, err)
	}
	return len(views)
}

// TestStationBriefAPIDeriveEdges: a body that is not JSON, and a count that
// fails after a derive succeeded.
func TestStationBriefAPIDeriveEdges(t *testing.T) {
	s, b := briefServer(t)

	// A body the decoder refuses never reaches the model.
	if rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`not json`, adminCookie(t, s)); rec.Code == http.StatusOK {
		t.Error("a body that is not JSON was accepted")
	}
	if n := b.calls.Load(); n != 0 {
		t.Errorf("the model was called %d times for an undecodable body", n)
	}

	// THE COUNT CAN FAIL AFTER THE DERIVE SUCCEEDED -- an unreadable library,
	// or a filter the model produced that the boundary refuses. Reported, not
	// answered as a station with no music: 0 would send the operator looking
	// for missing tracks.
	b.countErr = errors.New("no library to count against")
	rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`{"brief":"late-night rock"}`, adminCookie(t, s))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("= %d, want 500: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "no library") {
		t.Errorf("the reason was dropped: %s", rec.Body)
	}
}

// TestStationBriefAPIDeriveMethod: only POST. A GET reaching the id parser
// below would answer a bad-id error rather than saying what is wrong.
func TestStationBriefAPIDeriveMethod(t *testing.T) {
	s, _ := briefServer(t)
	if rec := as(t, s, http.MethodGet, "/admin/stations/derive", "",
		adminCookie(t, s)); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /admin/stations/derive = %d, want 405: %s", rec.Code, rec.Body)
	}
}

// Jockora-g1t.10 and the deadline g1t.5's amendment asked for.

// TestStationBriefAPIDeriveTimeout: server.go builds http.Server with NO
// WriteTimeout, so nothing above this handler will ever cut a hung request off.
// A local llama-server that stops answering would leave the request open for
// ever and the Describe button spinning with nothing to click.
func TestStationBriefAPIDeriveTimeout(t *testing.T) {
	s, b := briefServer(t)
	s.deriveDeadline = 20 * time.Millisecond
	b.block = true

	rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`{"brief":"late-night rock"}`, adminCookie(t, s))
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("= %d, want 504: %s", rec.Code, rec.Body)
	}
	body := strings.ToLower(rec.Body.String())
	// THE SAME COURTESY THE 503 GETS: the manual pickers are still there.
	if !strings.Contains(body, "hand") {
		t.Errorf("the timeout does not say the pickers still work: %s", rec.Body)
	}
	// AND THE EXPLANATION. The enrichment queue is serial and pauses only on
	// the operator's own toggle, so a derive fired mid-enrichment queues behind
	// a dossier pass on the same model. Without this the operator sees a
	// spinner then a timeout on a feature that worked yesterday.
	if !strings.Contains(body, "enrichment") {
		t.Errorf("the timeout does not mention enrichment: %s", rec.Body)
	}
	if !strings.Contains(body, "overview") {
		t.Errorf("the timeout does not say where to pause it: %s", rec.Body)
	}
}

// TestStationBriefAPIDeriveVerdict: the console owns both thresholds already
// and said nothing with them at the one moment the operator could still change
// the brief. Seven tracks is below the floor, and they found out only when the
// station refused to enable.
func TestStationBriefAPIDeriveVerdict(t *testing.T) {
	for _, tc := range []struct {
		tracks int
		want   string
	}{
		{7, "too few tracks to run"},
		{30, "fewer tracks than most stations"},
		{200, ""},
	} {
		s, b := briefServer(t)
		b.count = tc.tracks

		rec := as(t, s, http.MethodPost, "/admin/stations/derive",
			`{"brief":"late-night rock"}`, adminCookie(t, s))
		if rec.Code != http.StatusOK {
			t.Fatalf("%d tracks = %d: %s", tc.tracks, rec.Code, rec.Body)
		}
		var got struct {
			Tracks  int    `json:"tracks"`
			Warning string `json:"warning"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Warning != tc.want {
			t.Errorf("%d tracks answered warning %q, want %q", tc.tracks, got.Warning, tc.want)
		}
		// NOT A REFUSAL. An operator is entitled to a station of twelve deep
		// cuts; the words exist so they can decide, not so the console can.
		if got.Tracks != tc.tracks {
			t.Errorf("tracks = %d, want %d", got.Tracks, tc.tracks)
		}
	}
}

// TestStationBriefAPIDeriveTooLong: s.decode caps the whole body at 4KB, and
// over that MaxBytesReader answers a flat 400 that is not attached to any box.
// Somebody pasting a paragraph about their station hits an opaque wall, so the
// friendly limit sits well clear of it.
func TestStationBriefAPIDeriveTooLong(t *testing.T) {
	s, b := briefServer(t)

	// RUNES, NOT BYTES -- Jockora-9jo is the same mistake one field over. An
	// accented brief at the limit is over it in bytes and must still be
	// accepted, so this one is deliberately multi-byte.
	ok := strings.Repeat("é", MaxBriefRunes)
	body, _ := json.Marshal(map[string]string{"brief": ok})
	if rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		string(body), adminCookie(t, s)); rec.Code != http.StatusOK {
		t.Fatalf("a brief of exactly %d runes = %d: %s", MaxBriefRunes, rec.Code, rec.Body)
	}

	over, _ := json.Marshal(map[string]string{"brief": ok + "é"})
	rec := as(t, s, http.MethodPost, "/admin/stations/derive", string(over), adminCookie(t, s))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an over-long brief = %d, want 400: %s", rec.Code, rec.Body)
	}
	var e struct{ Field, Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Field != "brief" {
		t.Fatalf("answered %q, want a field error on brief", rec.Body)
	}
	// IT NAMES THE LIMIT. "Too long" with no number is a wall with no door.
	if !strings.Contains(e.Error, strconv.Itoa(MaxBriefRunes)) {
		t.Errorf("the refusal does not say how much is too much: %q", e.Error)
	}
	if b.calls.Load() != 1 {
		t.Errorf("the model saw %d briefs; the over-long one should not have reached it",
			b.calls.Load())
	}
}

// TestStationBriefAPIWordsAreJSON: every response these features produce that
// carries words for a human has to be JSON with an "error" key. http.Error
// writes text/plain, HttpClient then puts a STRING in e.error, and every
// console following the existing e.error?.error pattern falls through to its
// generic fallback -- the server's carefully written sentence dropped on the
// floor. A status-code-only test passes happily while that happens.
func TestStationBriefAPIWordsAreJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*fakeBriefs)
		code int
	}{
		{"no model", func(f *fakeBriefs) { f.err = enrich.ErrNoModel }, http.StatusServiceUnavailable},
		{"the model failed", func(f *fakeBriefs) { f.err = errors.New("rejected the API key") },
			http.StatusBadGateway},
		{"the count failed", func(f *fakeBriefs) { f.countErr = errors.New("no library") },
			http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, b := briefServer(t)
			tc.set(b)
			rec := as(t, s, http.MethodPost, "/admin/stations/derive",
				`{"brief":"late-night rock"}`, adminCookie(t, s))
			if rec.Code != tc.code {
				t.Fatalf("= %d, want %d: %s", rec.Code, tc.code, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
				t.Errorf("Content-Type = %q; text/plain is dropped by the console", ct)
			}
			var e struct{ Error string }
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error == "" {
				t.Errorf("body %q has no error key, so the console shows its own fallback", rec.Body)
			}
		})
	}
}

// ------------------------------------------------------------ Jockora-2fn --
//
// Reported: a brief derived six parameters, previewed 0 tracks, and the
// operator concluded the tempo was at fault. It was not -- every bpm on that
// deployment is NULL and a NULL bpm never excludes. The year range was the
// cause: every track has a year and only 312 of 7595 are from the 1980s.
//
// The console said "0" and, through moodHint, blamed the mood. It named the
// wrong suspect confidently and sent the operator after the wrong parameter.

func TestStationBriefAPIDeriveSaysWhatEmptiedIt(t *testing.T) {
	s, b := briefServer(t)
	admin := adminCookie(t, s)
	b.params.YearMin, b.params.YearMax = 1980, 1989
	b.params.TempoMin, b.params.TempoMax = 90, 140
	// Nothing matches as asked; without the YEARS, 412 do.
	b.countFor = func(f station.Filter) int {
		if f.YearMin == 0 && f.YearMax == 0 {
			return 412
		}
		return 0
	}

	rec := as(t, s, http.MethodPost, "/admin/stations/derive", `{"brief":"eighties night rock"}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Tracks  int    `json:"tracks"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Tracks != 0 {
		t.Fatalf("tracks = %d, want 0", got.Tracks)
	}
	// THE PARAMETER, BY NAME, and what removing it would give back.
	if !strings.Contains(got.Warning, "1980") || !strings.Contains(got.Warning, "1989") {
		t.Errorf("the warning does not name the years: %q", got.Warning)
	}
	if !strings.Contains(got.Warning, "412") {
		t.Errorf("the warning does not say what dropping them recovers: %q", got.Warning)
	}
	// And it must NOT blame something innocent.
	if strings.Contains(got.Warning, "tempo") {
		t.Errorf("the warning blames the tempo, which excludes nothing here: %q", got.Warning)
	}
}

func TestStationBriefAPIDeriveSaysWhenNothingSingleIsToBlame(t *testing.T) {
	s, b := briefServer(t)
	admin := adminCookie(t, s)
	b.params.YearMin, b.params.YearMax = 1980, 1989
	b.countFor = func(station.Filter) int { return 0 }

	rec := as(t, s, http.MethodPost, "/admin/stations/derive", `{"brief":"nothing at all"}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Honest rather than confident: no single change fixes it.
	if !strings.Contains(got.Warning, "no single") {
		t.Errorf("the warning invents a culprit: %q", got.Warning)
	}
}

// TestStationBriefAPIDeriveDiagnosesOnlyWhenEmpty: five extra counts on every
// preview would be five extra queries for a question nobody asked.
func TestStationBriefAPIDeriveDiagnosesOnlyWhenEmpty(t *testing.T) {
	s, b := briefServer(t)
	admin := adminCookie(t, s)
	calls := 0
	b.countFor = func(station.Filter) int {
		calls++
		return 412
	}
	if rec := as(t, s, http.MethodPost, "/admin/stations/derive",
		`{"brief":"night rock"}`, admin); rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	if calls != 1 {
		t.Errorf("counted %d times for a station that has music, want once", calls)
	}
}

// TestStationBriefAPIDeriveKeepsThePreviewWhenTheDiagnosisFails: naming the
// culprit is a courtesy. Losing it must not lose the preview the operator
// actually asked for.
func TestStationBriefAPIDeriveKeepsThePreviewWhenTheDiagnosisFails(t *testing.T) {
	s, b := briefServer(t)
	admin := adminCookie(t, s)
	b.params.YearMin, b.params.YearMax = 1980, 1989
	first := true
	b.countFor = func(station.Filter) int {
		if first {
			first = false
			return 0
		}
		b.countErr = errors.New("the database went away")
		return 0
	}

	rec := as(t, s, http.MethodPost, "/admin/stations/derive", `{"brief":"x"}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Name    string `json:"name"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Night Rock" {
		t.Errorf("the preview was lost: %+v", got)
	}
	if strings.Contains(got.Warning, "without that") {
		t.Errorf("a failed diagnosis still named a culprit: %q", got.Warning)
	}
}

// TestStationBriefAPIDeriveNamesTheBiggestCulprit: several bounds may each be
// enough to empty a station, and naming an arbitrary one sends the operator to
// the wrong place -- which is the whole reason this diagnosis exists.
//
// Falsification found this: taking the FIRST candidate that recovers instead of
// the best killed no test, because every other case here has only one.
func TestStationBriefAPIDeriveNamesTheBiggestCulprit(t *testing.T) {
	s, b := briefServer(t)
	admin := adminCookie(t, s)
	b.params.YearMin, b.params.YearMax = 1980, 1989
	b.params.TempoMin, b.params.TempoMax = 90, 140

	// Dropping the years recovers 12; dropping the tempo recovers 900. The
	// tempo is the answer, and it is checked SECOND.
	b.countFor = func(f station.Filter) int {
		switch {
		case f.YearMin == 0 && f.TempoMin != 0:
			return 12
		case f.TempoMin == 0 && f.YearMin != 0:
			return 900
		default:
			return 0
		}
	}

	rec := as(t, s, http.MethodPost, "/admin/stations/derive", `{"brief":"x"}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Warning, "tempo") || !strings.Contains(got.Warning, "900") {
		t.Errorf("the warning names the smaller culprit: %q", got.Warning)
	}
}
