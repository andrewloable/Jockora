// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package sched decides what goes on the timeline and when, in bus sample
// indices and nothing else.
//
// The protocol has one rule: the scheduler is a single producer appending to a
// queue, and the mixer is the single consumer draining it inside its own loop.
// The scheduler never reads the mixer's clock and never signals it directly.
//
// That is what keeps the mixer a single-owner component. A design where the
// scheduler reacts to the mixer's live position needs to read a clock the mixer
// is concurrently advancing, which is a data race that looks like ordinary
// method calls and is therefore invisible in review.
//
// There is no time.Time in this package. Everything is sample indices.
package sched

import (
	"fmt"
	"sort"
	"sync"
)

// Action is what the mixer should do with an entry.
type Action int

const (
	// ActionSpliceAudio plays a rendered audio file at the given sample.
	ActionSpliceAudio Action = iota
)

// Entry is one scheduled item on the timeline.
type Entry struct {
	// AfterSample is an absolute bus sample index, already compensated for the
	// mixer chain's latency by whoever scheduled it.
	AfterSample int64
	Action      Action
	Path        string // rendered audio on disk
	Placement   string // "ramp" | "outro" | "between"
}

// Queue is the handover point between the scheduler and the mixer. Its zero
// value is ready to use.
type Queue struct {
	mu      sync.Mutex
	entries []Entry
	drained int64 // highest sample index already consumed
}

// Enqueue adds an entry, rejecting anything the mixer has already played past.
// A silently dropped late entry would show up as a break that never aired, with
// nothing in the logs to say why.
func (q *Queue) Enqueue(e Entry) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.drained > 0 && e.AfterSample <= q.drained {
		return fmt.Errorf("sched: entry at sample %d is in the past: the mixer has already drained through %d",
			e.AfterSample, q.drained)
	}

	q.entries = append(q.entries, e)
	sort.SliceStable(q.entries, func(i, j int) bool {
		return q.entries[i].AfterSample < q.entries[j].AfterSample
	})
	return nil
}

// DrainDue removes and returns every entry due at or before now, in ascending
// sample order. An entry is returned exactly once.
func (q *Queue) DrainDue(now int64) []Entry {
	q.mu.Lock()
	defer q.mu.Unlock()

	if now > q.drained {
		q.drained = now
	}

	i := 0
	for i < len(q.entries) && q.entries[i].AfterSample <= now {
		i++
	}
	if i == 0 {
		return nil
	}

	due := make([]Entry, i)
	copy(due, q.entries[:i])
	q.entries = append(q.entries[:0], q.entries[i:]...)
	return due
}

// Pending reports how many entries are queued but not yet due.
func (q *Queue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries)
}
