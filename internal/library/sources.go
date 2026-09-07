// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/andrewloable/jockora/internal/store"
)

// locatorPrefix namespaces remote tracks.
//
// A subsonic row USED to store the stream URL as its path, and that URL carries
// the auth token: a credential in every one of thousands of rows, and a path
// that changes the moment the password does -- which would re-add the entire
// library under new paths and leave the old rows behind, dossiers and all. A
// locator is stable, carries no secret, and cannot collide with a file path.
const locatorPrefix = "subsonic:"

// SubsonicLocator is how a remote song is addressed in the tracks table.
func SubsonicLocator(sourceID int64, songID string) string {
	return fmt.Sprintf("%s%d:%s", locatorPrefix, sourceID, songID)
}

// ScanSources brings the library up to date from every ENABLED source, one at
// a time.
//
// SEQUENTIAL, deliberately: ffprobe is the whole cost of a scan and there is
// one disk, so running two sources at once makes both slower.
//
// ONE FAILING SOURCE DOES NOT ABORT THE REST. A Navidrome that is down must not
// stop a folder scan -- the operator is told which source failed, and the music
// they do have still plays. The error is joined, not returned early.
func ScanSources(ctx context.Context, s *store.Store, progress ScanProgress) (Stats, error) {
	rows, err := s.ListEnabledSources(ctx)
	if err != nil {
		return Stats{}, err
	}

	var total Stats
	var problems []error
	for _, row := range rows {
		// Adoption first, so rows that already exist are claimed before the
		// walk can add them again under a new path.
		if err := adopt(ctx, s, row); err != nil {
			problems = append(problems, err)
			continue
		}
		// Chosen BEFORE the walk: every row it sees gets this stamp, so
		// anything still carrying an older one was not seen. Per source, so a
		// slow source cannot make the next one look stale.
		stamp, err := scanStamp(ctx, s, row.ID)
		if err != nil {
			problems = append(problems, err)
			continue
		}

		stats, err := sourceFor(row, stamp).Scan(ctx, s, progress)
		total.merge(stats)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s %s: %w", row.Kind, row.Locator, err))
			// NOT MARKED. A walk that failed has seen part of the library, and
			// marking now would take the rest off the air.
			continue
		}

		marked, err := MarkMissing(ctx, s, row.ID, stamp)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		total.Marked += marked
	}
	return total, errors.Join(problems...)
}

// sourceFor builds the scanner for a row. No error and no default case: the
// schema's CHECK constraint closes the set of kinds, so an unknown one cannot
// reach here without the database having been edited by hand.
func sourceFor(row store.Source, stamp int64) Source {
	if row.Kind == store.SourceSubsonic {
		return Subsonic{
			BaseURL: row.Locator, User: row.Username, Password: row.Password,
			SourceID: row.ID, Stamp: stamp,
		}
	}
	return Folder{Root: row.Locator, SourceID: row.ID, Stamp: stamp}
}

// adopt claims the rows a source already owns but has never been stamped with.
//
// This is the upgrade path for a v0.1 database, where every row has a NULL
// source_id. Re-adding them instead would produce a duplicate with no dossier
// beside an original that still has one -- and a dossier is minutes of model
// time each, thousands of times over.
func adopt(ctx context.Context, s *store.Store, row store.Source) error {
	if row.Kind == store.SourceSubsonic {
		return adoptSubsonic(ctx, s, row)
	}
	// Prefix match on the folder root. substr rather than LIKE so a root
	// containing % or _ is not read as a pattern.
	prefix := strings.TrimSuffix(filepath.Clean(row.Locator), string(filepath.Separator)) +
		string(filepath.Separator)
	_, err := s.DB().ExecContext(ctx, `
		UPDATE tracks SET source_id = ?
		 WHERE source_id IS NULL AND substr(path, 1, ?) = ?`,
		row.ID, len(prefix), prefix)
	if err != nil {
		return fmt.Errorf("library: adopting tracks under %s: %w", prefix, err)
	}
	return nil
}

// adoptSubsonic rewrites v0.1 stream URLs into locators.
//
// The song id is recoverable from the URL's own query, so the row can be
// claimed in place. Without this the rescan would add every song again under
// its locator and silently double the library.
func adoptSubsonic(ctx context.Context, s *store.Store, row store.Source) error {
	claims, err := subsonicClaims(ctx, s, row)
	if err != nil {
		return err
	}
	for _, c := range claims {
		if _, err := s.DB().ExecContext(ctx,
			`UPDATE tracks SET path = ?, source_id = ? WHERE id = ?`,
			c.locator, row.ID, c.id); err != nil {
			return fmt.Errorf("library: adopting track %d: %w", c.id, err)
		}
	}
	return nil
}

// claim is one v0.1 row and the locator it should carry.
type claim struct {
	id      int64
	locator string
}

// subsonicClaims finds the rows whose song id can be recovered from their URL.
//
// A row that cannot be read or whose id cannot be recovered is LEFT ALONE
// rather than mangled: the rescan will add the song fresh, and an orphan row is
// recoverable where a wrong locator is not. That is why a failed Scan needs no
// branch of its own -- it leaves an empty path, which falls through the same
// door as a URL with no id, and the failure is reported at the end.
func subsonicClaims(ctx context.Context, s *store.Store, row store.Source) ([]claim, error) {
	prefix := strings.TrimSuffix(row.Locator, "/") + "/rest/stream.view?"
	rows, err := s.DB().QueryContext(ctx, `
		SELECT id, path FROM tracks
		 WHERE source_id IS NULL AND substr(path, 1, ?) = ?`, len(prefix), prefix)
	if err != nil {
		return nil, fmt.Errorf("library: finding tracks of %s: %w", row.Locator, err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	var claims []claim
	var failures error
	for rows.Next() {
		var id int64
		var path string
		failures = errors.Join(failures, rows.Scan(&id, &path))

		q, perr := url.ParseQuery(strings.TrimPrefix(path, prefix))
		if perr != nil || q.Get("id") == "" {
			continue
		}
		claims = append(claims, claim{id, SubsonicLocator(row.ID, q.Get("id"))})
	}
	return claims, readFailure(errors.Join(failures, rows.Err()), row.Locator)
}

// readFailure names a driver failure part way through a result set. Split out
// because both places it can happen -- a scan of a row, and the iteration
// itself -- are unreachable from a test against a working SQLite file, and an
// error path with no test is a guess about what the message will say.
func readFailure(err error, locator string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("library: reading tracks of %s: %w", locator, err)
}

// ResolvePath turns a stored path into something a decoder can open.
//
// Folder paths pass through untouched. A locator is expanded into a signed
// stream URL at PLAY TIME, which is the whole reason the credential is not in
// the row: changing a source's password changes nothing in the tracks table.
func ResolvePath(ctx context.Context, s *store.Store, path string) (string, error) {
	if !strings.HasPrefix(path, locatorPrefix) {
		return path, nil
	}
	rest := strings.TrimPrefix(path, locatorPrefix)
	// SplitN with 2, because a song id is free to contain colons -- and one
	// server really does use absolute file paths as ids.
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("library: malformed locator %q", path)
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("library: malformed locator %q", path)
	}

	row, err := s.GetSource(ctx, id)
	if err != nil {
		return "", fmt.Errorf("library: resolving %q: %w", path, err)
	}
	if row.Kind != store.SourceSubsonic {
		return "", fmt.Errorf("library: locator %q names a %s source", path, row.Kind)
	}
	return Subsonic{BaseURL: row.Locator, User: row.Username, Password: row.Password}.
		StreamURL(parts[1])
}

// merge accumulates one source's result into the whole scan's.
func (s *Stats) merge(o Stats) {
	s.Found += o.Found
	s.Added += o.Added
	s.Updated += o.Updated
	s.Skipped += o.Skipped
	s.Unplayable += o.Unplayable
	s.Rejected = append(s.Rejected, o.Rejected...)
}
