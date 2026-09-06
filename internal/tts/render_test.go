// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package tts

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/mix"
)

func renderFake(t *testing.T, text string) (string, float64) {
	t.Helper()
	s := startFake(t)
	waitFor(t, "healthy", 10*time.Second, s.Healthy)
	out := filepath.Join(t.TempDir(), "break.wav")
	dur, err := Render(context.Background(), s, text, "", out)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out, dur
}

// wavHeader reads rate, channels and bit depth straight out of the file rather
// than through mix.ReadWAV, which would accept only 48 kHz and so could never
// report that the resample was skipped.
func wavHeader(t *testing.T, path string) (rate uint32, channels, bits int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	le := binary.LittleEndian
	for off := 12; off+8 <= len(raw); {
		size := int(le.Uint32(raw[off+4 : off+8]))
		if string(raw[off:off+4]) == "fmt " {
			return le.Uint32(raw[off+12 : off+16]), int(le.Uint16(raw[off+10 : off+12])), int(le.Uint16(raw[off+22 : off+24]))
		}
		off += 8 + size + size%2
	}
	t.Fatalf("%s has no fmt chunk", path)
	return
}

var (
	shortTermRE      = regexp.MustCompile(`S:\s*(-?[\d.]+)`)
	integratedTestRE = regexp.MustCompile(`I:\s+(-?[\d.]+)\s+LUFS`)
)

// speechLoudness measures the rendered break with ffmpeg's ebur128, an
// EXTERNAL implementation. Measuring our own output with our own meter would
// only prove we are self-consistent, which is worth nothing.
//
// Short-term where short-term exists, integrated where it does not. Short-term
// is a THREE SECOND window and a real break is often shorter -- "Stay where
// you are." is 1.3 seconds -- and for those ebur128 reports its -120.7 floor,
// not a quiet clip. The first version of this test read that floor as a
// measurement and would have reported a correctly normalised clip as 104 dB
// too quiet.
func speechLoudness(t *testing.T, path string) (float64, string) {
	t.Helper()
	// -v verbose is required: framelog=verbose asks the filter to log per-frame
	// values, but ffmpeg drops them unless the log level admits them, and
	// without them only the gated summary comes back.
	out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-v", "verbose", "-i", path,
		"-af", "ebur128=framelog=verbose", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ebur128: %v\n%s", err, out)
	}

	loudest := math.Inf(-1)
	for _, m := range shortTermRE.FindAllStringSubmatch(string(out), -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err == nil && v > loudest {
			loudest = v
		}
	}
	// Anything at or below the absolute gate is "no window yet", not a level.
	if loudest > -70 {
		return loudest, "short-term"
	}

	m := integratedTestRE.FindAllStringSubmatch(string(out), -1)
	if len(m) == 0 {
		t.Fatalf("ebur128 reported neither short-term nor integrated loudness for %s:\n%s", path, out)
	}
	v, err := strconv.ParseFloat(m[len(m)-1][1], 64)
	if err != nil {
		t.Fatalf("unreadable integrated loudness %q", m[len(m)-1][1])
	}
	if v < -70 {
		t.Fatalf("%s measured %.1f LUFS: silence", path, v)
	}
	return v, "integrated"
}

func TestRenderOutputIsBusFormat(t *testing.T) {
	out, _ := renderFake(t, "the bus is 48 kilohertz stereo and nothing else")
	rate, channels, bits := wavHeader(t, out)
	if rate != mix.SampleRate {
		t.Errorf("sample rate %d, want %d: 24 kHz on a 48 kHz bus plays at half speed", rate, mix.SampleRate)
	}
	if channels != mix.Channels {
		t.Errorf("channels %d, want %d", channels, mix.Channels)
	}
	if bits != 16 {
		t.Errorf("bit depth %d, want 16 (mix.ReadWAV accepts nothing else)", bits)
	}
}

func TestRenderOutputIsNormalised(t *testing.T) {
	out, _ := renderFake(t, "speech and music share one loudness target")
	got, measure := speechLoudness(t, out)
	if math.Abs(got-mix.SpeechLUFS) > 1.0 {
		t.Errorf("%s loudness %.2f LUFS, want %.1f +/- 1.0: the fixed %.0f dB duck is only meaningful against a fixed speech level",
			measure, got, mix.SpeechLUFS, -mix.DuckDepthDB)
	}
}

func TestRenderNoClipping(t *testing.T) {
	out, _ := renderFake(t, "nothing may cross the ceiling")
	frames, err := mix.ReadWAV(out)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	ceiling := float32(math.Pow(10, mix.TruePeakCeilingDBTP/20))
	for i, f := range frames {
		if abs32(f.L) > ceiling || abs32(f.R) > ceiling {
			t.Fatalf("frame %d = (%.4f, %.4f) exceeds the %.0f dBTP ceiling %.4f", i, f.L, f.R, mix.TruePeakCeilingDBTP, ceiling)
		}
	}
}

func TestRenderMonoIsDuplicatedNotPanned(t *testing.T) {
	out, _ := renderFake(t, "one voice in the middle of the room")
	frames, err := mix.ReadWAV(out)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	for i, f := range frames {
		if f.L != f.R {
			t.Fatalf("frame %d = (%.6f, %.6f): mono speech must be duplicated to both channels, not placed in the field", i, f.L, f.R)
		}
	}
}

func TestRenderReturnsDurationSeconds(t *testing.T) {
	out, dur := renderFake(t, "how long was that")
	frames, err := mix.ReadWAV(out)
	if err != nil {
		t.Fatalf("ReadWAV: %v", err)
	}
	want := float64(len(frames)) / mix.SampleRate
	if math.Abs(dur-want) > 0.010 {
		t.Errorf("reported duration %.4fs, file holds %.4fs: the scheduler places breaks on this number", dur, want)
	}
	// And it must be in the right ballpark: the fake speaks five seconds.
	if dur < 4.0 || dur > 6.0 {
		t.Errorf("duration %.2fs for a 5s utterance: the resample changed the length", dur)
	}
}

func TestRenderPropagatesSidecarFailure(t *testing.T) {
	s := startFake(t)
	waitFor(t, "healthy", 10*time.Second, s.Healthy)
	_ = s.Close()
	out := filepath.Join(t.TempDir(), "break.wav")
	if _, err := Render(context.Background(), s, "anything", "", out); err == nil {
		t.Fatal("Render succeeded with a dead sidecar")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("Render left a file behind after failing; the scheduler would air it")
	}
}

func abs32(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}
