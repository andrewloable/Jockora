// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/encode"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/presence"
	"github.com/andrewloable/jockora/internal/store"
)

const mgrGrace = 30 * time.Second

// factory records what the manager asked it to build and hands back a real
// Runtime driven by a fake encoder -- real, because the point of the manager is
// that it starts and stops actual pipelines.
type factory struct {
	t *testing.T

	mu      sync.Mutex
	built   map[int64]int
	given   map[int64]store.SelectorState
	resumed map[int64]bool
	encs    map[int64]*fakeEncoder
	failFor map[int64]error
	stopErr error
}

func newFactory(t *testing.T) *factory {
	return &factory{t: t, built: map[int64]int{}, given: map[int64]store.SelectorState{},
		resumed: map[int64]bool{}, encs: map[int64]*fakeEncoder{}, failFor: map[int64]error{}}
}

func (f *factory) make() RuntimeFactory {
	return func(ctx context.Context, id int64, state store.SelectorState, resume bool) (*Runtime, error) {
		f.mu.Lock()
		if err := f.failFor[id]; err != nil {
			delete(f.failFor, id) // fails once, then works
			f.mu.Unlock()
			return nil, err
		}
		f.built[id]++
		f.given[id] = state
		f.resumed[id] = resume
		enc := &fakeEncoder{stopErr: f.stopErr}
		f.encs[id] = enc
		f.mu.Unlock()

		sel := NewSelector([]int64{1, 2, 3, 4, 5, 6, 7, 8}, state.Seed)
		if resume {
			sel = NewSelectorAt([]int64{1, 2, 3, 4, 5, 6, 7, 8}, state.Seed, state.Cursor, nil)
		}
		d := runtimeDeps(enc)
		d.Sel = sel
		return NewRuntime(d, id, filepath.Join(f.t.TempDir(), "seg")), nil
	}
}

func (f *factory) builds(id int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.built[id]
}

func (f *factory) stops(id int64) int {
	f.mu.Lock()
	enc := f.encs[id]
	f.mu.Unlock()
	if enc == nil {
		return 0
	}
	return enc.stops()
}

func manager(t *testing.T, f *factory, maxStations int) (*Manager, *presence.Tracker, *clock.Fake, *store.Store) {
	t.Helper()
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	tr := presence.New(clk, mgrGrace)
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for i := 1; i <= 3; i++ {
		if _, err := s.CreateStation(context.Background(), store.Station{
			Name: "S", Genre: "rock", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(tr, s, f.make(), clk, mgrGrace, maxStations)
	t.Cleanup(func() { m.StopAll() })
	return m, tr, clk, s
}

func TestManagerStartsOnFirstListener(t *testing.T) {
	f := newFactory(t)
	m, tr, _, _ := manager(t, f, 4)
	ctx := context.Background()

	// Nobody yet: nothing runs. A station with no listeners renders audio no
	// one hears and spends a core doing it.
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.Running(); len(got) != 0 {
		t.Fatalf("Running() = %v with no listeners", got)
	}

	tr.Touch(1, "s1")
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.Running(); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("Running() = %v, want [1]", got)
	}
	if m.Runtime(1) == nil {
		t.Error("the running station has no runtime")
	}

	// Ticking again must not start it twice.
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.builds(1); n != 1 {
		t.Errorf("the station was built %d times, want 1", n)
	}
}

func TestManagerStopsAfterGrace(t *testing.T) {
	f := newFactory(t)
	m, tr, clk, s := manager(t, f, 4)
	ctx := context.Background()

	tr.Touch(1, "s1")
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	// Let it play a little, so there is a cursor worth saving.
	rt := m.Runtime(1)
	if _, err := rt.Selector().Next(); err != nil {
		t.Fatal(err)
	}
	wantSeed, wantCursor := rt.Selector().State()

	clk.Advance(mgrGrace + time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.Running(); len(got) != 0 {
		t.Errorf("Running() = %v after the last listener left", got)
	}
	if n := f.stops(1); n != 1 {
		t.Errorf("the encoder was stopped %d times, want 1", n)
	}

	// The position is the whole reason a station can stop at all.
	got, ok, err := s.GetSelectorState(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("no saved position: ok = %v, err = %v", ok, err)
	}
	if got.Seed != wantSeed || got.Cursor != wantCursor {
		t.Errorf("saved %+v, want seed %d cursor %d", got, wantSeed, wantCursor)
	}
}

func TestManagerResumesFromCursor(t *testing.T) {
	f := newFactory(t)
	m, tr, _, s := manager(t, f, 4)
	ctx := context.Background()

	want := store.SelectorState{Seed: 4242, Cursor: 3}
	if err := s.SetSelectorState(ctx, 1, want); err != nil {
		t.Fatal(err)
	}

	tr.Touch(1, "s1")
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	f.mu.Lock()
	given, resumed := f.given[1], f.resumed[1]
	f.mu.Unlock()
	if given != want || !resumed {
		t.Fatalf("the factory was given %+v resume=%v, want %+v resume=true", given, resumed, want)
	}
	// And the selector it built really is at that position.
	if seed, cursor := m.Runtime(1).Selector().State(); seed != want.Seed || cursor != want.Cursor {
		t.Errorf("the running selector is at %d/%d, want %d/%d",
			seed, cursor, want.Seed, want.Cursor)
	}

	// A station that has never played is NOT resumed from zero, or every new
	// station would open with the same running order.
	tr.Touch(2, "s2")
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	freshResumed := f.resumed[2]
	f.mu.Unlock()
	if freshResumed {
		t.Error("a station with no stored position was resumed anyway")
	}
}

func TestManagerTwoStationsIndependent(t *testing.T) {
	f := newFactory(t)
	m, tr, clk, _ := manager(t, f, 4)
	ctx := context.Background()

	tr.Touch(1, "s1")
	tr.Touch(2, "s2")
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.Running(); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Fatalf("Running() = %v, want [1 2]", got)
	}

	// Only station 2's listener keeps polling.
	clk.Advance(mgrGrace - time.Second)
	tr.Touch(2, "s2")
	clk.Advance(2 * time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if got := m.Running(); !reflect.DeepEqual(got, []int64{2}) {
		t.Errorf("Running() = %v, want only [2]", got)
	}
	if f.stops(1) != 1 {
		t.Error("station 1 was not stopped")
	}
	if f.stops(2) != 0 {
		t.Error("station 2 was stopped while someone was listening to it")
	}
}

// TestManagerReloadDoesNotRestart: a listener reloading the page is a new
// session on the same station. Tearing the station down and building it again
// would drop the audio they were already hearing.
func TestManagerReloadDoesNotRestart(t *testing.T) {
	f := newFactory(t)
	m, tr, clk, _ := manager(t, f, 4)
	ctx := context.Background()

	tr.Touch(1, "s1")
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Second)
	tr.Touch(1, "s2") // the reload
	clk.Advance(20 * time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if got := m.Running(); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("Running() = %v, want [1] throughout the reload", got)
	}
	if n := f.builds(1); n != 1 {
		t.Errorf("the station was rebuilt %d times across a page reload, want 1", n)
	}
	if n := f.stops(1); n != 0 {
		t.Errorf("the station was stopped %d times across a page reload", n)
	}
}

// TestManagerGenerationSerialized: one GPU, one model resident at a time. Two
// stations generating at once is a model reload, discovered at airtime.
func TestManagerGenerationSerialized(t *testing.T) {
	f := newFactory(t)
	m, _, _, _ := manager(t, f, 4)

	var live, peak int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Generate(context.Background(), func() error {
				n := atomic.AddInt64(&live, 1)
				for {
					was := atomic.LoadInt64(&peak)
					if n <= was || atomic.CompareAndSwapInt64(&peak, was, n) {
						break
					}
				}
				time.Sleep(2 * time.Millisecond)
				atomic.AddInt64(&live, -1)
				return nil
			})
		}()
	}
	wg.Wait()

	if peak != 1 {
		t.Errorf("%d generations ran at once, want 1", peak)
	}

	// The caller's error comes back untouched, and a cancelled caller does not
	// wait forever for the gate.
	want := errors.New("the model refused")
	if err := m.Generate(context.Background(), func() error { return want }); !errors.Is(err, want) {
		t.Errorf("Generate = %v, want the caller's own error", err)
	}
	held := make(chan struct{})
	go func() {
		_ = m.Generate(context.Background(), func() error { <-held; return nil })
	}()
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Generate(ctx, func() error { t.Error("ran despite a cancelled context"); return nil }); err == nil {
		t.Error("a cancelled caller waited for the gate anyway")
	}
	close(held)
}

// TestManagerCapConcurrentStations: a per-station ffmpeg is the one cost that
// scales with stations, and the target box is small.
func TestManagerCapConcurrentStations(t *testing.T) {
	f := newFactory(t)
	m, tr, clk, _ := manager(t, f, 1)
	ctx := context.Background()

	tr.Touch(1, "s1")
	tr.Touch(2, "s2")
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.Running(); !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("Running() = %v under a cap of 1, want [1]", got)
	}

	// Station 1's listener leaves; station 2 has been waiting and takes the
	// slot on the SAME tick, because stops are reconciled before starts.
	clk.Advance(mgrGrace - time.Second)
	tr.Touch(2, "s2")
	clk.Advance(2 * time.Second)
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.Running(); !reflect.DeepEqual(got, []int64{2}) {
		t.Errorf("Running() = %v, want [2]", got)
	}
}

// TestManagerStartFailureDoesNotWedge: a station that failed to start must be
// retried. Recording it as running would leave it silent forever, and every
// later tick would believe it was fine.
func TestManagerStartFailureDoesNotWedge(t *testing.T) {
	f := newFactory(t)
	m, tr, _, _ := manager(t, f, 4)
	ctx := context.Background()

	f.mu.Lock()
	f.failFor[1] = errors.New("no encoder today")
	f.mu.Unlock()

	tr.Touch(1, "s1")
	if err := m.Tick(ctx); err == nil {
		t.Fatal("a failed start was reported as success")
	}
	if got := m.Running(); len(got) != 0 {
		t.Errorf("Running() = %v after a failed start", got)
	}

	if err := m.Tick(ctx); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if got := m.Running(); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("Running() = %v after the retry, want [1]", got)
	}
}

func TestManagerSurfacesFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("unreadable position", func(t *testing.T) {
		f := newFactory(t)
		m, tr, _, s := manager(t, f, 4)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		tr.Touch(1, "s1")
		if err := m.Tick(ctx); err == nil {
			t.Error("a station started without knowing where it had got to")
		}
		if got := m.Running(); len(got) != 0 {
			t.Errorf("Running() = %v", got)
		}
	})

	t.Run("unsaveable position", func(t *testing.T) {
		f := newFactory(t)
		m, tr, clk, s := manager(t, f, 4)
		tr.Touch(1, "s1")
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		s.DB().SetMaxOpenConns(1)
		if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
			t.Fatal(err)
		}
		clk.Advance(mgrGrace + time.Second)
		if err := m.Tick(ctx); err == nil {
			t.Error("a position that could not be saved was reported as saved")
		}
		// Off air regardless: a station that cannot save its place must still
		// release its encoder.
		if got := m.Running(); len(got) != 0 {
			t.Errorf("Running() = %v", got)
		}
		if f.stops(1) != 1 {
			t.Error("the encoder was left running")
		}
	})

	// An encoder that will not die is worth saying out loud: the next start
	// finds the port or the segment directory still held.
	t.Run("encoder will not stop", func(t *testing.T) {
		f := newFactory(t)
		f.stopErr = errors.New("ffmpeg would not die")
		m, tr, _, _ := manager(t, f, 4)
		tr.Touch(1, "s1")
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if err := m.StopAll(); err == nil {
			t.Error("StopAll hid an encoder that refused to stop")
		}
		// Forgotten by the manager regardless, or it can never be retried.
		if got := m.Running(); len(got) != 0 {
			t.Errorf("Running() = %v", got)
		}
	})

	t.Run("stopping one that is not running", func(t *testing.T) {
		f := newFactory(t)
		m, _, _, _ := manager(t, f, 4)
		if err := m.Stop(99); err != nil {
			t.Errorf("Stop(99) = %v, want nil", err)
		}
		if m.Runtime(99) != nil {
			t.Error("Runtime(99) invented a station")
		}
		if err := m.StopAll(); err != nil {
			t.Errorf("StopAll with nothing running = %v", err)
		}
	})

	t.Run("no store at all", func(t *testing.T) {
		// The spike path: a station list with no database behind it still has
		// to start and stop.
		f := newFactory(t)
		clk := clock.NewFake(time.Unix(1_700_000_000, 0))
		tr := presence.New(clk, mgrGrace)
		m := NewManager(tr, nil, f.make(), clk, mgrGrace, 4)
		tr.Touch(1, "s1")
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if got := m.Running(); !reflect.DeepEqual(got, []int64{1}) {
			t.Fatalf("Running() = %v", got)
		}
		if err := m.StopAll(); err != nil {
			t.Errorf("StopAll = %v", err)
		}
	})

	t.Run("runtime refuses to start", func(t *testing.T) {
		clk := clock.NewFake(time.Unix(1_700_000_000, 0))
		tr := presence.New(clk, mgrGrace)
		broken := func(ctx context.Context, id int64, st store.SelectorState, resume bool) (*Runtime, error) {
			d := runtimeDeps(nil)
			d.StartEncoder = func(context.Context, encode.Config, *slog.Logger) (Encoder, error) {
				return nil, errors.New("ffmpeg is not installed")
			}
			d.SampleRate = mix.SampleRate
			return NewRuntime(d, id, filepath.Join(t.TempDir(), "seg")), nil
		}
		m := NewManager(tr, nil, broken, clk, mgrGrace, 4)
		tr.Touch(1, "s1")
		if err := m.Tick(ctx); err == nil {
			t.Error("a runtime that could not start was reported as running")
		}
		if got := m.Running(); len(got) != 0 {
			t.Errorf("Running() = %v", got)
		}
	})
}
