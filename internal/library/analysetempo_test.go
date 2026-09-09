// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// quietLogger throws the output away; recordingLogger keeps it so a test can
// read what the pass reported.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

// syncBuf is written by the pass and read by the test.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func recordingLogger() (*syncBuf, *slog.Logger) {
	b := &syncBuf{}
	return b, slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// Jockora-ffh. Read from the deployment: 7595 playable tracks, 7595 with a
// loudness value, ZERO with a tempo. The pass had run over the whole library
// and stored not one BPM.
//
// The wiring was not the problem. next() selects on loudness alone, and
// loudness and tempo are measured together -- so a library measured by an
// earlier binary that had no BPM source ends up fully levelled, entirely
// unmeasured for tempo, and INVISIBLE to the pass for ever after.
//
// Every test here is TestAnalyseTempo*, which is the -run pattern for this fix.

// fakeBPM answers with a fixed tempo, or refuses.
type fakeBPM struct {
	bpm   float64
	err   error
	calls int
	saw   []string
}

func (f *fakeBPM) BPM(_ context.Context, path string) (float64, error) {
	f.calls++
	f.saw = append(f.saw, path)
	return f.bpm, f.err
}

// TestAnalyseTempoPicksUpATrackThatOnlyNeedsATempo is the deployment's exact
// state: levelled, and never measured for tempo.
func TestAnalyseTempoPicksUpATrackThatOnlyNeedsATempo(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracks (id, path, playable, loudness_lufs) VALUES (1, '/a.mp3', 1, -16.0)`); err != nil {
		t.Fatal(err)
	}

	a := &Analyser{Store: s}
	got, err := a.next(ctx)
	if err != nil {
		t.Fatalf("next: %v -- a track with loudness and no tempo was invisible", err)
	}
	if got != "/a.mp3" {
		t.Errorf("next = %q, want /a.mp3", got)
	}
}

// TestAnalyseTempoLeavesAFinishedTrackAlone: both halves measured means done,
// or the pass never terminates.
func TestAnalyseTempoLeavesAFinishedTrackAlone(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracks (id, path, playable, loudness_lufs, bpm)
		 VALUES (1, '/a.mp3', 1, -16.0, 128.0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Analyser{Store: s}).next(ctx); !errors.Is(err, errNothingToAnalyse) {
		t.Errorf("next = %v, want nothing left to do", err)
	}
}

// TestAnalyseTempoDoesNotRemeasureLoudness is the cost guard. Loudness is a
// FULL DECODE of the file; re-running it for 7595 tracks to fetch a tempo would
// turn a tempo backfill into a two-day job for a number already on the row.
func TestAnalyseTempoDoesNotRemeasureLoudness(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracks (id, path, playable, loudness_lufs) VALUES (1, '/a.mp3', 1, -16.0)`); err != nil {
		t.Fatal(err)
	}
	f := &fakeBPM{bpm: 128}
	a := &Analyser{Store: s, BPM: f, Log: quietLogger()}
	a.analyseOne(ctx, "/a.mp3")

	if f.calls != 1 {
		t.Errorf("the tempo was asked for %d times, want once", f.calls)
	}
	var lufs float64
	var bpm float64
	if err := s.DB().QueryRowContext(ctx,
		`SELECT loudness_lufs, coalesce(bpm, 0) FROM tracks WHERE path = '/a.mp3'`).
		Scan(&lufs, &bpm); err != nil {
		t.Fatal(err)
	}
	// THE MEASUREMENT THAT WAS ALREADY THERE SURVIVES. /a.mp3 does not exist,
	// so a re-measure would overwrite -16 with the zero a failure stores.
	if lufs != -16.0 {
		t.Errorf("loudness = %v, want the -16 already on the row: it was re-measured", lufs)
	}
	if bpm != 128 {
		t.Errorf("bpm = %v, want 128", bpm)
	}
}

// TestAnalyseTempoDrainsAFileItCannotMeasure: loudness deliberately stores a
// zero on failure so the row stops being NULL and the file is not chosen again.
// Tempo had no equivalent, so the moment selection includes a NULL bpm an
// unmeasurable file would be retried for ever.
func TestAnalyseTempoDrainsAFileItCannotMeasure(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracks (id, path, playable, loudness_lufs) VALUES (1, '/a.mp3', 1, -16.0)`); err != nil {
		t.Fatal(err)
	}
	a := &Analyser{Store: s, BPM: &fakeBPM{err: errors.New("no librosa here")}, Log: quietLogger()}
	a.analyseOne(ctx, "/a.mp3")

	if _, err := a.next(ctx); !errors.Is(err, errNothingToAnalyse) {
		t.Error("a track whose tempo cannot be measured is still queued, so the pass loops on it")
	}
}

// TestAnalyseTempoSaysWhenNoTempoWasMeasured: a bare return discarded every BPM
// error at every level, so 7595 consecutive failures produced no evidence at
// all and "why is bpm blank" could not be answered from the logs.
func TestAnalyseTempoSaysWhenNoTempoWasMeasured(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	for i, p := range []string{"/a.mp3", "/b.mp3"} {
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO tracks (id, path, playable, loudness_lufs) VALUES (?, ?, 1, -16.0)`,
			i+1, p); err != nil {
			t.Fatal(err)
		}
	}
	logs, log := recordingLogger()
	a := &Analyser{Store: s, BPM: &fakeBPM{err: errors.New("no librosa here")}, Log: log}
	if err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := logs.String(); !contains(got, "tempo_failed") {
		t.Errorf("the log never says the tempo failed:\n%s", got)
	}
}

// TestAnalyseTempoWithoutASidecarStillLevels: BPM is optional, and a
// deployment with no speech sidecar must still get its loudness rather than
// panicking on a nil source.
func TestAnalyseTempoWithoutASidecarStillLevels(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracks (id, path, playable, loudness_lufs) VALUES (1, '/a.mp3', 1, -16.0)`); err != nil {
		t.Fatal(err)
	}
	a := &Analyser{Store: s, Log: quietLogger()}
	a.analyseOne(ctx, "/a.mp3")

	// Nothing measured and nothing lost: the loudness already on the row
	// survives, and the tempo stays NULL because nothing could produce one.
	var lufs float64
	var bpm any
	if err := s.DB().QueryRowContext(ctx,
		`SELECT loudness_lufs, bpm FROM tracks WHERE path = '/a.mp3'`).Scan(&lufs, &bpm); err != nil {
		t.Fatal(err)
	}
	if lufs != -16.0 || bpm != nil {
		t.Errorf("lufs = %v, bpm = %v; want the loudness kept and no tempo invented", lufs, bpm)
	}
}

// TestAnalyseTempoStopsWithItsContext: a cancelled pass records nothing.
// Writing a zero on the way out would mark a track as "tried" when it was only
// interrupted, and it would never be measured again.
func TestAnalyseTempoStopsWithItsContext(t *testing.T) {
	s := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO tracks (id, path, playable, loudness_lufs) VALUES (1, '/a.mp3', 1, -16.0)`); err != nil {
		t.Fatal(err)
	}
	cancel()

	a := &Analyser{Store: s, BPM: &fakeBPM{err: context.Canceled}, Log: quietLogger()}
	a.measureTempo(ctx, "/a.mp3")

	var bpm any
	if err := s.DB().QueryRowContext(context.Background(),
		`SELECT bpm FROM tracks WHERE path = '/a.mp3'`).Scan(&bpm); err != nil {
		t.Fatal(err)
	}
	if bpm != nil {
		t.Errorf("bpm = %v; an interrupted pass marked the track as tried", bpm)
	}
	if a.tempoFailed != 0 {
		t.Errorf("tempoFailed = %d; a cancellation is not a measurement failure", a.tempoFailed)
	}
}
