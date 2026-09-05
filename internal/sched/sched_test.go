// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package sched

import (
	"bytes"
	"encoding/binary"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnqueueOrdersBySample(t *testing.T) {
	var q Queue
	for _, s := range []int64{5000, 1000, 9000, 3000} {
		if err := q.Enqueue(Entry{AfterSample: s, Action: ActionSpliceAudio, Path: "x.wav"}); err != nil {
			t.Fatalf("Enqueue(%d): %v", s, err)
		}
	}

	got := q.DrainDue(10000)
	if len(got) != 4 {
		t.Fatalf("drained %d entries, want 4", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].AfterSample < got[i-1].AfterSample {
			t.Fatalf("entry %d at sample %d follows %d: not ascending",
				i, got[i].AfterSample, got[i-1].AfterSample)
		}
	}
}

func TestDrainDueOnlyReturnsDue(t *testing.T) {
	var q Queue
	mustEnqueue(t, &q, 1000, 5000)

	got := q.DrainDue(2000)
	if len(got) != 1 || got[0].AfterSample != 1000 {
		t.Fatalf("DrainDue(2000) = %v, want just the entry at 1000", got)
	}

	if again := q.DrainDue(2000); len(again) != 0 {
		t.Errorf("second DrainDue(2000) returned %v, want nothing: entries must not repeat", again)
	}

	if late := q.DrainDue(5000); len(late) != 1 || late[0].AfterSample != 5000 {
		t.Errorf("DrainDue(5000) = %v, want the entry at 5000", late)
	}
}

func TestEnqueueRejectsPast(t *testing.T) {
	var q Queue
	mustEnqueue(t, &q, 1000)
	q.DrainDue(4000)

	err := q.Enqueue(Entry{AfterSample: 3000, Action: ActionSpliceAudio, Path: "x.wav"})
	if err == nil {
		t.Fatal("Enqueue accepted a sample the mixer has already passed")
	}
	if !strings.Contains(err.Error(), "3000") || !strings.Contains(err.Error(), "4000") {
		t.Errorf("error %q should name both the requested and the drained sample", err)
	}
}

// TestEnqueueRejectsPastIsInclusive: the boundary sample has already been
// consumed by the drain that reported it.
func TestEnqueueRejectsPastIsInclusive(t *testing.T) {
	var q Queue
	q.DrainDue(4000)

	if err := q.Enqueue(Entry{AfterSample: 4000, Action: ActionSpliceAudio}); err == nil {
		t.Error("Enqueue accepted the exact sample already drained")
	}
	if err := q.Enqueue(Entry{AfterSample: 4001, Action: ActionSpliceAudio}); err != nil {
		t.Errorf("Enqueue rejected the next sample after the drain: %v", err)
	}
}

func TestQueueZeroValueIsUsable(t *testing.T) {
	var q Queue
	if err := q.Enqueue(Entry{AfterSample: 1, Action: ActionSpliceAudio}); err != nil {
		t.Fatalf("zero-value Queue rejected an enqueue: %v", err)
	}
	if got := q.DrainDue(1); len(got) != 1 {
		t.Errorf("zero-value Queue drained %d entries, want 1", len(got))
	}
}

func TestPendingCountsUndrained(t *testing.T) {
	var q Queue
	mustEnqueue(t, &q, 1000, 5000, 9000)

	if got := q.Pending(); got != 3 {
		t.Errorf("Pending() = %d, want 3", got)
	}
	q.DrainDue(5000)
	if got := q.Pending(); got != 1 {
		t.Errorf("Pending() after draining two = %d, want 1", got)
	}
}

// TestSchedDoesNotImportTheMixer enforces the single-owner rule structurally.
// The scheduler must not be able to reach the mixer's clock even by accident:
// that coupling is a data race the race detector cannot see, because it would
// look like ordinary method calls.
func TestSchedDoesNotImportTheMixer(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}

	// Exact paths only. A suffix match would reject "runtime" for ending in
	// "time", and a test that fails on the wrong thing is worse than no test.
	banned := map[string]bool{
		"time": true,
		"github.com/andrewloable/jockora/internal/mix":   true,
		"github.com/andrewloable/jockora/internal/clock": true,
	}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if banned[path] {
					t.Errorf("%s imports %q: the scheduler works in sample indices only, "+
						"and must never reach the mixer or a clock", filepath.Base(name), path)
				}
			}
		}
	}
}

func TestFakeSilenceBreakWritesAPlayableWAV(t *testing.T) {
	dir := t.TempDir()

	path, err := FakeSilenceBreak(0.5, dir)
	if err != nil {
		t.Fatalf("FakeSilenceBreak: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	const rate, channels, bits = 48000, 2, 16
	dataBytes := int(0.5 * rate * channels * bits / 8)

	// Assert the header against literal expected bytes rather than by reading
	// it back with our own writer, which would prove nothing.
	if len(raw) != 44+dataBytes {
		t.Fatalf("file is %d bytes, want 44 header + %d data", len(raw), dataBytes)
	}
	if got := string(raw[0:4]); got != "RIFF" {
		t.Errorf("bytes 0-4 = %q, want RIFF", got)
	}
	if got := string(raw[8:12]); got != "WAVE" {
		t.Errorf("bytes 8-12 = %q, want WAVE", got)
	}
	if got := string(raw[12:16]); got != "fmt " {
		t.Errorf(`bytes 12-16 = %q, want "fmt "`, got)
	}
	if got := string(raw[36:40]); got != "data" {
		t.Errorf("bytes 36-40 = %q, want data", got)
	}

	le := binary.LittleEndian
	for _, c := range []struct {
		name string
		got  uint32
		want uint32
	}{
		{"RIFF chunk size", le.Uint32(raw[4:8]), uint32(36 + dataBytes)},
		{"fmt chunk size", le.Uint32(raw[16:20]), 16},
		{"sample rate", le.Uint32(raw[24:28]), rate},
		{"byte rate", le.Uint32(raw[28:32]), rate * channels * bits / 8},
		{"data size", le.Uint32(raw[40:44]), uint32(dataBytes)},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	for _, c := range []struct {
		name string
		got  uint16
		want uint16
	}{
		{"audio format", le.Uint16(raw[20:22]), 1}, // 1 = PCM
		{"channels", le.Uint16(raw[22:24]), channels},
		{"block align", le.Uint16(raw[32:34]), channels * bits / 8},
		{"bits per sample", le.Uint16(raw[34:36]), bits},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	if !bytes.Equal(raw[44:], make([]byte, dataBytes)) {
		t.Error("the payload is not pure silence")
	}
}

func TestFakeSilenceBreakRejectsBadDuration(t *testing.T) {
	for _, d := range []float64{0, -1} {
		if _, err := FakeSilenceBreak(d, t.TempDir()); err == nil {
			t.Errorf("FakeSilenceBreak(%v) returned no error", d)
		}
	}
}

func mustEnqueue(t *testing.T, q *Queue, samples ...int64) {
	t.Helper()
	for _, s := range samples {
		if err := q.Enqueue(Entry{AfterSample: s, Action: ActionSpliceAudio, Path: "x.wav"}); err != nil {
			t.Fatalf("Enqueue(%d): %v", s, err)
		}
	}
}
