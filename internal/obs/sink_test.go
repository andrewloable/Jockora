// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package obs

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// Jockora-69n.1. Logs go to stderr and nowhere else, so finding out why a
// station went quiet means ssh and then docker logs. Three live defects on the
// deployment were found exactly that way -- which is the argument for the
// feature and also the proof that the information exists and is out of reach.

// sinkFor wraps a JSON handler over a buffer, so a test can prove the record
// still reaches the real destination unchanged.
func sinkFor(t *testing.T, capacity int) (*Sink, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	// DEBUG, because Enabled defers to the wrapped handler and a JSON handler
	// defaults to Info -- so a debug record never reaches Handle at all. That is
	// the right behaviour and Jockora-69n.3 makes the level switchable; this
	// fixture just has to see every level to test filtering.
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return NewSink(h, capacity), &buf
}

func say(s *Sink, level slog.Level, msg string, attrs ...any) {
	slog.New(s).Log(context.Background(), level, msg, attrs...)
}

// TestSinkPassesThrough: stderr and Seq are the record of truth and this wraps
// them rather than replacing them.
func TestSinkPassesThrough(t *testing.T) {
	s, buf := sinkFor(t, 10)
	say(s, slog.LevelInfo, "break scheduled", "station", 2, "placement", "ramp")

	out := buf.String()
	for _, want := range []string{"break scheduled", `"station":2`, `"placement":"ramp"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the wrapped handler did not receive %q; got %s", want, out)
		}
	}
}

func TestSinkRecent(t *testing.T) {
	s, _ := sinkFor(t, 10)
	say(s, slog.LevelDebug, "debug line")
	say(s, slog.LevelInfo, "info line")
	say(s, slog.LevelWarn, "warn line")
	say(s, slog.LevelError, "error line")

	// NEWEST FIRST: an operator opening the page is looking at what just
	// happened, not at what happened first.
	all := s.Recent(slog.LevelDebug, 100)
	if len(all) != 4 {
		t.Fatalf("Recent returned %d records, want 4", len(all))
	}
	if all[0].Message != "error line" {
		t.Errorf("Recent[0] = %q, want the newest", all[0].Message)
	}

	if got := s.Recent(slog.LevelWarn, 100); len(got) != 2 {
		t.Errorf("Recent at warn returned %d, want 2", len(got))
	}
	if got := s.Recent(slog.LevelDebug, 2); len(got) != 2 {
		t.Errorf("Recent honoured no limit: returned %d, want 2", len(got))
	}
}

// TestSinkRingWraps: fixed size, allocated once, overwritten in place. An
// unbounded log in a process that runs for weeks is a memory leak with a
// justification.
func TestSinkRingWraps(t *testing.T) {
	s, _ := sinkFor(t, 4)
	for i := 0; i < 10; i++ {
		say(s, slog.LevelInfo, string(rune('a'+i)))
	}
	got := s.Recent(slog.LevelDebug, 100)
	if len(got) != 4 {
		t.Fatalf("ring holds %d records, want its capacity of 4", len(got))
	}
	if got[0].Message != "j" || got[3].Message != "g" {
		t.Errorf("ring kept %q..%q, want the last four newest-first", got[0].Message, got[3].Message)
	}
}

func TestSinkSubscribe(t *testing.T) {
	s, _ := sinkFor(t, 10)
	ch, cancel, err := s.Subscribe(4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	say(s, slog.LevelInfo, "on air")
	select {
	case r := <-ch:
		if r.Message != "on air" {
			t.Errorf("subscriber got %q", r.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber received nothing")
	}

	cancel()
	say(s, slog.LevelInfo, "after cancel")
	select {
	case r, ok := <-ch:
		if ok {
			t.Errorf("an unsubscribed channel still received %q", r.Message)
		}
	default:
	}
	// Twice must not panic: a browser closing a tab and an SSE handler
	// returning both reach for this.
	cancel()
}

// TestSinkNeverBlocks IS THE TEST THE FEATURE EXISTS AROUND.
//
// The mixer, the encoder and the pipeline all log. A browser tab that stopped
// reading its SSE connection must not be able to stall the audio path, so a
// full subscriber buffer DROPS the record and counts it.
func TestSinkNeverBlocks(t *testing.T) {
	s, _ := sinkFor(t, 100)
	_, cancel, err := s.Subscribe(1)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than the buffer of one, from a reader that never reads.
		for i := 0; i < 50; i++ {
			say(s, slog.LevelInfo, "into a full buffer")
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle blocked on a subscriber that stopped reading; the audio path logs too")
	}
	if s.Dropped() == 0 {
		t.Error("nothing was counted as dropped, so a gap in the console would be a mystery")
	}
	// And the ring is unaffected: a slow reader loses records, the log does not.
	if n := len(s.Recent(slog.LevelDebug, 100)); n != 50 {
		t.Errorf("the ring holds %d records; a slow subscriber cost the log itself", n)
	}
}

// TestSinkRedactsForbidden: obs.scrub only runs inside the typed event methods,
// so the hundreds of plain slog calls elsewhere are unscrubbed today. That is
// tolerable while logs reach stderr on a box only the developer can see; it is
// not tolerable once they render in a browser.
func TestSinkRedactsForbidden(t *testing.T) {
	s, buf := sinkFor(t, 10)
	say(s, slog.LevelInfo, "a plain slog call, not a typed obs one",
		"api_key", "cfat_realtoken", "token", "abc", "secret", "s",
		"password", "hunter2", "lyrics", "and the sign said", "station", 2)

	got := s.Recent(slog.LevelDebug, 1)
	if len(got) != 1 {
		t.Fatal("nothing recorded")
	}
	for _, k := range []string{"api_key", "token", "secret", "password", "lyrics"} {
		if v := got[0].Attrs[k]; v != "[redacted]" {
			t.Errorf("attr %s = %q, want [redacted]", k, v)
		}
	}
	if got[0].Attrs["station"] != "2" {
		t.Errorf("an ordinary attribute was lost: %q", got[0].Attrs["station"])
	}
	// The wrapped handler is the record of truth and keeps everything: stderr
	// on a box the developer already has is not the new exposure. The BROWSER
	// is, which is what the ring feeds.
	if !strings.Contains(buf.String(), "cfat_realtoken") {
		t.Error("redaction reached the wrapped handler; stderr and Seq must be unchanged")
	}
}

// TestSinkRedactsURLCredentials: serve.go logs the LLM base URL and an operator
// is free to paste https://user:pass@host into that field.
func TestSinkRedactsURLCredentials(t *testing.T) {
	s, _ := sinkFor(t, 10)
	say(s, slog.LevelInfo, "language model",
		"url", "https://oper:hunter2@llm.example.com/v1",
		"plain", "https://llm.example.com/v1")

	got := s.Recent(slog.LevelDebug, 1)[0]
	if strings.Contains(got.Attrs["url"], "hunter2") || strings.Contains(got.Attrs["url"], "oper") {
		t.Errorf("credentials survived in %q", got.Attrs["url"])
	}
	if !strings.Contains(got.Attrs["url"], "llm.example.com") {
		t.Errorf("the host was lost with the credentials: %q", got.Attrs["url"])
	}
	if got.Attrs["plain"] != "https://llm.example.com/v1" {
		t.Errorf("a URL with no credentials was altered: %q", got.Attrs["plain"])
	}
}

func TestSinkClear(t *testing.T) {
	s, _ := sinkFor(t, 10)
	ch, cancel, err := s.Subscribe(4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	say(s, slog.LevelInfo, "before")
	<-ch
	s.Clear()
	if n := len(s.Recent(slog.LevelDebug, 100)); n != 0 {
		t.Errorf("Recent returned %d records after Clear", n)
	}

	// A SUBSCRIBER SURVIVES A CLEAR. Clearing is for reading what happens
	// next, and dropping the live connection is the opposite of that.
	say(s, slog.LevelInfo, "after")
	select {
	case r := <-ch:
		if r.Message != "after" {
			t.Errorf("subscriber got %q", r.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("Clear disconnected a live subscriber")
	}
}

// TestSinkConcurrent: Handle is called from every goroutine in the process.
// Run under -race; this is what the mutex is for.
func TestSinkConcurrent(t *testing.T) {
	s, _ := sinkFor(t, 256)
	ch, cancel, err := s.Subscribe(64)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	go func() {
		for range ch {
		}
	}()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				say(s, slog.LevelInfo, "concurrent", "i", i)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = s.Recent(slog.LevelDebug, 20)
		}
	}()
	wg.Wait()

	if n := len(s.Recent(slog.LevelDebug, 1000)); n != 256 {
		t.Errorf("ring holds %d after 800 records, want its capacity of 256", n)
	}
}

// TestSinkSubscriberCap: eight is plenty for one operator with a few tabs, and
// refusing beyond it is better than growing goroutines without limit.
func TestSinkSubscriberCap(t *testing.T) {
	s, _ := sinkFor(t, 10)
	var cancels []func()
	for i := 0; i < MaxSubscribers; i++ {
		_, cancel, err := s.Subscribe(4)
		if err != nil {
			t.Fatalf("Subscribe %d of %d: %v", i+1, MaxSubscribers, err)
		}
		cancels = append(cancels, cancel)
	}
	if _, _, err := s.Subscribe(4); err == nil {
		t.Errorf("the %dth subscriber was accepted; the cap is %d", MaxSubscribers+1, MaxSubscribers)
	}
	// And a slot frees when one leaves, or closing a tab would lock the
	// operator out until a restart.
	cancels[0]()
	if _, cancel, err := s.Subscribe(4); err != nil {
		t.Errorf("no slot freed after unsubscribing: %v", err)
	} else {
		cancel()
	}
	for _, c := range cancels[1:] {
		c()
	}
}

// TestSinkKeepsWithAttrsInTheSameRing: serve.go line 179 builds the per-station
// logger as log.With("station", stationID). slog answers WithAttrs with a NEW
// handler, so if that one wrote into a different ring the console would show
// everything EXCEPT the lines saying which station broke -- which is most of
// what an operator is looking for.
func TestSinkKeepsWithAttrsInTheSameRing(t *testing.T) {
	s, _ := sinkFor(t, 10)
	perStation := slog.New(s).With("station", 2)

	perStation.Info("break dropped", "reason", "llm_error")
	say(s, slog.LevelInfo, "on air")

	got := s.Recent(slog.LevelDebug, 10)
	if len(got) != 2 {
		t.Fatalf("the ring holds %d records, want both handlers writing into it", len(got))
	}
	// Newest first, so the derived handler's record is second.
	if got[1].Attrs["station"] != "2" {
		t.Errorf("the station attribute did not reach the ring: %v", got[1].Attrs)
	}
	if got[1].Attrs["reason"] != "llm_error" {
		t.Errorf("the record's own attributes were lost: %v", got[1].Attrs)
	}

	// And a derived handler redacts too, or the exemption would be one
	// log.With away.
	perStation.Info("model", "api_key", "cfat_realtoken")
	if v := s.Recent(slog.LevelDebug, 1)[0].Attrs["api_key"]; v != "[redacted]" {
		t.Errorf("a derived handler leaked a key: %q", v)
	}
}

// TestSinkGroupsNestKeys: nothing in this repository uses slog groups today.
// This is here because a partial slog.Handler is a trap for whoever does, and
// silently losing every attribute under a group is the worst version of it.
func TestSinkGroupsNestKeys(t *testing.T) {
	s, _ := sinkFor(t, 10)
	slog.New(s).WithGroup("llm").With("provider", "cloudflare").
		Info("model changed", "model", "@cf/meta/llama")

	got := s.Recent(slog.LevelDebug, 1)[0]
	for k, want := range map[string]string{
		"llm.provider": "cloudflare",
		"llm.model":    "@cf/meta/llama",
	} {
		if got.Attrs[k] != want {
			t.Errorf("attr %s = %q, want %q (all: %v)", k, got.Attrs[k], want, got.Attrs)
		}
	}
	// An empty group name is a no-op in slog and must not produce a bare dot.
	if h := s.WithGroup(""); h != slog.Handler(s) {
		t.Error("WithGroup(\"\") returned a new handler; slog says it is a no-op")
	}
}

// TestSinkHandlesTheAwkwardInputs: the branches that only a hostile or empty
// case reaches, each of which is a real shape rather than a coverage exercise.
func TestSinkHandlesTheAwkwardInputs(t *testing.T) {
	// A record with no attributes at all, which is most of them.
	s, _ := sinkFor(t, 10)
	say(s, slog.LevelInfo, "on air")
	if got := s.Recent(slog.LevelDebug, 1)[0]; got.Attrs != nil {
		t.Errorf("a record with no attributes allocated a map: %v", got.Attrs)
	}

	// Capacity zero is a caller who meant the default, not a caller who meant
	// a ring that cannot hold anything -- which would divide by zero in Handle.
	def := NewSink(slog.NewJSONHandler(&bytes.Buffer{}, nil), 0)
	say(def, slog.LevelInfo, "still recorded")
	if n := len(def.Recent(slog.LevelDebug, 10)); n != 1 {
		t.Errorf("a zero capacity gave a ring holding %d records", n)
	}

	// A zero buffer would make every send fail and every record drop.
	if _, cancel, err := def.Subscribe(0); err != nil {
		t.Errorf("Subscribe(0): %v", err)
	} else {
		say(def, slog.LevelInfo, "into a zero buffer")
		if def.Dropped() != 0 {
			t.Error("a zero buffer dropped the first record; it should have been floored to one")
		}
		cancel()
	}

	// Values that look like URLs and are not.
	for _, tc := range []struct{ in, want string }{
		{"not a url at all", "not a url at all"},
		{"user@example.com", "user@example.com"},   // no scheme
		{"https://host/path", "https://host/path"}, // no userinfo
		{"://@ %%", "://@ %%"},                     // unparseable
	} {
		if got := stripURLCredentials(tc.in); got != tc.want {
			t.Errorf("stripURLCredentials(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSinkGroupsOnlyCoverAttrsAddedAfterThem: slog says an attribute added
// BEFORE a group is not inside it, and the ring has to agree with the handler
// it wraps.
//
// The prefix used to be applied at Handle time, so every stored attribute went
// into whatever group happened to be open last: log.With("station", 1) followed
// by WithGroup("llm") put llm.station in the ring while stderr said station.
// serve.go builds exactly that per-station logger, so the day somebody adds a
// group the console and the log would quietly stop matching.
func TestSinkGroupsOnlyCoverAttrsAddedAfterThem(t *testing.T) {
	var stderr bytes.Buffer
	s := NewSink(slog.NewTextHandler(&stderr, nil), 16)
	slog.New(s).With("station", "1").WithGroup("llm").Info("asked", "provider", "cloudflare")

	got := s.Recent(slog.LevelDebug, 10)
	if len(got) != 1 {
		t.Fatalf("%d records", len(got))
	}
	if got[0].Attrs["station"] != "1" {
		t.Errorf("station was added before the group and must not be inside it: %v", got[0].Attrs)
	}
	if got[0].Attrs["llm.provider"] != "cloudflare" {
		t.Errorf("an attribute added after the group is not in it: %v", got[0].Attrs)
	}
	// AND THE RING AGREES WITH STDERR, which is the whole claim: the wrapped
	// handler is the record of truth and the ring is a copy of it.
	for _, want := range []string{"station=1", "llm.provider=cloudflare"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("the wrapped handler did not write %q, so this test is not comparing "+
				"the ring against slog's own answer:\n%s", want, stderr.String())
		}
	}

	// Nested groups still nest, and each attribute keeps the depth it was added at.
	s2 := NewSink(slog.NewTextHandler(&bytes.Buffer{}, nil), 16)
	slog.New(s2).WithGroup("a").With("one", "1").WithGroup("b").Info("x", "two", "2")
	rec := s2.Recent(slog.LevelDebug, 10)[0]
	if rec.Attrs["a.one"] != "1" || rec.Attrs["a.b.two"] != "2" {
		t.Errorf("nested groups = %v, want a.one and a.b.two", rec.Attrs)
	}
}

// TestSinkParseLevelNamesWhatIsValid: refused BY NAME, listing the four.
//
// An operator who typed "verbose" should read a sentence rather than have the
// instruction silently ignored and then wonder why the log looks the same.
func TestSinkParseLevelNamesWhatIsValid(t *testing.T) {
	for _, c := range []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug}, {"DEBUG", slog.LevelDebug}, {" Info ", slog.LevelInfo},
		{"warn", slog.LevelWarn}, {"warning", slog.LevelWarn}, {"ERROR", slog.LevelError},
	} {
		got, err := ParseLevel(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"verbose", "", "trace", "warnn"} {
		got, err := ParseLevel(bad)
		if err == nil {
			t.Errorf("ParseLevel(%q) was accepted as %v", bad, got)
			continue
		}
		// The sentence has to say what IS valid, or it sends them to the docs.
		for _, name := range []string{"debug", "info", "warn", "error"} {
			if !strings.Contains(err.Error(), name) {
				t.Errorf("ParseLevel(%q) error does not offer %q: %v", bad, name, err)
			}
		}
		// And it falls back to info rather than to whatever zero means.
		if got != slog.LevelInfo {
			t.Errorf("ParseLevel(%q) fell back to %v, want info", bad, got)
		}
	}
}
