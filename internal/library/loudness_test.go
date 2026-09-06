// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/andrewloable/jockora/internal/mix"
)

// gainForTest is the mixer's own normalisation gain, used here so the two
// cannot silently disagree about the bus target.
func gainForTest(lufs float64) float32 { return mix.GainFor(lufs) }

// makeTone writes a sine at a given amplitude so loudness is predictable.
func makeTone(t *testing.T, dir, name string, seconds int, volume string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=" + itoa(seconds),
		"-ac", "2",
	}
	if volume != "" {
		args = append(args, "-af", "volume="+volume)
	}
	args = append(args, "-c:a", "pcm_s16le", path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("making %s: %v: %s", name, err, out)
	}
	return path
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

func TestMeasureReturnsPlausibleLUFS(t *testing.T) {
	path := makeTone(t, t.TempDir(), "tone.wav", 5, "")

	got, err := MeasureLUFS(context.Background(), path)
	if err != nil {
		t.Fatalf("MeasureLUFS: %v", err)
	}
	if got == 0 {
		t.Error("loudness is exactly 0 LUFS, which means nothing was parsed")
	}
	if got < -40 || got > 0 {
		t.Errorf("loudness = %v LUFS, outside the plausible -40..0 range", got)
	}
	t.Logf("full-scale tone measured %.2f LUFS", got)
}

func TestMeasureQuietAndLoudDiffer(t *testing.T) {
	dir := t.TempDir()
	loud := makeTone(t, dir, "loud.wav", 5, "")
	quiet := makeTone(t, dir, "quiet.wav", 5, "-12dB")

	l, err := MeasureLUFS(context.Background(), loud)
	if err != nil {
		t.Fatal(err)
	}
	q, err := MeasureLUFS(context.Background(), quiet)
	if err != nil {
		t.Fatal(err)
	}

	diff := l - q
	if math.Abs(diff-12) > 1 {
		t.Errorf("a 12 dB attenuated copy measured %.2f dB quieter (%.2f vs %.2f), want 12 +/- 1",
			diff, l, q)
	}
	t.Logf("loud %.2f LUFS, quiet %.2f LUFS, difference %.2f dB", l, q, diff)
}

// TestMeasureHandlesSilence: loudnorm reports -inf for silence, and storing that
// would make the mixer's gain calculation produce infinity.
func TestMeasureHandlesSilence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "silent.wav")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-t", "5",
		"-c:a", "pcm_s16le", path).CombinedOutput(); err != nil {
		t.Fatalf("making silence: %v: %s", err, out)
	}

	got, err := MeasureLUFS(context.Background(), path)
	if !errors.Is(err, ErrNoLoudness) {
		t.Fatalf("err = %v (value %v), want ErrNoLoudness", err, got)
	}
	if math.IsInf(got, 0) || math.IsNaN(got) {
		t.Errorf("returned %v alongside the error; callers must not see inf or NaN", got)
	}
}

func TestMeasureMissingFile(t *testing.T) {
	if _, err := MeasureLUFS(context.Background(), filepath.Join(t.TempDir(), "nope.wav")); err == nil {
		t.Error("MeasureLUFS returned no error for a missing file")
	}
}

// TestMeasureIsDeterministic: the same file must measure the same twice, or the
// normalisation gain would drift between rescans.
func TestMeasureIsDeterministic(t *testing.T) {
	path := makeTone(t, t.TempDir(), "tone.wav", 5, "-6dB")

	a, err := MeasureLUFS(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := MeasureLUFS(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("two measurements of the same file differ: %v then %v", a, b)
	}
}

// TestMeasureFeedsTheMixerGain closes the loop this task exists for: the number
// stored here is consumed by mix.GainFor, and the two must agree about the bus
// target AND about the clamp.
func TestMeasureFeedsTheMixerGain(t *testing.T) {
	dir := t.TempDir()

	// A track inside the +/-12 dB clamp must land exactly on the bus target.
	near := makeTone(t, dir, "near.wav", 5, "-6dB")
	lufs, err := MeasureLUFS(context.Background(), near)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(lufs-mix.MusicLUFS) > mix.MaxNormalisationDB {
		t.Fatalf("fixture at %.2f LUFS is outside the clamp; this case cannot test the exact path", lufs)
	}
	corrected := lufs + 20*math.Log10(float64(gainForTest(lufs)))
	if math.Abs(corrected-mix.MusicLUFS) > 0.5 {
		t.Errorf("a track at %.2f LUFS lands at %.2f LUFS, want %v +/- 0.5",
			lufs, corrected, mix.MusicLUFS)
	}

	// A track beyond the clamp must be moved 12 dB and no further. The clamp is
	// deliberate: a mis-measured or near-silent track would otherwise demand
	// +30 dB and blow up the mix.
	far := makeTone(t, dir, "far.wav", 5, "-12dB")
	lufs, err = MeasureLUFS(context.Background(), far)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(lufs-mix.MusicLUFS) <= mix.MaxNormalisationDB {
		t.Fatalf("fixture at %.2f LUFS is inside the clamp; this case cannot test it", lufs)
	}
	applied := 20 * math.Log10(float64(gainForTest(lufs)))
	if math.Abs(applied-mix.MaxNormalisationDB) > 0.01 {
		t.Errorf("a track at %.2f LUFS received %.2f dB, want the %v dB clamp",
			lufs, applied, mix.MaxNormalisationDB)
	}
	t.Logf("clamp verified: %.2f LUFS track gets %.2f dB, landing at %.2f LUFS (short of the target by design)",
		lufs, applied, lufs+applied)
}

// TestMeasureReportsDecodedDuration: the number that makes truncation
// detectable at all.
func TestMeasureReportsDecodedDuration(t *testing.T) {
	path := makeTone(t, t.TempDir(), "ten.wav", 10, "")

	m, err := Measure(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.DecodedSeconds-10) > 0.5 {
		t.Errorf("DecodedSeconds = %v, want about 10", m.DecodedSeconds)
	}
	if m.Truncated(10.0) {
		t.Error("an intact file was reported as truncated")
	}
}

// TestMeasureDetectsTruncation is the finding from ep0.3 finally being caught.
// A truncated MP3 is not a decode error, and ffprobe does not notice either:
// the container header still advertises the original length. Only comparing the
// decode against that advertised length reveals it.
func TestMeasureDetectsTruncation(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full.mp3")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=10",
		"-ac", "2", "-b:a", "128k", full).CombinedOutput(); err != nil {
		t.Fatalf("making full.mp3: %v: %s", err, out)
	}

	raw, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	truncPath := filepath.Join(dir, "trunc.mp3")
	if err := os.WriteFile(truncPath, raw[:len(raw)*40/100], 0o644); err != nil {
		t.Fatal(err)
	}

	// The container still claims the full length. This is the trap.
	advertised := probeDuration(t, truncPath)
	if advertised < 9 {
		t.Fatalf("fixture invalid: the truncated file already advertises %.2fs, so there is nothing to detect", advertised)
	}

	m, err := Measure(context.Background(), truncPath)
	if err != nil {
		t.Fatalf("Measure: %v", err)
	}
	t.Logf("container advertises %.2fs, decoder produced %.2fs", advertised, m.DecodedSeconds)

	if m.DecodedSeconds >= advertised*TruncationTolerance {
		t.Errorf("decoded %.2fs against an advertised %.2fs: the truncation was not visible",
			m.DecodedSeconds, advertised)
	}
	if !m.Truncated(advertised) {
		t.Error("Truncated() did not flag a file cut to 40 percent of its length")
	}
}

func TestTruncatedIsFalseWithoutData(t *testing.T) {
	var m Measurement
	if m.Truncated(10) {
		t.Error("an unmeasured track was reported truncated")
	}
	if (Measurement{DecodedSeconds: 5}).Truncated(0) {
		t.Error("a track with no advertised duration was reported truncated")
	}
}

func TestStoreLoudnessWritesTheColumn(t *testing.T) {
	s := openStore(t)
	root := library(t)
	if _, err := Scan(context.Background(), s, root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "a.mp3")

	if err := StoreLoudness(context.Background(), s, path, Measurement{LUFS: -18.25}); err != nil {
		t.Fatal(err)
	}

	var got float64
	if err := s.DB().QueryRow(`SELECT loudness_lufs FROM tracks WHERE path = ?`, path).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != -18.25 {
		t.Errorf("loudness_lufs = %v, want -18.25", got)
	}

	if err := MarkUnplayable(context.Background(), s, path); err != nil {
		t.Fatal(err)
	}
	var playable int
	if err := s.DB().QueryRow(`SELECT playable FROM tracks WHERE path = ?`, path).Scan(&playable); err != nil {
		t.Fatal(err)
	}
	if playable != 0 {
		t.Errorf("playable = %d after MarkUnplayable, want 0", playable)
	}
}

func probeDuration(t *testing.T, path string) float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration", "-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var d float64
	if _, err := fmt.Sscan(string(out), &d); err != nil {
		t.Fatalf("parsing duration %q: %v", out, err)
	}
	return d
}

// TestParseProgressHandlesEveryFfmpegDialect is the regression test for a CI
// failure that could not be reproduced locally.
//
// ffmpeg's -progress block is not the same on every build. Newer ones emit
// out_time_us; CI's does NOT, and emits only out_time_ms and out_time. Matching
// out_time_us alone therefore found nothing, silently fell through to scraping
// stderr, and reported 7.1 seconds for a 10-second file -- which then read as a
// TRUNCATED track, the one thing this number exists to detect.
//
// The out_time_ms trap is the other half: despite its name it carries
// MICROseconds, so anything treating it as milliseconds is wrong by a thousand.
// These cases are fixtures rather than live ffmpeg output precisely so the test
// does not depend on which ffmpeg the machine happens to have.
func TestParseProgressHandlesEveryFfmpegDialect(t *testing.T) {
	for _, tc := range []struct {
		name     string
		progress string
		want     float64
	}{
		{
			name: "modern build with out_time_us",
			progress: "out_time_us=4000000\nout_time_ms=4000000\nout_time=00:00:04.000000\nprogress=continue\n" +
				"out_time_us=10000000\nout_time_ms=10000000\nout_time=00:00:10.000000\nprogress=end\n",
			want: 10,
		},
		{
			// Exactly what CI produced. Before the fix this returned 0.
			name: "CI build with no out_time_us at all",
			progress: "out_time_ms=4000000\nout_time=00:00:04.000000\nprogress=continue\n" +
				"out_time_ms=10000000\nout_time=00:00:10.000000\nprogress=end\n",
			want: 10,
		},
		{
			name:     "only the microsecond field",
			progress: "out_time_us=10000000\nprogress=end\n",
			want:     10,
		},
		{
			name:     "over an hour, so the clock string must be parsed properly",
			progress: "out_time=01:02:03.500000\nprogress=end\n",
			want:     3723.5,
		},
		{
			name:     "nothing usable",
			progress: "bitrate=N/A\nprogress=end\n",
			want:     0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseProgressSeconds(tc.progress); math.Abs(got-tc.want) > 0.001 {
				t.Errorf("parseProgressSeconds = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestParseProgressNeverReadsOutTimeMsAsMilliseconds guards the misnomer
// directly: out_time_ms holds MICROseconds, so a 10-second file must never be
// reported as 10000 seconds or as 0.01.
func TestParseProgressNeverReadsOutTimeMsAsMilliseconds(t *testing.T) {
	got := parseProgressSeconds("out_time_ms=10000000\nout_time=00:00:10.000000\nprogress=end\n")
	if math.Abs(got-10) > 0.001 {
		t.Errorf("parseProgressSeconds = %v, want 10; out_time_ms is microseconds despite its name", got)
	}
}
