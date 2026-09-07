// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package web holds the browser app, embedded so the server is a single binary
// with no runtime asset path.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// Files holds the built app and the notice shown when it has not been built.
//
// dist is the Angular build and is NOT committed. fallback.html lives OUTSIDE
// it because `ng build` cleans its output directory: a notice kept in there
// would be deleted by the first build and show up as a missing file in every
// diff afterwards.
//
// all: so the .gitkeep that holds the empty directory is embedded too --
// without it the pattern matches nothing and the package will not compile on a
// tree that has never built the app.
//
// THE HAND-WRITTEN PAGES ARE GONE, deleted at parity in the row 15 gate along
// with the vendored hls.js they loaded. Two UIs is two of everything; the
// Angular app took over every behaviour they had, and test/parity_checklist.md
// is the row-by-row record of that.
//
//go:embed all:dist fallback.html
var Files embed.FS

// Handler serves the browser app.
func Handler() http.Handler { return HandlerFor(Files) }

// HandlerFor serves one filesystem, choosing what it can actually offer.
//
// TWO STATES: a built Angular app, or a notice saying how to build it. The
// second exists because a server with no browser app is still a working server
// -- it streams, and every client but the browser is unaffected -- and an
// operator meeting a blank page deserves the command rather than a mystery.
func HandlerFor(files fs.FS) http.Handler {
	if root, index, ok := spaRoot(files); ok {
		return spaHandler(root, index)
	}
	return fallbackHandler(files)
}

// spaRoot finds the built app. Angular 17 and later write into dist/browser;
// older layouts write straight into dist.
func spaRoot(files fs.FS) (fs.FS, string, bool) {
	for _, dir := range []string{"dist/browser", "dist"} {
		// The error is discarded because fs.Sub only rejects an invalid path
		// and both of these are constants: a branch no test can enter and no
		// build can produce.
		sub, _ := fs.Sub(files, dir)
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			return sub, "index.html", true
		}
	}
	return nil, "", false
}

// spaHandler serves the built app with a SPA fallback.
//
// A path that names no file falls back to index.html so a deep link the router
// owns still loads -- but ONLY when it could be a route. A missing asset stays
// a 404: answering an unknown .js with HTML makes a broken build look like a
// working one until the browser tries to parse it.
func spaHandler(root fs.FS, index string) http.Handler {
	server := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			serveFile(w, r, root, index)
			return
		}
		// A directory is not a listing. The file server would happily render
		// one, which publishes the shape of the build.
		if info, err := fs.Stat(root, name); err == nil && !info.IsDir() {
			server.ServeHTTP(w, r)
			return
		}
		if looksLikeAsset(name) {
			http.NotFound(w, r)
			return
		}
		serveFile(w, r, root, index)
	})
}

// fallbackHandler answers every page with the notice.
func fallbackHandler(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"dist/fallback.html", "fallback.html"} {
			if _, err := fs.Stat(files, name); err == nil {
				serveFile(w, r, files, name)
				return
			}
		}
		http.NotFound(w, r)
	})
}

// looksLikeAsset says whether a missing path should 404 rather than fall back
// to index.html. Anything with a file extension is a file somebody asked for
// by name.
func looksLikeAsset(name string) bool {
	base := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		base = name[i+1:]
	}
	return strings.Contains(base, ".")
}

func serveFile(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	body, err := fs.ReadFile(files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".html") {
		// The app shell changes with every build; a cached one is a stale app
		// pointing at hashed assets that no longer exist.
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, buildTime, strings.NewReader(string(body)))
}

// buildTime is the modification time every embedded file reports. Embedded
// files have none of their own, and a zero time makes ServeContent skip the
// conditional-request handling entirely.
var buildTime = time.Unix(0, 0)
