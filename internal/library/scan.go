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
	"encoding/json"
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
	Unplayable int // files ffprobe could not read

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

		added, upsertErr := upsert(ctx, s, path, meta, probeErr == nil)
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
	year                 int
	duration             float64
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
	m.year = parseYear(firstOf(tags, "date", "year", "originalyear", "originaldate"))
	m.duration, _ = strconv.ParseFloat(r.Format.Duration, 64)

	return m, nil
}

// upsert writes the row, reporting whether it was new.
//
// Only the columns the scanner owns are updated. Loudness, ramp, outro, bpm and
// the gapless flag cost hours of enrichment to produce and a rescan must never
// discard them.
func upsert(ctx context.Context, s *store.Store, path string, m metadata, playable bool) (bool, error) {
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
		INSERT INTO tracks (path, artist, title, album, year, duration_s, playable, scanned_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			artist     = excluded.artist,
			title      = excluded.title,
			album      = excluded.album,
			year       = excluded.year,
			duration_s = excluded.duration_s,
			playable   = excluded.playable,
			scanned_at = excluded.scanned_at`,
		path, m.artist, m.title, m.album, m.year, m.duration, playableInt, time.Now().Unix())
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
