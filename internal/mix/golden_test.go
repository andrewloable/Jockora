// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "regenerate golden fixtures")

// goldenTolerance is loose enough for float math to vary across architectures
// and far tighter than anything audible.
const goldenTolerance = 1e-6

// checkGolden compares got against a stored fixture, or rewrites the fixture
// when -update is passed.
//
// Fixtures are raw f32le, so a disputed diff can be listened to directly:
//
//	ffplay -f f32le -ar 48000 -ac 2 internal/mix/testdata/golden/<name>
func checkGolden(t *testing.T, name string, got []Frame) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)

	if *update {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := WriteF32LE(f, got); err != nil {
			t.Fatal(err)
		}
		t.Logf("regenerated %s (%d frames)", path, len(got))
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%v\nrun: go test ./internal/mix/ -run TestGolden -update", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	want := make([]Frame, int(info.Size())/FrameBytes)
	if _, err := ReadF32LE(f, want); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s: produced %d frames, fixture holds %d", name, len(got), len(want))
	}

	for i := range got {
		dL := math.Abs(float64(got[i].L - want[i].L))
		dR := math.Abs(float64(got[i].R - want[i].R))
		if dL > goldenTolerance || dR > goldenTolerance {
			t.Fatalf("%s: first difference at frame %d (%.3f s)\n"+
				"  got  L=%+.9f R=%+.9f\n"+
				"  want L=%+.9f R=%+.9f\n"+
				"  delta L=%.3e R=%.3e (tolerance %.0e)\n"+
				"If you changed the DSP on purpose, regenerate with -update and say so.\n"+
				"If you did not, you broke it.",
				name, i, FramesToDuration(i).Seconds(),
				got[i].L, got[i].R, want[i].L, want[i].R, dL, dR, goldenTolerance)
		}
	}
}

func TestGoldenCrossfade(t *testing.T) {
	const n = SampleRate

	a := sine(n, 1000, 0.8)
	b := sine(n, 440, 0.6)
	for i := range b {
		b[i].R = -b[i].R // make the two channels differ, so a channel swap shows up
	}

	out := make([]Frame, n)
	Crossfade(out, a, b, 0, n)

	checkGolden(t, "crossfade_1s.f32", out)
}

func TestGoldenDuck(t *testing.T) {
	const n = SampleRate
	const engageAt, releaseAt = 4800, 24000

	buf := sine(n, 1000, 0.7)
	d := NewDucker()

	d.Process(buf[:engageAt])
	d.Engage()
	d.Process(buf[engageAt:releaseAt])
	d.Release()
	d.Process(buf[releaseAt:])

	checkGolden(t, "duck_engage_release.f32", buf)
}

func TestGoldenLimiter(t *testing.T) {
	// Quiet, then a burst, then quiet again. The tail is not padding: without
	// it the fixture never exercises the release path, and a changed release
	// constant slips past the golden entirely. That was measured, not assumed.
	const seg = SampleRate / 4

	buf := make([]Frame, 3*seg)
	copy(buf, sine(seg, 440, 0.2))         // quiet lead-in
	copy(buf[seg:], sine(seg, 1000, 2.5))  // burst well over the ceiling
	copy(buf[2*seg:], sine(seg, 440, 0.2)) // quiet tail, so the gain must recover

	NewLimiter().Process(buf)

	checkGolden(t, "limiter_burst.f32", buf)
}
