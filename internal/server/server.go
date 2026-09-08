// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package server serves the playlist, the segments and the one static page.
package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/web"
)

// DefaultRetainSegments is how many segments stay on disk.
//
// It is deliberately wider than the playlist window ffmpeg advertises. A client
// that is briefly suspended still asks for the last segment it saw, and with
// retention equal to the window that request arrives just after the file was
// deleted: the client gets a 404 and hls.js can stall rather than recover.
//
// The fix is retention, not a larger hls_list_size. Raising the advertised
// window makes every client buffer more before playback starts, trading a rare
// stall for slower startup on every single listen.
const DefaultRetainSegments = 20

// SweepInterval is how often expired segments are removed.
const SweepInterval = 10 * time.Second

// Config describes the server.
type Config struct {
	ListenAddr     string
	SegmentDir     string
	RetainSegments int // default DefaultRetainSegments

	// AllowNonLoopback permits binding an address reachable from off-host.
	//
	// It exists for containers, where 127.0.0.1 is reachable only inside the
	// container's own network namespace and a published port would never
	// connect. It is an EXPLICIT opt-in and not a default, because this program
	// still speaks plain HTTP: turning it on publishes an
	// unauthenticated server to whatever network can reach the bind address.
	AllowNonLoopback bool
}

// segmentName allows exactly the two filename shapes this program produces.
// Everything else is a 400 before any filesystem path is built.
var segmentName = regexp.MustCompile(`^(seg\d+\.ts|stream\.m3u8)$`)

// stationID allows exactly the shape a station id has. Digits only, so no
// encoding trick can put a separator into the path built from it.
var stationID = regexp.MustCompile(`^\d{1,18}$`)

// segNumber extracts N from segN.ts.
var segNumber = regexp.MustCompile(`^seg(\d+)\.ts$`)

// Server serves HLS from a directory and sweeps it.
type Server struct {
	cfg    Config
	log    *slog.Logger
	ln     net.Listener
	status StatusSource

	// sessions identifies the listener; presence records that they are still
	// there. Both nil on the spike path, which refuses HLS rather than
	// streaming to an uncounted listener.
	sessions SessionSource
	presence Toucher

	// signer and users are sign-in. Nil until SetAuth, which is the spike
	// path: no database means no accounts to check against.
	signer   *auth.Signer
	users    Users
	sources  Sources
	rescan   Rescanner
	stations Stations
	jocks    Jocks
	voices   Voices
	personas Personas
	port     EnrichmentPort
	llm      LLM
	// importCap lowers MaxImportBytes, for tests only.
	importCap  int64
	playlists  Playlists
	regenerate Regenerator
	runtimes   Runtimes
	clk        clock.Clock
	failures   loginFailures
	tuner      Tuner
	admin      Admin
}

// SetStatusSource wires the /now.json data source. Without one the endpoint
// reports unavailable rather than lying about a healthy stream.
func (s *Server) SetStatusSource(src StatusSource) { s.status = src }

// New validates the configuration and opens the listener.
//
// A non-loopback listen address is refused. This program speaks plain HTTP
// of any kind, and self-hosters port-forward services routinely; binding
// anywhere else publishes an unauthenticated server.
func New(cfg Config, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.RetainSegments <= 0 {
		cfg.RetainSegments = DefaultRetainSegments
	}
	if cfg.SegmentDir == "" {
		return nil, errors.New("server: no segment directory")
	}
	if err := checkLoopback(cfg.ListenAddr); err != nil {
		if !cfg.AllowNonLoopback {
			return nil, err
		}
		// Say it loudly, every start, in the operator's log. A security posture
		// chosen once and then forgotten is how an unauthenticated service ends
		// up somewhere nobody meant it to be.
		log.Warn("SERVING WITHOUT AUTHENTICATION ON A NON-LOOPBACK ADDRESS",
			"listen", cfg.ListenAddr,
			"detail", "anyone who can reach this address can listen to the stream and read /now.json",
			"enabled_by", "--allow-lan / JOCKORA_ALLOW_LAN")
	}

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("server: listening on %s: %w", cfg.ListenAddr, err)
	}
	return &Server{cfg: cfg, log: log, ln: ln}, nil
}

// Addr is the address actually bound, with the port resolved.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Run serves until ctx is cancelled, sweeping expired segments as it goes.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{Handler: s.Handler()}

	go func() {
		t := time.NewTicker(SweepInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.Sweep(); err != nil {
					s.log.Warn("sweeping segments", "err", err)
				}
			}
		}
	}()

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown) //nolint:errcheck // shutting down regardless
	}()

	if err := srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return ctx.Err()
}

// Handler routes requests from the registry in routes.go.
//
// A TABLE rather than a switch, so the 401/403 matrix cannot go stale: a route
// added without an expectation fails the row 13 gate rather than quietly
// shipping unguarded.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath, not Path: Path is already percent-decoded, so ..%2f
		// would arrive here indistinguishable from a legitimate name. Matching
		// the raw form means an encoding trick cannot smuggle a separator past
		// the allowlist.
		p := r.URL.EscapedPath()

		rt, ok, wrongMethod := s.match(r.Method, p)
		if !ok {
			if wrongMethod {
				// "You cannot POST here" is the true answer; 404 would claim
				// the route does not exist.
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			http.NotFound(w, r)
			return
		}

		h := rt.H
		if rt.Role != "" {
			h = s.require(rt.Role, h)
		}
		h(w, r)
	})
}

// SessionSource identifies the listener behind a request.
//
// Takes the ResponseWriter as well as the request because a source that cannot
// set a cookie cannot CREATE a session, and a listener arriving for the first
// time has none to present.
type SessionSource func(w http.ResponseWriter, r *http.Request) (string, bool)

// Toucher records that a session is still listening to a station.
type Toucher interface {
	Touch(station int64, session string)
}

// SetSessions installs the listener identity. Without one every HLS request is
// refused: presence is what starts and stops stations, so a stream nobody can
// be counted for is a station that would never stop.
func (s *Server) SetSessions(src SessionSource) { s.sessions = src }

// SetPresence wires the heartbeat sink.
func (s *Server) SetPresence(t Toucher) { s.presence = t }

func (s *Server) serveHLS(w http.ResponseWriter, r *http.Request, rest string) {
	station, name, found := strings.Cut(rest, "/")
	if !found {
		// The v0.1 route, /hls/stream.m3u8. GONE rather than aliased: a
		// listener served from it would play a station without ever counting
		// as its listener, and the station would stop underneath them.
		http.NotFound(w, r)
		return
	}

	// Validate the RAW names before any path is built from them. filepath.Clean
	// is not a security boundary and is not being relied on here.
	if !stationID.MatchString(station) || !segmentName.MatchString(name) {
		s.log.Warn("rejected HLS request", "path", rest, "remote", r.RemoteAddr)
		http.Error(w, "bad segment name", http.StatusBadRequest)
		return
	}
	// No error to handle: stationID caps the id at 18 digits, which always
	// fits an int64, so the allowlist above is the whole guard.
	id, _ := strconv.ParseInt(station, 10, 64)

	session, ok := "", false
	if s.sessions != nil {
		session, ok = s.sessions(w, r)
	}
	if !ok {
		http.Error(w, "sign in to listen", http.StatusUnauthorized)
		return
	}

	// ONLY THE PLAYLIST is a heartbeat. It is the one request a client repeats
	// forever; segments are fetched once and then cached, so counting them
	// would keep a station alive on a client that had stopped playing.
	if name == "stream.m3u8" && s.presence != nil {
		s.presence.Touch(id, session)
	}

	full := filepath.Join(s.cfg.SegmentDir, station, name)
	f, err := os.Open(full)
	if err != nil {
		// no-store so no intermediary caches the negative result and keeps
		// answering 404 after the segment reappears.
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if name == "stream.m3u8" {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		// A cached playlist is a frozen stream: the client never learns that
		// new segments exist.
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	} else {
		w.Header().Set("Content-Type", "video/mp2t")
		// SHORT, NOT IMMUTABLE. "A segment's contents never change once
		// written" is true of a file and false of a URL: the encoder restarts
		// whenever a station goes on air, and its segment numbering restarts
		// with it -- the directory is a tmpfs, so a container restart empties
		// it and the next run begins at seg0 again. seg16.ts is then a
		// different four seconds of music than the seg16.ts a browser cached
		// under `immutable` for a year.
		//
		// Heard on the live station: a listener tuned in and got an Arctic
		// Monkeys segment spliced into the middle of Counting Crows, then back
		// again -- their browser replaying year-old-cached audio at the same
		// URLs the new run had just reused.
		//
		// A live stream fetches each segment ONCE per client, so a long cache
		// bought nothing to begin with. Thirty seconds covers a client that
		// briefly stalls and re-requests, and nothing beyond that.
		w.Header().Set("Cache-Control", "public, max-age=30")
	}

	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// Sweep deletes segments older than the retention window, keeping the newest
// RetainSegments.
//
// Ordering is by segment number, not filename and not mtime: numbers are
// monotonic across an encoder restart by construction, so they are exactly
// write order, while lexical order puts seg10 before seg9 and mtime depends on
// filesystem timestamp granularity.
//
// Only segN.ts files are considered. The playlist and anything else in the
// directory are left alone: the segment directory is operator-supplied and may
// not be exclusively ours.
func (s *Server) Sweep() error {
	entries, err := os.ReadDir(s.cfg.SegmentDir)
	if os.IsNotExist(err) {
		// Nothing has gone on air yet. A station creates its own directory
		// when it starts, so an absent root is the normal state of a server
		// with no listeners -- not a fault worth a warning every sweep.
		return nil
	}
	if err != nil {
		return fmt.Errorf("server: reading segment dir: %w", err)
	}

	// PER STATION. Segments moved one level down when stations got their own
	// directories, and a sweeper still looking only at the top level would
	// find nothing to prune -- so every station would keep every segment it
	// ever wrote and the disk would fill over days, with the stream healthy
	// right up until it stopped.
	var problems []error
	for _, e := range entries {
		if !e.IsDir() || !stationID.MatchString(e.Name()) {
			continue
		}
		if err := s.sweepDir(filepath.Join(s.cfg.SegmentDir, e.Name())); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

// sweepDir prunes one station's directory. Retention is per station because
// segment numbering is.
func (s *Server) sweepDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("server: reading segment dir: %w", err)
	}

	type seg struct {
		num  int
		name string
	}
	var segs []seg
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := segNumber.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		segs = append(segs, seg{num: n, name: e.Name()})
	}

	if len(segs) <= s.cfg.RetainSegments {
		return nil
	}

	sort.Slice(segs, func(i, j int) bool { return segs[i].num < segs[j].num })
	for _, sg := range segs[:len(segs)-s.cfg.RetainSegments] {
		if err := os.Remove(filepath.Join(dir, sg.name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("server: removing expired segment %s: %w", sg.name, err)
		}
	}
	return nil
}

// checkLoopback refuses any address that is reachable from off the machine.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("server: listen address %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("server: listen address %q binds every interface; this server speaks plain HTTP, use 127.0.0.1", addr)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("server: listen address %q: %q is not an IP address", addr, host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("server: listen address %q is not loopback; this server speaks plain HTTP, use 127.0.0.1", addr)
	}
	return nil
}

// serveAsset serves a file from the embedded web assets.
//
// The set of names is fixed at build time, so a name that is not in the bundle
// simply is not there: no filesystem is reachable from here at all.
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, name string) {
	raw, err := web.Files.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch filepath.Ext(name) {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The page names the stream URL, so it must not outlive a redeploy.
		w.Header().Set("Cache-Control", "no-cache")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}

	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(raw))
}
