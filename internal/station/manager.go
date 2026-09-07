// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/presence"
	"github.com/andrewloable/jockora/internal/store"
)

// RuntimeFactory builds one station's pipeline, ready to start.
//
// It is given the persisted playlist position so the station resumes where it
// stopped: a station that restarted from the top every time its last listener
// left would open every session with the same few tracks.
type RuntimeFactory func(ctx context.Context, stationID int64, state store.SelectorState, resume bool) (*Runtime, error)

// Manager keeps the running stations in step with who is listening.
//
// RECONCILIATION ON A TICK, not on events. A missed event leaves a station
// running with nobody on it, or never starting at all; a tick that recomputes
// the whole picture cannot drift, and a tick that is skipped is corrected by
// the next one.
type Manager struct {
	tracker *presence.Tracker
	store   *store.Store
	factory RuntimeFactory
	clk     clock.Clock
	cap     int

	// gate serialises break generation across every station. One GPU, one
	// model resident at a time -- 8GB cannot hold a writing model and a voice
	// model together, and two stations generating at once is how that gets
	// discovered at airtime.
	gate chan struct{}

	mu      sync.Mutex
	running map[int64]*Runtime
}

// NewManager builds the reconciler.
//
// grace is presence's own: a session survives that long after its last playlist
// fetch, so a station stays up exactly as long as its last listener is still
// counted. maxStations caps how many run at once, because a per-station ffmpeg
// is the one cost that scales with stations and the target box is small.
func NewManager(t *presence.Tracker, s *store.Store, f RuntimeFactory,
	clk clock.Clock, grace time.Duration, maxStations int) *Manager {
	_ = grace // presence owns the grace period; kept in the signature as the contract
	return &Manager{
		tracker: t, store: s, factory: f, clk: clk, cap: maxStations,
		gate:    make(chan struct{}, 1),
		running: map[int64]*Runtime{},
	}
}

// Tick brings the running stations in line with the listeners.
//
// STOPS BEFORE STARTS, so a station freed by its last listener leaving makes
// room for one that has been waiting behind the cap within the same tick
// rather than the next.
func (m *Manager) Tick(ctx context.Context) error {
	wanted := map[int64]bool{}
	for _, id := range m.tracker.Stations() {
		wanted[id] = true
	}

	var problems []error
	for _, id := range m.Running() {
		if wanted[id] {
			continue
		}
		if err := m.Stop(id); err != nil {
			problems = append(problems, err)
		}
	}

	for _, id := range m.tracker.Stations() {
		m.mu.Lock()
		already, atCap := m.running[id] != nil, len(m.running) >= m.cap
		m.mu.Unlock()
		if already {
			continue
		}
		// Over the cap, the station simply waits. Its listeners hear nothing
		// yet, which is honest; starting a fifth ffmpeg on a box that can run
		// four makes every station stutter instead of one being silent.
		if atCap {
			continue
		}
		if err := m.start(ctx, id); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

func (m *Manager) start(ctx context.Context, id int64) error {
	state, resume, err := m.selectorState(ctx, id)
	if err != nil {
		return err
	}

	rt, err := m.factory(ctx, id, state, resume)
	if err != nil {
		return fmt.Errorf("station %d: building the runtime: %w", id, err)
	}
	if err := rt.Start(ctx); err != nil {
		// NOT RECORDED as running. A failed start that left an entry behind
		// would wedge the station: never running, and never retried because
		// the manager believed it was.
		return err
	}

	m.mu.Lock()
	m.running[id] = rt
	m.mu.Unlock()
	return nil
}

// selectorState reads where the station had got to. A station with no stored
// state has never played, and starts fresh rather than resuming from zero --
// which would give every new station the same running order.
func (m *Manager) selectorState(ctx context.Context, id int64) (store.SelectorState, bool, error) {
	if m.store == nil {
		return store.SelectorState{}, false, nil
	}
	state, ok, err := m.store.GetSelectorState(ctx, id)
	if err != nil {
		return store.SelectorState{}, false, fmt.Errorf("station %d: reading its position: %w", id, err)
	}
	return state, ok, nil
}

// Stop takes a station off air and remembers where it was.
//
// The position is saved on a context that OUTLIVES the caller's: a station
// stopped by the server shutting down must still record its cursor, or every
// restart of the server restarts every station from the top.
func (m *Manager) Stop(id int64) error {
	m.mu.Lock()
	rt := m.running[id]
	delete(m.running, id)
	m.mu.Unlock()
	if rt == nil {
		return nil
	}

	var problems []error
	if sel := rt.Selector(); sel != nil && m.store != nil {
		seed, cursor := sel.State()
		if err := m.store.SetSelectorState(context.Background(), id,
			store.SelectorState{Seed: seed, Cursor: cursor}); err != nil {
			problems = append(problems, fmt.Errorf("station %d: saving its position: %w", id, err))
		}
	}
	if err := rt.Stop(); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

// StopAll takes every station off air, saving each one's position.
func (m *Manager) StopAll() error {
	var problems []error
	for _, id := range m.Running() {
		if err := m.Stop(id); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

// Running lists the stations on air, in order.
func (m *Manager) Running() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]int64, 0, len(m.running))
	for id := range m.running {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Runtime returns one station's pipeline, or nil if it is not on air.
func (m *Manager) Runtime(id int64) *Runtime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[id]
}

// Generate runs one break generation, and only one at a time across every
// station on the box.
//
// The buffer is what makes this free: a break is generated 7 to 17 minutes
// before it airs, so waiting behind another station's turn costs nothing an
// listener can hear. Two at once costs a model reload, which they can.
func (m *Manager) Generate(ctx context.Context, fn func() error) error {
	select {
	case m.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.gate }()
	return fn()
}
