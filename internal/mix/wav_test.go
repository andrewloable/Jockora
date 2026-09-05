// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildWAV assembles a WAV from explicit parts so a test can produce malformed
// files the writer would never emit.
func buildWAV(rate uint32, channels, bits int, extraChunk []byte, data []byte) []byte {
	le := binary.LittleEndian
	fmtChunk := make([]byte, 24)
	copy(fmtChunk, "fmt ")
	le.PutUint32(fmtChunk[4:], 16)
	le.PutUint16(fmtChunk[8:], 1)
	le.PutUint16(fmtChunk[10:], uint16(channels))
	le.PutUint32(fmtChunk[12:], rate)
	le.PutUint32(fmtChunk[16:], rate*uint32(channels*bits/8))
	le.PutUint16(fmtChunk[20:], uint16(channels*bits/8))
	le.PutUint16(fmtChunk[22:], uint16(bits))

	dataChunk := make([]byte, 8+len(data))
	copy(dataChunk, "data")
	le.PutUint32(dataChunk[4:], uint32(len(data)))
	copy(dataChunk[8:], data)

	body := append(append(fmtChunk, extraChunk...), dataChunk...)
	out := make([]byte, 12+len(body))
	copy(out, "RIFF")
	le.PutUint32(out[4:], uint32(4+len(body)))
	copy(out[8:], "WAVE")
	copy(out[12:], body)
	return out
}

// pcm16 encodes a signed sample the way a WAV file stores it.
func pcm16(v int16) uint16 { return uint16(v) }

func writeTemp(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.wav")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadWAVStereo(t *testing.T) {
	le := binary.LittleEndian
	data := make([]byte, 8) // two stereo frames
	le.PutUint16(data[0:], pcm16(16384))
	le.PutUint16(data[2:], pcm16(-16384))
	le.PutUint16(data[4:], pcm16(32767))
	le.PutUint16(data[6:], pcm16(-32768))

	got, err := ReadWAV(writeTemp(t, buildWAV(SampleRate, 2, 16, nil, data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d frames, want 2", len(got))
	}
	if got[0].L != 0.5 || got[0].R != -0.5 {
		t.Errorf("frame 0 = %+v, want {0.5 -0.5}", got[0])
	}
	if got[1].R != -1.0 {
		t.Errorf("frame 1 R = %v, want -1.0 (full-scale negative)", got[1].R)
	}
}

// TestReadWAVMonoIsDuplicated: a mono TTS clip must arrive centred, not only in
// the left channel.
func TestReadWAVMonoIsDuplicated(t *testing.T) {
	le := binary.LittleEndian
	data := make([]byte, 4)
	le.PutUint16(data[0:], pcm16(16384))
	le.PutUint16(data[2:], pcm16(-8192))

	got, err := ReadWAV(writeTemp(t, buildWAV(SampleRate, 1, 16, nil, data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d frames, want 2", len(got))
	}
	for i, f := range got {
		if f.L != f.R {
			t.Errorf("frame %d = %+v: mono was not duplicated to both channels", i, f)
		}
	}
}

// TestReadWAVSkipsUnknownChunks: real WAVs carry LIST and fact chunks between
// fmt and data, so fixed offsets do not work.
func TestReadWAVSkipsUnknownChunks(t *testing.T) {
	le := binary.LittleEndian
	list := make([]byte, 8+10)
	copy(list, "LIST")
	le.PutUint32(list[4:], 10)

	data := make([]byte, 4)
	le.PutUint16(data[0:], pcm16(16384))
	le.PutUint16(data[2:], pcm16(16384))

	got, err := ReadWAV(writeTemp(t, buildWAV(SampleRate, 2, 16, list, data)))
	if err != nil {
		t.Fatalf("a LIST chunk between fmt and data broke the reader: %v", err)
	}
	if len(got) != 1 || got[0].L != 0.5 {
		t.Errorf("read %+v, want one frame at 0.5", got)
	}
}

// TestReadWAVRejectsWrongRate: silently accepting 24 kHz would play every TTS
// clip at the wrong pitch, which is far harder to diagnose than a refusal.
func TestReadWAVRejectsWrongRate(t *testing.T) {
	_, err := ReadWAV(writeTemp(t, buildWAV(24000, 2, 16, nil, make([]byte, 8))))
	if err == nil {
		t.Fatal("ReadWAV accepted a 24 kHz file")
	}
	if !strings.Contains(err.Error(), "24000") || !strings.Contains(err.Error(), "48000") {
		t.Errorf("error %q should name both the found and the required rate", err)
	}
}

func TestReadWAVRejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"not RIFF":       []byte("XXXXXXXXXXXXXXXX"),
		"truncated":      []byte("RIFF"),
		"24-bit":         buildWAV(SampleRate, 2, 24, nil, make([]byte, 12)),
		"seven channels": buildWAV(SampleRate, 7, 16, nil, make([]byte, 14)),
	}
	for name, raw := range cases {
		if _, err := ReadWAV(writeTemp(t, raw)); err == nil {
			t.Errorf("%s: ReadWAV returned no error", name)
		}
	}
}

func TestReadWAVMissingFile(t *testing.T) {
	if _, err := ReadWAV(filepath.Join(t.TempDir(), "nope.wav")); err == nil {
		t.Error("ReadWAV returned no error for a missing file")
	}
}

// TestReadWAVRejectsOversizedChunk stops a corrupt header sending the reader
// past the end of the buffer.
func TestReadWAVRejectsOversizedChunk(t *testing.T) {
	raw := buildWAV(SampleRate, 2, 16, nil, make([]byte, 8))
	binary.LittleEndian.PutUint32(raw[len(raw)-12:], 1<<30) // data claims a gigabyte

	if _, err := ReadWAV(writeTemp(t, raw)); err == nil {
		t.Error("ReadWAV accepted a chunk size past the end of the file")
	}
}
