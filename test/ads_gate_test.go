// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/app"
	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// THE GATE for Jockora-iyw. Tasks 1 to 6 are the feature; this proves them
// together, from the button an operator presses to the words the AdWriter hands
// the mixer.
//
// NOT IN internal/server, where the sibling brief gate lives, and not behind
// the "gates" build tag. The path under test crosses app, server, store, dj and
// station, and internal/server cannot import internal/app -- app imports
// server. Rebuilding app.AdSource inside a server test to get around that would
// stub the exact join the feature is made of. This package can import all five,
// so nothing here is stubbed except the model. Untagged so it runs in
// "make test": it needs no ffmpeg, no network and no model, and a gate nobody
// runs is a gate that decays.

const gateAdmin, gateAdminPassword = "operator", "correct horse battery"

// gateModel answers with one canned advert, or refuses.
type gateModel struct {
	script string
	err    error
	calls  int
}

func (m *gateModel) Complete(context.Context, enrich.CompletionRequest) (enrich.Completion, error) {
	m.calls++
	if m.err != nil {
		return enrich.Completion{}, m.err
	}
	body, _ := json.Marshal(map[string]string{"brand": "whatever", "script": m.script})
	return enrich.Completion{Content: string(body), StopType: "eos"}, nil
}

// gateDJ is the DJ's own break. NOT the thing under test -- what this gate
// proves is WHOSE words come back, and "the DJ talking" is how it can tell.
type gateDJ struct{ n int }

func (g *gateDJ) Write(context.Context, int, mix.Placement) (string, error) {
	g.n++
	return "the DJ talking", nil
}

type adsWorld struct {
	base   string
	store  *store.Store
	model  *gateModel
	cookie *http.Cookie
	t      *testing.T
}

// adsWorld stands up a real store, a real App and a real HTTP server.
func newAdsWorld(t *testing.T, m *gateModel, personaPath string) *adsWorld {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(root, "gate.db"))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	hash, err := auth.HashPassword(gateAdminPassword, 4) // cheap on purpose
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, gateAdmin, hash, auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	// One playable track, so the App has a library to select from.
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO tracks (id, path, playable) VALUES (1, '/m/1.mp3', 1)`); err != nil {
		t.Fatal(err)
	}

	var llm *enrich.Switchable
	if m == nil {
		llm = enrich.NewSwitchable(nil) // a library with no model is a working shuffle
	} else {
		llm = enrich.NewSwitchable(m)
	}

	logs := &bytes.Buffer{}
	a, err := app.New(&config.Config{
		ListenAddr: "127.0.0.1:0", SegmentDir: filepath.Join(root, "segments"),
		SampleRate: mix.SampleRate, Channels: mix.Channels, SegmentSeconds: 2, ListSize: 10,
		PersonaPath: personaPath,
		// EXPLICIT, because app.New assigns this to the station package's
		// global: a config literal that omits it silently disables adverts
		// everywhere for the rest of the process, which is what made the first
		// run of this gate air nothing at all.
		AdEveryNBreaks: 4,
	}, app.Options{
		Library: &app.Library{Store: st, Selector: station.NewSelector([]int64{1}, 1)},
		LLM:     llm,
		Log:     slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	if err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && a.Addr() == "" {
		time.Sleep(10 * time.Millisecond)
	}
	if a.Addr() == "" {
		cancel()
		t.Fatal("the server never came up")
	}
	t.Cleanup(func() {
		cancel()
		<-done
		st.Close()
		if t.Failed() {
			t.Logf("server log:\n%s", logs.String())
		}
	})

	w := &adsWorld{base: "http://" + a.Addr(), store: st, model: m, t: t}
	w.cookie = w.login()
	return w
}

func (w *adsWorld) login() *http.Cookie {
	w.t.Helper()
	body, _ := json.Marshal(map[string]string{"name": gateAdmin, "password": gateAdminPassword})
	resp, err := http.Post(w.base+"/login", "application/json", bytes.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		out, _ := io.ReadAll(resp.Body)
		w.t.Fatalf("login = %d: %s", resp.StatusCode, out)
	}
	for _, c := range resp.Cookies() {
		if c.Value != "" {
			return c
		}
	}
	w.t.Fatal("login set no cookie")
	return nil
}

// do makes one authenticated request and returns the status and body.
func (w *adsWorld) do(method, path, body string) (int, []byte) {
	w.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, w.base+path, r)
	if err != nil {
		w.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(w.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// rotation is the two lines cmd/jockora/serve.go runs, over this store.
func (w *adsWorld) rotation() *dj.AdRotation {
	r := dj.NewAdRotation(nil)
	r.Source = app.AdSource(w.store)
	r.Aired = app.AdAired(w.store)
	return r
}

// airOne drives the AdWriter until a slot comes round, and returns the words.
//
// THE REAL station.AdWriter over the REAL rotation. One in AdEveryNBreaks slots
// belongs to an advert, so four calls reach one; the clock is advanced past
// MinAdInterval so only the ratio is in play.
func airOne(t *testing.T, w station.Writer, clk *clock.Fake) string {
	t.Helper()
	var last string
	for i := 0; i < station.AdEveryNBreaks; i++ {
		clk.Advance(2 * time.Hour)
		got, err := w.Write(context.Background(), 40, mix.PlacementRamp)
		if err != nil {
			t.Fatal(err)
		}
		last = got
	}
	return last
}

// TestAdsGateEndToEnd is the whole feature in one sequence: an advert written
// in the console is heard, an edited one changes with nothing restarted, and a
// deleted one stops being heard.
func TestAdsGateEndToEnd(t *testing.T) {
	const written = "Stillwater Ceramics. Hand-thrown mugs, one at a time, still here tomorrow."
	w := newAdsWorld(t, &gateModel{script: written}, "")

	// 1. WRITE IT. The console posts a brief and reads back a blurb.
	code, body := w.do(http.MethodPost, "/admin/ads/write",
		`{"brand":"Stillwater Ceramics","about":"hand-thrown mugs","delivery":"deadpan"}`)
	if code != http.StatusOK {
		t.Fatalf("write = %d: %s", code, body)
	}
	var drafted struct{ Brand, Script string }
	if err := json.Unmarshal(body, &drafted); err != nil {
		t.Fatal(err)
	}
	if drafted.Script != written {
		t.Fatalf("drafted %q", drafted.Script)
	}
	// NOTHING SAVED BY WRITING, at the far end of the whole stack.
	if rows, err := w.store.ListAds(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("writing saved %d rows (%v)", len(rows), err)
	}

	// 2. SAVE IT, as shown.
	save, _ := json.Marshal(map[string]string{"brand": drafted.Brand,
		"brief": "hand-thrown mugs", "delivery": "deadpan", "script": drafted.Script})
	code, body = w.do(http.MethodPost, "/admin/ads", string(save))
	if code != http.StatusCreated {
		t.Fatalf("save = %d: %s", code, body)
	}
	var made struct{ ID int64 }
	if err := json.Unmarshal(body, &made); err != nil || made.ID == 0 {
		t.Fatalf("save body = %s (%v)", body, err)
	}

	// 3. HEAR IT. A real rotation over the real store, inside a real AdWriter.
	dj0 := &gateDJ{}
	clk := clock.NewFake(time.Unix(1788840000, 0))
	writer := station.NewAdWriter(dj0, w.rotation(), clk)
	if got := airOne(t, writer, clk); got != written {
		t.Fatalf("the advert slot said %q, want the advert that was saved", got)
	}

	// 4. EDIT IT. NOTHING IS RESTARTED and no object is swapped: the same
	// writer, the same rotation, the next slot.
	const edited = "Stillwater Ceramics. Two mugs now, and we are as surprised as you are."
	put, _ := json.Marshal(map[string]string{"brand": "Stillwater Ceramics",
		"brief": "hand-thrown mugs", "delivery": "deadpan", "script": edited})
	if code, body := w.do(http.MethodPut,
		fmt.Sprintf("/admin/ads/%d", made.ID), string(put)); code != http.StatusNoContent {
		t.Fatalf("edit = %d: %s", code, body)
	}
	if got := airOne(t, writer, clk); got != edited {
		t.Fatalf("after the edit the slot said %q, want the new words", got)
	}
	if w.model.calls != 1 {
		t.Errorf("the model was called %d times; saving must never re-write", w.model.calls)
	}

	// 5. DELETE IT. The slot falls back to the DJ, because breaks are optional
	// and an empty pool is not a failure.
	if code, body := w.do(http.MethodDelete,
		fmt.Sprintf("/admin/ads/%d", made.ID), ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", code, body)
	}
	before := dj0.n
	if got := airOne(t, writer, clk); got != "the DJ talking" {
		t.Fatalf("after the delete the slot said %q, want the DJ's own break", got)
	}
	if dj0.n <= before {
		t.Error("the DJ was never asked for the slot the advert gave up")
	}
}

// TestAdsGateCooldownIsShared: two stations drawing on the same pool. An advert
// aired on one is inside its cooldown on the other.
//
// THIS IS WHY last_aired_at MOVED TO THE ROW. With the cooldown in a per-station
// map, a listener flipping stations hears the same advert twice, and nothing
// else in the suite would notice.
func TestAdsGateCooldownIsShared(t *testing.T) {
	w := newAdsWorld(t, nil, "")
	ctx := context.Background()
	for _, a := range []store.Ad{
		{Brand: "Stillwater", Script: "Stillwater. Mugs, thrown by hand, still here tomorrow."},
		{Brand: "Harrow", Script: "Harrow. Books, and a cat that sleeps on all of them."},
	} {
		if _, err := w.store.CreateAd(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	// TWO STATIONS: two writers, two rotations, ONE STORE. That is exactly the
	// deployment -- adverts are global and every station reads the same table.
	now := time.Unix(1788840000, 0)
	clkA, clkB := clock.NewFake(now), clock.NewFake(now)
	a := station.NewAdWriter(&gateDJ{}, w.rotation(), clkA)
	b := station.NewAdWriter(&gateDJ{}, w.rotation(), clkB)

	first := airOne(t, a, clkA)
	if !strings.HasPrefix(first, "Stillwater") && !strings.HasPrefix(first, "Harrow") {
		t.Fatalf("station A aired %q", first)
	}
	// B's clock is barely past A's, so the advert A just aired is inside its
	// cooldown -- and B must not repeat it.
	clkB.Advance(time.Minute)
	second := airOne(t, b, clkB)
	if second == first {
		t.Errorf("both stations aired %q; the cooldown is not shared", first)
	}
}

// TestAdsGateNoModel: a library with no model is a working shuffle, and this is
// where that is proved for adverts. Drafting is a convenience; selling airtime
// is not.
func TestAdsGateNoModel(t *testing.T) {
	w := newAdsWorld(t, nil, "")

	code, body := w.do(http.MethodPost, "/admin/ads/write",
		`{"brand":"Stillwater","about":"mugs"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("write with no model = %d: %s", code, body)
	}
	var e struct{ Error string }
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("the 503 is not JSON, so the console shows its own fallback: %s", body)
	}
	if !strings.Contains(e.Error, "Model page") {
		t.Errorf("the 503 does not name a way out: %q", e.Error)
	}

	// AND A HAND-WRITTEN ADVERT STILL AIRS.
	const mine = "Stillwater. Mugs, thrown by hand, and still here tomorrow morning."
	save, _ := json.Marshal(map[string]string{"brand": "Stillwater", "script": mine})
	if code, body := w.do(http.MethodPost, "/admin/ads", string(save)); code != http.StatusCreated {
		t.Fatalf("saving a hand-written advert = %d: %s", code, body)
	}
	clk := clock.NewFake(time.Unix(1788840000, 0))
	writer := station.NewAdWriter(&gateDJ{}, w.rotation(), clk)
	if got := airOne(t, writer, clk); got != mine {
		t.Fatalf("aired %q, want the advert nobody needed a model to write", got)
	}
}

// TestAdsGatePackImport: a pack travels with its adverts, and after startup
// they are ordinary rows -- they air, and they can be edited and deleted.
func TestAdsGatePackImport(t *testing.T) {
	dir := t.TempDir()
	const packAd = "Brindlewax Removals. We move it, and mostly it arrives."
	pack := `pack_version = 1
id = "gatejock"
name = "Gate Jock"
voice_id = "am_fenrir"
good_for_genres = ["rock"]
speech_style = "Flat, unhurried, and entirely uninterested in your day."
personality = "Reads the news like a shipping forecast, and the weather like bad news."

[[advert]]
id = "ad-01"
brand = "Brindlewax Removals"
script = "` + packAd + `"
`
	if err := os.WriteFile(filepath.Join(dir, "gatejock.toml"), []byte(pack), 0o644); err != nil {
		t.Fatal(err)
	}

	w := newAdsWorld(t, nil, dir)
	// The App does not import packs -- cmd/jockora/serve.go does, at startup,
	// through this exact call. Running it here is the startup step, not a
	// substitute for it.
	ads := packAdverts(t, dir)
	n, err := app.ImportPackAds(context.Background(), w.store, ads)
	if err != nil || n != 1 {
		t.Fatalf("imported %d adverts (%v)", n, err)
	}
	// IDEMPOTENT. A restart loads the same packs, and duplicating the pool
	// would make the rotation one advert repeated.
	if n, err := app.ImportPackAds(context.Background(), w.store, ads); err != nil || n != 0 {
		t.Fatalf("a second startup added %d (%v)", n, err)
	}

	clk := clock.NewFake(time.Unix(1788840000, 0))
	writer := station.NewAdWriter(&gateDJ{}, w.rotation(), clk)
	if got := airOne(t, writer, clk); got != packAd {
		t.Fatalf("aired %q, want the pack's advert", got)
	}

	// AN ORDINARY ROW NOW: editable and deletable through the API like any
	// other, which is the whole point of importing rather than reading packs
	// at every pick.
	code, body := w.do(http.MethodGet, "/admin/ads", "")
	if code != http.StatusOK {
		t.Fatalf("list = %d: %s", code, body)
	}
	var rows []struct {
		ID     int64
		Brand  string
		Script string
	}
	if err := json.Unmarshal(body, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("listed %s (%v)", body, err)
	}
	const edited = "Brindlewax Removals. We move it, and now it usually arrives."
	put, _ := json.Marshal(map[string]string{"brand": rows[0].Brand, "script": edited})
	if code, body := w.do(http.MethodPut,
		fmt.Sprintf("/admin/ads/%d", rows[0].ID), string(put)); code != http.StatusNoContent {
		t.Fatalf("editing a pack advert = %d: %s", code, body)
	}
	if got := airOne(t, writer, clk); got != edited {
		t.Fatalf("after editing the pack advert the slot said %q", got)
	}
	if code, _ := w.do(http.MethodDelete,
		fmt.Sprintf("/admin/ads/%d", rows[0].ID), ""); code != http.StatusNoContent {
		t.Fatalf("deleting a pack advert = %d", code)
	}
}

// packAdverts reads the adverts out of every pack in a directory, the way
// cmd/jockora/serve.go does at startup.
func packAdverts(t *testing.T, dir string) []dj.Ad {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no packs in %s (%v)", dir, err)
	}
	var ads []dj.Ad
	for _, p := range paths {
		pack, err := dj.LoadPack(p)
		if err != nil {
			t.Fatalf("loading %s: %v", p, err)
		}
		ads = append(ads, pack.Ads()...)
	}
	return ads
}
