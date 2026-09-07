// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Station is one dial position, owned by the admin.
//
// Mood and JockID are OPTIONAL and stored as NULL when empty: the catch-all has
// neither, and the schema unassigns a jock with ON DELETE SET NULL, so a
// station that lost its jock and a station that never had one must read back
// identically.
type Station struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Genre     string    `json:"genre"`
	Mood      string    `json:"mood,omitempty"`
	JockID    string    `json:"jock_id,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// SelectorState is where a station's playlist had got to.
//
// It lives on the station row because the station is the unit that stops when
// its last listener leaves and resumes when the next one tunes in.
type SelectorState struct {
	Seed   int64 `json:"seed"`
	Cursor int   `json:"cursor"`
}

// StationTrack is one playlist row with its operator overrides.
type StationTrack struct {
	TrackID  int64 `json:"track_id"`
	Pinned   bool  `json:"pinned,omitempty"`
	Excluded bool  `json:"excluded,omitempty"`
	// Missing is true when the library no longer has the file. Carried rather
	// than filtered out, because the console has to show an operator WHY a
	// station shrank -- a track that silently vanished from the list looks
	// like a bug in the playlist editor.
	Missing bool `json:"missing,omitempty"`
}

const stationColumns = `id, name, genre, mood, jock_id, enabled, created_at`

// CreateStation inserts a station and returns its id.
func (s *Store) CreateStation(ctx context.Context, st Station) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO stations (name, genre, mood, jock_id, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		st.Name, st.Genre, nullable(st.Mood), nullable(st.JockID),
		st.Enabled, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("store: creating station %q: %w", st.Name, err)
	}
	return idOf(res, st.Name)
}

// GetStation reads one station.
func (s *Store) GetStation(ctx context.Context, id int64) (Station, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+stationColumns+` FROM stations WHERE id = ?`, id)
	st, err := scanStation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Station{}, ErrNotFound
	}
	if err != nil {
		return Station{}, fmt.Errorf("store: reading station %d: %w", id, err)
	}
	return st, nil
}

// ListStations returns every station, oldest first -- creation order is the
// order ProposeDial seeded them in, which is biggest bucket first.
func (s *Store) ListStations(ctx context.Context) ([]Station, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+stationColumns+` FROM stations ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: listing stations: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []Station{}
	for rows.Next() {
		st, err := scanStation(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing stations: %w", err)
		}
		out = append(out, st)
	}
	return out, wrapErr(rows.Err(), "listing stations")
}

// UpdateStation writes every editable field back.
//
// Whole-row update rather than per-field setters: the console edits a form and
// submits it, and a partial update would need the caller to know which fields
// the operator touched.
func (s *Store) UpdateStation(ctx context.Context, st Station) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE stations SET name = ?, genre = ?, mood = ?, jock_id = ?, enabled = ?
		 WHERE id = ?`,
		st.Name, st.Genre, nullable(st.Mood), nullable(st.JockID), st.Enabled, st.ID)
	if err != nil {
		return fmt.Errorf("store: updating station %d: %w", st.ID, err)
	}
	return requireOneRow(res, st.ID)
}

// DeleteStation removes a station. Its playlist goes with it by ON DELETE
// CASCADE; the tracks themselves are library rows and are never touched.
func (s *Store) DeleteStation(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM stations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: deleting station %d: %w", id, err)
	}
	return requireOneRow(res, id)
}

// GetSelectorState reports where the playlist had got to, and whether it has
// ever been saved.
//
// NEVER PLAYED is not the same as PLAYED FROM ZERO. A fresh station must draw
// its own seed; resuming from a zero value would give every new station the
// same running order.
func (s *Store) GetSelectorState(ctx context.Context, id int64) (SelectorState, bool, error) {
	var seed, cursor sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT sel_seed, sel_cursor FROM stations WHERE id = ?`, id).Scan(&seed, &cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return SelectorState{}, false, ErrNotFound
	}
	if err != nil {
		return SelectorState{}, false, fmt.Errorf("store: reading station %d state: %w", id, err)
	}
	if !seed.Valid {
		return SelectorState{}, false, nil
	}
	return SelectorState{Seed: seed.Int64, Cursor: int(cursor.Int64)}, true, nil
}

// SetSelectorState saves the playlist position.
func (s *Store) SetSelectorState(ctx context.Context, id int64, st SelectorState) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE stations SET sel_seed = ?, sel_cursor = ? WHERE id = ?`,
		st.Seed, st.Cursor, id)
	if err != nil {
		return fmt.Errorf("store: saving station %d state: %w", id, err)
	}
	return requireOneRow(res, id)
}

// ReplaceStationTracks materialises a station's playlist.
//
// OPERATOR OVERRIDES SURVIVE. A rescan is a fact about the library; a pin or an
// exclusion is a decision about this station. Dropping either would mean a
// nightly rescan silently un-bans a track the operator banned and drops one
// they insisted on, with nothing in the UI to say it happened -- so only the
// unflagged rows are cleared.
func (s *Store) ReplaceStationTracks(ctx context.Context, id int64, trackIDs []int64) error {
	what := fmt.Sprintf("replacing station %d playlist", id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapErr(err, what)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM station_tracks
		  WHERE station_id = ? AND pinned = 0 AND excluded = 0`, id); err != nil {
		return wrapErr(err, what)
	}
	for _, tid := range trackIDs {
		// OR IGNORE, not REPLACE: a track that is already here carries flags,
		// and REPLACE would delete the row and reinsert it with them cleared.
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO station_tracks (station_id, track_id) VALUES (?, ?)`,
			id, tid); err != nil {
			return fmt.Errorf("store: adding track %d to station %d: %w", tid, id, err)
		}
	}
	return wrapErr(tx.Commit(), what)
}

// StationTrackIDs returns the tracks this station may actually play.
//
// Excluded tracks are omitted, so a caller cannot play one by forgetting to
// check the flag. Order is by track id, which is what PoolForTag produces, so
// re-seeding an unchanged library is a no-op.
func (s *Store) StationTrackIDs(ctx context.Context, id int64) ([]int64, error) {
	all, err := s.ListStationTracks(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []int64{}
	for _, st := range all {
		// A missing file is as unplayable as an excluded one, and finding out
		// at play time means discovering it mid-transition.
		if !st.Excluded && !st.Missing {
			out = append(out, st.TrackID)
		}
	}
	return out, nil
}

// ListStationTracks returns the whole playlist WITH its flags, excluded tracks
// included -- the console has to show an excluded track or the operator cannot
// find it to put it back.
func (s *Store) ListStationTracks(ctx context.Context, id int64) ([]StationTrack, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT st.track_id, st.pinned, st.excluded, t.missing_at IS NOT NULL
		  FROM station_tracks st
		  JOIN tracks t ON t.id = st.track_id
		 WHERE st.station_id = ? ORDER BY st.track_id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: reading station %d playlist: %w", id, err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []StationTrack{}
	for rows.Next() {
		var st StationTrack
		if err := rows.Scan(&st.TrackID, &st.Pinned, &st.Excluded, &st.Missing); err != nil {
			return nil, fmt.Errorf("store: reading station %d playlist: %w", id, err)
		}
		out = append(out, st)
	}
	return out, wrapErr(rows.Err(), "reading a station playlist")
}

// SetPinned keeps a track on the station across rescans.
func (s *Store) SetPinned(ctx context.Context, stationID, trackID int64, pinned bool) error {
	return s.setFlag(ctx, "pinned", stationID, trackID, pinned)
}

// SetExcluded keeps a track off the station without removing it from the
// library or from the operator's view of the playlist.
func (s *Store) SetExcluded(ctx context.Context, stationID, trackID int64, excluded bool) error {
	return s.setFlag(ctx, "excluded", stationID, trackID, excluded)
}

// setFlag is shared by both overrides. The column name is a constant chosen by
// the two callers above, never caller input.
func (s *Store) setFlag(ctx context.Context, col string, stationID, trackID int64, on bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE station_tracks SET `+col+` = ? WHERE station_id = ? AND track_id = ?`,
		on, stationID, trackID)
	if err != nil {
		return fmt.Errorf("store: setting %s on track %d of station %d: %w",
			col, trackID, stationID, err)
	}
	return requireOneRow(res, trackID)
}

// scanStation reads a station row. Takes the interface QueryRow and Rows share
// so one NULL-handling rule serves both.
func scanStation(sc interface{ Scan(...any) error }) (Station, error) {
	var st Station
	var mood, jock sql.NullString
	var created int64
	if err := sc.Scan(&st.ID, &st.Name, &st.Genre, &mood, &jock, &st.Enabled, &created); err != nil {
		return Station{}, err
	}
	st.Mood, st.JockID = mood.String, jock.String
	st.CreatedAt = time.Unix(created, 0)
	return st, nil
}

// nullable stores an empty string as NULL, so "no mood" is one value in the
// database rather than two that behave differently in a WHERE clause.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// StationTrackDetail is one playlist row with enough about the track for an
// operator to recognise it.
type StationTrackDetail struct {
	TrackID int64  `json:"track_id"`
	Artist  string `json:"artist"`
	Title   string `json:"title"`
	// Album and Year are what an operator curates by: a playlist of artists
	// and titles alone does not say whether a run of tracks is one record.
	Album    string `json:"album"`
	Year     int    `json:"year"`
	Pinned   bool   `json:"pinned"`
	Excluded bool   `json:"excluded"`
	Missing  bool   `json:"missing"`
	// Genres and Moods are what the track COUNTS AS -- the operator's own tags
	// where they exist, the dossier's otherwise -- so an operator curating a
	// playlist sees why a track landed here. Empty when nothing has said yet.
	Genres []string `json:"genres"`
	Moods  []string `json:"moods"`
	// Overridden marks a row an operator has re-tagged by hand, which is what
	// lets the console offer to put the enrichment's answer back.
	Overridden bool `json:"overridden"`
	// BPM is the measured tempo, and ZERO MEANS UNMEASURED rather than silent:
	// the column stays NULL until the background analysis pass reaches the
	// track. It is shown because a mood's tempo range decides whether a track
	// is on this playlist at all, and nothing else on the row would say so.
	BPM float64 `json:"bpm"`
}

// StationTrackPage is one page of a station's playlist, with the total so a
// console can show "showing 50 of 1,200" rather than guessing when to stop.
func (s *Store) StationTrackPage(ctx context.Context, id int64, limit, offset int) (
	[]StationTrackDetail, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM station_tracks WHERE station_id = ?`, id).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: counting station %d playlist: %w", id, err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT st.track_id, coalesce(t.artist, ''), coalesce(t.title, ''),
		       coalesce(t.album, ''), coalesce(t.year, 0),
		       st.pinned, st.excluded, t.missing_at IS NOT NULL,
		       e.station_tags, e.mood, e.overridden, coalesce(t.bpm, 0)
		  FROM station_tracks st
		  JOIN tracks t ON t.id = st.track_id
		  JOIN effective_tags e ON e.track_id = t.id
		 WHERE st.station_id = ? ORDER BY st.track_id LIMIT ? OFFSET ?`, id, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("store: reading station %d playlist: %w", id, err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []StationTrackDetail{}
	var failures error
	for rows.Next() {
		var d StationTrackDetail
		var genres, moods string
		if err := rows.Scan(&d.TrackID, &d.Artist, &d.Title, &d.Album, &d.Year,
			&d.Pinned, &d.Excluded, &d.Missing, &genres, &moods, &d.Overridden,
			&d.BPM); err != nil {
			failures = errors.Join(failures, err)
			out = append(out, d)
			continue
		}
		failures = errors.Join(failures, json.Unmarshal([]byte(genres), &d.Genres))
		failures = errors.Join(failures, json.Unmarshal([]byte(moods), &d.Moods))
		out = append(out, d)
	}
	return out, total, wrapErr(errors.Join(failures, rows.Err()), "reading a station playlist")
}
