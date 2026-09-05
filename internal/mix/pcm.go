// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package mix holds the canonical audio bus and everything that operates on it.
//
// One fixed bus format is what eliminates codec-parameter mismatch. A real music
// library mixes 44.1 kHz and 48 kHz, mono and stereo; resampling everything into
// one shape once means the encoder's input never changes mid-stream.
package mix

import (
	"encoding/binary"
	"io"
	"math"
	"time"
)

// The canonical bus. These are constants, not configuration.
const (
	SampleRate = 48000 // Hz, always, even when the source file is 44100
	Channels   = 2     // stereo, always; mono sources are duplicated
	FrameBytes = 4 * Channels
)

// Frame is one stereo sample pair, nominal range -1.0 .. +1.0.
//
// float32 in memory and f32le on the pipe, so the limiter's headroom survives
// all the way to the encoder. There is no s16 path anywhere in this program.
type Frame struct{ L, R float32 }

// chunkFrames bounds the scratch buffer used when moving frames to and from an
// io stream. It is a batching detail, never a limit on the caller's slice.
const chunkFrames = 1024

// DurationToFrames converts wall-clock time to a frame count on the bus.
//
// Seconds and remainder are converted separately so the intermediate product
// cannot overflow on a duration measured in days.
func DurationToFrames(d time.Duration) int {
	sec, rem := int64(d)/int64(time.Second), int64(d)%int64(time.Second)
	return int(sec*SampleRate + rem*SampleRate/int64(time.Second))
}

// FramesToDuration converts a frame count on the bus to wall-clock time.
func FramesToDuration(n int) time.Duration { return framesToDuration64(int64(n)) }

// WriteF32LE writes frames as interleaved little-endian float32: L,R,L,R...
func WriteF32LE(w io.Writer, f []Frame) error {
	buf := make([]byte, chunkFrames*FrameBytes)
	for len(f) > 0 {
		n := min(len(f), chunkFrames)
		for i, fr := range f[:n] {
			binary.LittleEndian.PutUint32(buf[i*FrameBytes:], math.Float32bits(fr.L))
			binary.LittleEndian.PutUint32(buf[i*FrameBytes+4:], math.Float32bits(fr.R))
		}
		if _, err := w.Write(buf[:n*FrameBytes]); err != nil {
			return err
		}
		f = f[n:]
	}
	return nil
}

// ReadF32LE fills f from an interleaved little-endian float32 stream and reports
// how many whole frames landed.
//
// The error distinguishes the two ways a stream can end, which is what callers
// decoding a possibly-truncated file need: io.EOF means the stream ended on a
// frame boundary, io.ErrUnexpectedEOF means it ended mid-frame.
func ReadF32LE(r io.Reader, f []Frame) (int, error) {
	buf := make([]byte, chunkFrames*FrameBytes)
	read := 0
	for read < len(f) {
		want := min(len(f)-read, chunkFrames)
		got, err := io.ReadFull(r, buf[:want*FrameBytes])
		for i := 0; i < got/FrameBytes; i++ {
			f[read+i].L = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*FrameBytes:]))
			f[read+i].R = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*FrameBytes+4:]))
		}
		read += got / FrameBytes
		if err != nil {
			// io.ReadFull reports io.EOF only when it read nothing at all, so a
			// clean end after some frames arrives as ErrUnexpectedEOF. Correct
			// it back to io.EOF when the truncation lands on a frame boundary.
			if err == io.ErrUnexpectedEOF && got%FrameBytes == 0 {
				err = io.EOF
			}
			return read, err
		}
	}
	return read, nil
}
