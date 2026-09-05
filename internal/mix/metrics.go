// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"sort"
	"sync"
	"time"
)

// Metrics records what a long unattended run has to be judged on: how evenly the
// mixer fed the encoder, and how close the ring came to running dry.
//
// The tail is what matters, not the average. A run whose mean inter-write gap is
// perfect can still have stalled for two seconds, and the average hides it
// completely, so the percentile and the maximum are both kept.
//
// A nil *Metrics is valid everywhere and costs nothing.
type Metrics struct {
	mu           sync.Mutex
	gaps         []time.Duration
	writes       int64
	maxGap       time.Duration
	minOccupancy time.Duration
	haveOcc      bool
}

// maxGapSamples bounds memory on a very long run. Thirty minutes at the default
// block size is about 21000 writes, and a 24-hour soak is about a million, so
// the cap only bites on the soak test, where the tail is already established.
const maxGapSamples = 250000

// MetricsSnapshot is a consistent read of the counters.
type MetricsSnapshot struct {
	Writes       int64
	P99Gap       time.Duration
	MaxGap       time.Duration
	MinOccupancy time.Duration
}

func (m *Metrics) recordWrite(gap time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.writes++
	if gap > m.maxGap {
		m.maxGap = gap
	}
	if len(m.gaps) < maxGapSamples {
		m.gaps = append(m.gaps, gap)
	}
}

func (m *Metrics) recordOccupancy(d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.haveOcc || d < m.minOccupancy {
		m.minOccupancy, m.haveOcc = d, true
	}
}

// Snapshot reports the run so far.
func (m *Metrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	s := MetricsSnapshot{
		Writes:       m.writes,
		MaxGap:       m.maxGap,
		MinOccupancy: m.minOccupancy,
	}
	if len(m.gaps) > 0 {
		sorted := append([]time.Duration(nil), m.gaps...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		idx := (len(sorted)*99 + 99) / 100 // ceil(0.99*n), 1-based
		if idx > len(sorted) {
			idx = len(sorted)
		}
		s.P99Gap = sorted[idx-1]
	}
	return s
}
