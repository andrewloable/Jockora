// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"encoding/binary"
	"fmt"
	"os"
)

// ReadWAV loads a WAV file into canonical-bus frames.
//
// The mixer reads speech this way rather than through internal/decode because
// decode imports this package, and because speech always arrives as WAV from
// the TTS sidecar: no ffmpeg process per break.
//
// It is deliberately strict. Anything that is not bus-rate 16-bit PCM is an
// error naming what was found, because silently mis-reading a rate produces
// speech at the wrong pitch, which is much harder to diagnose than a refusal.
func ReadWAV(path string) ([]Frame, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("wav: %w", err)
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, fmt.Errorf("wav: %s: not a RIFF/WAVE file", path)
	}

	le := binary.LittleEndian
	var channels, bits int
	var rate uint32
	var data []byte
	haveFmt := false

	// Walk the chunks. Real WAVs carry LIST, fact and other chunks between fmt
	// and data, so the offsets cannot be assumed.
	for off := 12; off+8 <= len(raw); {
		id := string(raw[off : off+4])
		size := int(le.Uint32(raw[off+4 : off+8]))
		body := off + 8
		if size < 0 || body+size > len(raw) {
			return nil, fmt.Errorf("wav: %s: chunk %q claims %d bytes, past the end of the file", path, id, size)
		}

		switch id {
		case "fmt ":
			if size < 16 {
				return nil, fmt.Errorf("wav: %s: fmt chunk is %d bytes, want at least 16", path, size)
			}
			if format := le.Uint16(raw[body : body+2]); format != 1 {
				return nil, fmt.Errorf("wav: %s: audio format %d, want 1 (uncompressed PCM)", path, format)
			}
			channels = int(le.Uint16(raw[body+2 : body+4]))
			rate = le.Uint32(raw[body+4 : body+8])
			bits = int(le.Uint16(raw[body+14 : body+16]))
			haveFmt = true
		case "data":
			data = raw[body : body+size]
		}

		off = body + size
		if size%2 == 1 {
			off++ // RIFF chunks are word-aligned
		}
	}

	if !haveFmt {
		return nil, fmt.Errorf("wav: %s: no fmt chunk", path)
	}
	if data == nil {
		return nil, fmt.Errorf("wav: %s: no data chunk", path)
	}
	if rate != SampleRate {
		return nil, fmt.Errorf("wav: %s: %d Hz, want %d: resample before handing it to the mixer", path, rate, SampleRate)
	}
	if bits != 16 {
		return nil, fmt.Errorf("wav: %s: %d-bit, want 16", path, bits)
	}
	if channels != 1 && channels != 2 {
		return nil, fmt.Errorf("wav: %s: %d channels, want 1 or 2", path, channels)
	}

	frames := len(data) / (2 * channels)
	out := make([]Frame, frames)
	for i := range out {
		l := float32(int16(le.Uint16(data[i*2*channels:]))) / 32768
		r := l // mono is duplicated to both channels
		if channels == 2 {
			r = float32(int16(le.Uint16(data[i*4+2:]))) / 32768
		}
		out[i] = Frame{L: l, R: r}
	}
	return out, nil
}
