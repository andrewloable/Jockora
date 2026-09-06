// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package decode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/mix"
)

// decodeAll runs Decode to completion and reports how many frames arrived. The
// caller owns the channel, so this helper is also the proof that Decode never
// closes it.
func decodeAll(t *testing.T, path string) (int, error) {
	t.Helper()

	out := make(chan []mix.Frame, 4)
	total := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		for b := range out {
			total += len(b)
		}
	}()

	err := Decode(context.Background(), path, out)
	close(out) // panics if Decode already closed it
	<-done
	return total, err
}

// testdata/short.mp3 is a 2.000 s 440 Hz stereo tone, so a correct decode is
// exactly 96000 frames on the 48 kHz bus.
const shortMP3Frames = 2 * mix.SampleRate

func TestDecodeProducesFrames(t *testing.T) {
	n, err := decodeAll(t, "testdata/short.mp3")
	if err != nil {
		t.Fatalf("Decode: %v (is ffmpeg on PATH?)", err)
	}
	if n == 0 {
		t.Fatal("no frames decoded")
	}
	if diff := float64(n-shortMP3Frames) / shortMP3Frames; diff > 0.05 || diff < -0.05 {
		t.Errorf("decoded %d frames, want %d within 5%% (off by %.1f%%)",
			n, shortMP3Frames, diff*100)
	}
}

func TestDecodeMissingFileReturnsError(t *testing.T) {
	n, err := decodeAll(t, "testdata/does-not-exist.mp3")
	if !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("err = %v, want ErrDecodeFailed", err)
	}
	if n != 0 {
		t.Errorf("emitted %d frames for a missing file, want 0", n)
	}
}

func TestDecodeZeroByteFileReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.mp3")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := decodeAll(t, path)
	if !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("err = %v, want ErrDecodeFailed", err)
	}
	if n != 0 {
		t.Errorf("emitted %d frames for a zero-byte file, want 0", n)
	}
}

func TestDecodeCorruptFileReturnsError(t *testing.T) {
	n, err := decodeAll(t, "testdata/corrupt.mp3")
	if !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("err = %v, want ErrDecodeFailed", err)
	}
	if n != 0 {
		t.Errorf("emitted %d frames for a corrupt file, want 0", n)
	}
	// The operator has to be able to act on this without re-running ffmpeg by
	// hand, so ffmpeg's own diagnosis must survive into the error.
	//
	// Matched by ffmpeg's LOG FORMAT rather than by its wording. The wording is
	// version-specific -- ffmpeg 8 says "Invalid data found when processing
	// input", Ubuntu's says "Failed to read frame size" and "Invalid argument"
	// -- and pinning it broke CI while the code was working perfectly. The
	// bracketed component tag is stable across every version.
	if !ffmpegLogLine.MatchString(err.Error()) {
		t.Errorf("error does not carry ffmpeg stderr: %v", err)
	}
}

// TestDecodeExitZeroWithNoOutputIsAnError covers the case that looks like
// success and is not: testdata/silent-zero.wav is a valid, well-formed header
// with zero samples. ffmpeg exits 0 and writes nothing at all.
func TestDecodeExitZeroWithNoOutputIsAnError(t *testing.T) {
	n, err := decodeAll(t, "testdata/silent-zero.wav")
	if !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("err = %v, want ErrDecodeFailed", err)
	}
	if n != 0 {
		t.Errorf("emitted %d frames, want 0", n)
	}
}

// TestDecodeTruncatedFileDecodesShort documents measured ffmpeg behaviour, not
// a wish: a truncated MP3 is NOT an error. ffmpeg decodes what is there and
// exits 0, so the track simply plays short. Nothing at this layer can catch it;
// the library scanner has to compare decoded duration against the container's.
func TestDecodeTruncatedFileDecodesShort(t *testing.T) {
	n, err := decodeAll(t, "testdata/truncated.mp3")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if n == 0 {
		t.Fatal("no frames decoded from the truncated file")
	}
	if n >= shortMP3Frames {
		t.Errorf("truncated file decoded %d frames, expected well under %d", n, shortMP3Frames)
	}
}

// TestDecodeContextCancelUnblocksSend is the deadlock this design fears: a slow
// consumer leaves Decode blocked mid-send. Cancelling must return, not hang.
func TestDecodeContextCancelUnblocksSend(t *testing.T) {
	out := make(chan []mix.Frame) // deliberately unread
	ctx, cancel := context.WithCancel(context.Background())

	errc := make(chan error, 1)
	go func() { errc <- Decode(ctx, "testdata/short.mp3", out) }()

	time.Sleep(100 * time.Millisecond) // let it fill the pipe and block on send
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want it to wrap context.Canceled", err)
		}
		if !errors.Is(err, ErrDecodeFailed) {
			t.Errorf("err = %v, want it to wrap ErrDecodeFailed too", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Decode did not return after context cancel: it is deadlocked on send")
	}
}

// ffmpegLogLine matches ffmpeg's component log prefix, "[mp3 @ 0x55...]",
// which every version emits and no wrapper of ours produces.
var ffmpegLogLine = regexp.MustCompile(`\[[^\]]{1,40} @ 0x[0-9a-f]+\]`)
