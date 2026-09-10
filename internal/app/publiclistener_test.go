// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/enrich"
)

// TestPublicListenerDefaultsToClosed: an install that has never been asked
// wants a login. Every other default here is a matter of taste; this one is the
// difference between a private server and a public one.
func TestPublicListenerDefaultsToClosed(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	a.applyStoredPublicListener(context.Background())
	if a.PublicListener() {
		t.Fatal("a database that was never asked reported public listening")
	}
}

// TestPublicListenerOutlivesARestart: the operator's answer is a decision about
// the install, and one that quietly reverted on a restart would either lock a
// household out or reopen a server they had closed.
func TestPublicListenerOutlivesARestart(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	if err := a.SetPublicListener(true); err != nil {
		t.Fatalf("opening: %v", err)
	}
	if !a.PublicListener() {
		t.Fatal("the switch reported success and did not move")
	}

	// The restart: a brand new App over the same database, as main.go builds.
	b := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}
	b.applyStoredPublicListener(ctx)
	if !b.PublicListener() {
		t.Error("after a restart the listener page asks for a login again")
	}

	// And closing it survives too, which is the direction that matters most.
	if err := a.SetPublicListener(false); err != nil {
		t.Fatalf("closing: %v", err)
	}
	c := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}
	c.applyStoredPublicListener(ctx)
	if c.PublicListener() {
		t.Error("a door the operator closed was open again after a restart")
	}
}

// TestPublicListenerStoredNonsenseStaysClosed: every unreadable answer falls
// the safe way. The unsafe way is a server on somebody's network streaming to
// anyone who finds it.
func TestPublicListenerStoredNonsenseStaysClosed(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	for _, stored := range []string{"banana", "yes please", ""} {
		a.publicListener.Store(false)
		if err := s.SetSetting(ctx, publicListenerSetting, stored); err != nil {
			t.Fatal(err)
		}
		a.applyStoredPublicListener(ctx)
		if a.PublicListener() {
			t.Errorf("stored %q opened the listener page", stored)
		}
	}

	// A stored value that IS readable is still honoured -- the guard above is
	// about nonsense, not about refusing the setting.
	if err := s.SetSetting(ctx, publicListenerSetting, "1"); err != nil {
		t.Fatal(err)
	}
	a.applyStoredPublicListener(ctx)
	if !a.PublicListener() {
		t.Error(`stored "1" was treated as nonsense`)
	}
}

// TestPublicListenerWithNoStoreDoesNotPanic: the spike path has no database,
// and the switch has to be a no-op there rather than a crash on startup.
func TestPublicListenerWithNoStoreDoesNotPanic(t *testing.T) {
	a := &App{opts: Options{}, cfg: testConfig(t), log: quietLogger()}
	a.applyStoredPublicListener(context.Background())
	if err := a.SetPublicListener(true); err != nil {
		t.Fatalf("SetPublicListener with no store: %v", err)
	}
	if !a.PublicListener() {
		t.Error("the switch did not move in memory")
	}
}

// TestPublicListenerAppearsInTheOverview: the console renders the toggle from
// this payload, so a switch the operator cannot see the state of is a switch
// they will flip twice.
func TestPublicListenerAppearsInTheOverview(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	got, ok := a.Overview().(map[string]any)["public_listener"]
	if !ok {
		t.Fatal("the overview does not report public_listener")
	}
	if got != false {
		t.Errorf("public_listener = %v, want false", got)
	}
	if err := a.SetPublicListener(true); err != nil {
		t.Fatal(err)
	}
	if got := a.Overview().(map[string]any)["public_listener"]; got != true {
		t.Errorf("after opening, public_listener = %v, want true", got)
	}
}

// TestEnrichRecallDefaultsToOffAndOutlivesARestart: the one setting that lets
// the model be its own source, so an install nobody has asked stays strict --
// and an operator who answered does not have to answer again.
func TestEnrichRecallDefaultsToOffAndOutlivesARestart(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	a.applyStoredEnrichRecall(ctx)
	if a.EnrichRecall() {
		t.Fatal("a database that was never asked allowed recall")
	}

	if err := a.SetEnrichRecall(true); err != nil {
		t.Fatalf("turning it on: %v", err)
	}
	b := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}
	b.applyStoredEnrichRecall(ctx)
	if !b.EnrichRecall() {
		t.Error("the setting did not survive a restart")
	}

	// Off again, which is the direction that has to work.
	if err := a.SetEnrichRecall(false); err != nil {
		t.Fatal(err)
	}
	c := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}
	c.applyStoredEnrichRecall(ctx)
	if c.EnrichRecall() {
		t.Error("recall was back on after a restart")
	}
}

// TestEnrichRecallStoredNonsenseStaysOff: every unreadable value falls the
// strict way. The loose direction is a DJ asserting on air something nothing
// ever looked up.
func TestEnrichRecallStoredNonsenseStaysOff(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	for _, stored := range []string{"banana", "", "sure", "true", "-1"} {
		a.enrichRecallSince.Store(0)
		if err := s.SetSetting(ctx, enrichRecallSetting, stored); err != nil {
			t.Fatal(err)
		}
		a.applyStoredEnrichRecall(ctx)
		// THE OUTCOME, not the branch. "-1" parses fine and is stored; it reads
		// as off through the same greater-than-zero rule that zero does, which
		// is why there is no separate guard for it.
		if a.EnrichRecall() {
			t.Errorf("stored %q turned recall on", stored)
		}
		if a.redoCutoff() > 0 {
			t.Errorf("stored %q offered a redo cutoff of %d", stored, a.redoCutoff())
		}
	}
	// And with no store at all it is a no-op rather than a crash.
	(&App{opts: Options{}, cfg: testConfig(t), log: quietLogger()}).applyStoredEnrichRecall(ctx)
}

// TestEnrichRecallAppearsInTheOverview: the console renders the switch and the
// redo count from this payload.
func TestEnrichRecallAppearsInTheOverview(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	got := a.Overview().(map[string]any)
	if got["enrich_recall"] != false {
		t.Errorf("enrich_recall = %v, want false", got["enrich_recall"])
	}
	if _, ok := got["dossiers_without_meaning"]; !ok {
		t.Error("the overview does not say how many dossiers a redo would clear")
	}
	if err := a.SetEnrichRecall(true); err != nil {
		t.Fatal(err)
	}
	if a.Overview().(map[string]any)["enrich_recall"] != true {
		t.Error("the overview did not follow the setting")
	}
}

// TestRedoUnknownDossiersWakesTheParkedWorker: clearing rows is only half of
// it.
//
// The enricher's Run RETURNS when there is no work and the goroutine parks on
// a wake channel that only SetEnriching sends to. So a redo that deletes four
// thousand dossiers and does not wake it leaves the operator watching a queue
// that never starts, with nothing on screen to say why.
func TestRedoUnknownDossiersWakesTheParkedWorker(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	// NOTHING IS OFFERED UNTIL THE SETTINGS CHANGE. Re-running enrichment under
	// the settings that produced a dossier reproduces it exactly.
	if n, err := a.UnknownDossierCount(ctx); n != 0 || err != nil {
		t.Fatalf("count with recall off = %d (%v), want 0", n, err)
	}

	// Turning recall on stamps the cutoff, and the fixture's own dossiers --
	// written with created_at 0, the shape a real library holds -- fall before
	// it.
	if err := a.SetEnrichRecall(true); err != nil {
		t.Fatal(err)
	}
	before, err := a.UnknownDossierCount(ctx)
	if err != nil || before == 0 {
		t.Fatalf("the fixture has %d dossiers with no meaning (%v); this test needs some", before, err)
	}
	a.enriching.Store(false)

	n, err := a.RedoUnknownDossiers(ctx)
	if err != nil {
		t.Fatalf("redo: %v", err)
	}
	if n != before {
		t.Fatalf("cleared %d, want the %d that had no meaning", n, before)
	}
	if !a.enriching.Load() {
		t.Error("the dossier was cleared and the enricher was left parked")
	}
}

// TestRedoUnknownDossiersWithNothingToDoLeavesTheWorkerAlone: the other half.
// A redo that clears nothing must not switch enrichment back on behind an
// operator who deliberately paused it.
func TestRedoUnknownDossiersWithNothingToDoLeavesTheWorkerAlone(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}
	if err := a.SetEnrichRecall(true); err != nil {
		t.Fatal(err)
	}
	// Clear them once so the second redo genuinely has nothing to do.
	if _, err := a.RedoUnknownDossiers(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.enriching.Store(false)

	n, err := a.RedoUnknownDossiers(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("redo = %d, %v; want 0 and no error", n, err)
	}
	if a.enriching.Load() {
		t.Error("a redo that cleared nothing un-paused enrichment")
	}
}

// TestRedoUnknownDossiersNeedsALibrary: pressing the button on a server with no
// database is an error the operator can read, not a panic.
func TestRedoUnknownDossiersNeedsALibrary(t *testing.T) {
	a := &App{opts: Options{}, cfg: testConfig(t), log: quietLogger()}
	if _, err := a.RedoUnknownDossiers(context.Background()); err == nil {
		t.Error("a redo with no library reported success")
	}
	// The count is the softer half: it answers zero rather than failing, so the
	// overview does not break on a server that has no library yet.
	if n, err := a.UnknownDossierCount(context.Background()); n != 0 || err != nil {
		t.Errorf("count = %d, %v; want 0 and no error", n, err)
	}
}

// TestWireEnricherHandsOverBothSwitches: the handoff from the App to the
// background worker.
//
// It is asserted separately because it is the one place a broken switch is
// invisible: with the recall line deleted, every other test in this tree still
// passes and the feature does nothing at all.
func TestWireEnricherHandsOverBothSwitches(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	q := &enrich.Queue{}
	a := &App{
		opts: Options{Library: &Library{Store: s}, Enricher: q},
		cfg:  testConfig(t), log: quietLogger(),
	}

	a.wireEnricher()
	if q.Recall == nil {
		t.Fatal("the enricher was never told whether it may recall")
	}
	if q.Paused == nil {
		t.Fatal("the enricher was never told whether it may run")
	}

	// AND IT IS THE LIVE ANSWER, not a copy. An operator throwing the switch
	// mid-library must be obeyed on the next track, not at the next restart.
	if q.Recall() {
		t.Error("recall was on before anybody asked for it")
	}
	if err := a.SetEnrichRecall(true); err != nil {
		t.Fatal(err)
	}
	if !q.Recall() {
		t.Error("the worker kept a stale copy of the switch")
	}

	// No enricher is the spike path, and must not panic.
	(&App{opts: Options{}, cfg: testConfig(t), log: quietLogger()}).wireEnricher()
}

// TestRedoStopsOfferingItselfOnceTheSettingsHaveBeenApplied.
//
// THE LOOP THIS PREVENTS COSTS HOURS. Recall does not rescue every track -- a
// model refuses what it does not know, which is correct -- so re-enrichment
// writes a fresh meaningless dossier for each track it still cannot describe.
// Without a cutoff the console offers to clear those too, the operator spends a
// night of model time reproducing them exactly, and it offers again.
func TestRedoStopsOfferingItselfOnceTheSettingsHaveBeenApplied(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	if err := a.SetEnrichRecall(true); err != nil {
		t.Fatal(err)
	}
	first, err := a.RedoUnknownDossiers(ctx)
	if err != nil || first == 0 {
		t.Fatalf("the first redo cleared %d (%v); this test needs some", first, err)
	}

	// Enrichment runs and the model still cannot describe those tracks, so a
	// fresh meaningless dossier is written for each. Simulated at the current
	// time, which is what the enricher would do.
	now := time.Now().Unix()
	for i := int64(1); i <= 3; i++ {
		if _, err := s.DB().ExecContext(ctx,
			`INSERT OR REPLACE INTO dossiers (track_id, json, confidence, created_at)
			 VALUES (?, '{"subject_summary":"","themes":[]}', 'none', ?)`, i, now); err != nil {
			t.Fatal(err)
		}
	}

	if n, err := a.UnknownDossierCount(ctx); n != 0 || err != nil {
		t.Errorf("the console would offer %d dossiers again (%v); the redo loops", n, err)
	}
	if n, err := a.RedoUnknownDossiers(ctx); n != 0 || err != nil {
		t.Errorf("a second redo cleared %d (%v), want 0", n, err)
	}
}

// TestRecallCutoffOnlyMovesOnTheTransition: flicking the switch to the value it
// already holds must not re-offer the whole library.
func TestRecallCutoffOnlyMovesOnTheTransition(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	if err := a.SetEnrichRecall(true); err != nil {
		t.Fatal(err)
	}
	first := a.redoCutoff()
	if first == 0 {
		t.Fatal("turning recall on did not stamp a cutoff")
	}
	// Forced backwards so a re-stamp would be visible however fast this runs.
	// ONE SETTING holds both the switch and the moment, so this is the same
	// row the toggle writes -- they cannot disagree.
	if err := s.SetSetting(ctx, enrichRecallSetting, "1000"); err != nil {
		t.Fatal(err)
	}
	a.applyStoredEnrichRecall(ctx)
	if err := a.SetEnrichRecall(true); err != nil {
		t.Fatal(err)
	}
	if got := a.redoCutoff(); got != 1000 {
		t.Errorf("cutoff moved to %d on a no-op write; the whole library would be re-offered", got)
	}
	if !a.EnrichRecall() {
		t.Error("a stored moment did not read as the switch being on")
	}

	// And with recall off there is nothing to offer at all, whatever is stored.
	if err := a.SetEnrichRecall(false); err != nil {
		t.Fatal(err)
	}
	if got := a.redoCutoff(); got != 0 {
		t.Errorf("cutoff = %d with recall off, want 0", got)
	}
}

// TestAFailedSaveLeavesTheDoorWhereItWas.
//
// THE ORDER OF TWO LINES, and it decides whether a refusal is safe. Applying
// the switch in memory before persisting meant a failed write returned an error
// while the door had already moved: the console reverts its checkbox and says
// the change did not happen, the operator believes the server is shut, and it
// is open.
//
// The store is broken by closing the database underneath it, which is the
// bluntest real failure available and needs no fake.
func TestAFailedSaveLeavesTheDoorWhereItWas(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}

	// Prove the switch works at all first, or a broken store would make this
	// test pass for the wrong reason.
	if err := a.SetPublicListener(true); err != nil {
		t.Fatalf("opening: %v", err)
	}
	if !a.PublicListener() {
		t.Fatal("the switch did not move on a working store")
	}
	if err := a.SetPublicListener(false); err != nil {
		t.Fatal(err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	if err := a.SetPublicListener(true); err == nil {
		t.Fatal("a write to a closed database reported success")
	}
	if a.PublicListener() {
		t.Error("the door opened on a write that failed; the console would say it is shut")
	}

	// The same rule for the other switch. Less dangerous, same failure shape.
	if err := a.SetEnrichRecall(true); err == nil {
		t.Fatal("a write to a closed database reported success")
	}
	if a.EnrichRecall() {
		t.Error("recall turned on on a write that failed")
	}

	// And a redo against the same broken store reports the failure rather than
	// answering "cleared 0", which reads exactly like success with nothing to
	// do -- and would leave the operator waiting for enrichment that is not
	// coming.
	if n, err := a.RedoUnknownDossiers(context.Background()); err == nil {
		t.Errorf("a redo on a closed database returned %d and no error", n)
	}
}

// TestTheRecallSwitchHoldsUnderConcurrentUse.
//
// The switch is written by an HTTP handler and read by the enrichment
// goroutine once per track, which is why it is an atomic rather than a plain
// field -- and nothing exercised those two at the same time. Run with -race;
// it asserts no outcome beyond "nothing tore".
//
// The redo runs alongside, because it deletes rows from the same database the
// enricher writes to and reads a cutoff the switch is moving underneath it.
func TestTheRecallSwitchHoldsUnderConcurrentUse(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}
	q := &enrich.Queue{}
	a.opts.Enricher = q
	a.wireEnricher()

	var wg sync.WaitGroup
	// The operator, flipping it.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = a.SetEnrichRecall(n%2 == 0)
		}(i)
	}
	// The enrichment goroutine, asking once per track.
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = q.Recall()
			_ = a.EnrichRecall()
			_ = a.redoCutoff()
		}()
	}
	// And the console, polling the overview and pressing the button.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.Overview()
			_, _ = a.UnknownDossierCount(context.Background())
		}()
	}
	wg.Wait()

	// Left in a known state, and still working.
	if err := a.SetEnrichRecall(true); err != nil {
		t.Fatalf("the switch stopped working: %v", err)
	}
	if !a.EnrichRecall() || a.redoCutoff() <= 0 {
		t.Errorf("recall=%v cutoff=%d after the load", a.EnrichRecall(), a.redoCutoff())
	}
}

// TestTheWholeRecallLifecycle walks the sequence an operator actually performs,
// across restarts, because every test above checks one step of it.
//
// The steps interact: the cutoff is stamped by the switch, read by the redo,
// and survives a restart; and turning the switch off and on again is the
// documented way to re-offer a library. Nothing checked that the whole path
// ends where the documentation says it does.
func TestTheWholeRecallLifecycle(t *testing.T) {
	s := openUpgraded(t, v01Fixture(t))
	ctx := context.Background()
	newApp := func() *App {
		a := &App{opts: Options{Library: &Library{Store: s}}, cfg: testConfig(t), log: quietLogger()}
		a.applyStoredEnrichRecall(ctx)
		a.applyStoredPublicListener(ctx)
		return a
	}

	// 1. A library enriched under the old rules. Nothing is offered, because
	//    re-running enrichment unchanged reproduces what is already there.
	a := newApp()
	if n, _ := a.UnknownDossierCount(ctx); n != 0 {
		t.Fatalf("step 1: %d offered before the setting changed, want 0", n)
	}

	// 2. The operator turns recall on. Now the old dossiers are worth redoing.
	//
	//    THE STAMP IS THEN PINNED TO A KNOWN PAST SECOND. Every step below
	//    turns on when a row was written relative to when the switch moved, and
	//    a test that runs inside one second cannot express "after" at all --
	//    which is a property of the test, not of the system. Pinning it makes
	//    the arithmetic say what the scenario means.
	if err := a.SetEnrichRecall(true); err != nil {
		t.Fatal(err)
	}
	const switchedOn = 1_700_000_000
	if err := s.SetSetting(ctx, enrichRecallSetting, strconv.FormatInt(switchedOn, 10)); err != nil {
		t.Fatal(err)
	}
	a.applyStoredEnrichRecall(ctx)
	offered, _ := a.UnknownDossierCount(ctx)
	if offered == 0 {
		t.Fatal("step 2: turning recall on offered nothing; the fixture has empty dossiers")
	}

	// 3. A restart. The answer and the cutoff both survive it.
	b := newApp()
	if !b.EnrichRecall() {
		t.Error("step 3: recall was off after a restart")
	}
	if n, _ := b.UnknownDossierCount(ctx); n != offered {
		t.Errorf("step 3: %d offered after a restart, want %d", n, offered)
	}

	// 4. The redo. Everything offered is cleared, and pressing again does
	//    nothing -- which is the loop terminating.
	cleared, err := b.RedoUnknownDossiers(ctx)
	if err != nil || cleared != offered {
		t.Fatalf("step 4: cleared %d (%v), want %d", cleared, err, offered)
	}
	if again, _ := b.RedoUnknownDossiers(ctx); again != 0 {
		t.Errorf("step 4: a second redo cleared %d, want 0", again)
	}

	// 5. Enrichment runs and the model still cannot place some of them, so
	//    fresh empty dossiers are written. They are not offered again.
	//
	//    AFTER the switch, which is when enrichment actually ran.
	now := int64(switchedOn + 10)
	for i := int64(1); i <= 5; i++ {
		if _, err := s.DB().ExecContext(ctx,
			`INSERT OR REPLACE INTO dossiers (track_id, json, confidence, created_at)
			 VALUES (?, '{"subject_summary":"","themes":[]}', 'none', ?)`, i, now); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := b.UnknownDossierCount(ctx); n != 0 {
		t.Errorf("step 5: %d offered again after re-enrichment; the redo loops", n)
	}

	// 6. The operator turns it off. Nothing is offered, whatever is stored.
	if err := b.SetEnrichRecall(false); err != nil {
		t.Fatal(err)
	}
	if b.EnrichRecall() || b.redoCutoff() != 0 {
		t.Errorf("step 6: recall=%v cutoff=%d after turning it off", b.EnrichRecall(), b.redoCutoff())
	}
	if n, _ := b.UnknownDossierCount(ctx); n != 0 {
		t.Errorf("step 6: %d offered with recall off, want 0", n)
	}

	// 7. And on again -- the documented escape hatch for changing models. It
	//    re-stamps the cutoff to NOW, which is later than step 5's writes, so
	//    those are worth another try under whatever model is loaded next.
	if err := b.SetEnrichRecall(true); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.UnknownDossierCount(ctx); n != 5 {
		t.Errorf("step 7: %d offered after turning it off and on, want the 5 from step 5", n)
	}

	// 8. That state survives one more restart, which is where this began.
	c := newApp()
	if !c.EnrichRecall() {
		t.Error("step 8: recall was off after the final restart")
	}
	if n, _ := c.UnknownDossierCount(ctx); n != 5 {
		t.Errorf("step 8: %d offered after a restart, want 5", n)
	}
}
