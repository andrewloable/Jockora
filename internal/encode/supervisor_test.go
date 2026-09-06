// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package encode

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// waitFor polls until cond is true or the deadline passes.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", d, what)
}

var segRE = regexp.MustCompile(`^seg(\d+)\.ts$`)

type playlist struct {
	mediaSeq int
	segments []string
	segNums  []int
	disconts int
}

func readPlaylist(t *testing.T, dir string) playlist {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, PlaylistName))
	if err != nil {
		t.Fatalf("reading playlist: %v", err)
	}
	p := playlist{mediaSeq: -1}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			n, err := strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
			if err != nil {
				t.Fatalf("bad media sequence %q", line)
			}
			p.mediaSeq = n
		case line == "#EXT-X-DISCONTINUITY":
			p.disconts++
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			p.segments = append(p.segments, line)
			if m := segRE.FindStringSubmatch(line); m != nil {
				n, _ := strconv.Atoi(m[1])
				p.segNums = append(p.segNums, n)
			}
		}
	}
	return p
}

// newSupervisor wires a supervisor over a temp dir and returns it with its log.
func newSupervisor(t *testing.T) (*Supervisor, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	logs := &bytes.Buffer{}

	s, err := StartSupervisor(context.Background(), Config{SegmentDir: dir},
		slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatalf("StartSupervisor: %v (is ffmpeg on PATH?)", err)
	}
	t.Cleanup(func() { s.Stop() })
	return s, dir, logs
}

// feed writes seconds of silence through the supervisor in realistic blocks.
func feed(t *testing.T, s *Supervisor, seconds float64) {
	t.Helper()
	const block = 4096 * 8
	total := int(seconds*48000) * 8
	buf := make([]byte, block)
	for written := 0; written < total; written += block {
		if _, err := s.Writer().Write(buf); err != nil {
			t.Fatalf("the mixer got an error from the supervisor: %v", err)
		}
	}
}

func TestSupervisorRestartsOnDeath(t *testing.T) {
	s, dir, _ := newSupervisor(t)

	feed(t, s, 6)
	waitFor(t, 5*time.Second, "the first segment", func() bool {
		m, _ := filepath.Glob(filepath.Join(dir, "*.ts"))
		return len(m) > 0
	})

	before := s.pid()
	if before == 0 {
		t.Fatal("no ffmpeg process running")
	}

	kill(t, s)

	// Keep feeding, exactly as the mixer would. The supervisor must notice and
	// swap in a new process without the mixer ever seeing an error.
	start := time.Now()
	waitFor(t, 2*time.Second, "a replacement ffmpeg", func() bool {
		feed(t, s, 0.5)
		return s.pid() != 0 && s.pid() != before
	})
	t.Logf("restarted after %v, restarts=%d", time.Since(start).Round(time.Millisecond), s.Restarts())

	if s.Restarts() != 1 {
		t.Errorf("Restarts() = %d, want 1", s.Restarts())
	}

	// And the new process is really producing output.
	feed(t, s, 8)
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop after restart: %v", err)
	}
	if got := readPlaylist(t, dir); len(got.segments) < 2 {
		t.Errorf("playlist has %d segments after the restart, want writing to have resumed", len(got.segments))
	}
}

func TestSupervisorMediaSequenceIsMonotonicAcrossRestart(t *testing.T) {
	s, dir, _ := newSupervisor(t)

	feed(t, s, 12)
	waitFor(t, 5*time.Second, "segments before the kill", func() bool {
		m, _ := filepath.Glob(filepath.Join(dir, "*.ts"))
		return len(m) >= 2
	})
	before := readPlaylist(t, dir)
	if len(before.segNums) == 0 {
		t.Fatal("no segments before the restart")
	}

	kill(t, s)
	waitFor(t, 3*time.Second, "the restart", func() bool {
		feed(t, s, 0.5)
		return s.Restarts() == 1
	})
	feed(t, s, 12)
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	after := readPlaylist(t, dir)

	if after.mediaSeq < before.mediaSeq {
		t.Errorf("EXT-X-MEDIA-SEQUENCE went backwards: %d then %d", before.mediaSeq, after.mediaSeq)
	}

	maxBefore := 0
	seen := map[string]bool{}
	for i, n := range before.segNums {
		if n > maxBefore {
			maxBefore = n
		}
		seen[before.segments[i]] = true
	}

	// Every segment written after the restart must carry a higher number, and no
	// filename may be reused: a client holding a reference to seg3.ts must never
	// be handed different audio under that name.
	newOnes := 0
	for i, name := range after.segments {
		if seen[name] {
			continue
		}
		newOnes++
		if after.segNums[i] <= maxBefore {
			t.Errorf("post-restart segment %s reuses or regresses below the pre-restart maximum seg%d",
				name, maxBefore)
		}
	}
	if newOnes == 0 {
		t.Error("no new segments after the restart")
	}
	t.Logf("before: mediaSeq=%d segs=%v | after: mediaSeq=%d segs=%v",
		before.mediaSeq, before.segments, after.mediaSeq, after.segments)
}

func TestSupervisorExactlyOneDiscontinuityAtRestart(t *testing.T) {
	s, dir, _ := newSupervisor(t)

	feed(t, s, 12)
	waitFor(t, 5*time.Second, "segments before the kill", func() bool {
		m, _ := filepath.Glob(filepath.Join(dir, "*.ts"))
		return len(m) >= 2
	})

	kill(t, s)
	waitFor(t, 3*time.Second, "the restart", func() bool {
		feed(t, s, 0.5)
		return s.Restarts() == 1
	})
	feed(t, s, 12)
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := readPlaylist(t, dir).disconts; got != 1 {
		raw, _ := os.ReadFile(filepath.Join(dir, PlaylistName))
		t.Errorf("playlist has %d EXT-X-DISCONTINUITY tags, want exactly 1 at the restart boundary:\n%s", got, raw)
	}
}

// TestSupervisorColdStartHasNoDiscontinuity: a stream that never restarted must
// carry no discontinuity at all.
func TestSupervisorColdStartHasNoDiscontinuity(t *testing.T) {
	s, dir, _ := newSupervisor(t)

	feed(t, s, 12)
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := readPlaylist(t, dir).disconts; got != 0 {
		t.Errorf("a cold start produced %d discontinuity tags, want 0", got)
	}
}

// TestSupervisorWriterSurvivesRestart: the mixer holds ONE writer for the life
// of the stream. It must never see an error, and must never need to re-fetch it.
func TestSupervisorWriterSurvivesRestart(t *testing.T) {
	s, _, _ := newSupervisor(t)

	w := s.Writer() // taken once, as the mixer does
	feed(t, s, 4)
	kill(t, s)

	buf := make([]byte, 4096*8)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && s.Restarts() == 0 {
		if _, err := w.Write(buf); err != nil {
			t.Fatalf("the mixer's writer returned an error across a restart: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s.Restarts() != 1 {
		t.Fatalf("Restarts() = %d, want 1", s.Restarts())
	}
	if _, err := w.Write(buf); err != nil {
		t.Errorf("the original writer stopped working after the restart: %v", err)
	}
}

func TestSupervisorWriteAfterStopFails(t *testing.T) {
	s, _, _ := newSupervisor(t)
	feed(t, s, 1)
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, err := s.Writer().Write(make([]byte, 8)); err == nil {
		t.Error("writing after Stop succeeded; shutdown must be final")
	}
	if s.Restarts() != 0 {
		t.Errorf("Stop triggered a restart: Restarts() = %d", s.Restarts())
	}
}

func kill(t *testing.T, s *Supervisor) {
	t.Helper()
	p := s.process()
	if p == nil {
		t.Fatal("no process to kill")
	}
	if err := p.Kill(); err != nil {
		t.Fatalf("killing ffmpeg: %v", err)
	}
	// Give the OS a moment to tear the pipe down, so the next write really fails.
	time.Sleep(100 * time.Millisecond)
}

// TestSupervisorDiskFullDegradesAndRecovers is the invariant this whole file
// protects, at its hardest moment. A full disk must not crash the server, must
// be visible to an operator, and must resolve itself the moment space returns.
func TestSupervisorDiskFullDegradesAndRecovers(t *testing.T) {
	s := &Supervisor{
		cfg: Config{SegmentDir: t.TempDir()},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	// A writable directory: a failed write is not the disk's fault.
	s.classifyLocked(errors.New("write |1: broken pipe"))
	if got := s.Degraded(); got != "" {
		t.Errorf("Degraded = %q on a writable directory, want none", got)
	}

	// A directory that cannot be written to at all.
	s.cfg.SegmentDir = filepath.Join(t.TempDir(), "does-not-exist")
	s.classifyLocked(errors.New("write |1: broken pipe"))
	if got := s.Degraded(); got == "" {
		t.Error("Degraded is empty with an unwritable segment directory")
	}

	// ffmpeg's own words, arriving across the process boundary where the errno
	// did not survive.
	s.cfg.SegmentDir = ""
	s.classifyLocked(errors.New("av_interleaved_write_frame(): No space left on device"))
	if got := s.Degraded(); got != "disk full" {
		t.Errorf("Degraded = %q for ffmpeg's ENOSPC message, want %q", got, "disk full")
	}

	// And it clears the moment a write succeeds again, without a restart.
	s.mu.Lock()
	s.degraded = "disk full"
	s.mu.Unlock()
	s.cfg.SegmentDir = t.TempDir()
	s.classifyLocked(nil)
	if got := s.Degraded(); got != "" {
		t.Errorf("Degraded = %q after the disk cleared, want none", got)
	}
}

// TestSupervisorProbeChecksTheSegmentDirectory covers what can actually be
// checked here: a healthy directory passes, a missing one fails, and an unset
// one is not an error.
//
// It does NOT cover the case the probe was written for. probeWritable writes
// 4096 bytes rather than only creating the file, because a full filesystem can
// still hand out an inode and a probe that only opened one would report health
// on a disk with no space at all. Simulating ENOSPC portably needs a loopback
// filesystem and root, so THAT LINE IS DEFENSIVE AND UNTESTED -- said plainly
// here rather than hidden behind a test name that implies otherwise.
func TestSupervisorProbeChecksTheSegmentDirectory(t *testing.T) {
	if err := probeWritable(t.TempDir()); err != nil {
		t.Errorf("probeWritable on a healthy directory: %v", err)
	}
	if err := probeWritable(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("probeWritable said a missing directory was fine")
	}
	if err := probeWritable(""); err != nil {
		t.Errorf("probeWritable with no directory configured: %v", err)
	}
}
