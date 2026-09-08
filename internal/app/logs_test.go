// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/store"

	"github.com/andrewloable/jockora/internal/obs"
)

// Jockora-69n.3. The level was fixed at build time, so getting more detail
// meant changing a flag and redeploying -- which restarts the station, cuts off
// whoever is listening, and may destroy the very state being investigated.

func levelApp(t *testing.T) *App {
	t.Helper()
	a, _ := llmApp(t)
	a.level = new(slog.LevelVar)
	return a
}

func TestLogLevelRoundTrip(t *testing.T) {
	a := levelApp(t)
	ctx := context.Background()

	if got := a.LogLevel(); got != slog.LevelInfo {
		t.Errorf("LogLevel = %v before anything is set, want info", got)
	}
	if err := a.SetLogLevel(ctx, slog.LevelDebug); err != nil {
		t.Fatalf("SetLogLevel: %v", err)
	}
	if got := a.LogLevel(); got != slog.LevelDebug {
		t.Errorf("LogLevel = %v after setting debug", got)
	}

	// STORED, so it survives the restart it exists to avoid needing.
	raw, ok, err := a.opts.Library.Store.Setting(ctx, LogLevelSetting)
	if err != nil || !ok {
		t.Fatalf("the level was not persisted: ok=%v err=%v", ok, err)
	}
	if raw != "debug" {
		t.Errorf("stored %q, want debug", raw)
	}
}

// TestLogLevelTakesEffect is the assertion that matters: the actual behaviour
// through a handler, not a string read back out of a struct.
func TestLogLevelTakesEffect(t *testing.T) {
	a := levelApp(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: a.level}))

	log.Debug("a detail nobody asked for")
	if buf.Len() != 0 {
		t.Errorf("a debug record was kept at info: %s", buf.String())
	}

	if err := a.SetLogLevel(context.Background(), slog.LevelDebug); err != nil {
		t.Fatal(err)
	}
	log.Debug("now they asked")
	if !strings.Contains(buf.String(), "now they asked") {
		t.Error("a debug record was still dropped after switching to debug")
	}

	// And back, without rebuilding the handler -- swapping one under running
	// goroutines is a race, which is what LevelVar exists to avoid.
	buf.Reset()
	if err := a.SetLogLevel(context.Background(), slog.LevelWarn); err != nil {
		t.Fatal(err)
	}
	log.Info("routine")
	if buf.Len() != 0 {
		t.Errorf("an info record survived a switch to warn: %s", buf.String())
	}
}

// TestLogLevelRestores: THE STARTUP ORDERING TRAP. The logger is built before
// the store is open, so the stored level cannot be read when the LevelVar is
// created. It is restored after.
func TestLogLevelRestores(t *testing.T) {
	a := levelApp(t)
	ctx := context.Background()
	if err := a.opts.Library.Store.SetSetting(ctx, LogLevelSetting, "debug"); err != nil {
		t.Fatal(err)
	}
	// A fresh process: the LevelVar starts at its compiled default.
	a.level = new(slog.LevelVar)

	a.RestoreLogLevel(ctx)
	if got := a.LogLevel(); got != slog.LevelDebug {
		t.Errorf("LogLevel = %v after restore, want the stored debug", got)
	}

	// Nothing stored is not an error and must not disturb the default.
	b := levelApp(t)
	b.RestoreLogLevel(ctx)
	if got := b.LogLevel(); got != slog.LevelInfo {
		t.Errorf("LogLevel = %v with nothing stored, want the info default", got)
	}

	// Rubbish stored is not an error either: a level nobody can parse should
	// not stop the station coming up.
	c := levelApp(t)
	if err := c.opts.Library.Store.SetSetting(ctx, LogLevelSetting, "verbose"); err != nil {
		t.Fatal(err)
	}
	c.RestoreLogLevel(ctx)
	if got := c.LogLevel(); got != slog.LevelInfo {
		t.Errorf("LogLevel = %v with rubbish stored, want the info default", got)
	}
}

func TestLogLevelRejects(t *testing.T) {
	a := levelApp(t)
	err := a.SetLogLevelNamed(context.Background(), "verbose")
	if err == nil {
		t.Fatal("accepted a level nobody defines")
	}
	// NAMED, not silently ignored: an operator who typed the wrong word should
	// read a sentence telling them the right ones.
	for _, want := range []string{"debug", "info", "warn", "error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to list %q as valid", err, want)
		}
	}
	if got := a.LogLevel(); got != slog.LevelInfo {
		t.Errorf("a refused level still changed the LevelVar to %v", got)
	}

	// And the four valid names all work, in the case an operator types them.
	for name, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "INFO": slog.LevelInfo,
		"Warn": slog.LevelWarn, "error": slog.LevelError,
	} {
		if err := a.SetLogLevelNamed(context.Background(), name); err != nil {
			t.Errorf("SetLogLevelNamed(%q): %v", name, err)
		} else if got := a.LogLevel(); got != want {
			t.Errorf("SetLogLevelNamed(%q) gave %v, want %v", name, got, want)
		}
	}
}

// TestLogLevelNoStore: the operator asked for debug and the useful half of that
// still works. Refusing to change the live level because it cannot be persisted
// would be the least useful possible reading of the failure.
func TestLogLevelNoStore(t *testing.T) {
	a := &App{log: quietLogger(), level: new(slog.LevelVar)}
	err := a.SetLogLevel(context.Background(), slog.LevelDebug)
	if err == nil {
		t.Error("the failure to persist was not reported at all")
	}
	if got := a.LogLevel(); got != slog.LevelDebug {
		t.Errorf("LogLevel = %v; the live level should have changed regardless", got)
	}
	// And with no LevelVar at all -- a spike-path App, which logs at whatever
	// it was built with -- nothing panics and nothing claims to have worked.
	spike := &App{log: quietLogger()}
	if err := spike.SetLogLevel(context.Background(), slog.LevelDebug); err == nil {
		t.Error("an App with no level var reported success")
	}
	if got := spike.LogLevel(); got != DefaultLogLevel {
		t.Errorf("LogLevel = %v with no level var, want the compiled default %v",
			got, DefaultLogLevel)
	}
	// Restoring one is a no-op rather than a nil dereference at startup.
	spike.RestoreLogLevel(context.Background())

	// A STORE THAT WILL NOT WRITE. The live level still changes -- that is the
	// half the operator asked for -- and the failure to remember it is named.
	a2, st := llmApp(t)
	a2.level = new(slog.LevelVar)
	if err := st.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}
	err = a2.SetLogLevel(context.Background(), slog.LevelWarn)
	if err == nil || !strings.Contains(err.Error(), "not saved") {
		t.Errorf("error = %v, want it to say the level changed but was not saved", err)
	}
	if got := a2.LogLevel(); got != slog.LevelWarn {
		t.Errorf("LogLevel = %v; an unwritable store must not block the live change", got)
	}
	// And restoring from a store that will not read leaves the default alone.
	a2.level = new(slog.LevelVar)
	a2.RestoreLogLevel(context.Background())
	if got := a2.LogLevel(); got != slog.LevelInfo {
		t.Errorf("LogLevel = %v after a failed restore, want the info default", got)
	}
}

// TestLogLevelPersistsWarningsToTheStore: the adapter and the wiring, which the
// unit tests either side of it cannot prove. obs never imports the store and
// the store never learns about slog, so this is the only place the two meet.
func TestLogLevelPersistsWarningsToTheStore(t *testing.T) {
	a, st := llmApp(t)
	a.level = new(slog.LevelVar)
	sink := obs.NewSink(slog.NewJSONHandler(&bytes.Buffer{},
		&slog.HandlerOptions{Level: a.level}), 100)
	a.opts.LogSink = sink

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.persistLogs(ctx)

	log := slog.New(sink)
	log.Info("on air", "station", 2)
	log.Warn("break dropped", "reason", "llm_error", "api_key", "cfat_realtoken")

	var got []store.LogRecord
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = st.RecentLogs(ctx, "WARN", 10)
		if len(got) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("%d records persisted, want just the warning", len(got))
	}
	if got[0].Message != "break dropped" || got[0].Level != "WARN" {
		t.Errorf("persisted the wrong record: %+v", got[0])
	}
	// THE ATTRS ARRIVE REDACTED, because the sink did it before the adapter
	// ever saw them. This is the assertion that keeps the redaction in the one
	// place every record passes through.
	if !strings.Contains(got[0].Attrs, "llm_error") {
		t.Errorf("attrs lost the reason: %q", got[0].Attrs)
	}
	if strings.Contains(got[0].Attrs, "cfat_realtoken") {
		t.Errorf("a key reached the database: %q", got[0].Attrs)
	}

	// A spike-path App has neither and must not panic.
	(&App{log: quietLogger()}).persistLogs(ctx)
	(&App{log: quietLogger(), opts: Options{LogSink: sink}}).persistLogs(ctx)
}

// TestLogLevelPersistsStartupWarningsToTheStore is Jockora-69n.9 at the only
// boundary that can prove it: a warning logged BEFORE the store opened, read
// back out of log_records afterwards. obs proves the sink drains its ring;
// this proves the drained records survive the adapter and land in the table.
//
// The order here is the real one. main builds the sink, app.New logs the
// non-loopback warning through it, and only then does Run reach persistLogs.
func TestLogLevelPersistsStartupWarningsToTheStore(t *testing.T) {
	a, st := llmApp(t)
	a.level = new(slog.LevelVar)
	sink := obs.NewSink(slog.NewJSONHandler(&bytes.Buffer{},
		&slog.HandlerOptions{Level: a.level}), 100)
	a.opts.LogSink = sink

	// Startup: the store is not persisting yet.
	log := slog.New(sink)
	log.Info("migrating", "to", 12)
	log.Warn("SERVING WITHOUT AUTHENTICATION ON A NON-LOOPBACK ADDRESS",
		"detail", "anyone who can reach this address can listen")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.persistLogs(ctx)

	var got []store.LogRecord
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ = st.RecentLogs(ctx, "WARN", 10)
		if len(got) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("%d records persisted, want the startup warning", len(got))
	}
	if got[0].Message != "SERVING WITHOUT AUTHENTICATION ON A NON-LOOPBACK ADDRESS" {
		t.Errorf("persisted %q, want the warning logged before the store opened",
			got[0].Message)
	}
}

// TestLogsAppMergesTheRingAndTheTable: the ring is the live view and the table
// is what survives a restart, and an operator does not care which is which.
// A record in both must appear once.
func TestLogsAppMergesTheRingAndTheTable(t *testing.T) {
	a, st := llmApp(t)
	a.level = new(slog.LevelVar)
	sink := obs.NewSink(slog.NewJSONHandler(&bytes.Buffer{},
		&slog.HandlerOptions{Level: slog.LevelDebug}), 100)
	a.opts.LogSink = sink
	ctx := context.Background()

	// Older, persisted, and NOT in the ring: the crash-loop case this whole
	// task exists for.
	old := time.Unix(1788830000, 0)
	if err := st.AppendLogs(ctx, []store.LogRecord{
		{At: old, Level: "ERROR", Message: "from before the restart", Attrs: `{"err":"boom"}`},
	}); err != nil {
		t.Fatal(err)
	}
	// And one in BOTH, which is every warning for as long as the ring holds it.
	both := time.Unix(1788840000, 0)
	if err := st.AppendLogs(ctx, []store.LogRecord{
		{At: both, Level: "WARN", Message: "in both", Attrs: ""},
	}); err != nil {
		t.Fatal(err)
	}
	slog.New(sink).Warn("in the ring only")

	got, err := a.RecentLogs(ctx, "WARN", 50)
	if err != nil {
		t.Fatalf("RecentLogs: %v", err)
	}
	seen := map[string]int{}
	for _, r := range got {
		seen[r.Message]++
	}
	for _, msg := range []string{"from before the restart", "in the ring only", "in both"} {
		if seen[msg] == 0 {
			t.Errorf("%q is missing from the merged view: %+v", msg, got)
		}
	}
	// NEWEST FIRST across both sources, or the restart boundary shows as a
	// jump backwards in the middle of the list.
	for i := 1; i < len(got); i++ {
		if got[i].Time.After(got[i-1].Time) {
			t.Errorf("out of order at %d: %v after %v", i, got[i].Time, got[i-1].Time)
		}
	}
	// The persisted attrs survive the trip through JSON.
	for _, r := range got {
		if r.Message == "from before the restart" && r.Attrs["err"] != "boom" {
			t.Errorf("persisted attrs were lost: %+v", r.Attrs)
		}
	}

	// A level nobody defines is refused rather than quietly matching nothing.
	if _, err := a.RecentLogs(ctx, "verbose", 10); err == nil {
		t.Error("RecentLogs accepted a level nobody defines")
	}

	// CLEARING EMPTIES BOTH. Clearing one leaves a console saying the log is
	// empty and a database that disagrees.
	if err := a.ClearLogs(ctx); err != nil {
		t.Fatalf("ClearLogs: %v", err)
	}
	if got, _ := a.RecentLogs(ctx, "DEBUG", 50); len(got) != 0 {
		t.Errorf("%d records survived the clear: %+v", len(got), got)
	}
}

// TestLogsAppWithoutARing: every path that is not the server has no sink, and
// must answer honestly rather than panicking.
func TestLogsAppWithoutARing(t *testing.T) {
	a, _ := llmApp(t)
	ctx := context.Background()

	if got, err := a.RecentLogs(ctx, "WARN", 10); err != nil || len(got) != 0 {
		t.Errorf("RecentLogs with no ring = %v (%v)", got, err)
	}
	if _, _, err := a.SubscribeLogs(4); err == nil {
		t.Error("SubscribeLogs reported success with no ring to subscribe to")
	}
	if n := a.DroppedLogs(); n != 0 {
		t.Errorf("DroppedLogs = %d with no ring", n)
	}
	if err := a.ClearLogs(ctx); err != nil {
		t.Errorf("ClearLogs with no ring: %v", err)
	}
	// And with no store either.
	bare := &App{log: quietLogger()}
	if err := bare.ClearLogs(ctx); err != nil {
		t.Errorf("ClearLogs on a bare App: %v", err)
	}
	if got, err := bare.RecentLogs(ctx, "WARN", 10); err != nil || len(got) != 0 {
		t.Errorf("RecentLogs on a bare App = %v (%v)", got, err)
	}
}

// TestLogsAppSubscribes: the live feed the SSE endpoint reads.
func TestLogsAppSubscribes(t *testing.T) {
	a, _ := llmApp(t)
	sink := obs.NewSink(slog.NewJSONHandler(&bytes.Buffer{}, nil), 10)
	a.opts.LogSink = sink

	ch, cancel, err := a.SubscribeLogs(4)
	if err != nil {
		t.Fatalf("SubscribeLogs: %v", err)
	}
	defer cancel()
	slog.New(sink).Warn("on air")
	select {
	case r := <-ch:
		if r.Message != "on air" {
			t.Errorf("received %q", r.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("the subscriber received nothing")
	}
}

// TestLogsAppMergeEdges: the shapes the merge meets on a real install -- a
// limit smaller than the ring, a stored row this build cannot read, and a
// store that will not answer at all.
func TestLogsAppMergeEdges(t *testing.T) {
	a, st := llmApp(t)
	sink := obs.NewSink(slog.NewJSONHandler(&bytes.Buffer{},
		&slog.HandlerOptions{Level: slog.LevelDebug}), 100)
	a.opts.LogSink = sink
	ctx := context.Background()

	// The dropped count comes from the ring it actually has.
	if n := a.DroppedLogs(); n != 0 {
		t.Errorf("DroppedLogs = %d on a fresh ring", n)
	}

	base := time.Unix(1788830000, 0)
	var stored []store.LogRecord
	for i := 0; i < 5; i++ {
		stored = append(stored, store.LogRecord{
			At: base.Add(time.Duration(i) * time.Second), Level: "WARN",
			Message: fmt.Sprintf("stored %d", i),
		})
	}
	// A LEVEL THIS BUILD CANNOT READ and ATTRS THAT WILL NOT PARSE: a
	// hand-edited row, or one written by a newer build. Neither may take the
	// whole view down, and the second must still show its message -- half a
	// record is more use than none when the console is what somebody is using
	// to find out what broke.
	stored = append(stored,
		store.LogRecord{At: base.Add(9 * time.Second), Level: "CATASTROPHE", Message: "unreadable level"},
		store.LogRecord{At: base.Add(10 * time.Second), Level: "WARN",
			Message: "unreadable attrs", Attrs: "{not json"})
	if err := st.AppendLogs(ctx, stored); err != nil {
		t.Fatal(err)
	}
	slog.New(sink).Warn("in the ring")

	got, err := a.RecentLogs(ctx, "WARN", 50)
	if err != nil {
		t.Fatalf("RecentLogs: %v", err)
	}
	var levels, attrs bool
	for _, r := range got {
		if r.Message == "unreadable level" {
			levels = true
		}
		if r.Message == "unreadable attrs" {
			attrs = true
		}
	}
	if levels {
		t.Error("a row with a level this build cannot read was returned as valid")
	}
	if !attrs {
		t.Error("a row with unparseable attrs was dropped; its message was still readable")
	}

	// A LIMIT SMALLER THAN WHAT IS AVAILABLE trims, and trims the OLDEST.
	small, err := a.RecentLogs(ctx, "WARN", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(small) != 2 {
		t.Fatalf("%d records for a limit of 2", len(small))
	}
	if small[0].Message != "in the ring" {
		t.Errorf("the newest is %q; the trim took the wrong end", small[0].Message)
	}

	// A STORE THAT WILL NOT ANSWER is reported rather than silently returning
	// only the ring -- which would look like a working log missing a day.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecentLogs(ctx, "WARN", 50); err == nil {
		t.Error("a failed read of the persisted half reported success")
	}
	// Unless the ring already answered the whole ask, in which case the table
	// is never touched and there is nothing to fail.
	if _, err := a.RecentLogs(ctx, "WARN", 1); err != nil {
		t.Errorf("a request the ring could satisfy still went to the store: %v", err)
	}
}

// TestLogsAppMergeFunction exercises the merge directly, because two of its
// branches cannot be reached through the store: a level the store's own filter
// would never return, and a de-duplication that only fires when the SAME record
// is in the ring and the table -- which is every warning for as long as the
// ring holds it, and which the integration test above did not actually set up.
func TestLogsAppMergeFunction(t *testing.T) {
	at := func(n int) time.Time { return time.Unix(1788840000+int64(n), 0) }

	ring := []obs.Record{
		{Time: at(3), Level: slog.LevelWarn, Message: "in both"},
		{Time: at(2), Level: slog.LevelWarn, Message: "ring only"},
	}
	stored := []store.LogRecord{
		// THE SAME RECORD, which is the normal case: a warning is written to
		// the ring and the table by the same call.
		{At: at(3), Level: "WARN", Message: "in both"},
		{At: at(1), Level: "WARN", Message: "table only"},
		// A level this build cannot read -- a hand-edited row, or one written
		// by a newer build. It is skipped rather than taking the view down.
		{At: at(0), Level: "CATASTROPHE", Message: "unreadable level"},
	}

	got := mergeLogs(append([]obs.Record{}, ring...), stored, 10)
	seen := map[string]int{}
	for _, r := range got {
		seen[r.Message]++
	}
	if seen["in both"] != 1 {
		t.Errorf(`"in both" appears %d times; the same record in the ring and the table is one record`,
			seen["in both"])
	}
	if seen["table only"] != 1 || seen["ring only"] != 1 {
		t.Errorf("a record was lost: %+v", got)
	}
	if seen["unreadable level"] != 0 {
		t.Error("a row with a level this build cannot read was returned as valid")
	}

	// THE LIMIT TRIMS THE OLDEST, and it has to be applied AFTER the merge:
	// the ring alone was under the limit and the merged view is over it.
	trimmed := mergeLogs(append([]obs.Record{}, ring...), stored, 3)
	if len(trimmed) != 3 {
		t.Fatalf("%d records for a limit of 3", len(trimmed))
	}
	if trimmed[0].Message != "in both" || trimmed[2].Message != "table only" {
		t.Errorf("the merge trimmed the wrong end: %+v", trimmed)
	}
}

// TestLogsMergeDedupesAcrossThePrecisionBoundary: a warning is in the ring AND
// in the table, and the console must show it once.
//
// THE TWO HALVES KEEP TIME DIFFERENTLY. The ring holds slog's timestamp with
// nanoseconds; the table stores At.Unix() and reads it back with the
// nanoseconds gone. Keying the dedupe on the formatted time therefore compared
// two strings that can never be equal, and every persisted warning still in the
// ring appeared twice -- which reads as the station having done the thing twice.
func TestLogsMergeDedupesAcrossThePrecisionBoundary(t *testing.T) {
	at := time.Date(2026, 9, 8, 7, 50, 56, 570123000, time.UTC)
	ring := []obs.Record{{Time: at, Level: slog.LevelWarn, Message: "the model refused"}}
	stored := []store.LogRecord{
		// Exactly what RecentLogs returns for the row AppendLogs wrote.
		{At: time.Unix(at.Unix(), 0), Level: "WARN", Message: "the model refused"},
		{At: time.Unix(at.Unix()-60, 0), Level: "ERROR", Message: "a different one"},
	}
	got := mergeLogs(ring, stored, 50)
	if len(got) != 2 {
		t.Fatalf("merged to %d records, want 2 -- the repeat was not spotted: %+v", len(got), got)
	}
	var refused int
	for _, r := range got {
		if r.Message == "the model refused" {
			refused++
		}
	}
	if refused != 1 {
		t.Errorf("the same warning appears %d times", refused)
	}
	// AND THE RING'S COPY SURVIVES, not the table's: it is the one with the
	// full timestamp and the attributes the sink captured.
	for _, r := range got {
		if r.Message == "the model refused" && r.Time.Nanosecond() == 0 {
			t.Error("the table's truncated copy displaced the ring's")
		}
	}
	// A record in the table and NOT in the ring is still merged in.
	if got[1].Message != "a different one" {
		t.Errorf("the older stored record is missing: %+v", got)
	}
}
