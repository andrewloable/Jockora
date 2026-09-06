// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/store"
)

// ErrAlreadyRunning means another enrichment worker holds the lock.
var ErrAlreadyRunning = errors.New("enrich: another enrichment run is already in progress")

// StaleLockAfter is how long a lock may go without a heartbeat before another
// worker may take it. A worker killed with -9 leaves its lock behind, and
// without stealing, enrichment would never restart.
const StaleLockAfter = 60 * time.Second

// heartbeatInterval must be comfortably shorter than StaleLockAfter, or a
// healthy worker's own lock would be stolen out from under it.
const heartbeatInterval = 15 * time.Second

// LyricsSource supplies transient lyric text for one track.
//
// The text is passed to the model and then dropped. It is never returned into
// anything that gets stored: Dossier has no field for it, and the Ramp type
// that IS stored is structurally incapable of holding it.
type LyricsSource interface {
	FetchLyrics(ctx context.Context, artist, title string, duration float64) (string, Ramp, error)
}

// ArtistSource supplies artist facts.
type ArtistSource interface {
	ArtistFacts(ctx context.Context, name string) (ArtistFacts, error)
}

// Queue enriches a whole library in the background.
//
// It processes ONE track at a time and commits each dossier before starting the
// next. That is what makes kill -9 safe: at most one track of work is lost, and
// this is the most expensive work in the system.
//
// It is deliberately serial. The GPU holds one model at a time and MusicBrainz
// permits one request per second, so concurrency buys nothing and costs the
// quota.
type Queue struct {
	Store   *store.Store
	LLM     Completer
	Artists ArtistSource
	Lyrics  LyricsSource
	// Onset derives a ramp from AUDIO when synced lyrics cannot. Optional: nil
	// means LRC or nothing. Measured coverage on the real library is 41.5%, so
	// nil leaves most tracks with no ramp at all.
	Onset OnsetSource
	Log   *slog.Logger
	Clock clock.Clock

	// Owner identifies this worker in the lock row.
	Owner string

	mu           sync.Mutex
	done, total  int
	lastActivity time.Time
}

// Progress reports how far the current run has got.
func (q *Queue) Progress() (done, total int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.done, q.total
}

func (q *Queue) logger() *slog.Logger {
	if q.Log == nil {
		return slog.Default()
	}
	return q.Log
}

func (q *Queue) clk() clock.Clock {
	if q.Clock == nil {
		return clock.Real{}
	}
	return q.Clock
}

func (q *Queue) owner() string {
	if q.Owner != "" {
		return q.Owner
	}
	host, _ := os.Hostname()
	return fmt.Sprintf("%s/%d", host, os.Getpid())
}

// Run enriches every track that has no dossier yet, then returns.
func (q *Queue) Run(ctx context.Context) error {
	if err := q.acquireLock(ctx); err != nil {
		return err
	}
	defer q.releaseLock()

	if err := q.refreshTotals(ctx); err != nil {
		return err
	}

	lastBeat := q.clk().Now()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		id, path, artist, title, duration, err := q.nextTrack(ctx)
		if errors.Is(err, errNoWork) {
			return nil
		}
		if err != nil {
			return err
		}

		if err := q.enrichOne(ctx, id, path, artist, title, duration); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// One bad track must not end a ten-thousand-track run. enrichOne
			// already stored a dossier row, so it will not be retried forever.
			q.logger().Warn("track enrichment failed", "path", path, "err", err)
		}

		q.mu.Lock()
		q.done++
		q.mu.Unlock()

		if now := q.clk().Now(); now.Sub(lastBeat) >= heartbeatInterval {
			q.beat(ctx)
			lastBeat = now
		}
	}
}

var errNoWork = errors.New("enrich: nothing left to do")

// nextTrack picks the lowest-numbered playable track with no dossier.
func (q *Queue) nextTrack(ctx context.Context) (id int64, path, artist, title string, duration float64, err error) {
	row := q.Store.DB().QueryRowContext(ctx, `
		SELECT id, path, COALESCE(artist,''), COALESCE(title,''), COALESCE(duration_s,0)
		FROM tracks
		WHERE playable = 1 AND id NOT IN (SELECT track_id FROM dossiers)
		ORDER BY id
		LIMIT 1`)

	switch err := row.Scan(&id, &path, &artist, &title, &duration); {
	case err == nil:
		return id, path, artist, title, duration, nil
	case errors.Is(err, sql.ErrNoRows):
		return 0, "", "", "", 0, errNoWork
	default:
		return 0, "", "", "", 0, fmt.Errorf("enrich: selecting work: %w", err)
	}
}

// enrichOne runs the whole pipeline for one track and commits its dossier.
//
// A dossier row is written even when everything fails, because a missing row is
// indistinguishable from unfinished work and the queue would pick the same
// track forever.
func (q *Queue) enrichOne(ctx context.Context, id int64, path, artist, title string, duration float64) error {
	in := TrackInput{Artist: artist, Title: title, DurationS: duration}

	if q.Artists != nil && artist != "" {
		facts, err := q.Artists.ArtistFacts(ctx, artist)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			q.logger().Debug("artist lookup failed", "artist", artist, "err", err)
		}
		in.ArtistFacts = facts
	}

	var ramp Ramp
	if q.Lyrics != nil && artist != "" && title != "" {
		// The text is used and dropped. Only the ramp is persisted.
		text, r, err := q.Lyrics.FetchLyrics(ctx, artist, title, duration)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			q.logger().Debug("lyrics lookup failed", "title", title, "err", err)
		} else {
			in.Lyrics = text
			ramp = r
		}
	}
	in.HasSyncedLyrics = ramp.Confidence == ConfidenceLRC

	// Audio analysis rescues what LRCLIB could not answer. That is most of the
	// library -- coverage measured 41.5% -- and it INCLUDES the untagged tracks
	// skipped above, which can never have an LRC entry and so are exactly the
	// ones only analysis can place a break on.
	ramp, rerr := ResolveRamp(ctx, ramp, q.Onset, path, duration)
	if rerr != nil {
		return rerr
	}
	// An empty confidence means nothing looked: neither source was configured,
	// or the lookup failed. Storing that would put the track in the coverage
	// denominator as a track LRCLIB does not know, which is a different claim.
	if ramp.Confidence != "" {
		if err := StoreRamp(ctx, q.Store, path, ramp); err != nil {
			return err
		}
	}

	d, err := GenerateDossier(ctx, q.LLM, in)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Store an empty dossier so this track is not attempted forever, then
		// report why.
		if storeErr := StoreDossier(ctx, q.Store, id, emptyDossier()); storeErr != nil {
			return storeErr
		}
		return err
	}

	return StoreDossier(ctx, q.Store, id, d)
}

// refreshTotals counts the work for Progress().
func (q *Queue) refreshTotals(ctx context.Context) error {
	var pending, have int
	if err := q.Store.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM tracks WHERE playable = 1`).Scan(&pending); err != nil {
		return fmt.Errorf("enrich: counting tracks: %w", err)
	}
	if err := q.Store.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM dossiers`).Scan(&have); err != nil {
		return fmt.Errorf("enrich: counting dossiers: %w", err)
	}

	q.mu.Lock()
	q.total = pending
	q.done = have
	q.mu.Unlock()
	return nil
}

// acquireLock takes the single-instance lock, stealing a stale one.
func (q *Queue) acquireLock(ctx context.Context) error {
	now := q.clk().Now().Unix()
	cutoff := now - int64(StaleLockAfter.Seconds())

	res, err := q.Store.DB().ExecContext(ctx, `
		INSERT INTO enrich_lock (id, owner, heartbeat) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET owner = excluded.owner, heartbeat = excluded.heartbeat
		WHERE enrich_lock.heartbeat < ?`,
		q.owner(), now, cutoff)
	if err != nil {
		return fmt.Errorf("enrich: acquiring lock: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		var owner string
		_ = q.Store.DB().QueryRowContext(ctx, `SELECT owner FROM enrich_lock WHERE id = 1`).Scan(&owner)
		return fmt.Errorf("%w: held by %s", ErrAlreadyRunning, owner)
	}
	return nil
}

// beat refreshes the lock so a long run is not mistaken for a dead one.
func (q *Queue) beat(ctx context.Context) {
	if _, err := q.Store.DB().ExecContext(ctx,
		`UPDATE enrich_lock SET heartbeat = ? WHERE id = 1 AND owner = ?`,
		q.clk().Now().Unix(), q.owner()); err != nil {
		q.logger().Warn("lock heartbeat failed", "err", err)
	}
}

// releaseLock drops the lock so the next run starts immediately rather than
// waiting out the stale timeout.
func (q *Queue) releaseLock() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := q.Store.DB().ExecContext(ctx,
		`DELETE FROM enrich_lock WHERE id = 1 AND owner = ?`, q.owner()); err != nil {
		q.logger().Warn("releasing lock", "err", err)
	}
}
