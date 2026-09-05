// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/sched"
)

// stopAfter cancels the mixer once it has written enough, so a test drives an
// exact number of blocks with no sleeping and no polling.
type stopAfter struct {
	buf    bytes.Buffer
	limit  int
	cancel context.CancelFunc
}

func (w *stopAfter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.buf.Len() >= w.limit {
		w.cancel()
	}
	return n, err
}

// newTestMixer wires a mixer against a fake clock and an in-memory writer.
func newTestMixer(t *testing.T, seconds float64, block int) (*Mixer, *stopAfter, context.Context, *bytes.Buffer) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	w := &stopAfter{limit: int(seconds*SampleRate) * FrameBytes, cancel: cancel}
	logs := &bytes.Buffer{}

	m := &Mixer{
		Ring:        NewRing(10 * SampleRate),
		Queue:       &sched.Queue{},
		Chain:       NewChain(),
		Pacer:       NewPacer(clock.NewFake(time.Unix(0, 0))),
		W:           w,
		Log:         slog.New(slog.NewTextHandler(logs, nil)),
		BlockFrames: block,
	}
	return m, w, ctx, logs
}

func TestMixerWritesContinuously(t *testing.T) {
	// 4800 divides ten seconds exactly, so the byte count can be asserted on
	// the nose rather than to the nearest block.
	m, w, ctx, _ := newTestMixer(t, 10, 4800)
	m.Ring.Write(sine(10*SampleRate, 440, 0.3))

	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run: %v", err)
	}

	if got, want := w.buf.Len(), 10*SampleRate*FrameBytes; got != want {
		t.Errorf("wrote %d bytes for ten seconds, want %d", got, want)
	}
	if got, want := w.buf.Len(), int(m.SamplePos())*FrameBytes; got != want {
		t.Errorf("wrote %d bytes but the clock says %d frames (%d bytes)", got, m.SamplePos(), want)
	}
}

func TestMixerSpliceLandsAtRequestedSample(t *testing.T) {
	const at = 96000

	m, w, ctx, _ := newTestMixer(t, 4, 4800)
	// No music at all, so any energy in the output is the break.
	path := writeToneWAV(t, 0.5, 0.5)
	if err := m.Queue.Enqueue(sched.Entry{AfterSample: at, Action: sched.ActionSpliceAudio, Path: path}); err != nil {
		t.Fatal(err)
	}

	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run: %v", err)
	}

	first := firstEnergyFrame(t, w.buf.Bytes(), 0.01)
	if first < 0 {
		t.Fatal("no speech energy in the output at all: the break never aired")
	}
	if d := first - at; d < -240 || d > 240 {
		t.Errorf("break energy starts at frame %d, want within 240 frames of %d (off by %d)", first, at, d)
	}
}

func TestMixerContinuesWhenRingEmpty(t *testing.T) {
	m, w, ctx, _ := newTestMixer(t, 2, 4096)
	// Nothing is ever written to the ring.

	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run: %v", err)
	}

	if w.buf.Len() < 2*SampleRate*FrameBytes {
		t.Fatalf("wrote only %d bytes with an empty ring: the mixer stalled", w.buf.Len())
	}
	if w.buf.Len()%FrameBytes != 0 {
		t.Errorf("wrote %d bytes, not a whole number of frames", w.buf.Len())
	}
	if !bytes.Equal(w.buf.Bytes(), make([]byte, w.buf.Len())) {
		t.Error("an empty ring should produce silence")
	}
	if m.Ring.UnderrunCount() == 0 {
		t.Error("underruns were not counted")
	}
}

func TestMixerDropsLateBreak(t *testing.T) {
	m, w, ctx, logs := newTestMixer(t, 2, 4096)
	m.Ring.Write(sine(2*SampleRate, 440, 0.3))
	missing := filepath.Join(t.TempDir(), "does-not-exist.wav")
	if err := m.Queue.Enqueue(sched.Entry{AfterSample: 4096, Action: sched.ActionSpliceAudio, Path: missing}); err != nil {
		t.Fatal(err)
	}

	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run returned %v: a missing break file must not kill the loop", err)
	}

	if w.buf.Len() < 2*SampleRate*FrameBytes {
		t.Errorf("wrote only %d bytes: the mixer stopped when the break file was missing", w.buf.Len())
	}
	if !strings.Contains(logs.String(), "does-not-exist.wav") {
		t.Errorf("the dropped break was not logged; log was:\n%s", logs.String())
	}
}

// TestMixerDrainsAheadByChainLatency pins the compensation direction. Draining
// at the raw sample position lands every break 240 frames late; draining at
// samplePos+latency lands it on time. Off by 240 in the wrong direction is 480
// frames of error and still "small", so the sign has to be asserted.
func TestMixerDrainsAheadByChainLatency(t *testing.T) {
	const at = 48000

	m, w, ctx, _ := newTestMixer(t, 2, 4800)
	path := writeToneWAV(t, 0.25, 0.5)
	if err := m.Queue.Enqueue(sched.Entry{AfterSample: at, Action: sched.ActionSpliceAudio, Path: path}); err != nil {
		t.Fatal(err)
	}
	if err := m.Run(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("Run: %v", err)
	}

	first := firstEnergyFrame(t, w.buf.Bytes(), 0.01)
	if first < 0 {
		t.Fatal("the break never aired")
	}
	// Sub-block placement means this is exact, not approximate. Draining at the
	// raw sample position instead lands it 240 frames late; adding the latency
	// with the wrong sign lands it 240 early.
	if first != at {
		t.Errorf("break energy starts at %d, want exactly %d (off by %d)", first, at, first-at)
	}
}

func TestMixerStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Mixer{
		Ring:  NewRing(SampleRate),
		Queue: &sched.Queue{},
		Chain: NewChain(),
		Pacer: NewPacer(clock.NewFake(time.Unix(0, 0))),
		W:     &bytes.Buffer{},
		Log:   slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}
	cancel()

	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Run returned nil after cancellation, want the context error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// writeToneWAV writes a 48 kHz 16-bit stereo WAV holding a 1 kHz tone. The
// header is spelled out here rather than reusing the reader, so the two are
// not checking each other.
func writeToneWAV(t *testing.T, seconds, amp float64) string {
	t.Helper()

	frames := int(seconds * SampleRate)
	data := make([]byte, frames*4)
	for i := 0; i < frames; i++ {
		// Cosine, not sine: a sine starts at zero, so an energy detector would
		// report the tone starting one frame late and blame the mixer.
		v := int16(amp * 32767 * math.Cos(2*math.Pi*1000*float64(i)/SampleRate))
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
	le.PutUint32(buf[24:], SampleRate)
	le.PutUint32(buf[28:], SampleRate*4)
	le.PutUint16(buf[32:], 4)
	le.PutUint16(buf[34:], 16)
	copy(buf[36:], "data")
	le.PutUint32(buf[40:], uint32(len(data)))
	copy(buf[44:], data)

	path := filepath.Join(t.TempDir(), "tone.wav")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// firstEnergyFrame returns the index of the first frame whose magnitude exceeds
// threshold, or -1.
func firstEnergyFrame(t *testing.T, raw []byte, threshold float32) int {
	t.Helper()

	frames := make([]Frame, len(raw)/FrameBytes)
	if _, err := ReadF32LE(bytes.NewReader(raw), frames); err != nil {
		t.Fatal(err)
	}
	for i, f := range frames {
		if abs32(f.L) > threshold || abs32(f.R) > threshold {
			return i
		}
	}
	return -1
}
