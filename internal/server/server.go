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
	// still ships no authentication of any kind: turning it on publishes an
	// unauthenticated server to whatever network can reach the bind address.
	AllowNonLoopback bool
}

// segmentName allows exactly the two filename shapes this program produces.
// Everything else is a 400 before any filesystem path is built.
var segmentName = regexp.MustCompile(`^(seg\d+\.ts|stream\.m3u8)$`)

// segNumber extracts N from segN.ts.
var segNumber = regexp.MustCompile(`^seg(\d+)\.ts$`)

// Server serves HLS from a directory and sweeps it.
type Server struct {
	cfg    Config
	log    *slog.Logger
	ln     net.Listener
	status StatusSource
	dial   DialSource
	tuner  Tuner
}

// SetDialSource wires the /stations.json data source. Without one the endpoint
// reports 503 rather than 404: the route exists, the dial does not yet.
func (s *Server) SetDialSource(d DialSource) { s.dial = d }

// SetStatusSource wires the /now.json data source. Without one the endpoint
// reports unavailable rather than lying about a healthy stream.
func (s *Server) SetStatusSource(src StatusSource) { s.status = src }

// New validates the configuration and opens the listener.
//
// A non-loopback listen address is refused. This program has no authentication
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

// Handler routes requests.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath, not Path: Path is already percent-decoded, so ..%2f
		// would arrive here indistinguishable from a legitimate name. Matching
		// the raw form means an encoding trick cannot smuggle a separator past
		// the allowlist.
		p := r.URL.EscapedPath()

		// The two POST routes are handled before the read-only guard below.
		// Everything else on this server is a GET by design.
		//
		// Only these two paths are diverted: a POST to any OTHER route still
		// falls through to the 405 below, because "you cannot POST to a
		// segment" is the true answer and 404 would claim it does not exist.
		if r.Method == http.MethodPost && (p == "/tune" || p == "/feedback") {
			if p == "/tune" {
				s.serveTune(w, r)
			} else {
				s.serveFeedback(w, r)
			}
			return
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		switch {
		case p == "/now.json":
			s.serveStatus(w, r)
		case p == "/stations.json":
			s.serveDial(w, r)
		case p == "/" || p == "/index.html":
			s.serveAsset(w, r, "index.html")
		case strings.HasPrefix(p, "/vendor/"):
			s.serveAsset(w, r, strings.TrimPrefix(p, "/"))
		case strings.HasPrefix(p, "/hls/"):
			// An empty name reaches the allowlist and is rejected there, so
			// /hls/ is a 400 like any other name that is not servable.
			s.serveHLS(w, r, strings.TrimPrefix(p, "/hls/"))
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *Server) serveHLS(w http.ResponseWriter, r *http.Request, name string) {
	// Validate the raw name BEFORE any path is built from it. filepath.Clean is
	// not a security boundary and is not being relied on here.
	if !segmentName.MatchString(name) {
		s.log.Warn("rejected HLS request", "name", name, "remote", r.RemoteAddr)
		http.Error(w, "bad segment name", http.StatusBadRequest)
		return
	}

	full := filepath.Join(s.cfg.SegmentDir, name)
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
		// A segment's contents never change once written.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
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
		if err := os.Remove(filepath.Join(s.cfg.SegmentDir, sg.name)); err != nil && !os.IsNotExist(err) {
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
		return fmt.Errorf("server: listen address %q binds every interface; this server has no authentication, use 127.0.0.1", addr)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("server: listen address %q: %q is not an IP address", addr, host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("server: listen address %q is not loopback; this server has no authentication, use 127.0.0.1", addr)
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
