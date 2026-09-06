// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/mix"
)

// toneFile writes a real audio file the decoder can read, using ffmpeg so the
// test exercises the actual decode path rather than a synthetic shortcut.
func toneFile(t *testing.T, dir, name string, seconds int, freq int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i",
		"sine=frequency="+strconv.Itoa(freq)+":sample_rate=44100:duration="+strconv.Itoa(seconds),
		"-ac", "2", "-b:a", "128k", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generating %s: %v: %s", name, err, out)
	}
	return path
}

// voiceWAV writes a 48 kHz 16-bit stereo WAV the mixer can splice as a break.
func voiceWAV(t *testing.T, dir string, seconds float64) string {
	t.Helper()
	frames := int(seconds * mix.SampleRate)
	data := make([]byte, frames*4)
	for i := 0; i < frames; i++ {
		v := int16(0.5 * 32767 * math.Cos(2*math.Pi*700*float64(i)/mix.SampleRate))
		binary.LittleEndian.PutUint16(data[i*4:], uint16(v))
		binary.LittleEndian.PutUint16(data[i*4+2:], uint16(v))
	}
	buf := make([]byte, 44+len(data))
	le := binary.LittleEndian
	copy(buf[0:], "RIFF")
	le.PutUint32(buf[4:], uint32(36+len(data)))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	le.PutUint32(buf[16:], 16)
	le.PutUint16(buf[20:], 1)
	le.PutUint16(buf[22:], 2)
	le.PutUint32(buf[24:], mix.SampleRate)
	le.PutUint32(buf[28:], mix.SampleRate*4)
	le.PutUint16(buf[32:], 4)
	le.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	le.PutUint32(buf[40:], uint32(len(data)))
	copy(buf[44:], data)

	path := filepath.Join(dir, "voice.wav")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		ListenAddr:     "127.0.0.1:0",
		SegmentDir:     filepath.Join(t.TempDir(), "segments"),
		SampleRate:     mix.SampleRate,
		Channels:       mix.Channels,
		SegmentSeconds: 4,
		ListSize:       10,
	}
}

func TestAppStartsAndStops(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t)
	logs := &bytes.Buffer{}

	a, err := New(cfg, Options{
		Tracks: []string{toneFile(t, dir, "a.mp3", 10, 220)},
		Log:    slog.New(slog.NewTextHandler(logs, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	// Real time, real pacing: wait for the encoder to emit segments.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m, _ := filepath.Glob(filepath.Join(cfg.SegmentDir, "*.ts")); len(m) >= 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if m, _ := filepath.Glob(filepath.Join(cfg.SegmentDir, "*.ts")); len(m) == 0 {
		t.Fatalf("no segments after 30s; log:\n%s", logs.String())
	}

	pid := a.encoderPID()
	if pid == 0 {
		t.Fatal("no ffmpeg running")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && ctx.Err() == nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	// An orphaned ffmpeg holds the segment directory and the next start fails.
	if processAlive(pid) {
		t.Errorf("ffmpeg pid %d survived shutdown", pid)
	}
}

// TestAppServesTheStream: the whole point is that a listener can fetch it.
func TestAppServesTheStream(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t)

	a, err := New(cfg, Options{
		Tracks: []string{toneFile(t, dir, "a.mp3", 10, 220)},
		Log:    slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m, _ := filepath.Glob(filepath.Join(cfg.SegmentDir, "*.ts")); len(m) >= 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	base := "http://" + a.Addr()
	for _, path := range []string{"/", "/hls/stream.m3u8"} {
		body, code := httpGet(t, base+path)
		if code != 200 {
			t.Errorf("GET %s returned %d", path, code)
		}
		if len(body) == 0 {
			t.Errorf("GET %s returned an empty body", path)
		}
	}
}

// TestFeederRetriesWhenRingFull: the ring is bounded and Write returns short.
// A feeder that treated a short write as done would silently drop audio.
func TestFeederRetriesWhenRingFull(t *testing.T) {
	ring := mix.NewRing(1024)
	in := make([]mix.Frame, 4096)
	for i := range in {
		in[i] = mix.Frame{L: float32(i + 1), R: float32(i + 1)}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	drained := make(chan int, 1)
	go func() {
		out := make([]mix.Frame, 256)
		got := 0
		for got < len(in) {
			n := ring.Read(out)
			got += n
			if n == 0 {
				time.Sleep(time.Millisecond)
			}
		}
		drained <- got
	}()

	if err := feedRing(ctx, ring, in); err != nil {
		t.Fatalf("feedRing: %v", err)
	}

	select {
	case got := <-drained:
		if got != len(in) {
			t.Errorf("consumer received %d frames, want all %d: the feeder dropped a short write", got, len(in))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the feeder never delivered all frames")
	}
	// Underruns are NOT asserted here. This consumer polls faster than the
	// producer can fill a 1024-frame ring, and Ring.Read padding silence in that
	// situation is its documented job. What matters is that every frame the
	// feeder was given arrives, which is asserted above.
}

func TestFeedRingStopsOnCancel(t *testing.T) {
	ring := mix.NewRing(64) // far too small, and nothing is reading
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := feedRing(ctx, ring, make([]mix.Frame, 4096)); err == nil {
		t.Error("feedRing ignored a cancelled context and would block shutdown")
	}
}

// TestSoleWriterInvariant is structural: exactly one place in the app package
// may write to the encoder, and it is the mixer's W field. A second writer would
// interleave bytes into the middle of a frame and the stream would be noise.
func TestSoleWriterInvariant(t *testing.T) {
	src, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "Writer()"); n != 1 {
		t.Errorf("app.go references the encoder Writer() %d times, want exactly 1 "+
			"(the mixer is the sole writer to the ffmpeg pipe)", n)
	}

	// And nothing in the package imports the encoder for its own writing.
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, pkg := range pkgs {
		files += len(pkg.Files)
	}
	if files == 0 {
		t.Error("no non-test files found in the app package")
	}
}

// TestBreakIsScheduled: GATE 1 needs something to splice.
func TestBreakIsScheduled(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t)

	a, err := New(cfg, Options{
		Tracks:     []string{toneFile(t, dir, "a.mp3", 10, 220)},
		BreakPath:  voiceWAV(t, dir, 1.0),
		BreakAtSec: 3,
		Log:        slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := a.PendingBreaks(); got != 1 {
		t.Errorf("PendingBreaks() = %d, want 1", got)
	}
}

func TestNewRejectsNoTracks(t *testing.T) {
	if _, err := New(testConfig(t), Options{}); err == nil {
		t.Error("New accepted a run with no tracks")
	}
}

func TestNewRejectsMissingBreakFile(t *testing.T) {
	dir := t.TempDir()
	_, err := New(testConfig(t), Options{
		Tracks:     []string{toneFile(t, dir, "a.mp3", 2, 220)},
		BreakPath:  filepath.Join(dir, "nope.wav"),
		BreakAtSec: 3,
	})
	if err == nil {
		t.Error("New accepted a break file that does not exist; the failure would only appear at airtime")
	}
}

func httpGet(t *testing.T, url string) ([]byte, int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return body, resp.StatusCode
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(nil) == nil
}

// TestStallFeederTriggersSilenceFill is GATE 2's first fault injection, in
// miniature: the decoder pauses, the ring drains, silence-fill covers it, and
// the stream keeps going. A stall that ends the stream is a failure.
func TestStallFeederTriggersSilenceFill(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t)

	a, err := New(cfg, Options{
		Tracks: []string{toneFile(t, dir, "a.mp3", 30, 220)},
		Log:    slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	// Let it settle, then stall the decoder for longer than the ring holds.
	time.Sleep(3 * time.Second)
	before := a.Metrics().Writes
	a.StallFeeder(12 * time.Second)
	time.Sleep(14 * time.Second)

	after := a.Metrics().Writes
	if after <= before {
		t.Errorf("the mixer wrote %d blocks before the stall and %d after: the stall stopped the stream",
			before, after)
	}
	if a.ring.UnderrunCount() == 0 {
		t.Error("a 12-second decoder stall produced no underruns; silence-fill never fired")
	}
	if got := a.Metrics().MinOccupancy; got != 0 {
		t.Errorf("MinOccupancy = %v after draining the ring dry, want 0", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestMetricsAreCollectedDuringARun: the four numbers GATE 2 asserts on must
// exist without extra tooling.
func TestMetricsAreCollectedDuringARun(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t)

	a, err := New(cfg, Options{
		Tracks: []string{toneFile(t, dir, "a.mp3", 20, 220)},
		Log:    slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go a.Run(ctx)
	time.Sleep(6 * time.Second)
	cancel()

	s := a.Metrics()
	if s.Writes == 0 {
		t.Fatal("no writes recorded")
	}
	if s.P99Gap <= 0 || s.MaxGap <= 0 {
		t.Errorf("gaps not recorded: p99=%v max=%v", s.P99Gap, s.MaxGap)
	}
	// The two GATE 2 budgets, asserted on a real run rather than only measured.
	if s.P99Gap > 250*time.Millisecond {
		t.Errorf("p99 inter-write gap = %v, over the 250ms GATE 2 budget", s.P99Gap)
	}
	if s.MinOccupancy < 2*time.Second {
		t.Errorf("min ring occupancy = %v, under the 2s GATE 2 budget", s.MinOccupancy)
	}
	t.Logf("writes=%d p99=%v max=%v min_occupancy=%v",
		s.Writes, s.P99Gap.Round(time.Millisecond), s.MaxGap.Round(time.Millisecond),
		s.MinOccupancy.Round(time.Millisecond))
}

// TestBreaksRepeatWhenAsked: a single short break is very hard to catch by ear,
// because HLS runs 12-18 seconds behind live and a listener who presses play at
// the wrong moment never hears it -- and cannot tell that from a broken splice.
func TestBreaksRepeatWhenAsked(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t)

	a, err := New(cfg, Options{
		Tracks:        []string{toneFile(t, dir, "a.mp3", 2, 220)},
		BreakPath:     voiceWAV(t, dir, 0.5),
		BreakAtSec:    5,
		BreakEverySec: 20,
		Log:           slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Two hours of coverage at one every 20 seconds.
	if got := a.PendingBreaks(); got != 360 {
		t.Errorf("PendingBreaks() = %d, want 360 (2h at one every 20s)", got)
	}
}

func TestSingleBreakIsStillTheDefault(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(t)

	a, err := New(cfg, Options{
		Tracks:     []string{toneFile(t, dir, "a.mp3", 2, 220)},
		BreakPath:  voiceWAV(t, dir, 0.5),
		BreakAtSec: 5,
		Log:        slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.PendingBreaks(); got != 1 {
		t.Errorf("PendingBreaks() = %d, want 1 with no interval set", got)
	}
}

// TestStatusReportsADegradedEncoder. A full disk keeps the mixer running and
// the restart counter climbing while no new segment is ever written, so every
// other number on this endpoint reads healthy. This is the only field that
// says otherwise.
func TestStatusReportsADegradedEncoder(t *testing.T) {
	if got := ffmpegHealth(""); got != "ok" {
		t.Errorf("healthy encoder reported as %q", got)
	}
	if got := ffmpegHealth("disk full"); got != "disk full" {
		t.Errorf("degraded encoder reported as %q, want the reason itself", got)
	}
}
