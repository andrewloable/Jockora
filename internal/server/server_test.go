// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// newServer returns the server and STATION 1's segment directory. Stations got
// their own directories in 12e, so the place a test writes a segment is one
// level below the configured root.
func newServer(t *testing.T, retain int) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	s, err := New(Config{ListenAddr: "127.0.0.1:0", SegmentDir: root, RetainSegments: retain},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Every listener is identified; the tests that care about the refusal
	// install their own source.
	s.SetSessions(func(http.ResponseWriter, *http.Request) (string, bool) {
		return "test-session", true
	})
	return s, dir
}

func writeSegs(t *testing.T, dir string, nums ...int) {
	t.Helper()
	for _, n := range nums {
		name := filepath.Join(dir, fmt.Sprintf("seg%d.ts", n))
		if err := os.WriteFile(name, []byte(fmt.Sprintf("segment %d", n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesPlaylist(t *testing.T) {
	s, dir := newServer(t, 20)
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\nseg0.ts\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := get(t, s, "/hls/1/stream.m3u8")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/vnd.apple.mpegurl" {
		t.Errorf("Content-Type = %q, want application/vnd.apple.mpegurl", got)
	}
	// A cached playlist is a frozen stream: the client never learns about new
	// segments.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") && !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want the playlist not to be cached", cc)
	}
	if !strings.Contains(rec.Body.String(), "#EXTM3U") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestServesSegment(t *testing.T) {
	s, dir := newServer(t, 20)
	writeSegs(t, dir, 0)

	rec := get(t, s, "/hls/1/seg0.ts")

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp2t" {
		t.Errorf("Content-Type = %q, want video/mp2t", got)
	}
	if rec.Body.String() != "segment 0" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestRejectsPathTraversal(t *testing.T) {
	s, dir := newServer(t, 20)
	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/hls/1/../../etc/passwd",
		"/hls/1/..%2f..%2fetc%2fpasswd",
		"/hls/1/..%2F..%2Fetc%2Fpasswd",
		"/hls/1/../secret.txt",
		"/hls/1/..%2fsecret.txt",
		"/hls/1/%2e%2e%2fsecret.txt",
		"/hls/1/seg0.ts/../../secret.txt",
		"/hls/1/./seg0.ts",
	} {
		rec := get(t, s, path)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", path, rec.Code)
		}
		for _, leak := range []string{"TOP-SECRET-CONTENT", "root:"} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Errorf("GET %s leaked file content: %q", path, rec.Body.String())
			}
		}
	}
}

func TestRejectsBadSegmentName(t *testing.T) {
	s, dir := newServer(t, 20)
	if err := os.WriteFile(filepath.Join(dir, "evil.sh"), []byte("#!/bin/sh\nrm -rf /"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"evil.sh", "seg.ts", "segX.ts", "seg0.TS", "stream.m3u", "SEG0.ts", "seg0.ts.bak", ""} {
		rec := get(t, s, "/hls/1/"+name)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET /hls/1/%s: status %d, want 400", name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "rm -rf") {
			t.Errorf("GET /hls/1/%s served the file", name)
		}
	}
}

func TestBindsLoopbackOnly(t *testing.T) {
	s, _ := newServer(t, 20)
	if !strings.HasPrefix(s.Addr(), "127.0.0.1") {
		t.Errorf("listen address %q does not start with 127.0.0.1", s.Addr())
	}

	// And a non-loopback address must be refused outright. This program has no
	// authentication of any kind, and self-hosters port-forward routinely.
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.168.1.10:0"} {
		if _, err := New(Config{ListenAddr: addr, SegmentDir: t.TempDir()}, nil); err == nil {
			t.Errorf("New accepted listen address %q", addr)
		}
	}
	// Port 0 throughout: a fixed port makes this test fail on a machine that
	// happens to be using it.
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "[::1]:0"} {
		if _, err := New(Config{ListenAddr: addr, SegmentDir: t.TempDir()}, nil); err != nil {
			t.Errorf("New rejected loopback address %q: %v", addr, err)
		}
	}
}

// TestPrunedSegmentStillServedWithinRetention: the playlist advertises a short
// window, but a briefly-suspended client still asks for what it last saw.
func TestPrunedSegmentStillServedWithinRetention(t *testing.T) {
	s, dir := newServer(t, 20)
	// seg0 is long gone from a 10-entry playlist but well inside a 20 retention.
	writeSegs(t, dir, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14)
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"),
		[]byte("#EXTM3U\nseg5.ts\nseg6.ts\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if rec := get(t, s, "/hls/1/seg0.ts"); rec.Code != http.StatusOK {
		t.Errorf("a segment pruned from the playlist but inside retention returned %d, want 200", rec.Code)
	}
}

func TestTrulyMissingSegmentReturns404WithNoStore(t *testing.T) {
	s, dir := newServer(t, 20)
	writeSegs(t, dir, 5)

	rec := get(t, s, "/hls/1/seg999.ts")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	// A cached 404 outlives the segment's absence and breaks the client for as
	// long as the intermediary holds it.
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store on a missing segment", got)
	}
}

func TestRetentionSweeperDeletesBeyondWindow(t *testing.T) {
	s, dir := newServer(t, 5)
	writeSegs(t, dir, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("not ours"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// The five newest survive; the five oldest are gone.
	for _, n := range []int{5, 6, 7, 8, 9} {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("seg%d.ts", n))); err != nil {
			t.Errorf("seg%d.ts was deleted but is inside the retention window", n)
		}
	}
	for _, n := range []int{0, 1, 2, 3, 4} {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("seg%d.ts", n))); err == nil {
			t.Errorf("seg%d.ts survived beyond the retention window", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "stream.m3u8")); err != nil {
		t.Error("the sweeper deleted the playlist")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("the sweeper deleted an unrelated file")
	}
}

// TestSweeperKeepsEverythingBelowThreshold: a young stream must not lose
// segments just because the sweeper ran.
func TestSweeperKeepsEverythingBelowThreshold(t *testing.T) {
	s, dir := newServer(t, 20)
	writeSegs(t, dir, 0, 1, 2)

	if err := s.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for _, n := range []int{0, 1, 2} {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("seg%d.ts", n))); err != nil {
			t.Errorf("seg%d.ts was deleted from a stream of only three segments", n)
		}
	}
}

// TestSweeperOrdersByNumberNotName: seg9 is newer than seg10 lexically but
// older numerically, and deleting by string order would delete live segments.
func TestSweeperOrdersByNumberNotName(t *testing.T) {
	s, dir := newServer(t, 3)
	writeSegs(t, dir, 8, 9, 10, 11, 12)

	if err := s.Sweep(); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for _, n := range []int{10, 11, 12} {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("seg%d.ts", n))); err != nil {
			t.Errorf("seg%d.ts deleted: the sweeper sorted by name instead of number", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "seg8.ts")); err == nil {
		t.Error("seg8.ts survived")
	}
}

func TestServesIndexPage(t *testing.T) {
	s, _ := newServer(t, 20)

	rec := get(t, s, "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / returned %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
}

// TestUnknownPathIs404: an unknown ASSET is a 404. An unknown PAGE is the app.
//
// The split matters in both directions. A deep link the Angular router owns --
// /login, or anything a bookmark still points at -- has to reach the app, or
// reloading the page a listener is looking at logs them out of it. But a
// missing .js must stay a 404: answering it with html makes a broken build
// look like a working one right up until the browser tries to parse it.
func TestUnknownPathIs404(t *testing.T) {
	s, _ := newServer(t, 20)

	if rec := get(t, s, "/nope.js"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope.js returned %d, want 404: a missing asset is not a page", rec.Code)
	}
	if rec := get(t, s, "/nope"); rec.Code == http.StatusNotFound {
		t.Error("GET /nope returned 404; a path with no extension is the app's to route")
	}
}

// TestOnlyGetIsAllowed: nothing here is writable, so anything but a read is a
// mistake or a probe.
func TestOnlyGetIsAllowed(t *testing.T) {
	s, dir := newServer(t, 20)
	writeSegs(t, dir, 0)

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(method, "/hls/1/seg0.ts", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /hls/seg0.ts returned %d, want 405", method, rec.Code)
		}
	}
}

// TestPlayerChecksNativeHLSBeforeHlsJs pins the order. iOS Safari has no Media

// TestHlsJsIsServedLocally: no CDN. This is a self-hosted product that has to

// TestVendoredLicenceIsShipped: hls.js is Apache-2.0, which requires the licence

func TestUnknownAssetIs404(t *testing.T) {
	if rec := get(t, mustServer(t), "/vendor/../server.go"); rec.Code == http.StatusOK {
		t.Error("an asset request escaped the embedded bundle")
	}
	if rec := get(t, mustServer(t), "/vendor/nope.js"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown asset returned %d, want 404", rec.Code)
	}
}

var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
var jsLineComment = regexp.MustCompile(`(?m)^\s*//.*$`)

// strippedPage returns the player page with comments removed, so a test can
// assert on what the page DOES rather than on prose explaining what it avoids.
func strippedPage(t *testing.T) string {
	t.Helper()
	body := get(t, mustServer(t), "/").Body.String()
	body = htmlComment.ReplaceAllString(body, "")
	return jsLineComment.ReplaceAllString(body, "")
}

func mustServer(t *testing.T) *Server {
	t.Helper()
	s, _ := newServer(t, 20)
	return s
}

// TestNonLoopbackRequiresExplicitOptIn: containers need 0.0.0.0 because
// 127.0.0.1 is reachable only inside their own network namespace. That is a
// real need, and it must still be a decision rather than a default: this
// program speaks plain HTTP.
func TestNonLoopbackRequiresExplicitOptIn(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.168.1.10:0"} {
		if _, err := New(Config{ListenAddr: addr, SegmentDir: t.TempDir()}, nil); err == nil {
			t.Errorf("New accepted %q without the opt-in", addr)
		}
	}

	s, err := New(Config{
		ListenAddr: "0.0.0.0:0", SegmentDir: t.TempDir(), AllowNonLoopback: true,
	}, nil)
	if err != nil {
		t.Fatalf("the opt-in did not permit 0.0.0.0: %v", err)
	}
	defer s.ln.Close()
}

// TestNonLoopbackIsLoggedLoudly: a posture chosen once and forgotten is how an
// unauthenticated service ends up somewhere nobody meant it to be.
func TestNonLoopbackIsLoggedLoudly(t *testing.T) {
	var buf bytes.Buffer
	s, err := New(Config{
		ListenAddr: "0.0.0.0:0", SegmentDir: t.TempDir(), AllowNonLoopback: true,
	}, slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.ln.Close()

	out := buf.String()
	if !strings.Contains(out, "WITHOUT AUTHENTICATION") {
		t.Errorf("binding off-host was not warned about:\n%s", out)
	}
	if !strings.Contains(out, "allow-lan") {
		t.Error("the warning does not name the flag that enabled it")
	}
}

// TestLoopbackNeverWarns: the safe default must stay quiet, or the warning
// becomes noise and stops being read.
func TestLoopbackNeverWarns(t *testing.T) {
	var buf bytes.Buffer
	s, err := New(Config{
		ListenAddr: "127.0.0.1:0", SegmentDir: t.TempDir(), AllowNonLoopback: true,
	}, slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.ln.Close()

	if strings.Contains(buf.String(), "WITHOUT AUTHENTICATION") {
		t.Error("a loopback bind warned anyway; the warning will be tuned out")
	}
}

// post sends a JSON body. header is an optional name/value pair.
func post(t *testing.T, s *Server, path, body string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if len(header) == 2 {
		req.Header.Set(header[0], header[1])
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// The player-page tests moved to package web in 15a. They are about the PAGE,
// and once the Angular app is built the route no longer serves it -- so they
// assert against the embedded file, where the answer does not depend on whether
// somebody has run `make web`.

// The dial, tuning, feedback and jock tests that lived here were replaced in
// 13g: /stations.json now comes from the stations table behind a login, /tune
// names a station id rather than a tag, and the /jock routes are gone. Their
// replacements are in listener_test.go, beside the code that answers them.

// GUARDED for the reason listenTuner is: it stands in for something reached
// from many request goroutines at once, and an unguarded double turns every
// concurrent test into a race report about the test.
type fakeAdmin struct {
	mu        sync.Mutex
	cadence   int
	overlap   float64
	enriching bool
	public    bool
	recall    bool
	cleared   int64
	said      string
	err       error
}

// setPublic is the operator's switch, used where a test drives it directly
// rather than through the route.
func (f *fakeAdmin) setPublic(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.public = on
}

func (f *fakeAdmin) Overview() any { return map[string]any{"cadence": f.cadence} }
func (f *fakeAdmin) SetCadence(n int) error {
	if f.err != nil {
		return f.err
	}
	f.cadence = n
	return nil
}
func (f *fakeAdmin) SetBreakOverlap(seconds float64) error {
	if f.err != nil {
		return f.err
	}
	f.overlap = seconds
	return nil
}
func (f *fakeAdmin) PublicListener() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.public
}
func (f *fakeAdmin) SetPublicListener(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.public = on
	return nil
}
func (f *fakeAdmin) SetEnrichRecall(on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.recall = on
	return nil
}
func (f *fakeAdmin) RedoUnknownDossiers(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return 0, f.err
	}
	return f.cleared, nil
}
func (f *fakeAdmin) SetEnriching(on bool) (string, error) {
	f.enriching = on
	if f.said != "" {
		return f.said, f.err
	}
	return "Enrichment running.", f.err
}

// oldTokenHeader is the header the shared-secret path used to accept. Spelled
// out here rather than imported, because the constant it came from is deleted:
// the point of the test is that sending it changes nothing.
const oldTokenHeader = "X-Jockora-Admin"

// TestAdminTokenNoLongerOpensWrites is the safety property of removing a
// second authority: an operator who still has the old secret in a script must
// not keep write access that nobody is tracking any more.
func TestAdminTokenNoLongerOpensWrites(t *testing.T) {
	s, _ := newServer(t, 0)
	admin := &fakeAdmin{cadence: 4}
	s.SetAdmin(admin)

	for _, tc := range []struct{ path, body string }{
		{"/admin/cadence", `{"cadence":99}`},
		{"/admin/enriching", `{"enriching":false}`},
	} {
		rec := post(t, s, tc.path, tc.body, oldTokenHeader, "correct-horse")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s with the old token = %d, want 401", tc.path, rec.Code)
		}
	}
	if admin.cadence != 4 {
		t.Errorf("cadence was changed to %d by the deleted token path", admin.cadence)
	}
	if admin.enriching {
		t.Error("enrichment was toggled by the deleted token path")
	}
}

// TestAdminTokenGoneAdminCanWrite.
//
// This used to assert that the door was closed to EVERYONE, which was the right
// state while 10g had deleted the shared token and no accounts existed yet.
// 13a built the accounts; the door now opens for an operator and nobody else.
func TestAdminTokenGoneAdminCanWrite(t *testing.T) {
	s, st, _ := authServer(t)
	admin := &fakeAdmin{cadence: 4}
	s.SetAdmin(admin)
	_ = st

	// Anonymous is still refused, and told to sign in rather than told the
	// route does not exist.
	rec := post(t, s, "/admin/cadence", `{"cadence":8}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /admin/cadence anonymously = %d, want 401: %s", rec.Code, rec.Body)
	}
	if admin.cadence != 4 {
		t.Errorf("cadence changed to %d through a closed door", admin.cadence)
	}

	// A listener is 403: they signed in, and it is not enough.
	if rec := as(t, s, http.MethodPost, "/admin/cadence", `{"cadence":8}`,
		listenerCookie(t, s)); rec.Code != http.StatusForbidden {
		t.Errorf("a listener = %d, want 403", rec.Code)
	}
	if admin.cadence != 4 {
		t.Errorf("a listener changed the cadence to %d", admin.cadence)
	}

	// An operator gets through, which is the whole point of the account.
	if rec := as(t, s, http.MethodPost, "/admin/cadence", `{"cadence":8}`,
		adminCookie(t, s)); rec.Code != http.StatusNoContent {
		t.Fatalf("an operator = %d, want 204: %s", rec.Code, rec.Body)
	}
	if admin.cadence != 8 {
		t.Errorf("cadence = %d after an operator changed it, want 8", admin.cadence)
	}
}

// TestAdminReadsNeedAnOperator.
//
// This used to assert the opposite: the overview was open, on the reasoning
// that /now.json already exposed that class of information. 13a gave the server
// accounts and 13g took the library detail off /now.json, so the overview is
// now the one place that carries it -- track counts, enrichment progress, what
// the operator has configured -- and it belongs behind the operator's door with
// everything else on that page.
func TestAdminReadsNeedAnOperator(t *testing.T) {
	s, _ := newServer(t, 0)
	s.SetAdmin(&fakeAdmin{cadence: 4})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/overview.json", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /admin/overview.json anonymously = %d, want 401", rec.Code)
	}
}

// TestListenerControlsStayOpen was about /tune and /jock not being behind the
// admin token. 13g made both routes require a listener session instead, and
// /jock no longer exists; listener_test.go's TestListenerCannotReachAdmin
// carries what this was protecting -- that a listener's own controls and the
// operator's are different doors.
