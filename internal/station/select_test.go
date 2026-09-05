// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/andrewloable/jockora/internal/store"
)

func ids(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}

func drawN(t *testing.T, s *Selector, n int) []int64 {
	t.Helper()
	out := make([]int64, n)
	for i := range out {
		v, err := s.Next()
		if err != nil {
			t.Fatalf("Next() at draw %d: %v", i, err)
		}
		out[i] = v
	}
	return out
}

func TestNoRepeatBeforeExhaustion(t *testing.T) {
	s := NewSelector(ids(50), 1)

	got := drawN(t, s, 50)

	seen := map[int64]int{}
	for i, v := range got {
		if prev, ok := seen[v]; ok {
			t.Fatalf("track %d drawn twice, at %d and %d, before the pool was exhausted", v, prev, i)
		}
		seen[v] = i
	}
	if len(seen) != 50 {
		t.Errorf("%d distinct tracks in 50 draws, want 50", len(seen))
	}
}

// TestReshuffleAvoidsBoundaryRepeat: without the guard, the last track of one
// pass can be the first of the next, which is the one repeat a listener
// definitely notices.
func TestReshuffleAvoidsBoundaryRepeat(t *testing.T) {
	// Over many seeds, because with a pool of five the collision only happens
	// on some of them and a single seed would pass by luck.
	for seed := int64(0); seed < 200; seed++ {
		s := NewSelector(ids(5), seed)
		got := drawN(t, s, 20)
		for i := 1; i < len(got); i++ {
			if got[i] == got[i-1] {
				t.Fatalf("seed %d: track %d repeated back-to-back at draws %d and %d: %v",
					seed, got[i], i-1, i, got)
			}
		}
	}
}

func TestDeterministicWithSeed(t *testing.T) {
	a := drawN(t, NewSelector(ids(30), 42), 60)
	b := drawN(t, NewSelector(ids(30), 42), 60)

	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("draw %d differs between two selectors with the same seed: %d vs %d", i, a[i], b[i])
		}
	}

	c := drawN(t, NewSelector(ids(30), 43), 60)
	same := true
	for i := range a {
		if a[i] != c[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("two different seeds produced the same order")
	}
}

func TestEmptyPool(t *testing.T) {
	s := NewSelector(nil, 1)
	if _, err := s.Next(); err == nil {
		t.Error("Next() on an empty pool returned no error")
	}
}

func TestSinglePool(t *testing.T) {
	s := NewSelector([]int64{7}, 1)

	// One track can only repeat. It must still work rather than loop forever
	// trying to avoid itself.
	for i := 0; i < 5; i++ {
		v, err := s.Next()
		if err != nil {
			t.Fatalf("draw %d: %v", i, err)
		}
		if v != 7 {
			t.Fatalf("draw %d = %d, want 7", i, v)
		}
	}
}

func TestTwoPoolAlternates(t *testing.T) {
	s := NewSelector([]int64{1, 2}, 5)
	got := drawN(t, s, 10)
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Fatalf("a two-track pool repeated back-to-back at %d: %v", i, got)
		}
	}
}

// TestEveryTrackAppearsEqually: shuffle-without-replacement should give every
// track the same number of plays over whole cycles. Random-with-replacement,
// the thing this exists to beat, would not.
func TestEveryTrackAppearsEqually(t *testing.T) {
	const pool, cycles = 20, 50
	s := NewSelector(ids(pool), 7)

	counts := map[int64]int{}
	for _, v := range drawN(t, s, pool*cycles) {
		counts[v]++
	}
	for id, n := range counts {
		if n != cycles {
			t.Errorf("track %d played %d times over %d cycles, want %d", id, n, cycles, cycles)
		}
	}
	if len(counts) != pool {
		t.Errorf("%d distinct tracks played, want %d", len(counts), pool)
	}
}

// --- database-backed pool ---

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestExcludesUnplayable(t *testing.T) {
	s := openStore(t)
	for i := 1; i <= 50; i++ {
		if _, err := s.DB().Exec(`INSERT INTO tracks (id, path) VALUES (?, ?)`,
			i, filepath.Join("/music", itoa(i)+".mp3")); err != nil {
			t.Fatal(err)
		}
	}
	excluded := []int64{3, 17, 42}
	for _, id := range excluded {
		if _, err := s.DB().Exec(`UPDATE tracks SET playable = 0 WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := PlayablePool(context.Background(), s)
	if err != nil {
		t.Fatalf("PlayablePool: %v", err)
	}
	if len(pool) != 47 {
		t.Fatalf("pool has %d tracks, want 47", len(pool))
	}

	sel := NewSelector(pool, 1)
	got := drawN(t, sel, 47)

	banned := map[int64]bool{}
	for _, id := range excluded {
		banned[id] = true
	}
	seen := map[int64]bool{}
	for _, v := range got {
		if banned[v] {
			t.Errorf("unplayable track %d was drawn", v)
		}
		if seen[v] {
			t.Errorf("track %d drawn twice in one cycle", v)
		}
		seen[v] = true
	}
	if len(seen) != 47 {
		t.Errorf("%d distinct tracks in 47 draws, want 47", len(seen))
	}

	// The 48th draw starts a new cycle rather than failing.
	if _, err := sel.Next(); err != nil {
		t.Errorf("the 48th draw failed instead of reshuffling: %v", err)
	}
}

func TestPlayablePoolIsOrdered(t *testing.T) {
	s := openStore(t)
	for _, id := range []int{5, 2, 9} {
		if _, err := s.DB().Exec(`INSERT INTO tracks (id, path) VALUES (?, ?)`,
			id, filepath.Join("/music", itoa(id)+".mp3")); err != nil {
			t.Fatal(err)
		}
	}

	pool, err := PlayablePool(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	// A stable order matters: with the same seed, the same library must produce
	// the same sequence, and row order from SQLite is not guaranteed otherwise.
	for i := 1; i < len(pool); i++ {
		if pool[i] < pool[i-1] {
			t.Errorf("pool is not ordered: %v", pool)
			break
		}
	}
}

func TestPlayablePoolEmptyLibrary(t *testing.T) {
	s := openStore(t)
	pool, err := PlayablePool(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 0 {
		t.Errorf("pool has %d tracks from an empty library", len(pool))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
