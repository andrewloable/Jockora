// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"math"
	"testing"
)

// ceiling is -1 dBTP expressed as linear amplitude.
var ceiling = float32(math.Pow(10, TruePeakCeilingDBTP/20))

// sine returns n frames of a tone at the given amplitude, identical on both
// channels.
func sine(n int, freq, amp float64) []Frame {
	f := make([]Frame, n)
	for i := range f {
		v := float32(amp * math.Sin(2*math.Pi*freq*float64(i)/SampleRate))
		f[i] = Frame{L: v, R: v}
	}
	return f
}

func TestLimiterNeverExceedsCeiling(t *testing.T) {
	l := NewLimiter()
	buf := sine(SampleRate/2, 1000, 2.0) // 6 dB over full scale, way over the ceiling

	l.Process(buf)

	const eps = 1e-6
	worst := float32(0)
	for i, f := range buf {
		for _, v := range []float32{f.L, f.R} {
			if m := float32(math.Abs(float64(v))); m > worst {
				worst = m
			}
			if math.Abs(float64(v)) > float64(ceiling)+eps {
				t.Fatalf("frame %d: |%v| exceeds the -1 dBTP ceiling %v", i, v, ceiling)
			}
		}
	}
	if worst < float32(0.5)*ceiling {
		t.Errorf("peak output %v is far below the ceiling %v: the limiter is over-attenuating", worst, ceiling)
	}
}

func TestLimiterDelayIsReported(t *testing.T) {
	if got := NewLimiter().LatencyFrames(); got != 240 {
		t.Errorf("LatencyFrames() = %d, want 240 (5ms at 48kHz)", got)
	}
}

func TestLimiterTransparentBelowThreshold(t *testing.T) {
	l := NewLimiter()
	lat := l.LatencyFrames()

	in := sine(SampleRate/4, 440, 0.3)
	want := append([]Frame(nil), in...)
	l.Process(in)

	for i := 0; i < lat; i++ {
		if in[i] != (Frame{}) {
			t.Fatalf("frame %d should be the initial latency padding, got %+v", i, in[i])
		}
	}
	for i := lat; i < len(in); i++ {
		if in[i] != want[i-lat] {
			t.Fatalf("frame %d = %+v, want the input delayed by %d frames: %+v",
				i, in[i], lat, want[i-lat])
		}
	}
}

// TestLimiterNoHardClipping distinguishes limiting from clipping. A clipper
// flattens the top of the waveform, leaving runs of samples pinned at exactly
// the ceiling; a limiter rides the gain down and keeps the shape.
func TestLimiterNoHardClipping(t *testing.T) {
	l := NewLimiter()
	buf := sine(SampleRate/2, 1000, 2.0)

	l.Process(buf)

	for i := 1; i < len(buf); i++ {
		a, b := math.Abs(float64(buf[i-1].L)), math.Abs(float64(buf[i].L))
		if a >= float64(ceiling) && b >= float64(ceiling) {
			t.Fatalf("frames %d and %d are both pinned at the ceiling: that is clipping, not limiting", i-1, i)
		}
	}
}

// TestLimiterStereoLinked: independent per-channel gain would swing the stereo
// image every time one side peaked.
func TestLimiterStereoLinked(t *testing.T) {
	l := NewLimiter()

	buf := sine(SampleRate/4, 1000, 2.0)
	for i := range buf {
		buf[i].R = buf[i].L / 4 // R is 12 dB quieter and must stay exactly so
	}
	want := append([]Frame(nil), buf...)
	l.Process(buf)

	for i := l.LatencyFrames(); i < len(buf); i++ {
		if want[i-l.LatencyFrames()].L == 0 {
			continue
		}
		if math.Abs(float64(buf[i].L-buf[i].R*4)) > 1e-6 {
			t.Fatalf("frame %d: L=%v R=%v, the 4:1 channel ratio was not preserved", i, buf[i].L, buf[i].R)
		}
	}
}

// TestLimiterReducesGainBeforeThePeak is what the lookahead is for. A burst
// arriving with no warning must already be under the ceiling on its first
// sample, not after the attack has caught up.
func TestLimiterReducesGainBeforeThePeak(t *testing.T) {
	l := NewLimiter()

	buf := make([]Frame, SampleRate/4)
	copy(buf, sine(SampleRate/10, 440, 0.2)) // quiet lead-in
	loud := sine(len(buf)-SampleRate/10, 1000, 3.0)
	copy(buf[SampleRate/10:], loud)

	l.Process(buf)

	for i, f := range buf {
		if math.Abs(float64(f.L)) > float64(ceiling)+1e-6 {
			t.Fatalf("frame %d overshot at %v: the gain was not reduced before the burst arrived", i, f.L)
		}
	}
}

// TestLimiterReleasesAfterBurst: the limiter must let go, or one loud moment
// leaves the rest of the show quiet.
func TestLimiterReleasesAfterBurst(t *testing.T) {
	l := NewLimiter()

	l.Process(sine(SampleRate/10, 1000, 3.0)) // burst
	quiet := sine(2*SampleRate, 440, 0.3)     // two seconds of quiet
	want := append([]Frame(nil), quiet...)
	l.Process(quiet)

	last := len(quiet) - 1
	if got, exp := quiet[last].L, want[last-l.LatencyFrames()].L; math.Abs(float64(got-exp)) > 1e-4 {
		t.Errorf("two seconds after the burst the output is %v, want the input back at %v", got, exp)
	}
}

func TestLimiterNoAllocation(t *testing.T) {
	l := NewLimiter()
	buf := sine(4096, 1000, 1.5)

	if got := testing.AllocsPerRun(20, func() { l.Process(buf) }); got != 0 {
		t.Errorf("Process allocated %v times per run, want 0", got)
	}
}

// TestLimiterSilenceStaysSilent guards against a gain computation that divides
// by a zero peak and produces NaN or Inf audio.
func TestLimiterSilenceStaysSilent(t *testing.T) {
	l := NewLimiter()
	buf := make([]Frame, SampleRate/4)

	l.Process(buf)

	for i, f := range buf {
		if f != (Frame{}) {
			t.Fatalf("frame %d = %+v, want silence", i, f)
		}
	}
}
