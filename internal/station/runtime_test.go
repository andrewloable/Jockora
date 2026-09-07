// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/encode"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/sched"
)

// fakeEncoder stands in for ffmpeg so a test can prove the process was reaped.
// The real supervisor spawns a child; a leaked one is a core burning per
// station that was stopped.
type fakeEncoder struct {
	mu       sync.Mutex
	stopped  int
	written  int64
	restarts int
	fail     error
	stopErr  error
}

func (f *fakeEncoder) Writer() io.Writer { return f }
func (f *fakeEncoder) Write(p []byte) (int, error) {
	f.mu.Lock()
	err := f.fail
	f.mu.Unlock()
	if err != nil {
		return 0, err
	}
	atomic.AddInt64(&f.written, int64(len(p)))
	return len(p), nil
}
func (f *fakeEncoder) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
	return f.stopErr
}
func (f *fakeEncoder) Restarts() int    { return f.restarts }
func (f *fakeEncoder) Degraded() string { return "" }
func (f *fakeEncoder) PID() int         { return 4242 }
func (f *fakeEncoder) stops() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// silence keeps the ring full so the mixer has something to write.
func silence(ctx context.Context, ring *mix.Ring, _ *sched.Queue) {
	block := make([]mix.Frame, 4800)
	for ctx.Err() == nil {
		rest := block
		for len(rest) > 0 && ctx.Err() == nil {
			n := ring.Write(rest)
			rest = rest[n:]
			if n == 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Millisecond):
				}
			}
		}
	}
}

func runtimeDeps(enc *fakeEncoder) Deps {
	d := Deps{
		Log: quietLog(), Clock: clock.Real{},
		SampleRate: mix.SampleRate, Channels: mix.Channels,
		SegmentSeconds: 1, ListSize: 5,
		Sel:  NewSelector([]int64{1, 2, 3}, 1),
		Feed: silence,
	}
	if enc != nil {
		d.StartEncoder = func(context.Context, encode.Config, *slog.Logger) (Encoder, error) {
			return enc, nil
		}
	}
	return d
}

// TestRuntimeStartWritesPlaylist uses the REAL encoder: a fake cannot show that
// ffmpeg was given a working configuration, and a station whose playlist never
// appears is one no client can play.
func TestRuntimeStartWritesPlaylist(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "segments")
	rt := NewRuntime(runtimeDeps(nil), 1, dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer rt.Stop()

	playlist := filepath.Join(dir, "stream.m3u8")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(playlist); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no playlist at %s", playlist)
}

func TestRuntimeStopReleasesEverything(t *testing.T) {
	enc := &fakeEncoder{}
	rt := NewRuntime(runtimeDeps(enc), 1, filepath.Join(t.TempDir(), "segments"))

	before := runtime.NumGoroutine()
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	if err := rt.Stop(); err != nil {
		t.Fatal(err)
	}
	if enc.stops() != 1 {
		t.Errorf("the encoder was stopped %d times, want 1", enc.stops())
	}
	if rt.Status().Running {
		t.Error("Status still reports the station on air")
	}

	// A feeder still decoding into a ring nobody drains is a station that
	// stopped on paper and is still burning a core.
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Errorf("%d goroutines after Stop, started from %d", after, before)
	}
}

// TestRuntimeStopIsIdempotent: the manager may stop a station that has already
// stopped itself, and a second Stop that panicked would take the server down.
func TestRuntimeStopIsIdempotent(t *testing.T) {
	enc := &fakeEncoder{}
	rt := NewRuntime(runtimeDeps(enc), 1, filepath.Join(t.TempDir(), "segments"))
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := rt.Stop(); err != nil {
		t.Errorf("second Stop = %v, want nil", err)
	}
	if enc.stops() != 1 {
		t.Errorf("the encoder was stopped %d times, want 1", enc.stops())
	}
	// Stopping one that never started is the same non-event.
	fresh := NewRuntime(runtimeDeps(&fakeEncoder{}), 2, t.TempDir())
	if err := fresh.Stop(); err != nil {
		t.Errorf("stopping a station that never ran = %v, want nil", err)
	}
	if err := fresh.Wait(); err != nil {
		t.Errorf("waiting on a station that never ran = %v, want nil", err)
	}
}

// TestRuntimeOwnSegmentDir: two stations writing into one directory would
// overwrite each other's segments, and every listener would hear a mixture.
func TestRuntimeOwnSegmentDir(t *testing.T) {
	root := t.TempDir()
	one := filepath.Join(root, "one")
	two := filepath.Join(root, "two")

	a := NewRuntime(runtimeDeps(nil), 1, one)
	b := NewRuntime(runtimeDeps(nil), 2, two)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, rt := range []*Runtime{a, b} {
		if err := rt.Start(ctx); err != nil {
			t.Fatal(err)
		}
		defer rt.Stop()
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, e1 := os.Stat(filepath.Join(one, "stream.m3u8"))
		_, e2 := os.Stat(filepath.Join(two, "stream.m3u8"))
		if e1 == nil && e2 == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	for _, dir := range []string{one, two} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			t.Fatalf("%s is empty; the station wrote nothing", dir)
		}
	}
	if a.SegmentDir() == b.SegmentDir() {
		t.Error("two stations share a segment directory")
	}
}

// TestRuntimeStartAfterStop: a station comes back when a listener returns, and
// it must come back fresh -- resuming a spent ring would open with whatever was
// buffered when the last listener left.
func TestRuntimeStartAfterStop(t *testing.T) {
	enc := &fakeEncoder{}
	rt := NewRuntime(runtimeDeps(enc), 1, filepath.Join(t.TempDir(), "segments"))

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Queue().Enqueue(sched.Entry{
		AfterSample: 0, Action: sched.ActionSpliceAudio, Path: "/tmp/x.wav",
		Placement: mix.PlacementBetween.String()}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := rt.Stop(); err != nil {
		t.Fatal(err)
	}

	firstRing, firstQueue := rt.Ring(), rt.Queue()
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rt.Stop()

	if rt.Ring() == firstRing {
		t.Error("the restarted station reused the old ring")
	}
	if rt.Queue() == firstQueue {
		t.Error("the restarted station reused the old queue")
	}
	if n := rt.Queue().Pending(); n != 0 {
		t.Errorf("%d breaks carried over from the previous run", n)
	}
	if enc.stops() != 1 {
		t.Errorf("the encoder was stopped %d times before the restart, want 1", enc.stops())
	}
}

func TestRuntimeStatus(t *testing.T) {
	enc := &fakeEncoder{restarts: 3}
	rt := NewRuntime(runtimeDeps(enc), 77, filepath.Join(t.TempDir(), "segments"))

	// Before anything runs: the station is real, it is simply not on air.
	st := rt.Status()
	if rt.StationID() != 77 {
		t.Errorf("StationID() = %d, want 77", rt.StationID())
	}
	if st.StationID != 77 || st.Running {
		t.Errorf("status before Start = %+v", st)
	}
	if rt.SamplePos() != 0 || rt.Drift() != 0 {
		t.Errorf("a station that never played reports position %d and drift %v",
			rt.SamplePos(), rt.Drift())
	}
	if rt.Encoder() != nil {
		t.Error("an encoder exists before Start")
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rt.Stop()
	if err := rt.Queue().Enqueue(sched.Entry{
		AfterSample: int64(mix.SampleRate) * 3600, Action: sched.ActionSpliceAudio,
		Path: "/tmp/x.wav", Placement: mix.PlacementBetween.String()}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	st = rt.Status()
	if !st.Running || st.StationID != 77 {
		t.Errorf("status while running = %+v", st)
	}
	if st.EncoderRestarts != 3 {
		t.Errorf("EncoderRestarts = %d, want 3", st.EncoderRestarts)
	}
	if st.PendingBreaks != 1 {
		t.Errorf("PendingBreaks = %d, want 1", st.PendingBreaks)
	}
	if st.RingOccupancyS <= 0 {
		t.Errorf("RingOccupancyS = %v with a feeder running", st.RingOccupancyS)
	}
	if rt.Encoder() == nil || rt.Selector() == nil {
		t.Error("the running station exposes no encoder or no selector")
	}
	if rt.Writer() != nil {
		t.Error("a runtime with no break writer reports one")
	}
	if rt.Metrics() == nil {
		t.Error("no metrics")
	}
	// Once it is playing, the position has to move: a station reporting frame
	// zero forever is indistinguishable from one that is not mixing at all.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && rt.SamplePos() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if rt.SamplePos() == 0 {
		t.Error("the mixer never advanced")
	}
	rt.Drift() // real, and its value is the pacer's business, not this test's
}

// TestRuntimeRefusesASecondStart: two mixers on one encoder would interleave
// their writes and produce an unplayable stream.
func TestRuntimeRefusesASecondStart(t *testing.T) {
	enc := &fakeEncoder{}
	rt := NewRuntime(runtimeDeps(enc), 1, filepath.Join(t.TempDir(), "segments"))
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rt.Stop()
	if err := rt.Start(context.Background()); !errors.Is(err, ErrRuntimeRunning) {
		t.Errorf("second Start = %v, want ErrRuntimeRunning", err)
	}
}

func TestRuntimeSurfacesFailures(t *testing.T) {
	t.Run("encoder will not start", func(t *testing.T) {
		d := runtimeDeps(nil)
		d.StartEncoder = func(context.Context, encode.Config, *slog.Logger) (Encoder, error) {
			return nil, errors.New("ffmpeg is not installed")
		}
		rt := NewRuntime(d, 1, filepath.Join(t.TempDir(), "segments"))
		if err := rt.Start(context.Background()); err == nil {
			t.Error("a station started with no encoder")
		}
		if rt.Status().Running {
			t.Error("a station that failed to start reports itself on air")
		}
	})

	t.Run("segment directory cannot be made", func(t *testing.T) {
		// A file where the directory should be.
		blocked := filepath.Join(t.TempDir(), "notadir")
		if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		rt := NewRuntime(runtimeDeps(&fakeEncoder{}), 1, filepath.Join(blocked, "segments"))
		if err := rt.Start(context.Background()); err == nil {
			t.Error("a station started with nowhere to write segments")
		}
	})

	t.Run("cancelled context is a normal stop", func(t *testing.T) {
		rt := NewRuntime(runtimeDeps(&fakeEncoder{}), 1, filepath.Join(t.TempDir(), "segments"))
		ctx, cancel := context.WithCancel(context.Background())
		if err := rt.Start(ctx); err != nil {
			t.Fatal(err)
		}
		cancel()
		if err := rt.Wait(); err != nil {
			t.Errorf("Wait after cancellation = %v, want nil", err)
		}
		if err := rt.Stop(); err != nil {
			t.Fatal(err)
		}
	})

	// A mixer that stops on a real fault must say so, or a station that fell
	// off the air reads exactly like one a listener left.
	t.Run("a broken encoder is not a normal stop", func(t *testing.T) {
		enc := &fakeEncoder{fail: errors.New("broken pipe")}
		rt := NewRuntime(runtimeDeps(enc), 1, filepath.Join(t.TempDir(), "segments"))
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer rt.Stop()
		if err := rt.Wait(); err == nil {
			t.Error("a mixer that died on a broken pipe reported a clean stop")
		}
	})

	t.Run("defaults fill themselves in", func(t *testing.T) {
		// No log, no clock, no encoder factory: a caller that supplies none of
		// them still gets a runtime rather than a nil dereference.
		rt := NewRuntime(Deps{SampleRate: mix.SampleRate, Channels: mix.Channels,
			SegmentSeconds: 1, ListSize: 5}, 1, filepath.Join(t.TempDir(), "segments"))
		if rt.Ring() == nil || rt.Queue() == nil {
			t.Fatal("NewRuntime left the pipeline nil")
		}
		if rt.deps.Log == nil || rt.deps.Clock == nil || rt.deps.StartEncoder == nil {
			t.Error("NewRuntime did not fill in its defaults")
		}
	})
}

// TestRuntimeIsTheSoleWriter is structural, and moved here from the app package
// when the pipeline did: exactly one place may write to the encoder pipe, and
// it is the mixer's W field. A second writer would interleave bytes into the
// middle of a frame and the stream would be noise.
func TestRuntimeIsTheSoleWriter(t *testing.T) {
	src, err := os.ReadFile("runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "enc.Writer()"); n != 1 {
		t.Errorf("runtime.go takes the encoder writer %d times, want exactly 1", n)
	}
}

// TestRuntimeSegmentNumberingContinues: segments are served with a one-year
// immutable cache. A restarted station that began again at seg0 would overwrite
// URLs it had already served, and a listener returning the next day would play
// yesterday's audio straight out of their own cache with no request reaching
// the server to correct it.
func TestRuntimeSegmentNumberingContinues(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "segments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"seg0.ts", "seg7.ts", "seg12.ts", "stream.m3u8", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := nextSegmentNumber(dir); got != 13 {
		t.Errorf("nextSegmentNumber = %d, want 13", got)
	}

	// A directory with nothing in it, and one that does not exist, both start
	// at zero -- the first because it is a new station, the second because
	// Start is about to fail on it anyway.
	if got := nextSegmentNumber(t.TempDir()); got != 0 {
		t.Errorf("an empty directory gave %d, want 0", got)
	}
	if got := nextSegmentNumber(filepath.Join(t.TempDir(), "nope")); got != 0 {
		t.Errorf("a missing directory gave %d, want 0", got)
	}
	// A number no int can hold is skipped rather than crashing the station.
	odd := t.TempDir()
	if err := os.WriteFile(filepath.Join(odd, "seg99999999999999999999.ts"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := nextSegmentNumber(odd); got != 0 {
		t.Errorf("an unparseable segment number gave %d, want 0", got)
	}

	// And the real encoder is told about it.
	var startFrom int
	d := runtimeDeps(nil)
	d.StartEncoder = func(_ context.Context, cfg encode.Config, _ *slog.Logger) (Encoder, error) {
		startFrom = cfg.StartNumber
		return &fakeEncoder{}, nil
	}
	rt := NewRuntime(d, 1, dir)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rt.Stop()
	if startFrom != 13 {
		t.Errorf("the encoder was told to start at %d, want 13", startFrom)
	}
}
