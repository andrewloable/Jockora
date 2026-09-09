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
	// tempoMeasured and tempoFailed are reported when the pass finishes, so a
	// tempo path that is wholly broken says so instead of being silent.
	tempoMeasured int
	tempoFailed   int
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
					"tempo_measured", a.tempoMeasured, "tempo_failed", a.tempoFailed,
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

// next picks the next playable track missing EITHER measurement, PLAYLIST FIRST.
//
// EITHER, not loudness alone. The two are measured together but only loudness
// used to decide whether a track was picked up, so a library measured by a
// binary that had no BPM source ended up fully levelled, entirely unmeasured
// for tempo, and invisible to this query for ever after. Read from the
// deployment: 7595 playable tracks, 7595 with loudness, ZERO with a tempo, and
// nothing left for the pass to select. Jockora-ffh.
//
// Loudness is what stops one record landing 12 dB louder than the last, and an
// unmeasured track plays at its own level by design -- so until a track is
// measured, it is the level jump. Measuring in id order spends that on records
// nobody is going to hear: on the deployed box, 6301 unmeasured tracks at 2.2 a
// minute is 48 hours, while only 55 of them were in any station's playlist.
// Everything audible could be levelled in half an hour.
//
// Prioritising, not filtering: the second ORDER BY term is the old id order, so
// once the playlists are done the analyser walks the rest of the library
// exactly as before. Excluded tracks do not count -- they are in the table but
// will never play.
func (a *Analyser) next(ctx context.Context) (string, error) {
	var path string
	err := a.Store.DB().QueryRowContext(ctx, `
		SELECT t.path FROM tracks t
		 WHERE t.playable = 1 AND (t.loudness_lufs IS NULL OR t.bpm IS NULL)
		 ORDER BY
		   CASE WHEN EXISTS (
		     SELECT 1 FROM station_tracks st
		      WHERE st.track_id = t.id AND st.excluded = 0
		   ) THEN 0 ELSE 1 END,
		   t.id
		 LIMIT 1`).Scan(&path)
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
	// LOUDNESS IS A FULL DECODE. Now that a missing tempo is also a reason to
	// select a track, most of a backfill is tracks that already have their
	// loudness -- and re-measuring 7595 files for a number already on the row
	// would turn a tempo backfill into a two-day job. Worse, it would overwrite
	// a real measurement with the zero a failure stores, for a file that has
	// since gone missing.
	if a.hasLoudness(ctx, path) {
		a.measureTempo(ctx, path)
		return
	}

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

	a.measureTempo(ctx, path)
}

// measureTempo stores a tempo, or a zero saying it was tried.
//
// A ZERO ON FAILURE, for the same reason loudness stores one: the row stops
// being NULL, so next() does not hand the same unmeasurable file back for ever
// now that a missing tempo is a reason to select a track. Zero reads as
// "no tempo" everywhere downstream -- the console renders it blank and the
// station filter never excludes on it.
//
// THE FAILURE IS COUNTED. It used to be a bare return with no log at any level,
// so 7595 consecutive failures produced no evidence at all and "why is the
// tempo blank" could not be answered from the logs. No tempo is still a valid
// answer for one track; it is not a valid answer for a whole library, and the
// difference has to be visible.
func (a *Analyser) measureTempo(ctx context.Context, path string) {
	if a.BPM == nil {
		return
	}
	bpm, err := a.BPM.BPM(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		a.tempoFailed++
		a.log().Debug("no tempo for this track", "path", path, "err", err)
		if err := StoreBPM(ctx, a.Store, path, 0); err != nil {
			a.log().Warn("could not record the tempo failure", "path", path, "err", err)
		}
		return
	}
	a.tempoMeasured++
	if err := StoreBPM(ctx, a.Store, path, bpm); err != nil {
		a.log().Warn("storing bpm", "path", path, "err", err)
	}
}

// hasLoudness reports whether this track has already been levelled.
//
// An unreadable row counts as NOT measured: measuring twice costs time, and
// skipping a measurement that never happened costs the loudness contract.
func (a *Analyser) hasLoudness(ctx context.Context, path string) bool {
	var n int
	err := a.Store.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM tracks WHERE path = ? AND loudness_lufs IS NOT NULL`,
		path).Scan(&n)
	return err == nil && n > 0
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
