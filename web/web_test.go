// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func hit(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestWebFallbackWhenDistEmpty: a server with no browser app is still a working
// server, and an operator meeting a blank page deserves the command rather than
// a mystery.
func TestWebFallbackWhenDistEmpty(t *testing.T) {
	h := HandlerFor(fstest.MapFS{
		"fallback.html": {Data: []byte("<h1>Web assets not built</h1><p>Run: make web</p>")},
	})

	for _, path := range []string{"/", "/admin", "/anything"} {
		rec := hit(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "not built") {
			t.Errorf("GET %s does not say the app is missing: %s", path, rec.Body)
		}
	}
}

// TestWebServesDistIndex: the built app, at both the listener's route and the
// operator's, because the router owns both and the server hands it the shell.
func TestWebServesDistIndex(t *testing.T) {
	h := HandlerFor(fstest.MapFS{
		"dist/browser/index.html":  {Data: []byte("<app-root></app-root>")},
		"dist/browser/assets/x.js": {Data: []byte("console.log(1)")},
	})

	for _, path := range []string{"/", "/admin", "/admin/stations", "/some/deep/route"} {
		rec := hit(t, h, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "app-root") {
			t.Errorf("GET %s did not serve the app shell: %s", path, rec.Body)
		}
	}

	// The shell must not be cached: it changes every build and points at
	// hashed assets that no longer exist.
	if cc := hit(t, h, "/").Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("the shell is cached: %q", cc)
	}

	asset := hit(t, h, "/assets/x.js")
	if asset.Code != http.StatusOK || asset.Body.String() != "console.log(1)" {
		t.Fatalf("asset = %d %q", asset.Code, asset.Body)
	}
	if ct := asset.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("asset Content-Type = %q", ct)
	}
}

// TestWebServesDistAtRoot covers the older layout, where the build lands
// straight in dist rather than dist/browser.
func TestWebServesDistAtRoot(t *testing.T) {
	h := HandlerFor(fstest.MapFS{
		"dist/index.html": {Data: []byte("<app-root></app-root>")},
	})
	if rec := hit(t, h, "/"); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "app-root") {
		t.Errorf("= %d %q", rec.Code, rec.Body)
	}
}

// TestWebNoDirectoryListing: a listing publishes the shape of the build.
func TestWebNoDirectoryListing(t *testing.T) {
	h := HandlerFor(fstest.MapFS{
		"dist/browser/index.html":  {Data: []byte("<app-root></app-root>")},
		"dist/browser/assets/x.js": {Data: []byte("console.log(1)")},
	})

	// A directory never becomes a listing, and never becomes a REDIRECT to
	// one either: http.FileServer answers a directory path without a trailing
	// slash with a 301, which sends a browser somewhere that then quietly
	// renders the app shell. Both shapes get the shell directly.
	for _, path := range []string{"/assets", "/assets/"} {
		rec := hit(t, h, path)
		if strings.Contains(rec.Body.String(), "x.js") {
			t.Errorf("GET %s listed the directory: %s", path, rec.Body)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the app shell", path, rec.Code)
		}
	}
}

// TestWebUnknownAssetIs404: answering an unknown .js with HTML makes a broken
// build look like a working one until the browser tries to parse it.
func TestWebUnknownAssetIs404(t *testing.T) {
	h := HandlerFor(fstest.MapFS{
		"dist/browser/index.html": {Data: []byte("<app-root></app-root>")},
	})

	for _, path := range []string{"/assets/nope.js", "/main.abc123.js", "/styles.css", "/favicon.ico"} {
		rec := hit(t, h, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (it served %q)", path, rec.Code,
				strings.TrimSpace(rec.Body.String()))
		}
	}
	// A route with no extension is the router's, and gets the shell.
	if rec := hit(t, h, "/admin/stations/3"); rec.Code != http.StatusOK {
		t.Errorf("a deep route = %d, want the app shell", rec.Code)
	}
}

// TestWebServesTheLegacyPagesUntilReplaced: 15b and 15c replace these, and
// TestWebFallsBackWhenNothingIsBuilt: with no dist, every page is the notice.
//
// TWO STATES now, not three. The hand-written pages were deleted at parity in
// the row 15 gate; test/parity_checklist.md is the row-by-row record of where
// each of their behaviours went. What is left is: a built app, or a notice
// saying how to build one.
//
// Over a FIXTURE rather than the real embed, so the answer does not change
// depending on whether somebody has run `make web` -- which is exactly the
// difference this layering exists to absorb.
func TestWebFallsBackWhenNothingIsBuilt(t *testing.T) {
	h := HandlerFor(fstest.MapFS{
		"fallback.html": {Data: []byte("not built")},
	})

	// EVERY path, including ones that used to be pages of their own. An
	// operator who lands anywhere gets the command rather than a 404 they have
	// to interpret.
	for _, path := range []string{"/", "/admin", "/index.html", "/vendor/hls.light.min.js", "/nope"} {
		rec := hit(t, h, path)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "not built") {
			t.Errorf("GET %s = %d %q, want the notice", path, rec.Code, rec.Body.String())
		}
	}
}

// TestWebHandlerAlwaysAnswers: whatever state the tree is in -- app built, app
// not built, pages not yet replaced -- a browser asking for the root must get a
// page. A blank 200 or a 404 there reads as a broken server.
func TestWebHandlerAlwaysAnswers(t *testing.T) {
	rec := hit(t, Handler(), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("GET / served an empty page")
	}
}

func TestWebNothingToServe(t *testing.T) {
	// Neither an app nor pages nor a notice: the honest answer is 404 rather
	// than a blank 200.
	h := HandlerFor(fstest.MapFS{})
	if rec := hit(t, h, "/"); rec.Code != http.StatusNotFound {
		t.Errorf("= %d, want 404", rec.Code)
	}
}

func TestWebEdgeCases(t *testing.T) {
	// A dist that is a FILE rather than a directory: fs.Sub succeeds and the
	// stat inside it does not, which is the path a half-finished build takes.
	h := HandlerFor(fstest.MapFS{
		"dist":          {Data: []byte("not a directory")},
		"fallback.html": {Data: []byte("not built")},
	})
	if rec := hit(t, h, "/"); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "not built") {
		t.Errorf("= %d %q", rec.Code, rec.Body)
	}

	// A page the fallback itself cannot read.
	empty := HandlerFor(fstest.MapFS{"dist/other.html": {Data: []byte("x")}})
	if rec := hit(t, empty, "/"); rec.Code != http.StatusNotFound {
		t.Errorf("= %d, want 404", rec.Code)
	}

	// A legacy page named in the routing but missing from the filesystem: the
	// answer is 404, not a panic or an empty 200.
	half := HandlerFor(fstest.MapFS{"index.html": {Data: []byte("<audio>")}})
	if rec := hit(t, half, "/admin"); rec.Code != http.StatusNotFound {
		t.Errorf("a missing console page = %d, want 404", rec.Code)
	}
}

// brokenFS stats fine and refuses to open. A real one: an embedded file is
// never unreadable, but a `dist` on disk during a rebuild is, and so is one on
// a filesystem that has gone away underneath a running server.
type brokenFS struct{ fs.FS }

func (b brokenFS) Open(name string) (fs.File, error) {
	if strings.HasSuffix(name, ".html") {
		return nil, fs.ErrPermission
	}
	return b.FS.Open(name)
}

// Stat still works, which is the whole point: the handler looks before it
// leaps, and the leap is what fails.
func (b brokenFS) Stat(name string) (fs.FileInfo, error) { return fs.Stat(b.FS, name) }

// TestWebUnreadableFileIs404: a file that cannot be read is a 404, not a panic
// and not a blank 200. A blank 200 is the worst of the three -- the browser
// renders nothing and reports nothing, and the server looks healthy.
func TestWebUnreadableFileIs404(t *testing.T) {
	h := HandlerFor(brokenFS{fstest.MapFS{
		"fallback.html": {Data: []byte("not built")},
	}})

	if rec := hit(t, h, "/"); rec.Code != http.StatusNotFound {
		t.Errorf("GET / = %d, want 404 when the page cannot be read", rec.Code)
	}
}

// TestWebKeepsTheEmbedMarker: dist/.gitkeep exists.
//
// It is the only thing that makes `//go:embed all:dist` legal on a tree that
// has never run `ng build`, and `ng build` DELETES IT -- it cleans its output
// directory. `make web` puts it back; this is what notices when something
// else does not. The failure it prevents is not subtle but it is remote: the
// web package stops compiling, on somebody else's machine, for a reason that
// has nothing to do with what they changed.
func TestWebKeepsTheEmbedMarker(t *testing.T) {
	if _, err := fs.Stat(Files, "dist/.gitkeep"); err != nil {
		t.Fatalf("web/dist/.gitkeep is missing: %v\n"+
			"without it the embed pattern matches nothing on an unbuilt tree", err)
	}
}
