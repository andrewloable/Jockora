// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package decode

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/mix"
)

// fakeFFmpeg writes a script that stands in for ffmpeg and returns its path.
func fakeFFmpeg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// useFake points the decoder at a stand-in for the rest of the test.
func useFake(t *testing.T, body string) {
	t.Helper()
	old := ffmpegPath
	ffmpegPath = fakeFFmpeg(t, body)
	t.Cleanup(func() { ffmpegPath = old })
}

// drain consumes the decoder's output so a test never wedges on a full channel.
func drain(out chan []mix.Frame) func() {
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-out:
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop) }
}

// TestHangingDecoderIsKilled is the whole point. A truncated container makes
// ffmpeg block forever on INPUT without ever exiting, which stalls the bus and
// breaks the one invariant this design exists to protect. A curated test set
// will not contain such a file; a real ten-thousand-file library will.
func TestHangingDecoderIsKilled(t *testing.T) {
	useFake(t, "exec sleep 300") // exec, not sleep: a forked child would survive the kill and hold the pipe open, which ffmpeg never does
	old := StallTimeout
	StallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { StallTimeout = old })

	out := make(chan []mix.Frame, 4)
	defer drain(out)()

	start := time.Now()
	err := Decode(context.Background(), "/music/truncated.mp3", out)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrDecodeTimeout) {
		t.Fatalf("Decode = %v, want ErrDecodeTimeout", err)
	}
	if elapsed > StallTimeout+2*time.Second {
		t.Errorf("Decode took %s to give up on a %s stall", elapsed, StallTimeout)
	}
}

// TestKilledProcessIsReaped: an unreaped child is a zombie, and one per bad
// track over a month exhausts the process table. This is where leaks
// accumulate, because it is the path nobody exercises.
func TestKilledProcessIsReaped(t *testing.T) {
	useFake(t, "exec sleep 300") // exec, not sleep: a forked child would survive the kill and hold the pipe open, which ffmpeg never does
	old := StallTimeout
	StallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { StallTimeout = old })

	out := make(chan []mix.Frame, 4)
	defer drain(out)()

	before := childCount(t)
	if err := Decode(context.Background(), "/music/truncated.mp3", out); !errors.Is(err, ErrDecodeTimeout) {
		t.Fatalf("Decode = %v", err)
	}
	if after := childCount(t); after > before {
		t.Errorf("child processes went from %d to %d: the killed decoder was not reaped", before, after)
	}
}

func TestReapedOnSuccess(t *testing.T) {
	// 8192 bytes is 1024 frames of f32le stereo.
	useFake(t, "head -c 8192 /dev/zero")

	out := make(chan []mix.Frame, 64)
	defer drain(out)()

	before := childCount(t)
	if err := Decode(context.Background(), "/music/ok.mp3", out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if after := childCount(t); after > before {
		t.Errorf("child processes went from %d to %d after a clean decode", before, after)
	}
}

func TestReapedOnContextCancel(t *testing.T) {
	useFake(t, "exec sleep 300") // exec, not sleep: a forked child would survive the kill and hold the pipe open, which ffmpeg never does

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan []mix.Frame, 4)
	defer drain(out)()

	before := childCount(t)
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if err := Decode(ctx, "/music/slow.mp3", out); err == nil {
		t.Fatal("Decode returned nil after cancellation")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %s to take effect", elapsed)
	}
	if after := childCount(t); after > before {
		t.Errorf("child processes went from %d to %d after cancellation", before, after)
	}
}

// TestNoFDLeakOver100Decodes: a slow descriptor leak is invisible in a
// thirty-minute gate and fatal over a month.
func TestNoFDLeakOver100Decodes(t *testing.T) {
	useFake(t, "head -c 8192 /dev/zero")

	out := make(chan []mix.Frame, 64)
	defer drain(out)()

	// Warm up first: the runtime opens descriptors of its own on the first few
	// executions, and counting from zero would read that as a leak.
	for i := 0; i < 5; i++ {
		if err := Decode(context.Background(), "/music/ok.mp3", out); err != nil {
			t.Fatal(err)
		}
	}
	before := fdCount(t)

	for i := 0; i < 100; i++ {
		if err := Decode(context.Background(), "/music/ok.mp3", out); err != nil {
			t.Fatalf("decode %d: %v", i, err)
		}
	}

	if after := fdCount(t); after > before+8 {
		t.Errorf("open descriptors went from %d to %d over 100 decodes", before, after)
	}
}

// TestSlowConsumerIsNotAStall is the trap this watchdog could easily have
// fallen into. The decoder blocks sending to the mixer BY DESIGN -- that is the
// pacing -- so a watchdog measuring the whole loop would kill healthy decodes of
// every long track. It must measure only time spent waiting on ffmpeg.
func TestSlowConsumerIsNotAStall(t *testing.T) {
	useFake(t, "head -c 65536 /dev/zero")
	old := StallTimeout
	// A full second against a two-second consumer. The earlier 300ms/500ms pair
	// held the property but had no margin, and it failed under the load of the
	// rest of the suite -- a scheduling delay on a read is not a stalled
	// decoder either, and a flaky guard is worse than none.
	StallTimeout = time.Second
	t.Cleanup(func() { StallTimeout = old })

	out := make(chan []mix.Frame) // unbuffered: every send waits for the reader
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-out:
				// Far slower than the stall timeout, exactly as a real-time
				// consumer is.
				time.Sleep(2 * time.Second)
			case <-stop:
				return
			}
		}
	}()

	if err := Decode(context.Background(), "/music/long.mp3", out); err != nil {
		t.Fatalf("Decode = %v: a slow CONSUMER is not a stalled decoder", err)
	}
}

func childCount(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("sh", "-c",
		"ps -o ppid= -A 2>/dev/null | tr -d ' ' | grep -c '^"+itoa(os.Getpid())+"$'").Output()
	if err != nil {
		return 0 // grep exits 1 when there are none
	}
	return atoiSafe(string(out))
}

func fdCount(t *testing.T) int {
	t.Helper()
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		if entries, err := os.ReadDir(dir); err == nil {
			return len(entries)
		}
	}
	t.Skip("no way to count open descriptors on this platform")
	return 0
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			continue
		}
		n = n*10 + int(c-'0')
	}
	return n
}
