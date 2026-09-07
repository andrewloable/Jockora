// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package encode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// silence returns n seconds of f32le silence: on this bus that is simply zeroed
// bytes, so the test needs no mixer.
func silence(seconds float64) []byte {
	return make([]byte, int(seconds*48000)*8)
}

func segments(t *testing.T, dir string) []string {
	t.Helper()
	got, err := filepath.Glob(filepath.Join(dir, "*.ts"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestEncoderProducesSegments(t *testing.T) {
	dir := t.TempDir()

	e, err := Start(context.Background(), Config{SegmentDir: dir}, true)
	if err != nil {
		t.Fatalf("Start: %v (is ffmpeg on PATH?)", err)
	}

	if _, err := e.Writer().Write(silence(12)); err != nil {
		t.Fatalf("writing to the encoder: %v", err)
	}
	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v\nffmpeg said: %s", err, e.Stderr())
	}

	if got := segments(t, dir); len(got) < 2 {
		t.Errorf("12 seconds produced %d segments (%v), want at least 2", len(got), got)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "stream.m3u8"))
	if err != nil {
		t.Fatalf("no playlist: %v", err)
	}
	pl := string(raw)
	for _, want := range []string{"#EXTM3U", "#EXT-X-TARGETDURATION", ".ts"} {
		if !strings.Contains(pl, want) {
			t.Errorf("playlist is missing %q:\n%s", want, pl)
		}
	}
	// Every segment the playlist names must actually exist, or clients error out
	// on their first fetch.
	for _, line := range strings.Split(pl, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, line)); err != nil {
			t.Errorf("playlist names %q which does not exist", line)
		}
	}
}

func TestColdStartClearsStaleSegments(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "seg99.ts")
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\nseg99.ts\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file that is not ours must survive: the segment dir is operator-supplied
	// and may not be exclusively ours.
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, err := Start(context.Background(), Config{SegmentDir: dir}, true)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop()

	if _, err := os.Stat(stale); err == nil {
		t.Error("seg99.ts survived a cold start: append_list would name a segment that no longer exists")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("a cold start deleted an unrelated file from the segment directory")
	}
}

func TestWarmStartKeepsSegments(t *testing.T) {
	dir := t.TempDir()
	kept := filepath.Join(dir, "seg99.ts")
	if err := os.WriteFile(kept, []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}

	e, err := Start(context.Background(), Config{SegmentDir: dir}, false)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop()

	if _, err := os.Stat(kept); err != nil {
		t.Error("a warm restart deleted existing segments; the supervisor needs them to keep the stream continuous")
	}
}

// TestEncoderStderrIsDrained runs ffmpeg at a log level that emits far more
// diagnostics than a pipe can hold. An undrained stderr pipe fills at about
// 64 KB and ffmpeg then blocks forever with no error anywhere: no log line, no
// exit code, just a frozen stream.
//
// Sixty seconds of audio at debug level was MEASURED at 258 KB of stderr, about
// four times the buffer. Twelve seconds produces 59 KB, which is under it, so a
// shorter run would pass whether or not anything was draining.
func TestEncoderStderrIsDrained(t *testing.T) {
	dir := t.TempDir()

	e, err := Start(context.Background(), Config{SegmentDir: dir, LogLevel: "debug"}, true)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		if _, err := e.Writer().Write(silence(60)); err != nil {
			done <- err
			return
		}
		done <- e.Stop()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("encoding with a verbose ffmpeg failed: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("encoder blocked: ffmpeg's stderr pipe filled and nothing was reading it")
	}

	// Prove the run actually exceeded the pipe buffer, so this test could have
	// deadlocked had the drain been missing.
	const pipeBuffer = 65536
	if n := len(e.Stderr()); n <= pipeBuffer {
		t.Errorf("captured only %d bytes of stderr, at or under the %d-byte pipe buffer: "+
			"this run could not have blocked, so the test proves nothing", n, pipeBuffer)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	e, err := Start(context.Background(), Config{SegmentDir: dir}, true)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	e.Writer().Write(silence(1))

	if err := e.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := e.Stop(); err != nil {
		t.Errorf("second Stop returned %v, want nil: shutdown paths call it more than once", err)
	}
}

func TestStartRejectsMissingBinary(t *testing.T) {
	dir := t.TempDir()

	_, err := Start(context.Background(), Config{SegmentDir: dir, FFmpegPath: "definitely-not-ffmpeg"}, true)
	if err == nil {
		t.Fatal("Start succeeded with a nonexistent ffmpeg")
	}
	if !strings.Contains(err.Error(), "definitely-not-ffmpeg") {
		t.Errorf("error %q should name the binary it could not run", err)
	}
}

func TestStartCreatesSegmentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "segments")

	e, err := Start(context.Background(), Config{SegmentDir: dir}, true)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop()

	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("segment directory was not created: %v", err)
	}
}

// TestEncodeLivePlaylistNeverEnds: a live playlist must not claim to be
// complete, even after the encoder stops.
//
// Every station stops routinely -- the last listener leaves, the grace expires,
// ffmpeg exits. Without omit_endlist ffmpeg appends EXT-X-ENDLIST on the way
// out, and the playlist left on disk then tells the next client this is a
// finished recording: hls.js loads it as VOD, plays the segments still listed,
// and never polls for more. Observed on the live station while it sat between
// listeners, and heard as a stream that would not start.
func TestEncodeLivePlaylistNeverEnds(t *testing.T) {
	dir := t.TempDir()

	e, err := Start(context.Background(), Config{SegmentDir: dir}, true)
	if err != nil {
		t.Fatalf("Start: %v (is ffmpeg on PATH?)", err)
	}
	if _, err := e.Writer().Write(silence(12)); err != nil {
		t.Fatalf("writing to the encoder: %v", err)
	}
	// STOPPED, which is exactly when ffmpeg would write the marker.
	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v\nffmpeg said: %s", err, e.Stderr())
	}

	raw, err := os.ReadFile(filepath.Join(dir, "stream.m3u8"))
	if err != nil {
		t.Fatalf("no playlist: %v", err)
	}
	if strings.Contains(string(raw), "EXT-X-ENDLIST") {
		t.Errorf("the playlist says the stream is over:\n%s", raw)
	}
}
