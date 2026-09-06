// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/store"
)

// ErrNoLoudness means the file has no measurable loudness: silence, or a
// measurement that came back as -inf or NaN.
var ErrNoLoudness = errors.New("library: no measurable loudness")

// loudnormSummary is the JSON block ffmpeg's loudnorm filter prints.
type loudnormSummary struct {
	InputI      string `json:"input_i"`
	InputTP     string `json:"input_tp"`
	InputLRA    string `json:"input_lra"`
	InputThresh string `json:"input_thresh"`
}

// Measurement is what one decode pass tells us about a track.
type Measurement struct {
	// LUFS is the integrated loudness.
	LUFS float64
	// DecodedSeconds is how much audio actually came out of the decoder, which
	// is NOT the same as the duration the container claims.
	DecodedSeconds float64
}

// Truncated reports whether a track decodes materially shorter than its
// container advertises.
//
// This is the only place the check is affordable. A truncated file is not a
// decode error: ffmpeg returns what exists and exits 0. Worse, ffprobe does not
// notice either -- measured on a 40%-truncated 10s MP3, ffprobe still reported
// duration=10.000000 because the Xing/LAME header declares the original length,
// while the decoder produced 4.00s. So tracks.duration_s written by the scanner
// is WRONG for these files, and nothing downstream can tell.
//
// The tolerance is generous because container durations are legitimately
// approximate for VBR files.
func (m Measurement) Truncated(containerSeconds float64) bool {
	if containerSeconds <= 0 || m.DecodedSeconds <= 0 {
		return false
	}
	return m.DecodedSeconds < containerSeconds*TruncationTolerance
}

// TruncationTolerance is the fraction of the advertised duration a track must
// decode to before it is considered intact.
const TruncationTolerance = 0.95

// MeasureLUFS returns a track's integrated loudness, in LUFS.
//
// This is a scan-time cost, paid once per track, so that playback stays cheap.
// Without it every crossfade between differently-mastered tracks carries an
// audible level jump, and the 12 dB duck floats relative to whatever happens to
// be playing rather than meaning a fixed thing.
//
// ReplayGain tags in the files are deliberately ignored: they are inconsistent
// across a real library and frequently absent.
func MeasureLUFS(ctx context.Context, path string) (float64, error) {
	m, err := Measure(ctx, path)
	return m.LUFS, err
}

// Measure decodes a track once and reports both its loudness and how much audio
// actually came out.
//
// One pass, because decoding the whole library is the expensive part of
// onboarding and doing it twice would double it.
func Measure(ctx context.Context, path string) (Measurement, error) {
	var out Measurement

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-hide_banner",
		"-i", path,
		"-af", "loudnorm=I=-16:print_format=json",
		// Structured progress on STDOUT, which ends with an exact final block.
		// The alternative -- scraping the periodic "time=" lines ffmpeg writes
		// to stderr and taking the last one -- reads whatever the last update
		// happened to say, and on CI's ffmpeg that was 7.1 seconds for a
		// 10-second file. This is the number truncation detection depends on,
		// so it must not be whatever a log line last mentioned.
		"-progress", "pipe:1",
		"-f", "null", "-",
	)

	// loudnorm prints its summary to STDERR, not stdout. Reading stdout gets
	// nothing at all and looks like a parse failure.
	var stderr, stdout strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return out, fmt.Errorf("library: measuring %s: %w: %s", path, err, lastLines(stderr.String(), 3))
	}

	diag := stderr.String()
	out.DecodedSeconds = parseProgressSeconds(stdout.String())
	if out.DecodedSeconds == 0 {
		// An ffmpeg too old for -progress. Fall back to the log scrape rather
		// than reporting zero, which Truncated would read as "unknown".
		out.DecodedSeconds = parseDecodedSeconds(diag)
	}

	summary, err := extractJSON(diag)
	if err != nil {
		return out, fmt.Errorf("library: measuring %s: %w", path, err)
	}

	var s loudnormSummary
	if err := json.Unmarshal([]byte(summary), &s); err != nil {
		return out, fmt.Errorf("library: measuring %s: %w", path, err)
	}

	v, err := strconv.ParseFloat(strings.TrimSpace(s.InputI), 64)
	if err != nil {
		// loudnorm writes "-inf" for silence, which does not parse as a float.
		return out, fmt.Errorf("%w: %s reported input_i=%q", ErrNoLoudness, path, s.InputI)
	}
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return out, fmt.Errorf("%w: %s measured %v", ErrNoLoudness, path, v)
	}
	// A track measured below the practical floor is silence with a little noise;
	// normalising it would apply the full clamped boost to a hiss.
	if v < -70 {
		return out, fmt.Errorf("%w: %s measured %.1f LUFS, below the practical floor", ErrNoLoudness, path, v)
	}

	out.LUFS = v
	return out, nil
}

// timeRE matches ffmpeg's progress stamp, e.g. "time=00:00:04.00".
var timeRE = regexp.MustCompile(`time=(\d+):(\d\d):(\d\d(?:\.\d+)?)`)

// parseDecodedSeconds reads how much audio ffmpeg actually processed from the
// LAST progress stamp in its diagnostics. Zero means it could not be found,
// which callers treat as "unknown" rather than "empty".
// progressTimeRE matches the microsecond field of an ffmpeg -progress block.
var progressTimeRE = regexp.MustCompile(`out_time_us=(\d+)`)

// parseProgressSeconds reads the LAST out_time_us from a -progress stream.
//
// The last block is emitted with progress=end when ffmpeg finishes, so it is
// the true total rather than whatever the periodic updates last reached.
func parseProgressSeconds(progress string) float64 {
	all := progressTimeRE.FindAllStringSubmatch(progress, -1)
	if len(all) == 0 {
		return 0
	}
	us, err := strconv.ParseFloat(all[len(all)-1][1], 64)
	if err != nil {
		return 0
	}
	return us / 1e6
}

// parseDecodedSeconds scrapes ffmpeg's periodic stderr progress.
//
// Kept only as a fallback for an ffmpeg without -progress. It reports whatever
// the last periodic update said, which is not necessarily the end of the file.
func parseDecodedSeconds(diag string) float64 {
	all := timeRE.FindAllStringSubmatch(diag, -1)
	if len(all) == 0 {
		return 0
	}
	m := all[len(all)-1]
	h, _ := strconv.ParseFloat(m[1], 64)
	min, _ := strconv.ParseFloat(m[2], 64)
	sec, _ := strconv.ParseFloat(m[3], 64)
	return h*3600 + min*60 + sec
}

// StoreLoudness records a measurement against a track.
func StoreLoudness(ctx context.Context, s *store.Store, path string, m Measurement) error {
	_, err := s.DB().ExecContext(ctx,
		`UPDATE tracks SET loudness_lufs = ? WHERE path = ?`, m.LUFS, path)
	if err != nil {
		return fmt.Errorf("library: storing loudness for %s: %w", path, err)
	}
	return nil
}

// MarkUnplayable records that a track cannot be trusted, with the reason left to
// the caller's log. Used for truncated files, which decode silently short.
func MarkUnplayable(ctx context.Context, s *store.Store, path string) error {
	_, err := s.DB().ExecContext(ctx, `UPDATE tracks SET playable = 0 WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("library: marking %s unplayable: %w", path, err)
	}
	return nil
}

// extractJSON pulls the last {...} block out of ffmpeg's stderr. The filter's
// summary is printed among other diagnostics, so the braces are located rather
// than the whole stream being parsed.
func extractJSON(s string) (string, error) {
	start := strings.LastIndex(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return "", fmt.Errorf("no loudnorm summary in ffmpeg output: %s", lastLines(s, 3))
	}
	return s[start : end+1], nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// NormalisationGain is the linear gain that brings a measured track to the bus
// target. It is the mixer's own function, re-exported here so the scan side and
// the playback side cannot drift apart on what the target is.
func NormalisationGain(lufs float64) float32 { return mix.GainFor(lufs) }
