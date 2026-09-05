// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"math"
	"testing"
)

// unityFrames returns frames at gain 1.0, so whatever the ducker writes back is
// the applied gain itself.
func unityFrames(n int) []Frame { return constFrames(n, 1.0) }

func dB(g float32) float64 { return 20 * math.Log10(float64(g)) }

// runDuck processes d milliseconds of unity frames and returns every per-frame
// gain the ducker applied.
func runDuck(d *Ducker, ms int) []float32 {
	buf := unityFrames(ms * SampleRate / 1000)
	d.Process(buf)
	g := make([]float32, len(buf))
	for i := range buf {
		g[i] = buf[i].L
	}
	return g
}

func TestDuckReachesTargetGain(t *testing.T) {
	d := NewDucker()
	d.Engage()

	g := runDuck(d, 200)

	if got := dB(g[len(g)-1]); math.Abs(got-DuckDepthDB) > 0.5 {
		t.Errorf("after 200ms the duck is %.2f dB, want %.1f dB +/- 0.5", got, DuckDepthDB)
	}
}

func TestDuckAttackIsSmooth(t *testing.T) {
	d := NewDucker()
	d.Engage()

	g := runDuck(d, 200)

	worst, at := float32(0), 0
	for i := 1; i < len(g); i++ {
		if step := float32(math.Abs(float64(g[i] - g[i-1]))); step > worst {
			worst, at = step, i
		}
	}
	if worst > 0.05 {
		t.Errorf("gain jumped %.4f in one frame at frame %d, want no step above 0.05 (zipper noise)", worst, at)
	}
}

func TestDuckReleaseReturnsToUnity(t *testing.T) {
	d := NewDucker()
	d.Engage()
	runDuck(d, 200) // settle into the duck first

	d.Release()
	g := runDuck(d, 1000)

	if last := g[len(g)-1]; math.Abs(float64(last)-1.0) > 0.01 {
		t.Errorf("after 1s of release the gain is %.4f, want 1.0 +/- 0.01", last)
	}
}

// TestDuckAttackMostlyCompleteAtStatedTime pins the reading of "attack 80ms":
// it is the time the ramp takes, not a one-pole time constant. Read as a time
// constant, the duck is still 1.9 dB shy of target at 200ms and
// TestDuckReachesTargetGain fails.
func TestDuckAttackMostlyCompleteAtStatedTime(t *testing.T) {
	d := NewDucker()
	d.Engage()

	g := runDuck(d, 80)

	target := float32(math.Pow(10, DuckDepthDB/20))
	remaining := float64(g[len(g)-1]-target) / float64(1-target)
	if remaining > 0.05 {
		t.Errorf("at the stated 80ms attack, %.1f%% of the ramp remains, want under 5%%", remaining*100)
	}
}

// TestDuckHoldsSteadyState guards against a smoother that creeps past its
// target or oscillates once it has arrived.
func TestDuckHoldsSteadyState(t *testing.T) {
	d := NewDucker()
	d.Engage()
	runDuck(d, 200)

	g := runDuck(d, 1000)

	target := float32(math.Pow(10, DuckDepthDB/20))
	for i, v := range g {
		if v < target-0.001 {
			t.Fatalf("frame %d overshot below target: %.5f < %.5f", i, v, target)
		}
	}
	if got := dB(g[len(g)-1]); math.Abs(got-DuckDepthDB) > 0.1 {
		t.Errorf("steady state drifted to %.3f dB, want %.1f dB", got, DuckDepthDB)
	}
}

// TestDuckDoesNotTouchAudioWhenIdle: an un-engaged ducker is transparent.
func TestDuckDoesNotTouchAudioWhenIdle(t *testing.T) {
	d := NewDucker()
	buf := []Frame{{L: 0.5, R: -0.25}, {L: -1, R: 1}}
	want := append([]Frame(nil), buf...)

	d.Process(buf)

	for i := range want {
		if buf[i] != want[i] {
			t.Errorf("idle ducker changed frame %d: %+v, want %+v", i, buf[i], want[i])
		}
	}
}

// TestDuckScalesBothChannelsEqually: a ducker that only touched L would shift
// the stereo image every time the DJ spoke.
func TestDuckScalesBothChannelsEqually(t *testing.T) {
	d := NewDucker()
	d.Engage()

	buf := constFrames(SampleRate/5, 1.0)
	for i := range buf {
		buf[i].R = -1.0
	}
	d.Process(buf)

	for i := range buf {
		if buf[i].L != -buf[i].R {
			t.Fatalf("frame %d: L=%v R=%v, channels scaled differently", i, buf[i].L, buf[i].R)
		}
	}
}

func TestDuckNoAllocation(t *testing.T) {
	d := NewDucker()
	d.Engage()
	buf := unityFrames(4096)

	if got := testing.AllocsPerRun(100, func() { d.Process(buf) }); got != 0 {
		t.Errorf("Process allocated %v times per run, want 0", got)
	}
}

// TestDuckLoudnessContract pins the fixed numbers so nobody quietly retunes
// them: music sits at -16 LUFS and drops to -28 LUFS under speech.
func TestDuckLoudnessContract(t *testing.T) {
	if MusicLUFS != -16 {
		t.Errorf("MusicLUFS = %v, want -16", MusicLUFS)
	}
	if MusicDuckedLUFS != -28 {
		t.Errorf("MusicDuckedLUFS = %v, want -28", MusicDuckedLUFS)
	}
	if SpeechLUFS != -16 {
		t.Errorf("SpeechLUFS = %v, want -16", SpeechLUFS)
	}
	if TruePeakCeilingDBTP != -1 {
		t.Errorf("TruePeakCeilingDBTP = %v, want -1", TruePeakCeilingDBTP)
	}
	if DuckDepthDB != MusicDuckedLUFS-MusicLUFS {
		t.Errorf("DuckDepthDB = %v, want %v (the gap between the two music targets)",
			DuckDepthDB, MusicDuckedLUFS-MusicLUFS)
	}
}
