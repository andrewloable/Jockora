// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

func TestFrameSizeBytes(t *testing.T) {
	if FrameBytes != 8 {
		t.Fatalf("FrameBytes = %d, want 8 (2 channels x 4 bytes)", FrameBytes)
	}
	if SampleRate != 48000 {
		t.Errorf("SampleRate = %d, want 48000", SampleRate)
	}
	if Channels != 2 {
		t.Errorf("Channels = %d, want 2", Channels)
	}
}

func TestDurationToFrames(t *testing.T) {
	if got := DurationToFrames(1500 * time.Millisecond); got != 72000 {
		t.Errorf("DurationToFrames(1500ms) = %d, want 72000", got)
	}
	if got := DurationToFrames(time.Second); got != 48000 {
		t.Errorf("DurationToFrames(1s) = %d, want 48000", got)
	}
	if got := DurationToFrames(0); got != 0 {
		t.Errorf("DurationToFrames(0) = %d, want 0", got)
	}
}

func TestFramesToDurationRoundTrip(t *testing.T) {
	for _, d := range []time.Duration{0, time.Second, 1500 * time.Millisecond, 4 * time.Second} {
		if got := FramesToDuration(DurationToFrames(d)); got != d {
			t.Errorf("round trip of %v = %v", d, got)
		}
	}
}

func TestWriteF32LERoundTrip(t *testing.T) {
	in := []Frame{{L: 1.0, R: -1.0}, {L: 0.5, R: 0.25}}

	var buf bytes.Buffer
	if err := WriteF32LE(&buf, in); err != nil {
		t.Fatalf("WriteF32LE: %v", err)
	}
	if buf.Len() != len(in)*FrameBytes {
		t.Fatalf("wrote %d bytes, want %d", buf.Len(), len(in)*FrameBytes)
	}

	out := make([]Frame, len(in))
	n, err := ReadF32LE(&buf, out)
	if err != nil {
		t.Fatalf("ReadF32LE: %v", err)
	}
	if n != len(in) {
		t.Fatalf("read %d frames, want %d", n, len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Errorf("frame %d = %+v, want %+v", i, out[i], in[i])
		}
	}
}

// TestWriteF32LEByteOrder pins little-endian on the wire. A big-endian pipe
// would decode as noise, and ffmpeg would not complain.
func TestWriteF32LEByteOrder(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteF32LE(&buf, []Frame{{L: 1.0, R: -1.0}}); err != nil {
		t.Fatalf("WriteF32LE: %v", err)
	}
	// float32(1.0) is 0x3f800000, float32(-1.0) is 0xbf800000.
	want := []byte{0x00, 0x00, 0x80, 0x3f, 0x00, 0x00, 0x80, 0xbf}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("bytes = % x, want % x", buf.Bytes(), want)
	}
}

// TestWriteF32LESpansChunks proves the internal scratch buffer is not a size
// limit: a buffer larger than one chunk must round-trip bit-exactly.
func TestWriteF32LESpansChunks(t *testing.T) {
	in := make([]Frame, 5000)
	for i := range in {
		in[i] = Frame{L: float32(i) / 5000, R: -float32(i) / 5000}
	}

	var buf bytes.Buffer
	if err := WriteF32LE(&buf, in); err != nil {
		t.Fatalf("WriteF32LE: %v", err)
	}
	out := make([]Frame, len(in))
	n, err := ReadF32LE(&buf, out)
	if err != nil {
		t.Fatalf("ReadF32LE: %v", err)
	}
	if n != len(in) {
		t.Fatalf("read %d frames, want %d", n, len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("frame %d = %+v, want %+v", i, out[i], in[i])
		}
	}
}

// TestReadF32LEShortStream is the branch the decoder wrapper depends on: a
// stream ending mid-frame must be distinguishable from a clean end of stream.
func TestReadF32LEShortStream(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteF32LE(&buf, []Frame{{L: 1, R: 1}, {L: 2, R: 2}}); err != nil {
		t.Fatalf("WriteF32LE: %v", err)
	}

	// Clean end of stream on a frame boundary: partial fill plus io.EOF.
	out := make([]Frame, 4)
	n, err := ReadF32LE(&buf, out)
	if n != 2 {
		t.Errorf("read %d frames, want 2", n)
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}

	// Truncated mid-frame: io.ErrUnexpectedEOF, and the whole frames still land.
	trunc := bytes.NewReader([]byte{0, 0, 0x80, 0x3f, 0, 0, 0x80, 0x3f, 0x11, 0x22})
	n, err = ReadF32LE(trunc, out)
	if n != 1 {
		t.Errorf("truncated: read %d frames, want 1", n)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated: err = %v, want io.ErrUnexpectedEOF", err)
	}
}
