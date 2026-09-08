// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/obs"
)

// Jockora-69n.4. THERE IS NO SSE ANYWHERE IN THIS CODEBASE -- grep for
// text/event-stream or http.Flusher and you get nothing. This is the first
// streaming endpoint that is not HLS, so none of it is copy-and-paste.

// fakeLogs stands in for the app.
type fakeLogs struct {
	mu        sync.Mutex
	recent    []obs.Record
	cleared   int
	level     slog.Level
	dropped   int64
	setErr    error
	recentErr error
	clearErr  error

	subs     atomic.Int64
	live     chan obs.Record
	capacity error
}

func (f *fakeLogs) RecentLogs(_ context.Context, min string, limit int) ([]obs.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recentErr != nil {
		return nil, f.recentErr
	}
	if _, err := obs.ParseLevel(min); err != nil {
		return nil, err
	}
	out := f.recent
	if limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeLogs) SubscribeLogs(int) (<-chan obs.Record, func(), error) {
	if f.capacity != nil {
		return nil, nil, f.capacity
	}
	f.subs.Add(1)
	ch := make(chan obs.Record, 8)
	f.live = ch
	var once sync.Once
	return ch, func() { once.Do(func() { f.subs.Add(-1) }) }, nil
}

func (f *fakeLogs) ClearLogs(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clearErr != nil {
		return f.clearErr
	}
	f.cleared++
	f.recent = nil
	return nil
}

func (f *fakeLogs) LogLevel() slog.Level { f.mu.Lock(); defer f.mu.Unlock(); return f.level }

func (f *fakeLogs) SetLogLevel(_ context.Context, l slog.Level) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.level = l
	return nil
}

func (f *fakeLogs) DroppedLogs() int64 { return f.dropped }

// captured is a log buffer a test may read while the handler writes to it.
type captured struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (c *captured) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *captured) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// adminName is the account authServer creates, and the name the clear must
// record.
const adminName = "andrew"

func logsServer(t *testing.T) (*Server, *fakeLogs) {
	t.Helper()
	s, f, _ := logsServerLogging(t)
	return s, f
}

func logsServerLogging(t *testing.T) (*Server, *fakeLogs, *captured) {
	t.Helper()
	s, _, _ := authServer(t)
	logs := &captured{}
	s.log = slog.New(slog.NewTextHandler(logs, nil))
	f := &fakeLogs{dropped: 7, level: slog.LevelInfo, recent: []obs.Record{
		{Time: time.Unix(1788840001, 0), Level: slog.LevelError, Message: "enrichment stopped"},
		{Time: time.Unix(1788840000, 0), Level: slog.LevelWarn, Message: "break dropped",
			Attrs: map[string]string{"reason": "llm_error"}},
	}}
	s.SetLogs(f)
	return s, f, logs
}

func TestLogsAPIRecent(t *testing.T) {
	s, _ := logsServer(t)
	rec := as(t, s, http.MethodGet, "/admin/logs", "", adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Records []obs.Record `json:"records"`
		Dropped int64        `json:"dropped"`
		Level   string       `json:"level"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	if len(body.Records) != 2 || body.Records[0].Message != "enrichment stopped" {
		t.Errorf("records = %+v, want the newest first", body.Records)
	}
	// THE DROPPED COUNT, so the console can say records were lost rather than
	// leaving a gap the operator has to notice on their own.
	if body.Dropped != 7 {
		t.Errorf("dropped = %d, want 7", body.Dropped)
	}
	if body.Level != "INFO" {
		t.Errorf("level = %q, want the level in force", body.Level)
	}
}

// TestLogsAPILimitClamped: a limit query is an operator-supplied number reaching
// a slice.
func TestLogsAPILimitClamped(t *testing.T) {
	s, f := logsServer(t)
	for _, q := range []string{"?limit=100000", "?limit=-1", "?limit=abc", "?limit=0", ""} {
		rec := as(t, s, http.MethodGet, "/admin/logs"+q, "", adminCookie(t, s))
		if rec.Code != http.StatusOK {
			t.Errorf("limit %q = %d: %s", q, rec.Code, rec.Body)
		}
	}
	// And the clamp is the documented one, checked at the boundary the handler
	// actually passes down.
	f.mu.Lock()
	f.recent = make([]obs.Record, MaxLogLimit+50)
	f.mu.Unlock()
	rec := as(t, s, http.MethodGet, "/admin/logs?limit=100000", "", adminCookie(t, s))
	var body struct {
		Records []obs.Record `json:"records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Records) != MaxLogLimit {
		t.Errorf("%d records for limit=100000, want the cap of %d", len(body.Records), MaxLogLimit)
	}

	// AND A SENSIBLE LIMIT IS HONOURED, which nothing above proved: every case
	// so far was clamped or defaulted, so a handler that ignored the query
	// entirely would have passed all of them.
	rec = as(t, s, http.MethodGet, "/admin/logs?limit=50", "", adminCookie(t, s))
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Records) != 50 {
		t.Errorf("%d records for limit=50, want 50", len(body.Records))
	}
}

func TestLogsAPILevelFilter(t *testing.T) {
	s, _ := logsServer(t)
	rec := as(t, s, http.MethodGet, "/admin/logs?level=verbose", "", adminCookie(t, s))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a level nobody defines = %d, want 400: %s", rec.Code, rec.Body)
	}
	// JSON WITH AN ERROR KEY, not http.Error: the console reads e.error?.error
	// and a text/plain body is silently discarded, leaving a generic fallback.
	var body struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("the error body is not JSON: %q", rec.Body)
	}
	if body.Error == "" {
		t.Errorf("the error body has no words: %q", rec.Body)
	}
}

func TestLogsAPIStreamHeaders(t *testing.T) {
	s, _ := logsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	rec := streamOnce(t, s, ctx, cancel, nil)

	for k, want := range map[string]string{
		"Content-Type":  "text/event-stream",
		"Cache-Control": "no-store",
		"Connection":    "keep-alive",
		// A BUFFERING REVERSE PROXY turns a live stream into nothing at all,
		// and the symptom is a blank page with no error anywhere.
		"X-Accel-Buffering": "no",
	} {
		if got := rec.h.Get(k); !strings.Contains(got, want) {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
}

func TestLogsAPIStreamDelivers(t *testing.T) {
	s, f := logsServer(t)

	// A COUNTING WRITER, not httptest's Flushed bool. That bool is already
	// true from the line sent on connect, so it says nothing about whether the
	// DATA EVENT was flushed -- and an unflushed data event sits in a buffer
	// while the operator watches an empty page. Removing the flush after a
	// record passed with the old assertion.
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
	for w.flushes() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	onConnect := w.flushes()

	f.live <- obs.Record{Time: time.Unix(1788840002, 0), Level: slog.LevelWarn,
		Message: "break dropped", Attrs: map[string]string{"reason": "llm_error"}}
	for !strings.Contains(w.body(), "break dropped") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	afterRecord := w.flushes()
	body := w.body()
	cancel()
	<-done

	if !strings.Contains(body, "data: ") {
		t.Fatalf("no SSE data frame: %q", body)
	}
	if !strings.Contains(body, "break dropped") || !strings.Contains(body, "llm_error") {
		t.Errorf("the record did not arrive intact: %q", body)
	}
	if afterRecord <= onConnect {
		t.Errorf("flushes went %d to %d over a record; the event sits in a buffer until the "+
			"request ends, which for a live stream is never", onConnect, afterRecord)
	}
}

// TestLogsAPIStreamHeartbeat: a station can be quiet for a long time and that
// is the NORMAL case, so an idle connection must not look dead to an
// intermediary that closes what it thinks is a stalled request.
func TestLogsAPIStreamHeartbeat(t *testing.T) {
	s, _ := logsServer(t)
	s.heartbeat = time.Millisecond

	req := httptest.NewRequest(http.MethodGet, "/admin/logs/stream", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	req.AddCookie(adminCookie(t, s))
	rec := &countingWriter{h: http.Header{}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(rec, req)
	}()

	// REPEATED, not just the one line sent on connect: a heartbeat that fires
	// once is a connection that still goes quiet a minute later.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) &&
		strings.Count(rec.body(), ": keep-alive") < 2 {
		time.Sleep(time.Millisecond)
	}
	got := rec.body()
	cancel()
	<-done

	if !strings.HasPrefix(got, ": connected") {
		t.Errorf("nothing was sent on connect, so a client cannot tell the stream is alive: %q", got)
	}
	if n := strings.Count(got, ": keep-alive"); n < 2 {
		t.Errorf("%d heartbeats in %q; it fired once and stopped", n, got)
	}
}

// TestLogsAPIStreamUnsubscribes: a leaked subscriber is a leaked channel the
// sink then drops into for ever, counting every record as lost.
func TestLogsAPIStreamUnsubscribes(t *testing.T) {
	s, f := logsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	streamOnce(t, s, ctx, cancel, nil)
	if n := f.subs.Load(); n != 0 {
		t.Errorf("%d subscribers left after the request ended", n)
	}
}

func TestLogsAPIStreamAtCapacity(t *testing.T) {
	s, f := logsServer(t)
	f.capacity = obs.ErrTooManySubscribers
	rec := as(t, s, http.MethodGet, "/admin/logs/stream", "", adminCookie(t, s))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("at capacity = %d, want 503: %s", rec.Code, rec.Body)
	}
	var body struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
		t.Errorf("a hang or a wordless refusal instead of a JSON explanation: %q", rec.Body)
	}
}

// TestLogsAPIStreamNoFlusher: streaming into a writer nobody flushes delivers
// nothing until the request ends, which for a live stream is never.
func TestLogsAPIStreamNoFlusher(t *testing.T) {
	s, _ := logsServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/logs/stream", nil)
	req.AddCookie(adminCookie(t, s))
	w := &noFlushWriter{h: http.Header{}}
	s.Handler().ServeHTTP(w, req)
	if w.code != http.StatusInternalServerError {
		t.Errorf("a non-flushing writer = %d, want 500", w.code)
	}
	if !strings.Contains(w.body.String(), "stream") {
		t.Errorf("the refusal does not say what went wrong: %q", w.body.String())
	}
}

func TestLogsAPILevel(t *testing.T) {
	s, f := logsServer(t)
	rec := as(t, s, http.MethodPost, "/admin/logs/level", `{"level":"debug"}`, adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if f.LogLevel() != slog.LevelDebug {
		t.Errorf("level = %v, want debug", f.LogLevel())
	}

	bad := as(t, s, http.MethodPost, "/admin/logs/level", `{"level":"verbose"}`, adminCookie(t, s))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("verbose = %d, want 400", bad.Code)
	}
	for _, want := range []string{"debug", "info", "warn", "error"} {
		if !strings.Contains(bad.Body.String(), want) {
			t.Errorf("the refusal does not name %q as valid: %q", want, bad.Body)
		}
	}
	// A store that will not remember it is still reported.
	f.setErr = errors.New("disk is full")
	if rec := as(t, s, http.MethodPost, "/admin/logs/level",
		`{"level":"warn"}`, adminCookie(t, s)); rec.Code == http.StatusOK {
		t.Error("a failure to persist the level was reported as success")
	}
}

// TestLogsAPIClear: the clear is LOGGED AFTER IT HAPPENS, with the operator's
// name, so the record of who emptied the log survives the emptying. That is one
// line and it is the difference between an audit trail and a hole.
func TestLogsAPIClear(t *testing.T) {
	s, f, logBuffer := logsServerLogging(t)
	rec := as(t, s, http.MethodDelete, "/admin/logs", "", adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if f.cleared != 1 {
		t.Errorf("cleared %d times, want 1", f.cleared)
	}
	if !strings.Contains(logBuffer.String(), "log records cleared") {
		t.Errorf("the clear was not recorded: %q", logBuffer.String())
	}
	if !strings.Contains(logBuffer.String(), adminName) {
		t.Errorf("the clear does not name who did it: %q", logBuffer.String())
	}

	f.clearErr = errors.New("read-only database")
	if rec := as(t, s, http.MethodDelete, "/admin/logs", "", adminCookie(t, s)); rec.Code == http.StatusOK {
		t.Error("a failed clear reported success")
	}
}

func TestLogsAPINeedsAdmin(t *testing.T) {
	s, _ := logsServer(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/logs", ""},
		{http.MethodGet, "/admin/logs/stream", ""},
		{http.MethodPost, "/admin/logs/level", `{"level":"debug"}`},
		{http.MethodDelete, "/admin/logs", ""},
	} {
		rec := as(t, s, tc.method, tc.path, tc.body, listenerCookie(t, s))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a listener = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

func TestLogsAPIUnwired(t *testing.T) {
	s, _, _ := authServer(t)
	rec := as(t, s, http.MethodGet, "/admin/logs", "", adminCookie(t, s))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("with no Logs wired = %d, want 503", rec.Code)
	}
}

// ---------------------------------------------------------------- helpers --

// streamOnce opens the stream, lets send run, then cancels and waits. It
// returns once the handler has actually returned, so an assertion about
// unsubscribing cannot race the handler's own cleanup.
// A countingWriter rather than httptest's recorder: this polls the body while
// the handler is still writing to it, and a bytes.Buffer read from two
// goroutines is a data race -- which is exactly what the detector found.
func streamOnce(t *testing.T, s *Server, ctx context.Context, cancel func(), send func()) *countingWriter {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/logs/stream", nil).WithContext(ctx)
	req.AddCookie(adminCookie(t, s))
	rec := &countingWriter{h: http.Header{}}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(rec, req)
	}()

	// The handler writes its heartbeat immediately so a client knows the
	// connection is alive; that is also how a test knows it is subscribed.
	deadline := time.Now().Add(3 * time.Second)
	for rec.body() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if send != nil {
		send()
		before := rec.body()
		for rec.body() == before && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream handler did not return when its request context was cancelled")
	}
	return rec
}

// noFlushWriter is an http.ResponseWriter that is deliberately NOT an
// http.Flusher.
type noFlushWriter struct {
	h    http.Header
	code int
	body strings.Builder
}

func (w *noFlushWriter) Header() http.Header  { return w.h }
func (w *noFlushWriter) WriteHeader(code int) { w.code = code }
func (w *noFlushWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.body.Write(b)
}

// TestLogsAPIRoutesAndEdges: the remaining shapes a browser and a proxy
// actually produce -- an unknown sub-path, a wrong method, a body that is not
// JSON, and a stream whose feed closes under it.
func TestLogsAPIRoutesAndEdges(t *testing.T) {
	s, f := logsServer(t)
	admin := adminCookie(t, s)

	// An unknown sub-path is a 404 with words, not a fall-through to the SPA.
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/admin/logs/nonsense"},
		{http.MethodPut, "/admin/logs"},
		{http.MethodPost, "/admin/logs"},
		{http.MethodGet, "/admin/logs/level"},
		{http.MethodDelete, "/admin/logs/stream"},
	} {
		rec := as(t, s, tc.method, tc.path, "", admin)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
		var body struct{ Error string }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error == "" {
			t.Errorf("%s %s answered without a JSON reason: %q", tc.method, tc.path, rec.Body)
		}
	}

	// A body that is not JSON is refused by the shared decoder.
	if rec := as(t, s, http.MethodPost, "/admin/logs/level", `not json`, admin); rec.Code == http.StatusOK {
		t.Error("a body that is not JSON was accepted")
	}

	// The recent read failing is reported, not swallowed.
	f.mu.Lock()
	f.recentErr = errors.New("the database is closed")
	f.mu.Unlock()
	if rec := as(t, s, http.MethodGet, "/admin/logs", "", admin); rec.Code == http.StatusOK {
		t.Error("a failed read reported success")
	}
	f.mu.Lock()
	f.recentErr = nil
	f.mu.Unlock()

	// A feed that CLOSES under the stream ends the handler rather than
	// spinning on a closed channel for ever.
	req := httptest.NewRequest(http.MethodGet, "/admin/logs/stream", nil)
	req.AddCookie(admin)
	streamed := &countingWriter{h: http.Header{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Handler().ServeHTTP(streamed, req)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for streamed.body() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(f.live)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler kept running after its feed closed")
	}
}

// countingWriter is an http.ResponseWriter that IS an http.Flusher and counts
// how often it was flushed, which httptest's single Flushed bool cannot say.
type countingWriter struct {
	mu  sync.Mutex
	h   http.Header
	buf strings.Builder
	n   int
}

func (w *countingWriter) Header() http.Header { return w.h }
func (w *countingWriter) WriteHeader(int)     {}

func (w *countingWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(b)
}

func (w *countingWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n++
}

func (w *countingWriter) flushes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

func (w *countingWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
