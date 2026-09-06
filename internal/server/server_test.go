// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func newServer(t *testing.T, retain int) (*Server, string) {
	t.Helper()
	dir := t.TempDir()

	s, err := New(Config{ListenAddr: "127.0.0.1:0", SegmentDir: dir, RetainSegments: retain},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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

	rec := get(t, s, "/hls/stream.m3u8")

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

	rec := get(t, s, "/hls/seg0.ts")

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
		"/hls/../../etc/passwd",
		"/hls/..%2f..%2fetc%2fpasswd",
		"/hls/..%2F..%2Fetc%2Fpasswd",
		"/hls/../secret.txt",
		"/hls/..%2fsecret.txt",
		"/hls/%2e%2e%2fsecret.txt",
		"/hls/seg0.ts/../../secret.txt",
		"/hls/./seg0.ts",
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
		rec := get(t, s, "/hls/"+name)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET /hls/%s: status %d, want 400", name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "rm -rf") {
			t.Errorf("GET /hls/%s served the file", name)
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

	if rec := get(t, s, "/hls/seg0.ts"); rec.Code != http.StatusOK {
		t.Errorf("a segment pruned from the playlist but inside retention returned %d, want 200", rec.Code)
	}
}

func TestTrulyMissingSegmentReturns404WithNoStore(t *testing.T) {
	s, dir := newServer(t, 20)
	writeSegs(t, dir, 5)

	rec := get(t, s, "/hls/seg999.ts")

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

func TestUnknownPathIs404(t *testing.T) {
	s, _ := newServer(t, 20)

	if rec := get(t, s, "/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /nope returned %d, want 404", rec.Code)
	}
}

// TestOnlyGetIsAllowed: nothing here is writable, so anything but a read is a
// mistake or a probe.
func TestOnlyGetIsAllowed(t *testing.T) {
	s, dir := newServer(t, 20)
	writeSegs(t, dir, 0)

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(method, "/hls/seg0.ts", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /hls/seg0.ts returned %d, want 405", method, rec.Code)
		}
	}
}

// --- player page (ep0.15) ---

func TestServesPlayerPage(t *testing.T) {
	s, _ := newServer(t, 20)

	rec := get(t, s, "/")
	body := rec.Body.String()

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / returned %d, want 200", rec.Code)
	}
	for _, want := range []string{"<audio", "/hls/stream.m3u8", "canPlayType", "Hls.isSupported"} {
		if !strings.Contains(body, want) {
			t.Errorf("player page is missing %q", want)
		}
	}
}

// TestPlayerChecksNativeHLSBeforeHlsJs pins the order. iOS Safari has no Media
// Source Extensions, so hls.js cannot work there at all; checking it first would
// break the one platform that has no fallback.
func TestPlayerChecksNativeHLSBeforeHlsJs(t *testing.T) {
	body := get(t, mustServer(t), "/").Body.String()

	native := strings.Index(body, "canPlayType('application/vnd.apple.mpegurl')")
	polyfill := strings.Index(body, "Hls.isSupported()")

	if native < 0 || polyfill < 0 {
		t.Fatalf("feature detection not found: native=%d hlsjs=%d", native, polyfill)
	}
	if native > polyfill {
		t.Error("hls.js is checked before native HLS; iOS Safari would take the path it cannot run")
	}
}

func TestPlayerDoesNotAutoplay(t *testing.T) {
	body := strippedPage(t)

	if strings.Contains(body, "autoplay") {
		t.Error("the page has an autoplay attribute; browsers block it and the block looks like a broken stream")
	}
	if strings.Contains(body, ".play()") {
		t.Error("the page calls play() itself, which is autoplay by another name")
	}
}

// TestPlayerHasNoSkipControl: there is no skip in this product by design.
func TestPlayerHasNoSkipControl(t *testing.T) {
	body := strings.ToLower(strippedPage(t))

	for _, banned := range []string{"skip", "next track", "currenttime ="} {
		if strings.Contains(body, banned) {
			t.Errorf("the player page contains %q", banned)
		}
	}
}

// TestHlsJsIsServedLocally: no CDN. This is a self-hosted product that has to
// work with no internet connection.
func TestHlsJsIsServedLocally(t *testing.T) {
	s := mustServer(t)
	body := get(t, s, "/").Body.String()

	for _, cdn := range []string{"//cdn.", "https://unpkg", "jsdelivr", "cdnjs"} {
		if strings.Contains(body, cdn) {
			t.Errorf("the page loads a script from %q instead of a vendored copy", cdn)
		}
	}

	rec := get(t, s, "/vendor/hls.light.min.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /vendor/hls.light.min.js returned %d, want 200", rec.Code)
	}
	if rec.Body.Len() < 100_000 {
		t.Errorf("vendored hls.js is only %d bytes; that is not the library", rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Errorf("Content-Type = %q, want a JavaScript type", got)
	}
}

// TestVendoredLicenceIsShipped: hls.js is Apache-2.0, which requires the licence
// to travel with the copy.
func TestVendoredLicenceIsShipped(t *testing.T) {
	rec := get(t, mustServer(t), "/vendor/hls.js.LICENSE")

	if rec.Code != http.StatusOK {
		t.Fatalf("the vendored licence is not served: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Apache License") {
		t.Error("the shipped licence is not the Apache License")
	}
}

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
// program has no authentication.
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

type fakeDial struct{ v any }

func (f fakeDial) Dial() any { return f.v }

// TestServerServesTheDial: the dial is the primary UI per §22A, so it needs a
// route a client can actually fetch.
func TestServerServesTheDial(t *testing.T) {
	s, _ := newServer(t, 0)
	s.SetDialSource(fakeDial{v: map[string]any{
		"stations": []map[string]any{{"tag": "rock", "tracks": 412, "jock": "dutch_mahoney"}},
		"enriched": 412, "total": 652,
	}})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stations.json", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /stations.json = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if got["total"] == nil {
		t.Error("the payload omits total, so a client cannot say how provisional the dial is")
	}
}

// TestServerDialUnavailableWithoutASource: 503, not 404. The route exists; the
// dial does not yet, which is a real state on an unenriched library.
func TestServerDialUnavailableWithoutASource(t *testing.T) {
	s, _ := newServer(t, 0)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stations.json", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /stations.json with no source = %d, want 503", rec.Code)
	}
}

type fakeTuner struct {
	tuned   string
	tracks  int
	verdict string
	jock    string
	err     error
}

func (f *fakeTuner) Tune(tag string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.tuned = tag
	return f.tracks, nil
}
func (f *fakeTuner) Feedback(v string) error { f.verdict = v; return f.err }
func (f *fakeTuner) Jocks() any {
	return map[string]any{"jocks": []map[string]any{
		{"id": "dutch_mahoney", "name": "Dutch", "on_air": true},
		{"id": "roxy_sinclair", "name": "Roxy", "on_air": false},
	}}
}
func (f *fakeTuner) SetJock(id string) error {
	if f.err != nil {
		return f.err
	}
	f.jock = id
	return nil
}

func post(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestServerTunes: the dial is the primary UI, so changing station has to be a
// real endpoint rather than a restart.
func TestServerTunes(t *testing.T) {
	s, _ := newServer(t, 0)
	tuner := &fakeTuner{tracks: 36}
	s.SetTuner(tuner)

	rec := post(t, s, "/tune", `{"tag":"alternative"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /tune = %d, want 200: %s", rec.Code, rec.Body)
	}
	if tuner.tuned != "alternative" {
		t.Errorf("tuned to %q", tuner.tuned)
	}
	if !strings.Contains(rec.Body.String(), "36") {
		t.Errorf("the reply does not say how big the station is: %s", rec.Body)
	}
}

// TestServerRecordsAThumbsDown. This is the only signal the writing ever gets
// from a real ear; everything else is a machine checking rules it was handed.
func TestServerRecordsAThumbsDown(t *testing.T) {
	s, _ := newServer(t, 0)
	tuner := &fakeTuner{}
	s.SetTuner(tuner)

	if rec := post(t, s, "/feedback", `{"verdict":"down"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("POST /feedback = %d, want 204: %s", rec.Code, rec.Body)
	}
	if tuner.verdict != "down" {
		t.Errorf("verdict recorded as %q", tuner.verdict)
	}
	if rec := post(t, s, "/feedback", `{"verdict":"maybe"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an invalid verdict returned %d, want 400", rec.Code)
	}
}

// TestServerTuningRequiresAPost: a GET that changed what is playing would be
// followed by any prefetcher on the network and retuned by a page reload.
func TestServerTuningRequiresAPost(t *testing.T) {
	s, _ := newServer(t, 0)
	s.SetTuner(&fakeTuner{tracks: 5})

	tuner := &fakeTuner{tracks: 5}
	s.SetTuner(tuner)

	// The request carries a VALID BODY. Neither a status check nor an
	// "was the tuner called" check catches a mis-routed GET on its own: with
	// an empty body the handler fails on decoding and returns 400 before it
	// reaches the tuner, so both assertions pass for the wrong reason. A GET
	// that would actually have worked is the only thing that distinguishes
	// "the router refused it" from "the body happened to be missing".
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tune", strings.NewReader(`{"tag":"alternative"}`))
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)

	if tuner.tuned != "" {
		t.Errorf("GET /tune reached the tuner and tuned to %q", tuner.tuned)
	}
	if rec.Code == http.StatusOK {
		t.Error("GET /tune returned 200")
	}
}

// TestServerTuningUnavailableWithoutATuner: 503, because the route exists and
// the capability does not -- the spike path has no library to tune.
func TestServerTuningUnavailableWithoutATuner(t *testing.T) {
	s, _ := newServer(t, 0)
	for _, p := range []string{"/tune", "/feedback"} {
		if rec := post(t, s, p, `{}`); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("POST %s with no tuner = %d, want 503", p, rec.Code)
		}
	}
}

// TestServerListsAndChangesTheJock.
//
// Nine personas shipped and the roster was INVISIBLE: the station picked one
// from what the library sounded like and a listener could not see the others,
// let alone choose.
func TestServerListsAndChangesTheJock(t *testing.T) {
	s, _ := newServer(t, 0)
	tuner := &fakeTuner{}
	s.SetTuner(tuner)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/jocks.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /jocks.json = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "on_air") {
		t.Error("the roster does not say who is on air")
	}

	if rec := post(t, s, "/jock", `{"id":"roxy_sinclair"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("POST /jock = %d, want 204: %s", rec.Code, rec.Body)
	}
	if tuner.jock != "roxy_sinclair" {
		t.Errorf("jock set to %q", tuner.jock)
	}

	tuner.err = errNoSuchJock
	if rec := post(t, s, "/jock", `{"id":"nobody"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown jock returned %d, want 400", rec.Code)
	}
}

var errNoSuchJock = fmt.Errorf("no jock with id \"nobody\"")

type fakeAdmin struct {
	cadence   int
	enriching bool
	err       error
}

func (f *fakeAdmin) Overview() any { return map[string]any{"cadence": f.cadence} }
func (f *fakeAdmin) SetCadence(n int) error {
	if f.err != nil {
		return f.err
	}
	f.cadence = n
	return nil
}
func (f *fakeAdmin) SetEnriching(on bool) error { f.enriching = on; return f.err }

func postAuth(t *testing.T, s *Server, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(AdminTokenHeader, token)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestAdminWritesRefusedWithoutAToken is the safety property, and the one that
// matters most on a product shipping no authentication at all.
//
// An UNSET token must refuse every write. Treating unset as "allow anyone"
// would make the default configuration -- the one the supplied compose file
// publishes on the LAN -- the dangerous one.
func TestAdminWritesRefusedWithoutAToken(t *testing.T) {
	s, _ := newServer(t, 0)
	admin := &fakeAdmin{cadence: 4}
	s.SetAdmin(admin)
	// No SetAdminToken call at all: this is the default configuration.

	for _, tc := range []struct{ path, body string }{
		{"/admin/cadence", `{"cadence":99}`},
		{"/admin/enriching", `{"enriching":false}`},
	} {
		rec := postAuth(t, s, tc.path, tc.body, "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s with no token configured = %d, want 403", tc.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "JOCKORA_ADMIN_TOKEN") {
			t.Errorf("the refusal does not name the fix: %s", rec.Body)
		}
	}
	if admin.cadence != 4 {
		t.Errorf("cadence was changed to %d despite no token", admin.cadence)
	}
}

// TestAdminWritesRejectAWrongToken.
func TestAdminWritesRejectAWrongToken(t *testing.T) {
	s, _ := newServer(t, 0)
	admin := &fakeAdmin{cadence: 4}
	s.SetAdmin(admin)
	s.SetAdminToken("correct-horse")

	if rec := postAuth(t, s, "/admin/cadence", `{"cadence":99}`, "wrong"); rec.Code != http.StatusForbidden {
		t.Errorf("a wrong token = %d, want 403", rec.Code)
	}
	if admin.cadence != 4 {
		t.Errorf("cadence changed to %d on a wrong token", admin.cadence)
	}
	if rec := postAuth(t, s, "/admin/cadence", `{"cadence":8}`, "correct-horse"); rec.Code != http.StatusNoContent {
		t.Fatalf("the right token = %d, want 204: %s", rec.Code, rec.Body)
	}
	if admin.cadence != 8 {
		t.Errorf("cadence = %d after an authorised write, want 8", admin.cadence)
	}
}

// TestAdminReadsAreOpen: /now.json already exposes this class of information on
// an unauthenticated service, so gating reads would break the page and protect
// nothing new.
func TestAdminReadsAreOpen(t *testing.T) {
	s, _ := newServer(t, 0)
	s.SetAdmin(&fakeAdmin{cadence: 4})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/overview.json", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /admin/overview.json = %d, want 200", rec.Code)
	}
}

// TestListenerControlsAreNotBehindTheAdminToken: tuning and picking a jock
// change what is playing now, which is what the dial is for. The token guards
// operator CONFIGURATION, which outlives the session.
func TestListenerControlsAreNotBehindTheAdminToken(t *testing.T) {
	s, _ := newServer(t, 0)
	tuner := &fakeTuner{tracks: 12}
	s.SetTuner(tuner)
	s.SetAdmin(&fakeAdmin{})
	s.SetAdminToken("correct-horse")

	if rec := postAuth(t, s, "/tune", `{"tag":"rock"}`, ""); rec.Code != http.StatusOK {
		t.Errorf("POST /tune without the admin token = %d, want 200", rec.Code)
	}
	if rec := postAuth(t, s, "/jock", `{"id":"roxy_sinclair"}`, ""); rec.Code != http.StatusNoContent {
		t.Errorf("POST /jock without the admin token = %d, want 204", rec.Code)
	}
}
