// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package library reads the music library and records what it finds.
//
// The library is READ-ONLY. Jockora layers on top of Navidrome and an
// OpenSubsonic server; it must never become a competing writer, so nothing here
// opens a file for writing, renames, retags or moves anything under the root.
package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/andrewloable/jockora/internal/errs"
	"github.com/andrewloable/jockora/internal/store"
)

// audioExtensions are the containers this program ingests.
var audioExtensions = map[string]bool{
	".mp3": true, ".flac": true, ".m4a": true,
	".ogg": true, ".opus": true, ".wav": true,
}

// IsAudio reports whether a path is one of the containers this program ingests.
func IsAudio(path string) bool {
	return audioExtensions[strings.ToLower(filepath.Ext(path))]
}

// Stats summarises a scan.
type Stats struct {
	Found      int // audio files seen
	Added      int // new rows
	Updated    int // existing rows refreshed
	Skipped    int // unchanged since the last scan, not re-probed
	Unplayable int // files ffprobe could not read
	Marked     int // rows flagged missing because the walk did not see them

	// Rejected carries one wrapped errs.ErrUnsupportedFormat per unplayable
	// file, so a caller can report WHICH files were refused rather than only
	// how many. A count alone sends an operator hunting through ten thousand
	// files for the four that failed.
	Rejected []error
}

// probeResult is the subset of ffprobe's JSON this scanner reads.
type probeResult struct {
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
	} `json:"streams"`
}

// Scan walks root and records every audio file in the tracks table.
//
// A file that will not probe is recorded with playable=0 rather than skipped or
// aborted on: a library of ten thousand tracks always contains a few damaged
// ones, and losing the whole scan to one of them is not acceptable.
// ScanProgress is called as a scan proceeds, so a long one can say so.
//
// A library of ten thousand files takes MINUTES because every one is probed
// with ffprobe, and a scan that prints nothing is indistinguishable from a
// hang -- which is exactly how it was read the first time a real library was
// pointed at it.
type ScanProgress func(found int, path string)

// Scan reads a music library into the database.
func Scan(ctx context.Context, s *store.Store, root string) (Stats, error) {
	return ScanWithProgress(ctx, s, root, nil)
}

// ScanWithProgress is Scan, reporting as it goes.
func ScanWithProgress(ctx context.Context, s *store.Store, root string, progress ScanProgress) (Stats, error) {
	return scanFolder(ctx, s, root, 0, time.Now().Unix(), progress)
}

// scanFolder is ScanWithProgress with the source the rows belong to. A zero id
// records NULL, which is what a v0.1 database and a bare `jockora scan` leave
// behind, and what ScanSources later adopts.
func scanFolder(ctx context.Context, s *store.Store, root string, sourceID, stamp int64, progress ScanProgress) (Stats, error) {
	var stats Stats

	fi, err := os.Stat(root)
	if err != nil {
		return stats, fmt.Errorf("library: %w", err)
	}
	if !fi.IsDir() {
		return stats, fmt.Errorf("library: %s is not a directory", root)
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory must not end the scan.
			return nil //nolint:nilerr // deliberately tolerant
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() || !audioExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}

		stats.Found++
		if progress != nil {
			progress(stats.Found, path)
		}

		// Unchanged files are not re-probed. ffprobe is the entire cost of a
		// scan, and a library is overwhelmingly the same library it was last
		// time. An os.Stat is already paid for by the walk.
		if info, statErr := d.Info(); statErr == nil {
			unchanged, err := unchangedSince(ctx, s, path, info.Size(), info.ModTime().Unix())
			if err != nil {
				return err
			}
			if unchanged {
				stats.Skipped++
				// Seen, even though not read. Skipping without recording it
				// would make every unchanged track look gone.
				return touch(ctx, s, path, stamp)
			}
		}

		meta, probeErr := probe(ctx, path)
		if probeErr != nil {
			// Detected HERE, at scan time, and never at play time. A scan is
			// allowed to take an hour; a stream is not allowed to discover
			// mid-transition that the next track was a video file somebody
			// dropped into the folder.
			stats.Unplayable++
			probeErr = fmt.Errorf("%w: %s: %v", errs.ErrUnsupportedFormat, path, probeErr)
			stats.Rejected = append(stats.Rejected, probeErr)
		}

		// A missing tag falls back to the FILENAME, and only to the filename.
		// Real tags always win: this fills gaps, it never overrides. 519 of
		// this library's 7,595 playable tracks carry neither an artist nor a
		// title, and each one was reaching the DJ as an empty dossier.
		if meta.artist == "" || meta.title == "" {
			fnArtist, fnTitle := NamesFromPath(path)
			if meta.artist == "" {
				meta.artist = fnArtist
			}
			if meta.title == "" {
				meta.title = fnTitle
			}
		}

		var size, modified int64
		if info, statErr := d.Info(); statErr == nil {
			size, modified = info.Size(), info.ModTime().Unix()
		}
		added, upsertErr := upsert(ctx, s, path, meta, probeErr == nil, size, modified, sourceID, stamp)
		if upsertErr != nil {
			return upsertErr
		}
		if added {
			stats.Added++
		} else {
			stats.Updated++
		}
		return nil
	})
	if err != nil {
		return stats, err
	}
	return stats, nil
}

// metadata is what the scanner records about a file.
type metadata struct {
	artist, title, album string
	// genre is the file's OWN tag, kept as a hint about enrichment order and
	// never as a statement about what a station contains -- that comes from
	// the dossier. See migration 8.
	genre    string
	year     int
	duration float64
}

// probe reads tags and duration with ffprobe.
//
// ffprobe rather than a Go tag parser: it handles every container correctly, and
// a per-format parsing matrix would never be complete. Tag keys vary in case
// between formats, so every key is lowercased before lookup.
func probe(ctx context.Context, path string) (metadata, error) {
	var m metadata

	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "quiet", "-print_format", "json",
		"-show_format", "-show_streams", path,
	).Output()
	if err != nil {
		return m, fmt.Errorf("library: probing %s: %w", path, err)
	}

	var r probeResult
	if err := json.Unmarshal(out, &r); err != nil {
		return m, fmt.Errorf("library: probing %s: %w", path, err)
	}

	hasAudio := false
	for _, st := range r.Streams {
		if st.CodecType == "audio" {
			hasAudio = true
			break
		}
	}
	if !hasAudio {
		return m, fmt.Errorf("library: %s has no audio stream", path)
	}

	tags := make(map[string]string, len(r.Format.Tags))
	for k, v := range r.Format.Tags {
		tags[strings.ToLower(k)] = v
	}

	m.artist = firstOf(tags, "artist", "album_artist", "albumartist", "performer")
	m.title = firstOf(tags, "title")
	m.album = firstOf(tags, "album")
	m.genre = firstOf(tags, "genre")
	m.year = parseYear(firstOf(tags, "date", "year", "originalyear", "originaldate"))
	m.duration, _ = strconv.ParseFloat(r.Format.Duration, 64)

	return m, nil
}

// upsert writes the row, reporting whether it was new.
//
// Only the columns the scanner owns are updated. Loudness, ramp, outro, bpm and
// the gapless flag cost hours of enrichment to produce and a rescan must never
// discard them.
// unchangedSince reports whether this exact file was already scanned.
//
// Size AND mtime, never one alone: a retag usually preserves the size, and a
// file copied off a backup usually preserves neither. Both matching is not a
// proof -- nothing short of hashing every byte is -- but it is the same bet
// every incremental build tool makes, and the failure mode is a stale tag until
// the file is touched, not a corrupt library.
//
// A row with a NULL size or mtime was written before those columns existed, so
// it re-probes once and is never asked again.
func unchangedSince(ctx context.Context, s *store.Store, path string, size, modified int64) (bool, error) {
	var storedSize, storedMod sql.NullInt64
	var genre sql.NullString
	err := s.DB().QueryRowContext(ctx,
		`SELECT size_bytes, modified_at, genre FROM tracks WHERE path = ?`, path).
		Scan(&storedSize, &storedMod, &genre)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("library: checking %s: %w", path, err)
	}
	if !storedSize.Valid || !storedMod.Valid {
		return false, nil
	}
	// A ROW MISSING DATA A NEWER SCHEMA WANTS IS NOT UNCHANGED, whatever its
	// size and mtime say. Migration 8 added tracks.genre, and every existing
	// row has it NULL: without this the skip means an established library never
	// fills the column in, the migration is a no-op in practice, and enrichment
	// priority silently does nothing. Costs one re-probe per track, once.
	if !genre.Valid {
		return false, nil
	}
	return storedSize.Int64 == size && storedMod.Int64 == modified, nil
}

func upsert(ctx context.Context, s *store.Store, path string, m metadata, playable bool, size, modified, sourceID, stamp int64) (bool, error) {
	playableInt := 0
	if playable {
		playableInt = 1
	}

	// Ask whether the row exists rather than inferring it from RowsAffected:
	// SQLite reports 1 for both the insert and the update arm of an upsert, so
	// the counters would report every rescan as a library full of new tracks.
	// One extra indexed lookup is nothing beside the ffprobe call above it.
	var existing int
	err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM tracks WHERE path = ?`, path).Scan(&existing)
	if err != nil {
		return false, fmt.Errorf("library: checking %s: %w", path, err)
	}

	_, err = s.DB().ExecContext(ctx, `
		INSERT INTO tracks (path, artist, title, album, genre, year, duration_s, playable, scanned_at, size_bytes, modified_at, source_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			artist      = excluded.artist,
			title       = excluded.title,
			album       = excluded.album,
			genre       = excluded.genre,
			year        = excluded.year,
			duration_s  = excluded.duration_s,
			playable    = excluded.playable,
			scanned_at  = excluded.scanned_at,
			size_bytes  = excluded.size_bytes,
			modified_at = excluded.modified_at,
			-- coalesce, never a plain assignment: a bare 'jockora scan' passes
			-- no source, and overwriting would UNCLAIM every row it touched.
			source_id   = coalesce(excluded.source_id, tracks.source_id),
			-- Present again. A row that is being written is by definition not
			-- missing, so the mark clears itself with no separate pass.
			missing_at  = NULL`,
		path, m.artist, m.title, m.album, m.genre, m.year, m.duration, playableInt, stamp,
		size, modified, nullableID(sourceID))
	if err != nil {
		return false, fmt.Errorf("library: recording %s: %w", path, err)
	}

	return existing == 0, nil
}

func firstOf(tags map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(tags[k]); v != "" {
			return v
		}
	}
	return ""
}

// parseYear pulls a year out of the many date shapes tags carry: "1987",
// "1987-06-15", "1987/06" and so on.
func parseYear(s string) int {
	if len(s) < 4 {
		return 0
	}
	y, err := strconv.Atoi(s[:4])
	if err != nil || y < 1000 || y > 3000 {
		return 0
	}
	return y
}

// nullableID records an unset source as NULL rather than as source 0, which is
// not a source and would fail the foreign key.
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
