// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/server"
	"github.com/andrewloable/jockora/internal/store"
)

// Jockora-iyw.3, the store half.

func TestAdRotationLiveReadsTheStore(t *testing.T) {
	_, st := llmApp(t)
	ctx := context.Background()
	source, aired := AdSource(st), AdAired(st)

	id, err := st.CreateAd(ctx, store.Ad{
		Brand: "Stillwater Ceramics", Script: "Stillwater Ceramics. One mug, still."})
	if err != nil {
		t.Fatal(err)
	}

	got, err := source(ctx)
	if err != nil {
		t.Fatalf("AdSource: %v", err)
	}
	if len(got) != 1 || got[0].Brand != "Stillwater Ceramics" {
		t.Fatalf("AdSource = %+v", got)
	}
	// NEVER AIRED is the zero time, so it is eligible rather than pinned to
	// 1970 at the bottom of the rotation.
	if !got[0].LastAiredAt.IsZero() {
		t.Errorf("LastAiredAt = %v on an advert that never aired", got[0].LastAiredAt)
	}

	// THE ROW ID, AS A STRING. A silent mismatch means the cooldown never
	// matches a row and every advert looks eligible for ever.
	at := time.Unix(1788840000, 0)
	if err := aired(ctx, got[0].ID, at); err != nil {
		t.Fatalf("AdAired: %v", err)
	}
	back, err := st.GetAd(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !back.LastAiredAt.Equal(at) {
		t.Errorf("the row says %v, want %v -- the id did not reach the right row",
			back.LastAiredAt, at)
	}

	// And the source reflects it on the very next read, with nothing rebuilt.
	again, err := source(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !again[0].LastAiredAt.Equal(at) {
		t.Errorf("the pool did not see the airing: %v", again[0].LastAiredAt)
	}
}

func TestAdRotationLiveRefusesAnIdThatIsNotARow(t *testing.T) {
	_, st := llmApp(t)
	// AND FOR THE RIGHT REASON. Without the parse guard the id degrades to 0
	// and the store refuses it as a missing row -- still an error, so a bare
	// non-nil check passes against a bug, and the operator is told "no such
	// advert 0" about an advert called "ad-01".
	err := AdAired(st)(context.Background(), "ad-01", time.Now())
	if err == nil {
		t.Fatal("a pack-local id was accepted as a row id")
	}
	if !strings.Contains(err.Error(), "not a row id") || !strings.Contains(err.Error(), "ad-01") {
		t.Errorf("error is %q, want one naming ad-01 as not a row id", err)
	}
	// And with no store at all, both halves report rather than panicking.
	if _, err := AdSource(nil)(context.Background()); err == nil {
		t.Error("AdSource answered with no library")
	}
	if err := AdAired(nil)(context.Background(), "1", time.Now()); err == nil {
		t.Error("AdAired answered with no library")
	}
}

// TestAdRotationLiveImportsPack: a pack travels with its adverts, and importing
// them makes them editable like everything else.
func TestAdRotationLiveImportsPack(t *testing.T) {
	_, st := llmApp(t)
	ctx := context.Background()

	pack := []dj.Ad{
		{ID: "ad-01", Brand: "Stillwater Ceramics", Script: "Stillwater Ceramics. One mug, still."},
		{ID: "ad-02", Brand: "Harrow & Fen", Script: "Harrow and Fen. Books, and a cat."},
	}
	n, err := ImportPackAds(ctx, st, pack)
	if err != nil {
		t.Fatalf("ImportPackAds: %v", err)
	}
	if n != 2 {
		t.Fatalf("imported %d, want 2", n)
	}

	// IDEMPOTENT. A station restart loads the same packs, and duplicating the
	// pool every time would make the rotation a loop of one advert repeated.
	if n, err := ImportPackAds(ctx, st, pack); err != nil || n != 0 {
		t.Errorf("re-importing added %d adverts (%v)", n, err)
	}
	rows, err := st.ListAds(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%d adverts after two imports (%v)", len(rows), err)
	}

	// AND THEY ARE ORDINARY ROWS NOW: editable and deletable.
	rows[0].Script = "Stillwater Ceramics. Two mugs now, and we are as surprised as you."
	if err := st.UpdateAd(ctx, rows[0]); err != nil {
		t.Fatalf("editing an imported advert: %v", err)
	}
	if err := st.DeleteAd(ctx, rows[1].ID); err != nil {
		t.Fatalf("deleting an imported advert: %v", err)
	}
	left, err := st.ListAds(ctx)
	if err != nil || len(left) != 1 {
		t.Fatalf("%d left after deleting one (%v)", len(left), err)
	}

	// An EDITED advert is not the pack's any more, so re-importing brings the
	// original back rather than silently reverting the operator's edit -- and
	// the deleted one comes back too. That is the honest consequence of
	// matching on content, and it is why the key is documented.
	if n, err := ImportPackAds(ctx, st, pack); err != nil || n != 2 {
		t.Errorf("re-importing after an edit added %d, want the 2 that no longer match (%v)", n, err)
	}

	// Nothing to import is not an error and touches nothing.
	if n, err := ImportPackAds(ctx, st, nil); err != nil || n != 0 {
		t.Errorf("importing nothing = %d (%v)", n, err)
	}
}

// TestAdRotationLiveReportsStoreFailures: every one of these is reported to the
// caller rather than swallowed. AdWriter gives the slot back to the DJ when the
// rotation cannot produce an advert -- breaks are optional, music is not -- and
// that only works if the failure actually travels.
func TestAdRotationLiveReportsStoreFailures(t *testing.T) {
	_, st := llmApp(t)
	ctx := context.Background()
	pack := []dj.Ad{{ID: "ad-01", Brand: "Stillwater", Script: "Stillwater. One mug, still."}}

	// A TRIGGER, so reading still works and only writing fails -- which is the
	// half of ImportPackAds a dropped table can never reach.
	if _, err := st.DB().ExecContext(ctx,
		`CREATE TRIGGER no_ads BEFORE INSERT ON ads BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
		t.Fatal(err)
	}
	if n, err := ImportPackAds(ctx, st, pack); err == nil {
		t.Errorf("importing into a table that refuses inserts reported %d and no error", n)
	}
	if _, err := st.DB().ExecContext(ctx, `DROP TRIGGER no_ads`); err != nil {
		t.Fatal(err)
	}

	// And with the table gone, both readers say so instead of an empty pool --
	// an empty pool is a station with no adverts, which is a different thing
	// and one nobody would investigate.
	if _, err := st.DB().ExecContext(ctx, `DROP TABLE ads`); err != nil {
		t.Fatal(err)
	}
	if got, err := AdSource(st)(ctx); err == nil {
		t.Errorf("AdSource returned %d adverts from a table that is not there", len(got))
	}
	if n, err := ImportPackAds(ctx, st, pack); err == nil {
		t.Errorf("ImportPackAds reported %d and no error with no table", n)
	}
}

// Jockora-iyw.4, the writing half.

// adCompleter answers with one canned advert, or fails.
type adCompleter struct {
	reply string
	err   error
	saw   enrich.CompletionRequest
	calls int
}

func (c *adCompleter) Complete(_ context.Context, req enrich.CompletionRequest) (enrich.Completion, error) {
	c.calls++
	c.saw = req
	if c.err != nil {
		return enrich.Completion{}, c.err
	}
	return enrich.Completion{Content: c.reply}, nil
}

// TestAppWriteAdNoModel: NO MODEL AND A FAILED MODEL ARE DIFFERENT PROBLEMS.
// One sends the operator to the model page and the other sends them to their
// provider, and reporting them alike sends them to configure something that is
// already configured.
func TestAppWriteAdNoModel(t *testing.T) {
	brief := dj.AdBrief{Brand: "Stillwater Ceramics", About: "hand-thrown mugs"}

	nilLLM, _ := llmApp(t)
	nilLLM.opts.LLM = nil
	if _, err := nilLLM.WriteAd(context.Background(), brief); !errors.Is(err, enrich.ErrNoModel) {
		t.Errorf("a nil model gave %v, want ErrNoModel", err)
	}

	// AND AN UNCONFIGURED ONE, which is the state a fresh install is in: the
	// Switchable exists, everything holds it, and nothing has been chosen.
	unset, _ := llmApp(t)
	if _, err := unset.WriteAd(context.Background(), brief); !errors.Is(err, enrich.ErrNoModel) {
		t.Errorf("an unconfigured model gave %v, want ErrNoModel", err)
	}
}

func TestAppWriteAdHappy(t *testing.T) {
	a, _ := llmApp(t)
	c := &adCompleter{reply: `{"brand":"ignored","script":"Stillwater Ceramics. ` +
		`Hand-thrown mugs, one at a time, and they are still there tomorrow."}`}
	a.opts.LLM.Use(c)

	got, err := a.WriteAd(context.Background(), dj.AdBrief{
		Brand: "Stillwater Ceramics", About: "hand-thrown mugs", Delivery: "deadpan"})
	if err != nil {
		t.Fatalf("WriteAd: %v", err)
	}
	// THE BRAND IS THE OPERATOR'S. The model may echo it back differently and
	// the advert is for the thing they named.
	if got.Brand != "Stillwater Ceramics" {
		t.Errorf("brand = %q, want the operator's", got.Brand)
	}
	if !strings.Contains(got.Script, "Stillwater") {
		t.Errorf("script = %q", got.Script)
	}
	// NOT SAVED. Writing is a preview; saving is the API layer's job.
	// (The store is the one llmApp gave us, and nothing here touched it.)
	if rows, err := a.opts.Library.Store.ListAds(context.Background()); err != nil || len(rows) != 0 {
		t.Errorf("WriteAd saved %d adverts (%v)", len(rows), err)
	}
	// Constrained at the SAMPLER, not merely checked afterwards.
	if c.saw.JSONSchema == nil {
		t.Error("the advert was written with no schema")
	}
	if c.saw.NPredict <= 0 {
		t.Errorf("NPredict = %d", c.saw.NPredict)
	}
	// The operator presses the button twice and expects two different blurbs,
	// which is what the writing temperature is for.
	if c.saw.Temperature < enrich.WritingTemperature {
		t.Errorf("Temperature = %v, want at least the writing temperature", c.saw.Temperature)
	}
	// The brief reached the model as data.
	if !strings.Contains(c.saw.Prompt, "hand-thrown mugs") ||
		!strings.Contains(c.saw.Prompt, "deadpan") {
		t.Errorf("the brief did not reach the prompt: %q", c.saw.Prompt)
	}
}

// TestAppWriteAdPropagatesError: a model that failed is not a model that is
// missing, and errors.Is must be able to tell them apart.
func TestAppWriteAdPropagatesError(t *testing.T) {
	a, _ := llmApp(t)
	c := &adCompleter{err: errors.New("402 payment required")}
	a.opts.LLM.Use(c)

	_, err := a.WriteAd(context.Background(), dj.AdBrief{
		Brand: "Stillwater Ceramics", About: "hand-thrown mugs"})
	if err == nil {
		t.Fatal("a failing model produced an advert")
	}
	if errors.Is(err, enrich.ErrNoModel) {
		t.Error("a model that failed was reported as no model at all")
	}
	if !strings.Contains(err.Error(), "402") {
		t.Errorf("the provider's reason was lost: %v", err)
	}
	// NOT RETRIED. A model that refused refuses again, and the reason is the
	// operator's to fix rather than ours to burn four calls papering over.
	if c.calls != 1 {
		t.Errorf("called the model %d times for one refusal", c.calls)
	}
}

// TestAppWriteAdIsWiredToTheConsole: the section is served by the store's rows
// and the App's model, joined. Twelve subsystems in v0.1 were built and never
// wired to a caller; this is the check that stops this one joining them.
func TestAppWriteAdIsWiredToTheConsole(t *testing.T) {
	a, st := llmApp(t)
	var iface server.Ads = consoleAds{Store: st, app: a}

	// The CRUD half is the store's, untouched.
	ctx := context.Background()
	id, err := iface.CreateAd(ctx, store.Ad{Brand: "Stillwater",
		Script: "Stillwater. Mugs, thrown by hand, and still here tomorrow."})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := iface.GetAd(ctx, id); err != nil || got.Brand != "Stillwater" {
		t.Fatalf("GetAd = %+v (%v)", got, err)
	}

	// And the drafting half reaches the App, which with no model configured
	// answers ErrNoModel rather than panicking through a nil field.
	if _, err := iface.WriteAd(ctx, dj.AdBrief{Brand: "Stillwater", About: "mugs"}); !errors.Is(
		err, enrich.ErrNoModel) {
		t.Errorf("WriteAd through the console adapter = %v, want ErrNoModel", err)
	}
}

// TestAdRotationLiveSkipsDisabled is Jockora-e9a.55's real requirement: the
// task asks for the pool exclusion PROVED, not just the flag stored. A flag
// nothing reads is a switch wired to nothing, and the operator would go on
// hearing the advert they had just paused.
func TestAdRotationLiveSkipsDisabled(t *testing.T) {
	_, st := llmApp(t)
	ctx := context.Background()
	source := AdSource(st)

	on, err := st.CreateAd(ctx, store.Ad{
		Brand: "Stillwater Ceramics", Script: "Stillwater Ceramics. One mug, still."})
	if err != nil {
		t.Fatal(err)
	}
	off, err := st.CreateAd(ctx, store.Ad{
		Brand: "Harrow Books", Script: "Harrow Books. A cat, and no computer at all."})
	if err != nil {
		t.Fatal(err)
	}

	// BOTH ARE IN THE POOL FIRST, so this cannot pass against a source that
	// only ever returns one advert.
	if got, err := source(ctx); err != nil || len(got) != 2 {
		t.Fatalf("AdSource = %+v, %v -- want both before either is disabled", got, err)
	}

	if err := st.SetAdEnabled(ctx, off, false); err != nil {
		t.Fatal(err)
	}
	got, err := source(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the pool has %d adverts, want only the enabled one: %+v", len(got), got)
	}
	if got[0].ID != strconv.FormatInt(on, 10) {
		t.Errorf("the pool kept the disabled advert: %+v", got[0])
	}

	// AND IT COMES BACK. Disable is reversible; that is the whole point of it
	// not being Delete.
	if err := st.SetAdEnabled(ctx, off, true); err != nil {
		t.Fatal(err)
	}
	if back, err := source(ctx); err != nil || len(back) != 2 {
		t.Fatalf("AdSource = %+v, %v -- an enabled advert did not come back", back, err)
	}
}
