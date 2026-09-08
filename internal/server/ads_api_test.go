// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

// Jockora-iyw.5. THE OPERATOR SELLS AIRTIME: they describe a product, read the
// blurb the model wrote, edit it if they like, and save it. Everything here
// turns on the difference between writing and saving.

// fakeAds is the real store for the CRUD, plus a model we can steer.
type fakeAds struct {
	*store.Store
	ad    dj.Ad
	err   error
	block bool
	calls int
	saw   dj.AdBrief
}

func (f *fakeAds) WriteAd(ctx context.Context, in dj.AdBrief) (dj.Ad, error) {
	f.calls++
	f.saw = in
	if f.block {
		// A HUNG MODEL, which is the failure the deadline exists for: not a
		// refusal, an answer that never comes.
		<-ctx.Done()
		return dj.Ad{}, ctx.Err()
	}
	return f.ad, f.err
}

func adsServer(t *testing.T) (*Server, *store.Store, *fakeAds) {
	t.Helper()
	s, st, _ := authServer(t)
	f := &fakeAds{Store: st, ad: dj.Ad{
		Brand:  "Stillwater Ceramics",
		Script: "Stillwater Ceramics. Hand-thrown mugs, one at a time, still here tomorrow."}}
	s.SetAds(f)
	return s, st, f
}

// adBody is what the console posts to save one.
func savePost(brand, brief, delivery, script string) string {
	b, _ := json.Marshal(map[string]string{
		"brand": brand, "brief": brief, "delivery": delivery, "script": script})
	return string(b)
}

// errorOf reads the {"field","error"} shape every console in this app reads.
func errorOf(t *testing.T, body []byte) (field, msg string) {
	t.Helper()
	var e struct {
		Field string `json:"field"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("body is not the JSON the console reads: %s (%v)", body, err)
	}
	return e.Field, e.Error
}

func TestAdsAPIList(t *testing.T) {
	s, st, _ := adsServer(t)
	admin := adminCookie(t, s)
	ctx := context.Background()

	if rec := as(t, s, http.MethodGet, "/admin/ads", "", admin); rec.Code != http.StatusOK {
		t.Fatalf("an empty list = %d: %s", rec.Code, rec.Body)
	}

	for _, a := range []store.Ad{
		{Brand: "Stillwater", Brief: "mugs", Delivery: "deadpan",
			Script: "Stillwater. Mugs, thrown by hand, and still here tomorrow."},
		{Brand: "Harrow", Script: "Harrow. Books, and a cat that sleeps on them."},
	} {
		if _, err := st.CreateAd(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	rec := as(t, s, http.MethodGet, "/admin/ads", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var got []struct {
		ID                             int64
		Brand, Brief, Delivery, Script string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d, want 2: %s", len(got), rec.Body)
	}
	// EVERY FIELD THE FORM EDITS comes back, or the operator opens an advert
	// and finds the brief they wrote has vanished.
	var stillwater bool
	for _, a := range got {
		if a.Brand == "Stillwater" {
			stillwater = true
			if a.Brief != "mugs" || a.Delivery != "deadpan" {
				t.Errorf("brief/delivery lost: %+v", a)
			}
			if a.ID == 0 {
				t.Error("no id, so nothing can be edited or deleted")
			}
		}
	}
	if !stillwater {
		t.Errorf("the advert with a brief is not in the list: %s", rec.Body)
	}

	// AND AN AIRED ONE CARRIES A TIME. The console shows this beside an advert,
	// so "never" has to be empty rather than 1 January 1970.
	rows, err := st.ListAds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC)
	if err := st.MarkAdAired(ctx, rows[0].ID, when); err != nil {
		t.Fatal(err)
	}
	var aired []struct {
		LastAiredAt string `json:"last_aired_at"`
	}
	if err := json.Unmarshal(as(t, s, http.MethodGet, "/admin/ads", "", admin).Body.Bytes(),
		&aired); err != nil {
		t.Fatal(err)
	}
	var seen, blank int
	for _, a := range aired {
		if strings.HasPrefix(a.LastAiredAt, "2026-09-08T14:30:00") {
			seen++
		}
		if a.LastAiredAt == "" {
			blank++
		}
	}
	if seen != 1 || blank != 1 {
		t.Errorf("aired times = %+v, want one stamped and one empty", aired)
	}
}

func TestAdsAPICreate(t *testing.T) {
	s, st, f := adsServer(t)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodPost, "/admin/ads",
		savePost("Stillwater Ceramics", "hand-thrown mugs", "deadpan",
			"Stillwater Ceramics. Mugs, thrown by hand, still here tomorrow."), admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var made struct{ ID int64 }
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil || made.ID == 0 {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}

	got, err := st.GetAd(context.Background(), made.ID)
	if err != nil {
		t.Fatal(err)
	}
	// STORED AS SHOWN. The operator read those exact words and pressed save.
	if got.Brand != "Stillwater Ceramics" || got.Brief != "hand-thrown mugs" ||
		got.Delivery != "deadpan" || !strings.HasPrefix(got.Script, "Stillwater Ceramics.") {
		t.Errorf("stored %+v", got)
	}
	if f.calls != 0 {
		t.Errorf("saving called the model %d times", f.calls)
	}
}

func TestAdsAPIUpdate(t *testing.T) {
	s, st, f := adsServer(t)
	admin := adminCookie(t, s)
	ctx := context.Background()

	id, err := st.CreateAd(ctx, store.Ad{Brand: "Stillwater",
		Script: "Stillwater. Mugs, thrown by hand, and still here tomorrow."})
	if err != nil {
		t.Fatal(err)
	}

	edited := "Stillwater. Two mugs now, and we are as surprised as you are."
	rec := as(t, s, http.MethodPut, "/admin/ads/"+strconv.FormatInt(id, 10),
		savePost("Stillwater", "mugs, more of them", "warm", edited), admin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	got, err := st.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Script != edited || got.Brief != "mugs, more of them" || got.Delivery != "warm" {
		t.Errorf("stored %+v", got)
	}
	if f.calls != 0 {
		t.Errorf("editing called the model %d times", f.calls)
	}

	// An id nothing matches is a 404, not a silent success.
	if rec := as(t, s, http.MethodPut, "/admin/ads/9999",
		savePost("Stillwater", "", "", edited), admin); rec.Code != http.StatusNotFound {
		t.Errorf("editing a missing advert = %d, want 404", rec.Code)
	}
}

func TestAdsAPIDelete(t *testing.T) {
	s, st, _ := adsServer(t)
	admin := adminCookie(t, s)
	ctx := context.Background()

	id, err := st.CreateAd(ctx, store.Ad{Brand: "Stillwater",
		Script: "Stillwater. Mugs, thrown by hand, and still here tomorrow."})
	if err != nil {
		t.Fatal(err)
	}
	rec := as(t, s, http.MethodDelete, "/admin/ads/"+strconv.FormatInt(id, 10), "", admin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if _, err := st.GetAd(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the advert is still there: %v", err)
	}
}

// TestAdsAPIDeleteMissing: 404, not a 204. A console that reports "deleted" for
// an advert that was never there teaches the operator to distrust the list.
func TestAdsAPIDeleteMissing(t *testing.T) {
	s, _, _ := adsServer(t)
	admin := adminCookie(t, s)
	if rec := as(t, s, http.MethodDelete, "/admin/ads/9999", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("= %d, want 404", rec.Code)
	}
	// And a path that is not an id at all is a 404 rather than a 500.
	if rec := as(t, s, http.MethodDelete, "/admin/ads/banana", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("a non-numeric id = %d, want 404", rec.Code)
	}
}

// TestAdsAPIWrite: WRITING IS NOT SAVING. The operator reads the blurb first,
// and re-writing on save would air words they never saw.
func TestAdsAPIWrite(t *testing.T) {
	s, st, f := adsServer(t)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodPost, "/admin/ads/write",
		`{"brand":"Stillwater Ceramics","about":"hand-thrown mugs","delivery":"deadpan"}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Brand, Script, Warning string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Script, "Stillwater") {
		t.Errorf("script = %q", got.Script)
	}
	if got.Warning != "" {
		t.Errorf("an invented brand was flagged: %q", got.Warning)
	}
	// THE BRIEF REACHED THE MODEL, all three parts of it.
	if f.saw.Brand != "Stillwater Ceramics" || f.saw.About != "hand-thrown mugs" ||
		f.saw.Delivery != "deadpan" {
		t.Errorf("the model saw %+v", f.saw)
	}
	// NOTHING WAS SAVED. This route never writes to the table, ever.
	rows, err := st.ListAds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("writing a blurb saved %d adverts", len(rows))
	}
}

// TestAdsAPIWriteRealBrand: an OPERATOR naming their own advertiser is the
// opposite of a MODEL inventing Coca-Cola. Warn, and let them through.
func TestAdsAPIWriteRealBrand(t *testing.T) {
	s, _, f := adsServer(t)
	admin := adminCookie(t, s)
	f.ad = dj.Ad{Brand: "Coca-Cola",
		Script: "Coca-Cola. The one you already know, cold, at the shop on the corner."}

	rec := as(t, s, http.MethodPost, "/admin/ads/write",
		`{"brand":"Coca-Cola","about":"the drink"}`, admin)
	// A 200. NOT A REFUSAL: refusing would make the feature useless to anybody
	// whose client is a real company.
	if rec.Code != http.StatusOK {
		t.Fatalf("a real brand was refused with %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Script, Warning, Matched string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Warning == "" {
		t.Fatal("no warning on an advert naming a real brand")
	}
	// WHICH NAME IT MATCHED, or the operator cannot tell whether it is right.
	if !strings.Contains(strings.ToLower(got.Matched+got.Warning), "coca-cola") {
		t.Errorf("the warning does not name the match: %+v", got)
	}
	if got.Script == "" {
		t.Error("the blurb was withheld along with the warning")
	}
}

// TestAdsAPIWriteNoModel: 503 with a sentence naming what to do, as JSON --
// http.Error would write text/plain, the console would find a string in
// e.error, and every carefully written sentence here would be dropped.
func TestAdsAPIWriteNoModel(t *testing.T) {
	s, _, f := adsServer(t)
	admin := adminCookie(t, s)
	f.err = enrich.ErrNoModel

	rec := as(t, s, http.MethodPost, "/admin/ads/write",
		`{"brand":"Stillwater","about":"mugs"}`, admin)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("= %d, want 503: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q; the console reads e.error.error and would see a string", ct)
	}
	_, msg := errorOf(t, rec.Body.Bytes())
	// THE WAY OUT, named. A bare 503 sends the operator to the logs for
	// something the console can simply say.
	if !strings.Contains(strings.ToLower(msg), "model") {
		t.Errorf("the 503 does not mention the model: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "write") &&
		!strings.Contains(strings.ToLower(msg), "your own") {
		t.Errorf("the 503 does not say they can write it themselves: %q", msg)
	}
}

// TestAdsAPIWriteFails: 502, not 503. The model IS configured and answered
// badly, which is a different thing for the operator to go and fix.
func TestAdsAPIWriteFails(t *testing.T) {
	s, _, f := adsServer(t)
	admin := adminCookie(t, s)
	f.err = errors.New("402 payment required")

	rec := as(t, s, http.MethodPost, "/admin/ads/write",
		`{"brand":"Stillwater","about":"mugs"}`, admin)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("= %d, want 502: %s", rec.Code, rec.Body)
	}
	_, msg := errorOf(t, rec.Body.Bytes())
	// THE PROVIDER'S OWN WORDS. "402 payment required" is the whole answer and
	// swallowing it sends the operator to the logs to find it.
	if !strings.Contains(msg, "402") {
		t.Errorf("the model's reason was lost: %q", msg)
	}
}

// TestAdsAPIWriteTimeout: nothing above the handler cuts off a hung model --
// server.go builds http.Server with no WriteTimeout on purpose, for the SSE
// log stream. The deadline is here or it is nowhere.
func TestAdsAPIWriteTimeout(t *testing.T) {
	s, _, f := adsServer(t)
	admin := adminCookie(t, s)
	f.block = true
	s.deriveDeadline = 20 * time.Millisecond

	start := time.Now()
	rec := as(t, s, http.MethodPost, "/admin/ads/write",
		`{"brand":"Stillwater","about":"mugs"}`, admin)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("= %d, want 504: %s", rec.Code, rec.Body)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v, so the deadline did not fire", d)
	}
	_, msg := errorOf(t, rec.Body.Bytes())
	// THE SAME EXPLANATION THE STATION BRIEF GIVES. Two screens in one console
	// that fail differently is worse than either failure.
	if !strings.Contains(strings.ToLower(msg), "enrichment") {
		t.Errorf("the 504 does not mention enrichment: %q", msg)
	}
}

// TestAdsAPIWriteBlank: nothing to write from, and no round trip to discover
// it. Also the field caps, which exist so the operator meets a message on the
// box they were typing in rather than the flat 400 MaxBytesReader gives.
func TestAdsAPIWriteBlank(t *testing.T) {
	s, _, f := adsServer(t)
	admin := adminCookie(t, s)

	for _, c := range []struct{ body, field string }{
		{`{"brand":"  ","about":"mugs"}`, "brand"},
		{`{"brand":"Stillwater","about":"   "}`, "about"},
		{`{"brand":"Stillwater","about":"` + strings.Repeat("m", MaxAdAboutRunes+1) + `"}`, "about"},
		{`{"brand":"Stillwater","about":"mugs","delivery":"` +
			strings.Repeat("d", MaxAdDeliveryRunes+1) + `"}`, "delivery"},
	} {
		rec := as(t, s, http.MethodPost, "/admin/ads/write", c.body, admin)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400: %s", c.field, rec.Code, rec.Body)
		}
		field, msg := errorOf(t, rec.Body.Bytes())
		if field != c.field {
			t.Errorf("named field %q, want %q", field, c.field)
		}
		if msg == "" {
			t.Errorf("field %q got no sentence", c.field)
		}
	}
	if rec := as(t, s, http.MethodPost, "/admin/ads/write", "not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
	if f.calls != 0 {
		t.Errorf("called the model %d times for a body it could not use", f.calls)
	}

	// RUNES, NOT BYTES. An "about" of accented characters is under the limit
	// in characters and over it in bytes, and refusing it would be the same
	// mistake one field over -- see Jockora-9jo.
	ok := `{"brand":"Stillwater","about":"` + strings.Repeat("é", MaxAdAboutRunes) + `"}`
	if rec := as(t, s, http.MethodPost, "/admin/ads/write", ok, admin); rec.Code != http.StatusOK {
		t.Errorf("an about of %d accented characters = %d, want 200: %s",
			MaxAdAboutRunes, rec.Code, rec.Body)
	}
}

// TestAdsAPISaveDoesNotWrite: the operator either accepted a blurb they read or
// wrote their own. Re-writing on save would air words they never saw.
func TestAdsAPISaveDoesNotWrite(t *testing.T) {
	s, st, f := adsServer(t)
	admin := adminCookie(t, s)
	script := "Stillwater. Mugs, thrown by hand, and still here tomorrow morning."

	rec := as(t, s, http.MethodPost, "/admin/ads",
		savePost("Stillwater", "mugs", "", script), admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var made struct{ ID int64 }
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	if rec := as(t, s, http.MethodPut, "/admin/ads/"+strconv.FormatInt(made.ID, 10),
		savePost("Stillwater", "mugs", "", script), admin); rec.Code != http.StatusNoContent {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if f.calls != 0 {
		t.Fatalf("saving called the model %d times", f.calls)
	}
	// AND THE WORDS ARE THE WORDS. Byte for byte what was posted.
	got, err := st.GetAd(context.Background(), made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Script != script {
		t.Errorf("stored %q, want the script that was posted", got.Script)
	}
}

// TestAdsAPISaveValidates: the cap is what makes an advert fit its slot, so an
// operator's own text is held to it too -- a 900-character advert is not a
// style choice, it is a break that gets dropped. A real brand is ACCEPTED.
func TestAdsAPISaveValidates(t *testing.T) {
	s, st, _ := adsServer(t)
	admin := adminCookie(t, s)

	for _, c := range []struct{ name, brand, script string }{
		{"over the slot", "Stillwater", "Stillwater. " + strings.Repeat("mugs ", 200)},
		{"never says the name", "Stillwater",
			"A shop that sells things you did not know you needed until today."},
		{"too short to be one", "Stillwater", "Stillwater. Mugs."},
		{"degenerate run", "Stillwater",
			"Stillwater mugs mugs mugs mugs mugs and more of them than anyone needs."},
		{"no brand", "", "Stillwater. Mugs, thrown by hand, and still here tomorrow."},
	} {
		rec := as(t, s, http.MethodPost, "/admin/ads", savePost(c.brand, "", "", c.script), admin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", c.name, rec.Code, rec.Body)
			continue
		}
		field, msg := errorOf(t, rec.Body.Bytes())
		if field == "" || msg == "" {
			t.Errorf("%s: no field error the console can attach to a box: %s", c.name, rec.Body)
		}
	}

	// A REAL BRAND IS ACCEPTED. The operator's client may be a real company,
	// and refusing theirs would make the whole feature useless to them.
	rec := as(t, s, http.MethodPost, "/admin/ads",
		savePost("Coca-Cola", "the drink", "",
			"Coca-Cola. The one you already know, cold, at the shop on the corner."), admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("a real brand was refused on save with %d: %s", rec.Code, rec.Body)
	}
	rows, err := st.ListAds(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("%d adverts stored (%v)", len(rows), err)
	}

	// The same rules on the way through PUT, or an advert can be edited past
	// the cap it could not be created past.
	if rec := as(t, s, http.MethodPut, "/admin/ads/"+strconv.FormatInt(rows[0].ID, 10),
		savePost("Stillwater", "", "", "Stillwater. Mugs."), admin); rec.Code != http.StatusBadRequest {
		t.Errorf("editing past the rules = %d, want 400", rec.Code)
	}
	// And a body that is not JSON at all, on both routes that save.
	if rec := as(t, s, http.MethodPost, "/admin/ads", "not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
	if rec := as(t, s, http.MethodPut, "/admin/ads/"+strconv.FormatInt(rows[0].ID, 10),
		"not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT with a body that is not JSON = %d, want 400", rec.Code)
	}

	// THE SAME FIELD CAPS THE WRITE FORM HAS. An operator who drafted a blurb,
	// edited it and pressed save must not meet the flat 4KB 400 on a box that
	// warned them nothing while they typed.
	good := "Stillwater. Mugs, thrown by hand, and still here tomorrow morning."
	for _, c := range []struct{ name, field, body string }{
		{"no script", "script", savePost("Stillwater", "", "", "   ")},
		{"brief over the cap", "brief",
			savePost("Stillwater", strings.Repeat("m", MaxAdAboutRunes+1), "", good)},
		{"delivery over the cap", "delivery",
			savePost("Stillwater", "", strings.Repeat("d", MaxAdDeliveryRunes+1), good)},
	} {
		rec := as(t, s, http.MethodPost, "/admin/ads", c.body, admin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", c.name, rec.Code, rec.Body)
			continue
		}
		if field, _ := errorOf(t, rec.Body.Bytes()); field != c.field {
			t.Errorf("%s named %q, want %q", c.name, field, c.field)
		}
	}
}

// TestAdsAPIStoreFails: a broken table is a 500 with the reason, not a 200 with
// an empty list -- an empty list is "you have no adverts", which is a different
// thing and one nobody would investigate.
func TestAdsAPIStoreFails(t *testing.T) {
	s, st, _ := adsServer(t)
	admin := adminCookie(t, s)
	if _, err := st.DB().ExecContext(context.Background(), `DROP TABLE ads`); err != nil {
		t.Fatal(err)
	}
	good := savePost("Stillwater", "", "",
		"Stillwater. Mugs, thrown by hand, and still here tomorrow morning.")
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/ads", ""},
		{http.MethodPost, "/admin/ads", good},
		{http.MethodPut, "/admin/ads/1", good},
		{http.MethodDelete, "/admin/ads/1", ""},
		{http.MethodPost, "/admin/ads/1/disable", ""},
		{http.MethodPost, "/admin/ads/1/enable", ""},
	} {
		rec := as(t, s, c.method, c.path, c.body, admin)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s = %d, want 500: %s", c.method, c.path, rec.Code, rec.Body)
			continue
		}
		if _, msg := errorOf(t, rec.Body.Bytes()); msg == "" {
			t.Errorf("%s %s carried no reason", c.method, c.path)
		}
	}
}

// TestAdsAPINeedsAdmin: adverts are what the station SAYS and what the operator
// SELLS. A listener has no business anywhere near them.
func TestAdsAPINeedsAdmin(t *testing.T) {
	s, _, _ := adsServer(t)
	listener := listenerCookie(t, s)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/ads", ""},
		{http.MethodPost, "/admin/ads", savePost("A", "", "", "A. Something.")},
		{http.MethodPost, "/admin/ads/write", `{"brand":"A","about":"b"}`},
		{http.MethodPut, "/admin/ads/1", savePost("A", "", "", "A. Something.")},
		{http.MethodDelete, "/admin/ads/1", ""},
	} {
		if rec := as(t, s, c.method, c.path, c.body, listener); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a listener = %d, want 403", c.method, c.path, rec.Code)
		}
	}
}

// TestAdsAPIUnwired: 503, the way s.stations nil already does. A build with no
// ads wired must not panic on a route the console will call anyway.
func TestAdsAPIUnwired(t *testing.T) {
	s, _, _ := authServer(t)
	admin := adminCookie(t, s)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/admin/ads"},
		{http.MethodPost, "/admin/ads/write"},
		{http.MethodDelete, "/admin/ads/1"},
	} {
		if rec := as(t, s, c.method, c.path, "", admin); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s unwired = %d, want 503", c.method, c.path, rec.Code)
		}
	}
	// A method nothing claims is 405, not a 404 that reads as a missing route.
	s2, _, _ := adsServer(t)
	a2 := adminCookie(t, s2)
	if rec := as(t, s2, http.MethodPatch, "/admin/ads", "", a2); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PATCH /admin/ads = %d, want 405", rec.Code)
	}
	if rec := as(t, s2, http.MethodGet, "/admin/ads/1", "", a2); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET one advert = %d, want 405", rec.Code)
	}
	// /write is a POST. A GET must not read as a missing route, or whoever is
	// wiring the console goes looking for a registration bug that is not there.
	if rec := as(t, s2, http.MethodGet, "/admin/ads/write", "", a2); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /admin/ads/write = %d, want 405", rec.Code)
	}
	// And a sub-path nothing serves is a 404.
	if rec := as(t, s2, http.MethodPut, "/admin/ads/1/rewrite", "", a2); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown sub-path = %d, want 404", rec.Code)
	}
}

// ------------------------------------------------------------ Jockora-e9a.55 --
//
// An advert could only be created or destroyed, never paused, while sources,
// stations and accounts all disable. A seasonal advertiser cost the operator
// hand-written copy and a retype.

func TestAdsAPIDisableAndEnable(t *testing.T) {
	s, st, _ := adsServer(t)
	admin := adminCookie(t, s)
	ctx := context.Background()

	id, err := st.CreateAd(ctx, store.Ad{Brand: "Stillwater",
		Script: "Stillwater. Mugs, thrown by hand, and still here tomorrow."})
	if err != nil {
		t.Fatal(err)
	}
	at := "/admin/ads/" + strconv.FormatInt(id, 10)

	// ON FIRST. Without this the assertion below is satisfied by a view that
	// dropped the field altogether -- which is exactly what a mutation did, and
	// it survived until this line existed.
	if list := adsList(t, s, admin); len(list) != 1 || !list[0].Enabled {
		t.Fatalf("a new advert reads as %+v, want it on air", list)
	}

	if rec := as(t, s, http.MethodPost, at+"/disable", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("disable = %d: %s", rec.Code, rec.Body)
	}
	off, err := st.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if off.Enabled {
		t.Error("the row is still enabled")
	}
	// STILL IN THE LIST. Disabling is not deleting, and an advert the operator
	// cannot see is an advert they cannot enable again.
	if list := adsList(t, s, admin); len(list) != 1 || list[0].Enabled {
		t.Errorf("the list is %+v, want the disabled advert still on it", list)
	}

	if rec := as(t, s, http.MethodPost, at+"/enable", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("enable = %d: %s", rec.Code, rec.Body)
	}
	if on, err := st.GetAd(ctx, id); err != nil || !on.Enabled {
		t.Errorf("the advert did not come back on: %+v %v", on, err)
	}
}

// TestAdsAPIDisableIsNotADelete: an edit must not carry the flag. The console
// saves by writing the whole card back, so an advert paused this morning would
// come back on the moment somebody fixed a typo in it.
func TestAdsAPIDisableIsNotADelete(t *testing.T) {
	s, st, _ := adsServer(t)
	admin := adminCookie(t, s)
	ctx := context.Background()

	id, err := st.CreateAd(ctx, store.Ad{Brand: "Stillwater",
		Script: "Stillwater. Mugs, thrown by hand, and still here tomorrow."})
	if err != nil {
		t.Fatal(err)
	}
	at := "/admin/ads/" + strconv.FormatInt(id, 10)
	if rec := as(t, s, http.MethodPost, at+"/disable", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("disable = %d", rec.Code)
	}

	rec := as(t, s, http.MethodPut, at, savePost("Stillwater", "mugs", "flat",
		"Stillwater. Twelve years now, and still the one mug."), admin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body)
	}
	got, err := st.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Error("saving an edit turned a paused advert back on")
	}
	if !strings.Contains(got.Script, "Twelve years") {
		t.Errorf("the edit did not land: %q", got.Script)
	}
}

func TestAdsAPIDisableMissing(t *testing.T) {
	s, _, _ := adsServer(t)
	admin := adminCookie(t, s)
	if rec := as(t, s, http.MethodPost, "/admin/ads/9999/disable", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("= %d, want 404", rec.Code)
	}
	// An action nobody defined is a 404, not a 405 and not a silent success.
	if rec := as(t, s, http.MethodPost, "/admin/ads/1/incinerate", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown action = %d, want 404", rec.Code)
	}
	// And enable is a POST like everywhere else in this console.
	if rec := as(t, s, http.MethodGet, "/admin/ads/1/enable", "", admin); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET enable = %d, want 405", rec.Code)
	}
}

// adsList reads the console's own view of the list.
func adsList(t *testing.T, s *Server, admin *http.Cookie) []adView {
	t.Helper()
	rec := as(t, s, http.MethodGet, "/admin/ads", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body)
	}
	var out []adView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("list body: %v", err)
	}
	return out
}
