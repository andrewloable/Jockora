// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type stationTouch struct {
	mu   sync.Mutex
	last int64
	sess string
	n    int
}

func (s *stationTouch) Touch(station int64, session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, s.sess, s.n = station, session, s.n+1
}
func (s *stationTouch) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// hlsServer builds a server over two station directories with distinct
// playlists, so a test can prove which one answered.
func hlsServer(t *testing.T, session string) (*Server, *stationTouch) {
	t.Helper()
	s, _ := newServer(t, 0)
	root := s.cfg.SegmentDir
	for _, id := range []string{"1", "2"} {
		sub := filepath.Join(root, id)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "stream.m3u8"),
			[]byte("#EXTM3U\n#station-"+id+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "seg0.ts"),
			[]byte("segment-"+id), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	touch := &stationTouch{}
	s.SetPresence(touch)
	if session != "" {
		s.SetSessions(func(http.ResponseWriter, *http.Request) (string, bool) {
			return session, true
		})
	} else {
		s.SetSessions(func(http.ResponseWriter, *http.Request) (string, bool) {
			return "", false
		})
	}
	return s, touch
}

// TestHLSPerStationDir: two stations writing into one directory would overwrite
// each other's segments, so each gets its own and the URL says which.
func TestHLSPerStationDir(t *testing.T) {
	s, _ := hlsServer(t, "s1")

	for _, id := range []string{"1", "2"} {
		rec := get(t, s, "/hls/"+id+"/stream.m3u8")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /hls/%s/stream.m3u8 = %d: %s", id, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "station-"+id) {
			t.Errorf("station %s was served %q", id, rec.Body)
		}
		seg := get(t, s, "/hls/"+id+"/seg0.ts")
		if seg.Code != http.StatusOK || seg.Body.String() != "segment-"+id {
			t.Errorf("station %s segment = %d %q", id, seg.Code, seg.Body)
		}
	}

	// A station with no directory is a 404, not another station's audio.
	if rec := get(t, s, "/hls/9/stream.m3u8"); rec.Code != http.StatusNotFound {
		t.Errorf("a station that is not running = %d, want 404", rec.Code)
	}
}

func TestHLSPlaylistTouchesPresence(t *testing.T) {
	s, touch := hlsServer(t, "s1")

	if rec := get(t, s, "/hls/2/stream.m3u8"); rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	touch.mu.Lock()
	station, sess, n := touch.last, touch.sess, touch.n
	touch.mu.Unlock()
	if n != 1 || station != 2 || sess != "s1" {
		t.Errorf("Touch(%d, %q) called %d times, want Touch(2, \"s1\") once", station, sess, n)
	}
}

// TestHLSSegmentDoesNotTouch: a segment is fetched once and then cached, so
// counting it would keep a station alive on a client that had stopped playing.
func TestHLSSegmentDoesNotTouch(t *testing.T) {
	s, touch := hlsServer(t, "s1")

	if rec := get(t, s, "/hls/1/seg0.ts"); rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	if n := touch.count(); n != 0 {
		t.Errorf("a segment fetch recorded %d heartbeats, want 0", n)
	}
}

// TestHLSNoSessionIs401: presence is what starts and stops stations, so a
// listener who cannot be counted must not be served -- their station would
// never stop.
func TestHLSNoSessionIs401(t *testing.T) {
	s, touch := hlsServer(t, "")

	for _, path := range []string{"/hls/1/stream.m3u8", "/hls/1/seg0.ts"} {
		rec := get(t, s, path)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with no session = %d, want 401", path, rec.Code)
		}
	}
	if n := touch.count(); n != 0 {
		t.Errorf("%d heartbeats recorded for an unauthenticated listener", n)
	}

	// A server with no session source at all refuses everything rather than
	// streaming to a listener it cannot count.
	bare, _ := newServer(t, 0)
	bare.SetSessions(nil)
	if rec := get(t, bare, "/hls/1/stream.m3u8"); rec.Code != http.StatusUnauthorized {
		t.Errorf("a server with no session source = %d, want 401", rec.Code)
	}
}

func TestHLSBadStationID(t *testing.T) {
	s, touch := hlsServer(t, "s1")

	for _, path := range []string{
		"/hls/abc/stream.m3u8",
		"/hls/1/..%2f..%2fx",
		"/hls/..%2f..%2fetc/stream.m3u8",
		"/hls/1/stream.m3u8.bak",
		"/hls/-1/stream.m3u8",
		"/hls//stream.m3u8",
		"/hls/1/",
		// Longer than any real id, so ParseInt would overflow.
		"/hls/99999999999999999999999/stream.m3u8",
	} {
		if rec := get(t, s, path); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
	if n := touch.count(); n != 0 {
		t.Errorf("%d heartbeats recorded for rejected requests", n)
	}
}

// TestHLSLegacyPathGone: /hls/stream.m3u8 served the one station v0.1 had. It
// is removed rather than aliased, because a listener served from it would play
// without counting as anyone's listener.
func TestHLSLegacyPathGone(t *testing.T) {
	s, _ := hlsServer(t, "s1")

	for _, path := range []string{"/hls/stream.m3u8", "/hls/seg0.ts", "/hls/"} {
		if rec := get(t, s, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestHLSCacheHeadersUnchanged(t *testing.T) {
	s, _ := hlsServer(t, "s1")

	// A cached playlist is a frozen stream: the client never learns that new
	// segments exist.
	pl := get(t, s, "/hls/1/stream.m3u8")
	if got := pl.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("playlist Cache-Control = %q, want no-store", got)
	}
	if got := pl.Header().Get("Content-Type"); got != "application/vnd.apple.mpegurl" {
		t.Errorf("playlist Content-Type = %q", got)
	}

	// A segment's contents never change once written.
	seg := get(t, s, "/hls/1/seg0.ts")
	if got := seg.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("segment Cache-Control = %q, want immutable", got)
	}
	if got := seg.Header().Get("Content-Type"); got != "video/mp2t" {
		t.Errorf("segment Content-Type = %q", got)
	}

	// A miss must not be cached either, or an intermediary keeps answering 404
	// after the segment appears.
	miss := get(t, s, "/hls/1/seg99.ts")
	if miss.Code != http.StatusNotFound {
		t.Fatalf("= %d", miss.Code)
	}
	if got := miss.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("404 Cache-Control = %q, want no-store", got)
	}
}

// TestHLSDirectoryIsNotServed: a station id that names a directory rather than
// a file must not produce a listing.
func TestHLSDirectoryIsNotServed(t *testing.T) {
	s, _ := hlsServer(t, "s1")

	// stream.m3u8 as a DIRECTORY inside station 3.
	sub := filepath.Join(s.cfg.SegmentDir, "3", "stream.m3u8")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, s, "/hls/3/stream.m3u8"); rec.Code != http.StatusNotFound {
		t.Errorf("a directory was served as a playlist: %d", rec.Code)
	}
}

// TestHLSSweepIsPerStation: segments moved a level down when stations got their
// own directories. A sweeper still looking only at the root would find nothing
// to prune, so every station would keep every segment it ever wrote and the
// disk would fill over days -- with the stream healthy right up until it
// stopped.
func TestHLSSweepIsPerStation(t *testing.T) {
	s, _ := newServer(t, 2)
	root := s.cfg.SegmentDir

	for _, id := range []string{"1", "2"} {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 5; n++ {
			if err := os.WriteFile(filepath.Join(dir, "seg"+strconv.Itoa(n)+".ts"),
				[]byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// Not segments: a playlist, a stray file, and a directory. The segment
		// directory is operator-supplied and may not be exclusively ours.
		if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A number too large for an int, which the name allowlist happily
		// accepts and Atoi does not.
		if err := os.WriteFile(filepath.Join(dir, "seg99999999999999999999.ts"),
			[]byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Junk at the ROOT is skipped: it belongs to no station.
	if err := os.WriteFile(filepath.Join(root, "seg0.ts"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "notastation"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := s.Sweep(); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"1", "2"} {
		left, _ := filepath.Glob(filepath.Join(root, id, "seg[0-9].ts"))
		if len(left) != 2 {
			t.Errorf("station %s kept %v, want the newest 2", id, left)
		}
		if _, err := os.Stat(filepath.Join(root, id, "stream.m3u8")); err != nil {
			t.Errorf("station %s lost its playlist: %v", id, err)
		}
		if _, err := os.Stat(filepath.Join(root, id, "notes")); err != nil {
			t.Errorf("station %s lost a directory it did not own: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "seg0.ts")); err != nil {
		t.Errorf("a file at the root, belonging to no station, was swept: %v", err)
	}
}

func TestHLSSweepSurfacesFailures(t *testing.T) {
	// A root that does not exist yet is the NORMAL state of a server nobody is
	// listening to: a station creates its own directory when it starts. Warning
	// about it every sweep would fill the log of an idle station with a fault
	// that is not one.
	t.Run("no segment root yet", func(t *testing.T) {
		s, _ := newServer(t, 2)
		if err := os.RemoveAll(s.cfg.SegmentDir); err != nil {
			t.Fatal(err)
		}
		if err := s.Sweep(); err != nil {
			t.Errorf("sweeping before any station started = %v, want nil", err)
		}
	})

	t.Run("unreadable segment root", func(t *testing.T) {
		s, _ := newServer(t, 2)
		if err := os.Chmod(s.cfg.SegmentDir, 0o000); err != nil {
			t.Skipf("cannot make a directory unreadable here: %v", err)
		}
		t.Cleanup(func() { os.Chmod(s.cfg.SegmentDir, 0o755) })
		if err := s.Sweep(); err == nil {
			t.Error("a segment root that could not be read was reported as swept")
		}
	})

	t.Run("unreadable station directory", func(t *testing.T) {
		s, _ := newServer(t, 2)
		dir := filepath.Join(s.cfg.SegmentDir, "7")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Skipf("cannot make a directory unreadable here: %v", err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		if err := s.Sweep(); err == nil {
			t.Error("a station directory that could not be read was reported as swept")
		}
	})

	t.Run("undeletable segment", func(t *testing.T) {
		s, _ := newServer(t, 1)
		dir := filepath.Join(s.cfg.SegmentDir, "8")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for n := 0; n < 3; n++ {
			if err := os.WriteFile(filepath.Join(dir, "seg"+strconv.Itoa(n)+".ts"),
				[]byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// Readable, so the listing works; not writable, so the removal fails.
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Skipf("cannot make a directory read-only here: %v", err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		if err := s.Sweep(); err == nil {
			t.Error("a segment that could not be deleted was reported as swept")
		}
	})
}
