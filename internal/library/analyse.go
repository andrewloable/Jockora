// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/andrewloable/jockora/internal/store"
)

// Analyser measures what the SCANNER deliberately does not.
//
// The scan reads tags with ffprobe and stops there, because opening every file
// to decode it is what turns a scan into an hour. Loudness needs a full decode
// and tempo needs a real analysis, so both belong in a background pass that
// runs while the station is already playing -- exactly like enrichment.
//
// WITHOUT THIS PASS NOTHING WAS EVER MEASURED. Measured on the development
// library: 0 of 7,595 playable tracks had a loudness value, so GainFor returned
// unity for every one of them and the loudness contract -- music at -16 LUFS --
// was enforced for speech and not for music. A quiet album followed a loud one
// at whatever level it was mastered at. library.Measure and StoreLoudness had
// existed since the spine and had no callers at all.
type Analyser struct {
	Store *store.Store
	// BPM is optional. Without it loudness is still measured, because loudness
	// is the audible half and tempo only reorders a queue.
	BPM BPMSource
	Log *slog.Logger
}

// BPMSource estimates a track's tempo. Implemented by the speech sidecar, which
// already exists and already holds librosa.
type BPMSource interface {
	BPM(ctx context.Context, path string) (float64, error)
}

func (a *Analyser) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

var errNothingToAnalyse = errors.New("library: every track is analysed")

// Run measures tracks until there are none left or the context ends.
//
// One track at a time and never in parallel: a decode is I/O and CPU heavy and
// this shares a machine with a station that must not stutter. It is slow on
// purpose.
func (a *Analyser) Run(ctx context.Context) error {
	measured := 0
	started := time.Now()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, err := a.next(ctx)
		if errors.Is(err, errNothingToAnalyse) {
			if measured > 0 {
				a.log().Info("analysis complete", "tracks", measured,
					"elapsed", time.Since(started).Round(time.Second))
			}
			return nil
		}
		if err != nil {
			return err
		}
		a.analyseOne(ctx, path)
		measured++
		if measured%50 == 0 {
			a.log().Info("analysing", "tracks", measured,
				"elapsed", time.Since(started).Round(time.Second))
		}
	}
}

// next picks the lowest-numbered playable track with no loudness yet.
func (a *Analyser) next(ctx context.Context) (string, error) {
	var path string
	err := a.Store.DB().QueryRowContext(ctx, `
		SELECT path FROM tracks
		 WHERE playable = 1 AND loudness_lufs IS NULL
		 ORDER BY id LIMIT 1`).Scan(&path)
	switch {
	case err == nil:
		return path, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", errNothingToAnalyse
	default:
		return "", fmt.Errorf("library: selecting a track to analyse: %w", err)
	}
}

// analyseOne measures one track and stores whatever it learned.
//
// A failure is never fatal and never retried forever: loudness is written even
// when the value is poor, because a row that stays NULL is picked again on the
// next pass and the analyser would loop on the same broken file for ever.
func (a *Analyser) analyseOne(ctx context.Context, path string) {
	m, err := Measure(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		a.log().Warn("loudness measurement failed", "path", path, "err", err)
		// Stored as zero, which GainFor reads as unity. The point is that the
		// row stops being NULL so this file is not chosen again next time.
		if err := StoreLoudness(ctx, a.Store, path, Measurement{}); err != nil {
			a.log().Warn("could not record the failure", "path", path, "err", err)
		}
		return
	}
	if err := StoreLoudness(ctx, a.Store, path, m); err != nil {
		a.log().Warn("storing loudness", "path", path, "err", err)
		return
	}

	if a.BPM == nil {
		return
	}
	bpm, err := a.BPM.BPM(ctx, path)
	if err != nil {
		// No tempo is a valid answer. The selector widens its window until
		// something fits, so an unmeasured track is still playable.
		return
	}
	if err := StoreBPM(ctx, a.Store, path, bpm); err != nil {
		a.log().Warn("storing bpm", "path", path, "err", err)
	}
}

// StoreBPM records a track's tempo.
func StoreBPM(ctx context.Context, s *store.Store, path string, bpm float64) error {
	_, err := s.DB().ExecContext(ctx,
		`UPDATE tracks SET bpm = ? WHERE path = ?`, bpm, path)
	if err != nil {
		return fmt.Errorf("library: storing bpm for %s: %w", path, err)
	}
	return nil
}
