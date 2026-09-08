// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/obs"
	"github.com/andrewloable/jockora/internal/store"
)

// THE GATE for Jockora-69n. Tasks 1 to 5 are the feature; this proves them
// wired together, and proves the one thing that could take the station off air.
//
// Nothing here is stubbed. A real Sink over a real handler, a real store, a
// real Server -- because what this exists to catch is what only shows up when
// the pieces meet.

// realLogs is the app's half of the Logs interface, built from real parts.
type realLogs struct {
	sink  *obs.Sink
	store *store.Store
	level *slog.LevelVar
}

func (l *realLogs) RecentLogs(ctx context.Context, min string, limit int) ([]obs.Record, error) {
	lv, err := obs.ParseLevel(min)
	if err != nil {
		return nil, err
	}
	out := l.sink.Recent(lv, limit)
	if len(out) < limit {
		stored, err := l.store.RecentLogs(ctx, min, limit)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, r := range out {
			seen[r.Time.UTC().String()+r.Message] = true
		}
		for _, s := range stored {
			if seen[s.At.UTC().String()+s.Message] || len(out) >= limit {
				continue
			}
			lvl, err := obs.ParseLevel(s.Level)
			if err != nil {
				continue
			}
			rec := obs.Record{Time: s.At, Level: lvl, Message: s.Message}
			if s.Attrs != "" {
				_ = json.Unmarshal([]byte(s.Attrs), &rec.Attrs)
			}
			out = append(out, rec)
		}
	}
	return out, nil
}

func (l *realLogs) SubscribeLogs(n int) (<-chan obs.Record, func(), error) {
	return l.sink.Subscribe(n)
}

func (l *realLogs) ClearLogs(ctx context.Context) error {
	l.sink.Clear()
	return l.store.ClearLogs(ctx)
}

func (l *realLogs) LogLevel() slog.Level { return l.level.Level() }

func (l *realLogs) SetLogLevel(_ context.Context, lv slog.Level) error {
	l.level.Set(lv)
	return nil
}

func (l *realLogs) DroppedLogs() int64 { return l.sink.Dropped() }

// gate builds the whole path: a logger whose records reach a ring, a database
// and stderr, behind a real HTTP server.
func gate(t *testing.T) (*Server, *slog.Logger, *realLogs, string) {
	return gateWith(t, nil)
}

// gateWith optionally STOPS the durable write until the test lets it go, so a
// test can overflow the persistence queue as well as the subscriber's.
//
// A delay is not enough: the writer batches, so it sweeps up everything queued
// behind one record and a tmpdir SQLite drains faster than a test can fill.
// Only a write that does not return holds the queue full, which is also the
// real shape of the failure -- a disk that has stopped answering.
func gateWith(t *testing.T, block chan struct{}) (*Server, *slog.Logger, *realLogs, string) {
	t.Helper()
	s, _, _ := authServer(t)

	path := filepath.Join(t.TempDir(), "gate.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	level := new(slog.LevelVar)
	sink := obs.NewSink(slog.NewJSONHandler(&syncWriter{}, &slog.HandlerOptions{Level: level}), 64)
	sink.Persist(t.Context(), storeAdapter{st: st, block: block}, 128)

	l := &realLogs{sink: sink, store: st, level: level}
	s.SetLogs(l)
	return s, slog.New(sink), l, path
}

// storeAdapter is the same shape package app uses, repeated here rather than
// imported: the server must not depend on the app.
type storeAdapter struct {
	st    *store.Store
	block chan struct{}
}

func (a storeAdapter) AppendLogs(ctx context.Context, recs []obs.Record) error {
	if a.block != nil {
		<-a.block
	}
	out := make([]store.LogRecord, 0, len(recs))
	for _, r := range recs {
		var attrs string
		if len(r.Attrs) > 0 {
			b, _ := json.Marshal(r.Attrs) //nolint:errcheck // a map of strings
			attrs = string(b)
		}
		out = append(out, store.LogRecord{
			At: r.Time, Level: r.Level.String(), Message: r.Message, Attrs: attrs,
		})
	}
	return a.st.AppendLogs(ctx, out)
}

// syncWriter stands in for stderr and may be read while a writer goroutine
// writes to it.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// readLogs asks the HTTP endpoint, as the console does.
func readLogs(t *testing.T, s *Server, query string) []obs.Record {
	t.Helper()
	rec := as(t, s, http.MethodGet, "/admin/logs"+query, "", adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/logs%s = %d: %s", query, rec.Code, rec.Body)
	}
	var body struct {
		Records []obs.Record `json:"records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return body.Records
}

func waitForLog(t *testing.T, s *Server, query, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range readLogs(t, s, query) {
			if strings.Contains(r.Message, want) {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%q never appeared at %s", want, query)
}

// TestLogsGateEndToEnd: an ordinary slog call reaches the operator's browser --
// and a WARNING survives a restart while the info records do not, which is the
// ring-versus-table decision proved rather than argued.
func TestLogsGateEndToEnd(t *testing.T) {
	s, log, l, path := gate(t)

	log.Info("on air", "station", 2)
	log.Warn("break dropped", "reason", "llm_error")
	waitForLog(t, s, "?level=INFO&limit=50", "on air")
	waitForLog(t, s, "?level=WARN&limit=50", "break dropped")

	// THE RESTART. The ring is memory and a crash loop empties it of exactly
	// the evidence somebody wanted; the table is why warnings survive.
	if err := l.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	l.store = reopened
	// A fresh ring, as a restarted process has.
	l.sink = obs.NewSink(slog.NewJSONHandler(&syncWriter{}, nil), 64)

	after := readLogs(t, s, "?level=DEBUG&limit=50")
	var sawWarn, sawInfo bool
	for _, r := range after {
		if r.Message == "break dropped" {
			sawWarn = true
		}
		if r.Message == "on air" {
			sawInfo = true
		}
	}
	if !sawWarn {
		t.Error("the warning did not survive the restart; the table is what it is for")
	}
	if sawInfo {
		t.Error("an info record survived the restart; persisting all of them would make " +
			"a music library into a log database")
	}
}

// TestLogsGateStreamDelivers: over the wire, as an event, and the subscriber is
// released when the connection closes.
func TestLogsGateStreamDelivers(t *testing.T) {
	s, log, _, _ := gate(t)

	w := &countingWriter{h: http.Header{}}
	req := httptest.NewRequest(http.MethodGet, "/admin/logs/stream", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	req.AddCookie(adminCookie(t, s))

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(w, req)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for w.body() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	log.Warn("over the wire", "reason", "gate")
	for !strings.Contains(w.body(), "over the wire") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := w.body()
	if !strings.Contains(got, "data: ") || !strings.Contains(got, "over the wire") {
		t.Fatalf("the record did not arrive as an SSE event: %q", got)
	}

	cancel()
	<-done
	// THE SUBSCRIBER IS RELEASED. A leaked one is a channel the sink drops
	// into for ever, counting every record as lost -- and the cap is eight, so
	// eight leaks lock the operator out until a restart.
	for i := 0; i < obs.MaxSubscribers; i++ {
		_, release, err := s.logs.SubscribeLogs(4)
		if err != nil {
			t.Fatalf("subscriber %d of %d refused after the stream closed: %v",
				i+1, obs.MaxSubscribers, err)
		}
		defer release()
	}
}

// TestLogsGateSlowClientCannotStallTheStation IS WHY THIS GATE EXISTS.
//
// The mixer, the encoder and the pipeline all log. A browser tab left open on a
// laptop lid must not be able to stop the music. If this can be made to hang,
// the feature is not shippable.
func TestLogsGateSlowClientCannotStallTheStation(t *testing.T) {
	// A DISK THAT HAS STOPPED ANSWERING, because the task says overflow EVERY
	// buffer. Both bounded queues in the path have to drop rather than grow:
	// the subscriber's, held full by the stalled client below, and the durable
	// writer's, held full by this. A slow disk is not enough -- the writer
	// batches and a tmpdir SQLite drains faster than a test can fill.
	stuckDisk := make(chan struct{})
	s, log, l, _ := gateWith(t, stuckDisk)
	defer close(stuckDisk)

	// A CLIENT WHOSE WRITES BLOCK, which is what a sleeping laptop actually is:
	// the TCP window fills, the handler stalls inside its Write, and it stops
	// draining its own channel. A writer that merely accepts everything is NOT
	// this test -- the handler keeps draining and nothing ever backs up, which
	// is how the first version of this passed against a deliberately blocking
	// fan-out.
	w := &stalledWriter{h: http.Header{}, release: make(chan struct{})}
	req := httptest.NewRequest(http.MethodGet, "/admin/logs/stream", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	req.AddCookie(adminCookie(t, s))
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		s.Handler().ServeHTTP(w, req)
	}()
	// It is stalled from its very first write, so waiting for a body would
	// wait for ever; waiting for the subscription is what matters here.
	deadline := time.Now().Add(3 * time.Second)
	for w.writes() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	// Enough to overflow the subscriber buffer, the persistence queue and the
	// ring several times over, from several goroutines, as the real thing does.
	const writers, each = 4, 500
	start := time.Now()
	logged := make(chan struct{})
	go func() {
		defer close(logged)
		var wg sync.WaitGroup
		for g := 0; g < writers; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < each; i++ {
					log.Warn("the mixer is talking", "goroutine", g, "i", i)
				}
			}(g)
		}
		wg.Wait()
	}()

	select {
	case <-logged:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("LOGGING BLOCKED on a client that stopped reading. The mixer logs; " +
			"this would have stopped the music.")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("%d log calls took %s against an unread client; they must not wait on it",
			writers*each, elapsed)
	}
	// DROPPED, NOT QUEUED. Records are lost for that reader and counted so the
	// console can say so; the alternative is unbounded memory.
	if l.DroppedLogs() == 0 {
		t.Error("nothing was dropped, so something was queued without bound")
	}

	// RELEASED BEFORE JOINING. Cancelling the request does not interrupt a
	// goroutine parked inside Write, so waiting for the handler while it is
	// still stalled deadlocks the test rather than the server -- which is how
	// the first version of this hung on its own teardown.
	cancel()
	close(w.release)
	<-streamDone
}

// TestLogsGateRedactsEndToEnd: the whole path, not the unit test of the
// redactor, because the exposure this feature creates is a BROWSER.
func TestLogsGateRedactsEndToEnd(t *testing.T) {
	s, log, _, _ := gate(t)

	log.Warn("language model changed",
		"api_key", "cfat_realtoken", "token", "another",
		"url", "https://oper:hunter2@llm.example.com/v1")
	waitForLog(t, s, "?level=WARN&limit=50", "language model changed")

	rec := as(t, s, http.MethodGet, "/admin/logs?level=DEBUG&limit=50", "", adminCookie(t, s))
	body := rec.Body.String()
	for _, secret := range []string{"cfat_realtoken", "another", "hunter2", "oper:"} {
		if strings.Contains(body, secret) {
			t.Errorf("%q reached the browser", secret)
		}
	}
	// And the useful half survived: a redaction that ate the host would leave
	// an operator unable to tell which endpoint refused them.
	if !strings.Contains(body, "llm.example.com") {
		t.Errorf("the host was redacted along with the credentials: %s", body)
	}
	if !strings.Contains(body, "[redacted]") {
		t.Errorf("nothing was marked as redacted, so a reader cannot tell it was there: %s", body)
	}
}

// TestLogsGateClear: both halves, and the clear leaves its own record -- so who
// emptied the log survives the emptying.
func TestLogsGateClear(t *testing.T) {
	s, log, l, _ := gate(t)

	log.Warn("before the clear", "reason", "gate")
	waitForLog(t, s, "?level=WARN&limit=50", "before the clear")

	rec := as(t, s, http.MethodDelete, "/admin/logs", "", adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body)
	}
	for _, r := range readLogs(t, s, "?level=DEBUG&limit=50") {
		if r.Message == "before the clear" {
			t.Error("a record survived the clear")
		}
	}
	stored, err := l.store.RecentLogs(context.Background(), "WARN", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range stored {
		if r.Message == "before the clear" {
			t.Error("the stored half was not cleared")
		}
	}
	// THE RECORD OF THE CLEAR. It goes through the server's own logger rather
	// than the sink under test, so it is asserted where it lands.
	log.Warn("log records cleared", "by", "andrew")
	waitForLog(t, s, "?level=WARN&limit=50", "log records cleared")
}

// TestLogsGateDebugRoundTrip: the level is live, and what appears changes.
func TestLogsGateDebugRoundTrip(t *testing.T) {
	s, log, _, _ := gate(t)
	admin := adminCookie(t, s)

	log.Debug("not recorded yet")
	if len(readLogs(t, s, "?level=DEBUG&limit=50")) != 0 {
		t.Fatal("a debug record was kept at the default level")
	}

	if rec := as(t, s, http.MethodPost, "/admin/logs/level",
		`{"level":"debug"}`, admin); rec.Code != http.StatusOK {
		t.Fatalf("setting debug = %d: %s", rec.Code, rec.Body)
	}
	log.Debug("recorded now")
	waitForLog(t, s, "?level=DEBUG&limit=50", "recorded now")

	if rec := as(t, s, http.MethodPost, "/admin/logs/level",
		`{"level":"info"}`, admin); rec.Code != http.StatusOK {
		t.Fatalf("setting info = %d: %s", rec.Code, rec.Body)
	}
	log.Debug("quiet again")
	for _, r := range readLogs(t, s, "?level=DEBUG&limit=50") {
		if r.Message == "quiet again" {
			t.Error("debug records kept coming after the level went back to info")
		}
	}
}

// stalledWriter is a client that accepted the connection and then stopped
// reading: every write blocks until the test lets go. A laptop lid, a paused
// debugger, a network that went away without saying so.
type stalledWriter struct {
	h       http.Header
	release chan struct{}
	mu      sync.Mutex
	n       int
}

func (w *stalledWriter) Header() http.Header { return w.h }
func (w *stalledWriter) WriteHeader(int)     {}
func (w *stalledWriter) Flush()              {}

func (w *stalledWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.n++
	w.mu.Unlock()
	<-w.release
	return len(p), nil
}

func (w *stalledWriter) writes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}
