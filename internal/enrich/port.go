// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/andrewloable/jockora/internal/store"
)

// PortFormat names the file so a program that is handed one can tell what it
// is, and so this one can refuse anything else.
const PortFormat = "jockora-enrichment"

// PortVersion is the shape of the RECORDS. It moves when a field's meaning
// changes, independently of the database schema, which travels alongside it as
// provenance rather than as a compatibility check.
const PortVersion = 1

// DurationTolerance is how far a track's length may differ before an imported
// record is refused.
//
// TWO SECONDS. Different encoders disagree about where a file ends by a frame
// or two, so an exact match would refuse honest libraries; ten seconds would
// let a different recording of the same song through, and true facts about the
// wrong song is worse than no facts at all.
const DurationTolerance = 2.0

// Header is the first line of an export.
type Header struct {
	Format     string `json:"format"`
	Version    int    `json:"version"`
	Schema     int    `json:"schema"`
	ExportedAt int64  `json:"exported_at,omitempty"`
	Tracks     int    `json:"tracks,omitempty"`
}

// Record is one track's enrichment, as it travels.
//
// THE PATH IS THE KEY; the artist, title and duration are a CHECK. Matching on
// artist and title across libraries would let a cover or a live take inherit
// the studio recording's dossier, which is the same wrong-song failure the
// duration check exists to prevent.
type Record struct {
	Path      string  `json:"path"`
	Artist    string  `json:"artist,omitempty"`
	Title     string  `json:"title,omitempty"`
	Album     string  `json:"album,omitempty"`
	DurationS float64 `json:"duration_s,omitempty"`

	Dossier *Dossier `json:"dossier,omitempty"`
	// Tags is the OPERATOR'S OWN override of station tags and mood, which
	// outranks the dossier here as everywhere else.
	Tags     *PortTags     `json:"tags,omitempty"`
	Cost     *PortCost     `json:"cost,omitempty"`
	Analysis *PortAnalysis `json:"analysis,omitempty"`
}

// PortTags is an operator override.
type PortTags struct {
	Genres []string `json:"genres"`
	Moods  []string `json:"moods"`
}

// PortCost is what the enrichment pass cost.
type PortCost struct {
	Tokens      int     `json:"tokens"`
	WallSeconds float64 `json:"wall_seconds"`
}

// PortAnalysis is the measured audio, which costs hours of ffmpeg and LRCLIB
// work and is worth as much as the dossier to a mixer.
type PortAnalysis struct {
	LoudnessLUFS    float64 `json:"loudness_lufs,omitempty"`
	RampS           float64 `json:"ramp_s,omitempty"`
	OutroS          float64 `json:"outro_s,omitempty"`
	RampConfidence  string  `json:"ramp_confidence,omitempty"`
	BPM             float64 `json:"bpm,omitempty"`
	NoCrossfadeNext bool    `json:"no_crossfade_next,omitempty"`
}

// PortReport is what an import did, in numbers.
//
// THE REPORT IS PART OF THE FEATURE. An import that silently does nothing is
// the failure to design against: every record ends in exactly one of these.
type PortReport struct {
	Applied     int `json:"applied"`
	HadDossier  int `json:"had_dossier"`
	HadOverride int `json:"had_override"`
	Unmatched   int `json:"unmatched"`
	// WrongTrack counts records refused because the local file is a different
	// length, which means the path has been reused.
	WrongTrack int `json:"wrong_track"`
	Rejected   int `json:"rejected"`
	Unreadable int `json:"unreadable"`
}

// Total is every record the file accounted for.
func (r PortReport) Total() int {
	return r.Applied + r.HadDossier + r.HadOverride + r.Unmatched +
		r.WrongTrack + r.Rejected + r.Unreadable
}

// Port binds export and import to one library.
//
// It exists so the server can be handed a named thing -- the enrichment, not
// the database -- and so the store never has to know this format exists.
type Port struct{ Store *store.Store }

// NewPort returns the enrichment port for a library.
func NewPort(s *store.Store) *Port { return &Port{Store: s} }

// ExportEnrichment writes this library's enrichment.
func (p *Port) ExportEnrichment(ctx context.Context, w io.Writer) error {
	return Export(ctx, p.Store, w)
}

// ImportEnrichment merges an export into this library.
func (p *Port) ImportEnrichment(ctx context.Context, r io.Reader, overwrite bool) (PortReport, error) {
	return Import(ctx, p.Store, r, overwrite)
}

// Export writes every track's enrichment as gzipped JSONL.
//
// NOT A DATABASE BACKUP. The file this comes out of holds password hashes,
// stations, jocks and said_lines; an admin is invited to hand this to somebody
// with the same records, so it carries the enrichment and nothing else.
//
// One object per line, streamed: a partial file is still mostly readable, and a
// ten-thousand-track library never becomes one document in memory.
func Export(ctx context.Context, s *store.Store, w io.Writer) error {
	gz := gzip.NewWriter(w)
	enc := json.NewEncoder(gz)

	rows, err := s.DB().QueryContext(ctx, `
		SELECT t.path, coalesce(t.artist,''), coalesce(t.title,''), coalesce(t.album,''),
		       coalesce(t.duration_s,0),
		       d.json, o.station_tags, o.mood, c.tokens, c.wall_seconds,
		       coalesce(t.loudness_lufs,0), coalesce(t.ramp_s,0), coalesce(t.outro_s,0),
		       coalesce(t.ramp_confidence,''), coalesce(t.bpm,0), t.no_crossfade_next
		  FROM tracks t
		  LEFT JOIN dossiers d    ON d.track_id = t.id
		  LEFT JOIN track_tags o  ON o.track_id = t.id
		  LEFT JOIN enrich_cost c ON c.track_id = t.id
		 WHERE d.track_id IS NOT NULL OR o.track_id IS NOT NULL
		    OR t.loudness_lufs IS NOT NULL OR t.bpm IS NOT NULL
		 ORDER BY t.id`)
	if err != nil {
		return fmt.Errorf("enrich: reading the library to export: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	if err := enc.Encode(Header{
		Format: PortFormat, Version: PortVersion,
		Schema: store.CurrentSchemaVersion, ExportedAt: time.Now().Unix(),
	}); err != nil {
		return fmt.Errorf("enrich: writing the export header: %w", err)
	}

	for rows.Next() {
		// ONE failure branch in this loop, whichever half failed. Both halves
		// are unreachable from a working database -- a row of the wrong shape
		// needs a schema change under a running export -- so they are reached
		// from a test through writeRecord rather than through a fixture nobody
		// can write honestly.
		rec, scanErr := scanExport(rows)
		if err := writeRecord(enc, rec, scanErr); err != nil {
			return err
		}
	}
	// rows.Err() is a failure DURING iteration: a connection dropped halfway
	// through a library, which is the difference between a short export and a
	// whole one. Joined with the flush so both are reported and neither hides
	// the other.
	return errors.Join(wrapExport(rows.Err()), gz.Close())
}

// writeRecord encodes one record, or reports whatever stopped it being read.
func writeRecord(enc *json.Encoder, rec Record, scanErr error) error {
	if scanErr != nil {
		return scanErr
	}
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("enrich: writing %s: %w", rec.Path, err)
	}
	return nil
}

// wrapExport names an export failure, and turns nil into nil.
func wrapExport(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("enrich: reading the library to export: %w", err)
}

// scanExport turns one joined row into a record.
func scanExport(rows *sql.Rows) (Record, error) {
	var rec Record
	var dossierJSON, genres, moods, rampConf sql.NullString
	var tokens sql.NullInt64
	var wall sql.NullFloat64
	var lufs, ramp, outro, bpm float64
	var noFade int

	if err := rows.Scan(&rec.Path, &rec.Artist, &rec.Title, &rec.Album, &rec.DurationS,
		&dossierJSON, &genres, &moods, &tokens, &wall,
		&lufs, &ramp, &outro, &rampConf, &bpm, &noFade); err != nil {
		return Record{}, fmt.Errorf("enrich: reading a track to export: %w", err)
	}

	if dossierJSON.Valid {
		var d Dossier
		if err := json.Unmarshal([]byte(dossierJSON.String), &d); err == nil {
			rec.Dossier = &d
		}
		// A dossier that will not parse is left out rather than failing the
		// whole export: it is unreadable here too, and one bad row must not
		// cost an operator the other nine thousand.
	}
	if genres.Valid && moods.Valid {
		var t PortTags
		if json.Unmarshal([]byte(genres.String), &t.Genres) == nil &&
			json.Unmarshal([]byte(moods.String), &t.Moods) == nil {
			rec.Tags = &t
		}
	}
	if tokens.Valid {
		rec.Cost = &PortCost{Tokens: int(tokens.Int64), WallSeconds: wall.Float64}
	}
	if lufs != 0 || ramp != 0 || outro != 0 || bpm != 0 || rampConf.String != "" || noFade != 0 {
		rec.Analysis = &PortAnalysis{
			LoudnessLUFS: lufs, RampS: ramp, OutroS: outro,
			RampConfidence: rampConf.String, BPM: bpm, NoCrossfadeNext: noFade != 0,
		}
	}
	return rec, nil
}

// ErrNotAnExport refuses a file this program did not write.
var ErrNotAnExport = errors.New("enrich: not a Jockora enrichment export")

// Import merges an export into this library.
//
// IT NEVER REPLACES. No local row is deleted, and by default a track that
// already has a dossier is left alone; overwrite lets an imported dossier
// replace one. AN OPERATOR OVERRIDE IS NEVER OVERWRITTEN under any flag,
// because somebody sat and typed it.
func Import(ctx context.Context, s *store.Store, r io.Reader, overwrite bool) (PortReport, error) {
	var rep PortReport

	gz, err := gzip.NewReader(r)
	if err != nil {
		// BOTH wrapped: a caller upstream needs to tell "not our file" from
		// "the upload was cut off at the size limit", and %v would flatten the
		// second into text nothing can match on.
		return rep, fmt.Errorf("%w: %w", ErrNotAnExport, err)
	}
	defer gz.Close() //nolint:errcheck // read-only

	sc := bufio.NewScanner(gz)
	// A dossier with lyrics-derived summary and a long fact list is comfortably
	// past bufio's default 64KB line.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	if err := readHeader(sc); err != nil {
		return rep, err
	}

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil || rec.Path == "" {
			// A file cut off mid-line, or a hand-edited one. The records before
			// it are already applied and the rest of the file still gets read.
			rep.Unreadable++
			continue
		}
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		apply(ctx, s, rec, overwrite, &rep)
	}
	if err := sc.Err(); err != nil {
		// A gzip stream that ends mid-member reports here. Everything already
		// applied stays applied: the report says how far it got.
		rep.Unreadable++
	}
	return rep, nil
}

// readHeader refuses a file this build cannot read before it changes anything.
func readHeader(sc *bufio.Scanner) error {
	if !sc.Scan() {
		// A READ THAT FAILED IS NOT AN EMPTY FILE. An upload cut off at a size
		// limit stops here, and reporting it as "empty" would send the operator
		// looking for the wrong problem -- and would hide the error a caller
		// needs to answer 413 rather than 400.
		if err := sc.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrNotAnExport, err)
		}
		return fmt.Errorf("%w: the file is empty", ErrNotAnExport)
	}
	var h Header
	if err := json.Unmarshal(sc.Bytes(), &h); err != nil || h.Format != PortFormat {
		return fmt.Errorf("%w: it does not begin with a Jockora export header", ErrNotAnExport)
	}
	if h.Version > PortVersion {
		return fmt.Errorf("%w: it is version %d and this build understands %d. "+
			"Upgrade Jockora rather than editing the file", ErrNotAnExport, h.Version, PortVersion)
	}
	return nil
}

// apply lands one record, counting exactly one outcome for it.
func apply(ctx context.Context, s *store.Store, rec Record, overwrite bool, rep *PortReport) {
	var id int64
	var localDur sql.NullFloat64
	err := s.DB().QueryRowContext(ctx,
		`SELECT id, duration_s FROM tracks WHERE path = ?`, rec.Path).Scan(&id, &localDur)
	if err != nil {
		// No such path here. A Subsonic source stores a stream URL as its path,
		// so it carries the host and moves when the server does; that shows up
		// as unmatched and is a known limit rather than a fault.
		rep.Unmatched++
		return
	}
	// THE DURATION IS THE CHECK. A path reused for a different recording would
	// otherwise hand the DJ true facts about the wrong song.
	if rec.DurationS > 0 && localDur.Valid && localDur.Float64 > 0 &&
		math.Abs(localDur.Float64-rec.DurationS) > DurationTolerance {
		rep.WrongTrack++
		return
	}

	applied := false
	if rec.Analysis != nil {
		applyAnalysis(ctx, s, id, rec.Analysis)
		applied = true
	}
	if rec.Cost != nil {
		_, _ = s.DB().ExecContext(ctx, `
			INSERT INTO enrich_cost (track_id, tokens, wall_seconds) VALUES (?, ?, ?)
			ON CONFLICT(track_id) DO UPDATE SET
				tokens = excluded.tokens, wall_seconds = excluded.wall_seconds`,
			id, rec.Cost.Tokens, rec.Cost.WallSeconds) //nolint:errcheck // counted below
	}

	switch {
	case rec.Tags == nil:
	case hasOverride(ctx, s, id):
		// NEVER. Somebody sat and typed the local one.
		rep.HadOverride++
		return
	default:
		if err := s.SetTrackTags(ctx, id, rec.Tags.Genres, rec.Tags.Moods); err == nil {
			applied = true
		}
	}

	if rec.Dossier != nil {
		switch {
		case hasDossier(ctx, s, id) && !overwrite:
			rep.HadDossier++
			return
		default:
			if !storeImported(ctx, s, id, rec) {
				rep.Rejected++
				return
			}
			applied = true
		}
	}

	if applied {
		rep.Applied++
		return
	}
	rep.Rejected++
}

func applyAnalysis(ctx context.Context, s *store.Store, id int64, a *PortAnalysis) {
	noFade := 0
	if a.NoCrossfadeNext {
		noFade = 1
	}
	// COALESCE on the way in: an export carrying a zero for something it never
	// measured must not erase a local measurement.
	_, _ = s.DB().ExecContext(ctx, `
		UPDATE tracks SET
			loudness_lufs   = CASE WHEN ? != 0 THEN ? ELSE loudness_lufs END,
			ramp_s          = CASE WHEN ? != 0 THEN ? ELSE ramp_s END,
			outro_s         = CASE WHEN ? != 0 THEN ? ELSE outro_s END,
			ramp_confidence = CASE WHEN ? != '' THEN ? ELSE ramp_confidence END,
			bpm             = CASE WHEN ? != 0 THEN ? ELSE bpm END,
			no_crossfade_next = ?
		 WHERE id = ?`,
		a.LoudnessLUFS, a.LoudnessLUFS, a.RampS, a.RampS, a.OutroS, a.OutroS,
		a.RampConfidence, a.RampConfidence, a.BPM, a.BPM, noFade, id) //nolint:errcheck // best effort
}

// storeImported puts an imported dossier through the SAME gate the model's own
// output goes through.
//
// This is the requirement not to negotiate. Dossier text is fed to the break
// writer and spoken on air, so a hand-edited or hostile file is a
// prompt-injection path straight into the microphone -- strictly more hostile
// than the scanner metadata the fence was built for. validate and sanitizeTag
// are the same functions the enrichment worker uses, called rather than copied,
// so the two cannot drift.
func storeImported(ctx context.Context, s *store.Store, id int64, rec Record) bool {
	d := *rec.Dossier

	// SANITISED FIELD BY FIELD, because every one of these is printed into the
	// break prompt.
	d.SubjectSummary = sanitizeTag(d.SubjectSummary)
	d.NotableLine = sanitizeTag(d.NotableLine)
	d.Release = sanitizeTag(d.Release)
	d.ArtistFacts = sanitizeAll(d.ArtistFacts)
	d.Themes = sanitizeAll(d.Themes)
	d.Sources = sanitizeAll(d.Sources)

	// The TrackInput validate needs, rebuilt from what the record actually
	// carries. Found is set from the dossier's own sources: a dossier that
	// arrives with them WAS looked up, on the other machine, and one that
	// arrives without them earns the same downgrade it would have earned here.
	in := TrackInput{
		Artist: rec.Artist, Title: rec.Title, Album: rec.Album,
		DurationS:   rec.DurationS,
		ArtistFacts: ArtistFacts{Found: len(d.Sources) > 0},
	}
	if rec.Analysis != nil && rec.Analysis.RampConfidence != "" {
		in.HasSyncedLyrics = true
	}
	d = validate(d, in)

	return StoreDossier(ctx, s, id, d) == nil
}

func sanitizeAll(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = sanitizeTag(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func hasDossier(ctx context.Context, s *store.Store, id int64) bool {
	var n int
	_ = s.DB().QueryRowContext(ctx, `SELECT count(*) FROM dossiers WHERE track_id = ?`, id).Scan(&n)
	return n > 0
}

func hasOverride(ctx context.Context, s *store.Store, id int64) bool {
	var n int
	_ = s.DB().QueryRowContext(ctx, `SELECT count(*) FROM track_tags WHERE track_id = ?`, id).Scan(&n)
	return n > 0
}
