// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"sync"
	"testing"
	"time"
)

func ramp(n int) []Frame {
	f := make([]Frame, n)
	for i := range f {
		f[i] = Frame{L: float32(i + 1), R: -float32(i + 1)}
	}
	return f
}

func TestRingWriteThenRead(t *testing.T) {
	r := NewRing(48000)
	in := ramp(100)

	if n := r.Write(in); n != 100 {
		t.Fatalf("Write accepted %d frames, want 100", n)
	}

	out := make([]Frame, 100)
	if n := r.Read(out); n != 100 {
		t.Fatalf("Read delivered %d real frames, want 100", n)
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("frame %d = %+v, want %+v", i, out[i], in[i])
		}
	}
	if r.UnderrunCount() != 0 {
		t.Errorf("UnderrunCount = %d, want 0", r.UnderrunCount())
	}
}

func TestRingUnderrunFillsSilence(t *testing.T) {
	r := NewRing(10 * SampleRate)

	out := make([]Frame, 1000)
	for i := range out {
		out[i] = Frame{L: 9, R: 9} // poison, must be overwritten
	}

	if n := r.Read(out); n != 0 {
		t.Errorf("Read delivered %d real frames from an empty ring, want 0", n)
	}
	for i, f := range out {
		if f != (Frame{}) {
			t.Fatalf("frame %d = %+v, want silence", i, f)
		}
	}
	if r.UnderrunCount() != 1 {
		t.Errorf("UnderrunCount = %d, want 1", r.UnderrunCount())
	}
}

func TestRingPartialUnderrun(t *testing.T) {
	r := NewRing(10 * SampleRate)
	in := ramp(400)
	r.Write(in)

	out := make([]Frame, 1000)
	if n := r.Read(out); n != 400 {
		t.Errorf("Read delivered %d real frames, want 400", n)
	}
	for i := 0; i < 400; i++ {
		if out[i] != in[i] {
			t.Fatalf("frame %d = %+v, want %+v", i, out[i], in[i])
		}
	}
	for i := 400; i < 1000; i++ {
		if out[i] != (Frame{}) {
			t.Fatalf("frame %d = %+v, want silence", i, out[i])
		}
	}
	if r.UnderrunCount() != 1 {
		t.Errorf("UnderrunCount = %d, want 1", r.UnderrunCount())
	}
}

func TestRingOccupancy(t *testing.T) {
	r := NewRing(10 * SampleRate)
	if got := r.Occupancy(); got != 0 {
		t.Errorf("empty Occupancy = %v, want 0", got)
	}

	r.Write(make([]Frame, SampleRate))
	if got := r.Occupancy(); got != time.Second {
		t.Errorf("Occupancy = %v, want 1s", got)
	}

	r.Read(make([]Frame, SampleRate/2))
	if got := r.Occupancy(); got != 500*time.Millisecond {
		t.Errorf("Occupancy after half drained = %v, want 500ms", got)
	}
}

// TestRingWrapAround walks the read and write cursors past the end of the
// backing array several times. An off-by-one here is silent: it corrupts audio
// rather than failing.
func TestRingWrapAround(t *testing.T) {
	r := NewRing(1000)
	next := 0
	out := make([]Frame, 700)

	for round := 0; round < 5; round++ {
		in := make([]Frame, 700)
		for i := range in {
			next++
			in[i] = Frame{L: float32(next), R: -float32(next)}
		}
		if n := r.Write(in); n != 700 {
			t.Fatalf("round %d: Write accepted %d, want 700", round, n)
		}
		if n := r.Read(out); n != 700 {
			t.Fatalf("round %d: Read delivered %d, want 700", round, n)
		}
		for i := range in {
			if out[i] != in[i] {
				t.Fatalf("round %d frame %d = %+v, want %+v", round, i, out[i], in[i])
			}
		}
	}
	if r.UnderrunCount() != 0 {
		t.Errorf("UnderrunCount = %d, want 0", r.UnderrunCount())
	}
}

// TestRingWriteStopsAtCapacity proves the buffer is bounded. A ring that grew
// on demand would turn a slow encoder into unbounded memory growth.
func TestRingWriteStopsAtCapacity(t *testing.T) {
	r := NewRing(500)

	if n := r.Write(ramp(800)); n != 500 {
		t.Fatalf("Write accepted %d frames into a 500-frame ring, want 500", n)
	}
	if n := r.Write(ramp(10)); n != 0 {
		t.Errorf("Write accepted %d frames into a full ring, want 0", n)
	}
	if got := r.Occupancy(); got != FramesToDuration(500) {
		t.Errorf("Occupancy = %v, want %v", got, FramesToDuration(500))
	}

	// The frames kept must be the FIRST 500 written, not the last.
	out := make([]Frame, 500)
	r.Read(out)
	want := ramp(500)
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("frame %d = %+v, want %+v", i, out[i], want[i])
		}
	}
}

func TestRingReadZeroLength(t *testing.T) {
	r := NewRing(100)
	r.Write(ramp(10))

	if n := r.Read(nil); n != 0 {
		t.Errorf("Read(nil) = %d, want 0", n)
	}
	if r.UnderrunCount() != 0 {
		t.Errorf("a zero-length read counted as an underrun")
	}
	if got := r.Occupancy(); got != FramesToDuration(10) {
		t.Errorf("Read(nil) consumed frames: Occupancy = %v", got)
	}
}

// TestRingConcurrent is the real usage: decoders write while the mixer reads.
// Meaningful under -race.
func TestRingConcurrent(t *testing.T) {
	r := NewRing(4096)
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			r.Write(ramp(64))
		}
	}()
	go func() {
		defer wg.Done()
		out := make([]Frame, 64)
		for i := 0; i < 500; i++ {
			r.Read(out)
			r.Occupancy()
			r.UnderrunCount()
		}
	}()
	wg.Wait()
}
