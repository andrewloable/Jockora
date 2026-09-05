// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/store"
)

// MarkerFile lets an operator declare a directory gapless and be certain of it.
//
// The escape hatch matters more than the heuristic. Someone who knows an album
// runs continuously should be able to say so without arguing with a detector,
// and it costs nothing to check.
const MarkerFile = ".jockora-gapless"

const (
	// tailSeconds is how much of a track's ending is examined.
	tailSeconds = 3.0
	// windowSeconds is the RMS window within that tail.
	windowSeconds = 0.1
	// silenceDBFS is how quiet the last window must be to count as a fade.
	silenceDBFS = -40.0
	// monotonicFraction is how many window-to-window steps must be
	// non-increasing. Real music under a fade fluctuates, so a strictly
	// monotonic test would reject almost every genuinely fading track.
	monotonicFraction = 0.8
	// minDecayDB is how much quieter the tail must END than it STARTED.
	//
	// Quietness alone is not a fade. Measured without this: a track ending in
	// three seconds of digital silence, and one ending in three seconds of
	// constant quiet, were both flagged as fading -- their RMS windows are equal,
	// and equal windows satisfy a non-increasing test perfectly. Trailing
	// silence is extremely common in real files, so that was a large
	// false-positive class.
	minDecayDB = 20.0
)

// DetectNoCrossfade decides whether a track must be spliced sample-adjacent to
// whatever follows it, with no crossfade at all.
//
// Some albums are meant to run without a gap: live records, DJ mixes, concept
// albums, classical movements. A forced two-to-four second crossfade there is
// worse than the 25ms gap it prevents, and it gets noticed on exactly the albums
// a music person cares about.
//
// Album tags are deliberately not consulted: they do not reliably indicate
// gapless intent.
func DetectNoCrossfade(ctx context.Context, path string, dur, fadeSeconds float64) (bool, error) {
	// The operator's own statement, checked first because it is free and it
	// outranks anything measured.
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), MarkerFile)); err == nil {
		return true, nil
	}

	// A track that would spend most of its length inside the fade.
	if fadeSeconds > 0 && dur > 0 && dur < 2*fadeSeconds {
		return true, nil
	}

	fading, err := endsInAFade(ctx, path)
	if err != nil {
		// The safe default is an ordinary crossfade: wrongly splicing two
		// unrelated tracks sample-adjacent is more jarring than a fade.
		return false, err
	}
	return fading, nil
}

// endsInAFade reports whether a track's last few seconds decay to near-silence.
//
// This catches the common case where the track already fades out and an added
// crossfade digs a level hole in the middle of it.
func endsInAFade(ctx context.Context, path string) (bool, error) {
	frames, err := decodeTail(ctx, path, tailSeconds)
	if err != nil {
		return false, err
	}

	windowFrames := int(windowSeconds * mix.SampleRate)
	if len(frames) < 2*windowFrames {
		return false, nil // too short to say anything about its shape
	}

	var windows []float64
	for i := 0; i+windowFrames <= len(frames); i += windowFrames {
		windows = append(windows, rmsDBFS(frames[i:i+windowFrames]))
	}
	if len(windows) < 3 {
		return false, nil
	}

	last := windows[len(windows)-1]
	if last > silenceDBFS {
		return false, nil
	}

	// It must have come DOWN, not merely be quiet.
	head := windows[0]
	for _, w := range windows[:max(1, len(windows)/3)] {
		if w > head {
			head = w
		}
	}
	if head-last < minDecayDB {
		return false, nil
	}

	nonIncreasing := 0
	for i := 1; i < len(windows); i++ {
		if windows[i] <= windows[i-1] {
			nonIncreasing++
		}
	}
	return float64(nonIncreasing)/float64(len(windows)-1) >= monotonicFraction, nil
}

// decodeTail returns the last `seconds` of a file as canonical-bus frames.
//
// -sseof seeks from the end, so only the tail is decoded rather than the whole
// track: this runs once per track over an entire library.
func decodeTail(ctx context.Context, path string, seconds float64) ([]mix.Frame, error) {
	out, err := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-sseof", "-"+strconv.FormatFloat(seconds, 'f', 3, 64),
		"-i", path,
		"-f", "f32le",
		"-ar", strconv.Itoa(mix.SampleRate),
		"-ac", strconv.Itoa(mix.Channels),
		"-",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("library: reading the end of %s: %w", path, err)
	}

	frames := make([]mix.Frame, len(out)/mix.FrameBytes)
	for i := range frames {
		frames[i] = mix.Frame{
			L: math.Float32frombits(uint32(out[i*8]) | uint32(out[i*8+1])<<8 | uint32(out[i*8+2])<<16 | uint32(out[i*8+3])<<24),
			R: math.Float32frombits(uint32(out[i*8+4]) | uint32(out[i*8+5])<<8 | uint32(out[i*8+6])<<16 | uint32(out[i*8+7])<<24),
		}
	}
	return frames, nil
}

// rmsDBFS returns the RMS of a window in dBFS, floored so silence does not
// produce negative infinity.
func rmsDBFS(frames []mix.Frame) float64 {
	var sum float64
	for _, f := range frames {
		sum += float64(f.L)*float64(f.L) + float64(f.R)*float64(f.R)
	}
	rms := math.Sqrt(sum / float64(2*len(frames)))
	if rms <= 0 {
		return -200
	}
	return 20 * math.Log10(rms)
}

// StoreNoCrossfade records the flag against a track.
func StoreNoCrossfade(ctx context.Context, s *store.Store, path string, flagged bool) error {
	v := 0
	if flagged {
		v = 1
	}
	if _, err := s.DB().ExecContext(ctx,
		`UPDATE tracks SET no_crossfade_next = ? WHERE path = ?`, v, path); err != nil {
		return fmt.Errorf("library: storing gapless flag for %s: %w", path, err)
	}
	return nil
}
