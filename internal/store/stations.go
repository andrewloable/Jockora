// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

	// A STATION IS DESCRIBED, NOT TAGGED. Brief is the sentence the operator
	// wrote; everything below is what the model derived from it.
	Brief string `json:"brief,omitempty"`

	// ZERO MEANS UNSET for every one of these, and is written as NULL, so "no
	// upper bound" and "the year 0" cannot be confused. It matters more for the
	// tempo and the length than for the years, because 0 BPM and 0 seconds are
	// values SQL would happily compare a track against.
	YearMin      int     `json:"year_min,omitempty"`
	YearMax      int     `json:"year_max,omitempty"`
	TempoMin     float64 `json:"tempo_min,omitempty"`
	TempoMax     float64 `json:"tempo_max,omitempty"`
	DurationMinS float64 `json:"duration_min_s,omitempty"`
	DurationMaxS float64 `json:"duration_max_s,omitempty"`
}

// BriefFor writes the sentence a station's tags already say.
//
// SHARED, because two callers need the identical sentence and writing it twice
// is how they drift: migration 10 back-fills every station that already exists,
// and seed.go writes it on a FRESH install where the back-fill has nothing to
// back-fill. Without both, the very first dial an operator ever sees is the one
// dial with empty briefs -- exactly the two-shapes problem the back-fill was
// decided on to remove.
//
// English, not a tag dump: the stored commas are spaced out and the mood clause
// is omitted when there is no mood. The catch-all reads "Plays unsorted.",
// which is correct and is what the operator sees.
func BriefFor(genre, mood string) string {
	genre = spaceTags(genre)
	if genre == "" {
		return ""
	}
	brief := "Plays " + genre + "."
	if m := spaceTags(mood); m != "" {
		brief += " Feels " + m + "."
	}
	return brief
}

// spaceTags turns "rock,punk" into "rock, punk" without doubling a space that
// is already there.
func spaceTags(tags string) string {
	parts := strings.Split(tags, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

// nullableInt and nullableFloat store a zero as NULL, the same rule nullable
// follows for a string. See the field comments above for why zero is unset.
func nullableInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullableFloat(f float64) any {
	if f == 0 {
		return nil
	}
	return f
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

const stationColumns = `id, name, genre, mood, jock_id, enabled, created_at, brief, year_min, year_max, tempo_min, tempo_max, duration_min_s, duration_max_s`

// CreateStation inserts a station and returns its id.
func (s *Store) CreateStation(ctx context.Context, st Station) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO stations (name, genre, mood, jock_id, enabled, created_at,
		                      brief, year_min, year_max,
		                      tempo_min, tempo_max, duration_min_s, duration_max_s)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.Name, st.Genre, nullable(st.Mood), nullable(st.JockID),
		st.Enabled, time.Now().Unix(),
		nullable(st.Brief), nullableInt(st.YearMin), nullableInt(st.YearMax),
		nullableFloat(st.TempoMin), nullableFloat(st.TempoMax),
		nullableFloat(st.DurationMinS), nullableFloat(st.DurationMaxS))
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
		UPDATE stations SET name = ?, genre = ?, mood = ?, jock_id = ?, enabled = ?,
		                    brief = ?, year_min = ?, year_max = ?,
		                    tempo_min = ?, tempo_max = ?,
		                    duration_min_s = ?, duration_max_s = ?
		 WHERE id = ?`,
		st.Name, st.Genre, nullable(st.Mood), nullable(st.JockID), st.Enabled,
		nullable(st.Brief), nullableInt(st.YearMin), nullableInt(st.YearMax),
		nullableFloat(st.TempoMin), nullableFloat(st.TempoMax),
		nullableFloat(st.DurationMinS), nullableFloat(st.DurationMaxS), st.ID)
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
	var mood, jock, brief sql.NullString
	var yearMin, yearMax sql.NullInt64
	var tempoMin, tempoMax, durMin, durMax sql.NullFloat64
	var created int64
	if err := sc.Scan(&st.ID, &st.Name, &st.Genre, &mood, &jock, &st.Enabled, &created,
		&brief, &yearMin, &yearMax, &tempoMin, &tempoMax, &durMin, &durMax); err != nil {
		return Station{}, err
	}
	st.Mood, st.JockID, st.Brief = mood.String, jock.String, brief.String
	st.YearMin, st.YearMax = int(yearMin.Int64), int(yearMax.Int64)
	st.TempoMin, st.TempoMax = tempoMin.Float64, tempoMax.Float64
	st.DurationMinS, st.DurationMaxS = durMin.Float64, durMax.Float64
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

// playlistOrder is the ORDER BY for each column a playlist can be sorted by,
// keyed by the name the API uses for it.
//
// A MAP AND NOT INTERPOLATION. The column name arrives from a query string, and
// the only safe way to put an operator's text into a SQL clause is not to: the
// key selects a fragment written here, and anything unrecognised falls back to
// the insertion order the playlist has always had.
//
// BLANKS LAST IN BOTH DIRECTIONS, which is what the console's own client-side
// tables already do. A year of 0 or an unmeasured tempo is "nothing here", and
// sorting to find the oldest record must not answer with a screenful of tracks
// that have no year -- nor must reversing it. The direction is applied to the
// value only, never to the blank test.
//
// TEXT COMPARED WITHOUT CASE, or every lower-cased artist sorts after every
// capitalised one and the column reads as unsorted.
var playlistOrder = map[string]struct{ blank, value string }{
	"artist": {"coalesce(t.artist, '') = ''", "t.artist COLLATE NOCASE"},
	"title":  {"coalesce(t.title, '') = ''", "t.title COLLATE NOCASE"},
	"album":  {"coalesce(t.album, '') = ''", "t.album COLLATE NOCASE"},
	"year":   {"coalesce(t.year, 0) = 0", "t.year"},
	"bpm":    {"coalesce(t.bpm, 0) = 0", "t.bpm"},
}

// playlistOrderBy is the clause for one sort key, or the default order.
func playlistOrderBy(sort string, desc bool) string {
	o, ok := playlistOrder[sort]
	if !ok {
		return "st.track_id"
	}
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	// THE TRACK ID LAST AND ALWAYS. Two tracks off one record share a year, and
	// a page boundary falling inside a tie can show a row twice or skip it
	// entirely: LIMIT/OFFSET asks the database twice and nothing else says
	// which of the tied rows comes first.
	return o.blank + ", " + o.value + " " + dir + ", st.track_id"
}

// StationTrackPage is one page of a station's playlist, with the total so a
// console can show "showing 50 of 1,200" rather than guessing when to stop.
//
// SORTED HERE RATHER THAN IN THE CONSOLE, because the console holds fifty rows
// out of seven thousand: ordering those fifty would answer a question about the
// page while looking like an answer about the library. An unknown sort is the
// default order, the same way an unreadable limit is the default page size.
func (s *Store) StationTrackPage(ctx context.Context, id int64, limit, offset int, sort string, desc bool) (
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
		 WHERE st.station_id = ? ORDER BY ` + playlistOrderBy(sort, desc) + ` LIMIT ? OFFSET ?`,
		id, limit, offset)
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
