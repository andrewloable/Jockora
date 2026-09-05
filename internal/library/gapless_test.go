// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/andrewloable/jockora/internal/mix"
)

// makeFading writes a tone that fades to silence over the last fadeOut seconds.
func makeFading(t *testing.T, dir, name string, total, fadeOut int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	start := total - fadeOut
	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=" + itoa(total),
		"-af", "afade=t=out:st=" + itoa(start) + ":d=" + itoa(fadeOut),
		"-ac", "2", "-c:a", "pcm_s16le", path,
	}
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("making %s: %v: %s", name, err, out)
	}
	return path
}

func TestGaplessShortTrackFlagged(t *testing.T) {
	dir := t.TempDir()
	path := makeTone(t, dir, "short.wav", 3, "")

	// A three-second track with a four-second crossfade would spend its entire
	// length inside the fade.
	got, err := DetectNoCrossfade(context.Background(), path, 3.0, 4.0)
	if err != nil {
		t.Fatalf("DetectNoCrossfade: %v", err)
	}
	if !got {
		t.Error("a 3s track with a 4s fade was not flagged")
	}
}

func TestGaplessBoundaryOfTheLengthRule(t *testing.T) {
	dir := t.TempDir()
	path := makeTone(t, dir, "t.wav", 10, "")

	// Exactly 2x the fade is long enough; anything under is not.
	if got, err := DetectNoCrossfade(context.Background(), path, 4.0, 2.0); err != nil {
		t.Fatal(err)
	} else if got {
		t.Error("a track of exactly 2x the fade was flagged")
	}
	if got, err := DetectNoCrossfade(context.Background(), path, 3.9, 2.0); err != nil {
		t.Fatal(err)
	} else if !got {
		t.Error("a track just under 2x the fade was not flagged")
	}
}

func TestGaplessExistingFadeOutFlagged(t *testing.T) {
	dir := t.TempDir()
	path := makeFading(t, dir, "fading.wav", 12, 5)

	got, err := DetectNoCrossfade(context.Background(), path, 12.0, 2.0)
	if err != nil {
		t.Fatalf("DetectNoCrossfade: %v", err)
	}
	if !got {
		t.Error("a track that already fades to silence was not flagged; " +
			"crossfading it produces a level hole")
	}
}

func TestGaplessNormalTrackNotFlagged(t *testing.T) {
	dir := t.TempDir()
	path := makeTone(t, dir, "normal.wav", 20, "")

	got, err := DetectNoCrossfade(context.Background(), path, 20.0, 2.0)
	if err != nil {
		t.Fatalf("DetectNoCrossfade: %v", err)
	}
	if got {
		t.Error("an ordinary track with a hard ending was flagged")
	}
}

// TestGaplessMarkerFileOverrides is the escape hatch, and it matters more than
// the heuristic: an operator who knows an album is gapless can say so and be
// certain, without arguing with a detector.
func TestGaplessMarkerFileOverrides(t *testing.T) {
	dir := t.TempDir()
	path := makeTone(t, dir, "normal.wav", 20, "")

	// Without the marker this track is not flagged.
	if got, _ := DetectNoCrossfade(context.Background(), path, 20.0, 2.0); got {
		t.Fatal("fixture invalid: the track is flagged even without the marker")
	}

	if err := os.WriteFile(filepath.Join(dir, MarkerFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DetectNoCrossfade(context.Background(), path, 20.0, 2.0)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Errorf("the %s marker did not flag a track in its directory", MarkerFile)
	}
}

// TestGaplessMarkerIsCheapest: the marker must short-circuit before any decode,
// because an operator's explicit statement should not cost a decode per track.
func TestGaplessMarkerIsCheapest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, MarkerFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "does-not-exist.wav")

	got, err := DetectNoCrossfade(context.Background(), missing, 200.0, 2.0)
	if err != nil {
		t.Fatalf("the marker did not short-circuit: %v", err)
	}
	if !got {
		t.Error("a track in a marked directory was not flagged")
	}
}

func TestGaplessUnreadableFileIsNotFlagged(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.wav")

	got, err := DetectNoCrossfade(context.Background(), missing, 200.0, 2.0)
	if err == nil {
		t.Error("DetectNoCrossfade returned no error for a missing file")
	}
	if got {
		t.Error("an unreadable file was flagged; the safe default is an ordinary crossfade")
	}
}

// TestGaplessFeedsTheMixer closes the loop: the flag this task writes is
// consumed by mix.TransitionFor, and a flagged track must produce a zero fade.
func TestGaplessFeedsTheMixer(t *testing.T) {
	dir := t.TempDir()
	path := makeFading(t, dir, "fading.wav", 12, 5)

	flagged, err := DetectNoCrossfade(context.Background(), path, 12.0, 2.0)
	if err != nil {
		t.Fatal(err)
	}

	tr := mix.TransitionFor(flagged, 2*mix.SampleRate)
	if !tr.Adjacent() || tr.FadeFrames != 0 {
		t.Errorf("a flagged track produced FadeFrames=%d, want a sample-adjacent splice", tr.FadeFrames)
	}
}

func TestStoreNoCrossfadeWritesTheColumn(t *testing.T) {
	s := openStore(t)
	root := library(t)
	if _, err := Scan(context.Background(), s, root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "a.mp3")

	if err := StoreNoCrossfade(context.Background(), s, path, true); err != nil {
		t.Fatal(err)
	}

	var flag int
	if err := s.DB().QueryRow(`SELECT no_crossfade_next FROM tracks WHERE path = ?`, path).Scan(&flag); err != nil {
		t.Fatal(err)
	}
	if flag != 1 {
		t.Errorf("no_crossfade_next = %d, want 1", flag)
	}
}

// TestGaplessQuietEndingIsNotAFade covers the false-positive class that the
// specified rule alone would have hit. Quietness is not decay: a track ending in
// trailing silence, or in constant quiet, has EQUAL RMS windows, and equal
// windows satisfy a non-increasing test perfectly. Trailing silence is very
// common in real files, so this would have flagged a large slice of a library.
func TestGaplessQuietEndingIsNotAFade(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name   string
		filter string
	}{
		{"trailing digital silence", "volume=enable='gte(t,7)':volume=0"},
		{"trailing constant quiet", "volume=enable='gte(t,7)':volume=0.004"},
	}
	for _, c := range cases {
		path := filepath.Join(dir, c.name+".wav")
		args := []string{
			"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=12",
			"-af", c.filter, "-ac", "2", "-c:a", "pcm_s16le", path,
		}
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("making %s: %v: %s", c.name, err, out)
		}

		got, err := DetectNoCrossfade(context.Background(), path, 12.0, 2.0)
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Errorf("%s was flagged as a fade; it never decays, it is just quiet", c.name)
		}
	}
}

// TestGaplessLongFadeFlagged: a fade longer than the examined tail must still be
// recognised from the part of it that is visible.
func TestGaplessLongFadeFlagged(t *testing.T) {
	dir := t.TempDir()
	path := makeFading(t, dir, "long.wav", 12, 6)

	got, err := DetectNoCrossfade(context.Background(), path, 12.0, 2.0)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("a six-second fade was not recognised")
	}
}
