// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeSubsonic is a minimal OpenSubsonic server: two albums, three songs, and a
// record of every request so a test can prove nothing was written.
type fakeSubsonic struct {
	mu       sync.Mutex
	requests []string
	failAuth bool
}

func (f *fakeSubsonic) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if f.failAuth {
			// A Subsonic server reports failure INSIDE a 200.
			fmt.Fprint(w, `{"subsonic-response":{"status":"failed",
				"error":{"code":40,"message":"Wrong username or password"}}}`)
			return
		}

		switch {
		case strings.HasSuffix(r.URL.Path, "/ping.view"):
			fmt.Fprint(w, `{"subsonic-response":{"status":"ok"}}`)
		case strings.HasSuffix(r.URL.Path, "/getAlbumList2.view"):
			// One album per page, so a PageSize of 1 forces real paging.
			switch r.URL.Query().Get("offset") {
			case "0":
				fmt.Fprint(w, `{"subsonic-response":{"status":"ok","albumList2":{"album":[{"id":"al-1"}]}}}`)
			case "1":
				fmt.Fprint(w, `{"subsonic-response":{"status":"ok","albumList2":{"album":[{"id":"al-2"}]}}}`)
			default:
				fmt.Fprint(w, `{"subsonic-response":{"status":"ok","albumList2":{"album":[]}}}`)
			}
		case strings.HasSuffix(r.URL.Path, "/getAlbum.view"):
			songs := map[string][]map[string]any{
				"al-1": {
					{"id": "s1", "title": "Creep", "artist": "Radiohead", "album": "Pablo Honey", "year": 1993, "duration": 238, "size": 5000000},
					{"id": "s2", "title": "Anyone Can Play Guitar", "artist": "Radiohead", "album": "Pablo Honey", "year": 1993, "duration": 216, "size": 4600000},
				},
				"al-2": {
					{"id": "s3", "title": "Glycerine", "artist": "Bush", "album": "Sixteen Stone", "year": 1994, "duration": 267, "size": 5400000},
				},
			}[r.URL.Query().Get("id")]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"subsonic-response": map[string]any{"status": "ok", "album": map[string]any{"song": songs}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSubsonic(t *testing.T, f *fakeSubsonic) (Subsonic, *httptest.Server) {
	srv := f.start(t)
	// PageSize 1 so the album loop must fetch a second page to see everything.
	return Subsonic{BaseURL: srv.URL, User: "andrew", Password: "hunter2",
		Client: srv.Client(), PageSize: 1}, srv
}

func TestSubsonicScansEveryAlbum(t *testing.T) {
	f := &fakeSubsonic{}
	src, _ := newSubsonic(t, f)
	s := openStore(t)

	stats, err := src.Scan(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Found != 3 || stats.Added != 3 {
		t.Errorf("found/added = %d/%d, want 3/3", stats.Found, stats.Added)
	}

	var artist, title, album string
	var year int
	if err := s.DB().QueryRow(
		`SELECT artist, title, album, year FROM tracks WHERE title = 'Glycerine'`).
		Scan(&artist, &title, &album, &year); err != nil {
		t.Fatal(err)
	}
	if artist != "Bush" || album != "Sixteen Stone" || year != 1994 {
		t.Errorf("metadata not carried across: %s / %s / %d", artist, album, year)
	}
}

// TestSubsonicScanIsIdempotent is the bug this design exists to avoid.
//
// Rows are keyed on the path, and for a remote library the path IS the stream
// URL. A random auth salt would produce a different URL for the same song on
// every scan, so a rescan would not update ten thousand rows, it would ADD ten
// thousand more.
func TestSubsonicScanIsIdempotent(t *testing.T) {
	f := &fakeSubsonic{}
	src, _ := newSubsonic(t, f)
	s := openStore(t)

	if _, err := src.Scan(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}
	stats, err := src.Scan(context.Background(), s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Added != 0 {
		t.Errorf("second scan added %d rows; the library is duplicating", stats.Added)
	}
	if got := countTracks(t, s); got != 3 {
		t.Errorf("%d rows after two scans, want 3", got)
	}
}

// TestSubsonicNeverWritesToTheLibrary is the invariant, proven rather than
// promised: every request the provider makes is a GET, and none of them touches
// a mutating endpoint.
func TestSubsonicNeverWritesToTheLibrary(t *testing.T) {
	f := &fakeSubsonic{}
	src, _ := newSubsonic(t, f)
	s := openStore(t)

	if _, err := src.Scan(context.Background(), s, nil); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("no requests recorded; the test proves nothing")
	}
	for _, req := range f.requests {
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("non-GET request %q: a library provider must never write", req)
		}
		for _, mutating := range []string{
			"setRating", "star", "unstar", "scrobble", "createPlaylist",
			"updatePlaylist", "deletePlaylist", "createUser", "deleteUser",
		} {
			if strings.Contains(req, mutating) {
				t.Errorf("provider called mutating endpoint %q", mutating)
			}
		}
	}
}

// TestSubsonicReportsAuthFailure: a Subsonic server returns errors inside a 200,
// so trusting the HTTP status reads "wrong password" as an empty library and
// produces a silent station with no music.
func TestSubsonicReportsAuthFailure(t *testing.T) {
	f := &fakeSubsonic{failAuth: true}
	src, _ := newSubsonic(t, f)
	s := openStore(t)

	_, err := src.Scan(context.Background(), s, nil)
	if err == nil {
		t.Fatal("a rejected password scanned successfully")
	}
	if !strings.Contains(err.Error(), "Wrong username or password") {
		t.Errorf("error does not carry the server's reason: %v", err)
	}
}

// TestSubsonicStreamURLCarriesCredentials: ffmpeg fetches this URL knowing
// nothing about this package, so everything it needs must be in the string.
func TestSubsonicStreamURLCarriesCredentials(t *testing.T) {
	src := Subsonic{BaseURL: "https://music.example/", User: "andrew", Password: "hunter2"}

	got, err := src.StreamURL("s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/rest/stream.view", "id=s1", "u=andrew", "t=", "s=", "c=jockora"} {
		if !strings.Contains(got, want) {
			t.Errorf("stream URL is missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "hunter2") {
		t.Error("the password itself is in the URL; the token exists so it is not")
	}
	if again, _ := src.StreamURL("s1"); again != got {
		t.Error("two calls produced different URLs; scans would duplicate the library")
	}
}
