// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package encode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"

	"github.com/andrewloable/jockora/internal/errs"
	"github.com/andrewloable/jockora/internal/obs"
)

// ErrSupervisorStopped is returned once the stream has been shut down.
var ErrSupervisorStopped = errors.New("encode: supervisor stopped")

// Supervisor keeps an encoder running, replacing it if ffmpeg dies, without
// breaking playback for connected clients.
//
// It owns the process handle so that subprocess lifecycle lives in one place,
// and it hands the mixer a single io.Writer that stays valid across restarts.
// The mixer must never see a write error from a dead process: an unhandled
// EPIPE there would kill the mixer goroutine and take the whole stream with it.
type Supervisor struct {
	ctx context.Context
	cfg Config
	log *slog.Logger

	mu       sync.Mutex
	enc      *Encoder
	restarts int
	stopped  bool
	degraded string
}

// StartSupervisor cold-starts a stream and supervises it.
func StartSupervisor(ctx context.Context, cfg Config, log *slog.Logger) (*Supervisor, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &Supervisor{ctx: ctx, cfg: cfg, log: log}

	enc, err := Start(ctx, cfg, true)
	if err != nil {
		return nil, err
	}
	s.enc = enc
	return s, nil
}

// Writer returns the stream input. Take it once: it stays valid for the life of
// the supervisor, including across restarts.
func (s *Supervisor) Writer() io.Writer { return s }

// Write forwards mixed audio to the live encoder, restarting it if it has died.
//
// It reports success even when a block is lost to a restart. Music never
// stopping outranks any single block of audio, and the mixer has nothing useful
// it could do with the error anyway.
func (s *Supervisor) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return 0, ErrSupervisorStopped
	}

	if s.enc != nil {
		if _, err := s.enc.Writer().Write(p); err == nil {
			s.degraded = ""
			return len(p), nil
		} else {
			s.log.Warn("encoder write failed; restarting ffmpeg", "err", err)
			s.classifyLocked(err)
		}
	}

	if err := s.restartLocked(); err != nil {
		s.log.Error("could not restart the encoder", "err", err)
		s.classifyLocked(err)
		return len(p), nil // the mixer keeps going regardless
	}
	if _, err := s.enc.Writer().Write(p); err != nil {
		s.log.Warn("first write to the new encoder failed", "err", err)
	}
	return len(p), nil
}

// classifyLocked works out WHY the encoder failed, so /now.json can say.
//
// A dead encoder always looks the same from here -- a broken pipe -- because
// ffmpeg is the process that touches the disk, and its errno does not cross the
// boundary. So the segment directory is probed directly. That is not a
// heuristic: it is the same question ffmpeg just failed to answer, asked by the
// process that can still report it.
//
// A full disk MUST NOT crash the server. A disk that fills and is then cleared
// -- by logrotate, by a cleanup job, by somebody noticing -- should find the
// stream still running and resume. Crashing turns a five-minute problem into a
// listener who left.
func (s *Supervisor) classifyLocked(err error) {
	if probeErr := probeWritable(s.cfg.SegmentDir); probeErr != nil {
		if errs.IsDiskFull(probeErr) {
			if s.degraded == "" {
				// Error, not warn: this one needs a person, and it is the only
				// failure here that does not fix itself.
				s.log.Error("SEGMENT DIRECTORY IS FULL; the stream keeps mixing and will resume when space is freed",
					"dir", s.cfg.SegmentDir, "err", probeErr)
			}
			s.degraded = "disk full"
			return
		}
		s.degraded = "segment directory not writable"
		return
	}
	if errs.IsDiskFull(err) {
		s.degraded = "disk full"
		return
	}
	s.degraded = ""
}

// probeWritable asks the segment directory whether it can still take bytes.
func probeWritable(dir string) error {
	if dir == "" {
		return nil
	}
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return fmt.Errorf("%w: %v", errs.ErrSegmentWrite, err)
	}
	name := f.Name()
	defer os.Remove(name) //nolint:errcheck // best effort
	// A probe that writes nothing proves nothing: an inode is available on a
	// full filesystem, and CreateTemp alone would report health.
	if _, err := f.Write(make([]byte, 4096)); err != nil {
		f.Close() //nolint:errcheck // already failing
		return fmt.Errorf("%w: %v", errs.ErrSegmentWrite, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("%w: %v", errs.ErrSegmentWrite, err)
	}
	return nil
}

// Degraded reports why the encoder is unhealthy, or "" when it is fine.
func (s *Supervisor) Degraded() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.degraded
}

// Restarts reports how many times ffmpeg has been replaced.
func (s *Supervisor) Restarts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}

// Stop shuts the stream down for good.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}
	s.stopped = true
	if s.enc == nil {
		return nil
	}
	return s.enc.Stop()
}

// restartLocked reaps the dead encoder and starts a replacement that continues
// the playlist rather than replacing it.
func (s *Supervisor) restartLocked() error {
	if s.enc != nil {
		// Reap it. The exit status is why we are here, so it is not news.
		_ = s.enc.Stop()
		s.enc = nil
	}

	cfg := s.cfg
	// A fresh ffmpeg has no idea what the previous playlist counted to, and a
	// media-sequence regression makes Safari's native HLS player behave
	// unpredictably. Continue from what is already on disk.
	cfg.StartNumber = highestSegment(cfg.SegmentDir) + 1
	cfg.DiscontStart = true

	// Warm start: the directory is NOT cleared. Clients hold references to
	// segments that still have to resolve.
	enc, err := Start(s.ctx, cfg, false)
	if err != nil {
		return fmt.Errorf("encode: restarting: %w", err)
	}

	s.enc = enc
	s.restarts++
	obs.New(s.log).EncoderRestart(s.restarts, "start_number", cfg.StartNumber)
	return nil
}

// process returns the live ffmpeg process, or nil.
func (s *Supervisor) process() *os.Process {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enc == nil {
		return nil
	}
	return s.enc.process()
}

func (s *Supervisor) pid() int {
	if p := s.process(); p != nil {
		return p.Pid
	}
	return 0
}

var segFileRE = regexp.MustCompile(`^seg(\d+)\.ts$`)

// highestSegment returns the largest N among segN.ts in dir, or -1 if there are
// none, so that the caller's +1 starts a fresh stream at zero.
func highestSegment(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return -1
	}
	highest := -1
	for _, e := range entries {
		m := segFileRE.FindStringSubmatch(filepath.Base(e.Name()))
		if m == nil {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > highest {
			highest = n
		}
	}
	return highest
}

// PID reports the running ffmpeg's process id, or 0. It exists so that a
// shutdown path can be proven to have reaped the process.
func (s *Supervisor) PID() int { return s.pid() }
