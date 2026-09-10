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
		 WHERE d.track_id IS NOT NULL OR o.track_id IS NOT NULL OR c.track_id IS NOT NULL
		    OR t.loudness_lufs IS NOT NULL OR t.bpm IS NOT NULL
		    OR t.ramp_s IS NOT NULL OR t.outro_s IS NOT NULL
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
		// THE ERROR DECIDES. Throwing it away meant a record whose write the
		// database refused was still counted as applied, and the report is the
		// whole point of this endpoint.
		if applyAnalysis(ctx, s, id, rec.Analysis, overwrite) == nil {
			applied = true
		}
	}
	if rec.Cost != nil {
		// FILL A HOLE unless asked otherwise, like everything else here. What
		// the other install spent is not a measurement of this library.
		clause := `INSERT INTO enrich_cost (track_id, tokens, wall_seconds) VALUES (?, ?, ?)
			ON CONFLICT(track_id) DO NOTHING`
		if overwrite {
			clause = `INSERT INTO enrich_cost (track_id, tokens, wall_seconds) VALUES (?, ?, ?)
				ON CONFLICT(track_id) DO UPDATE SET
					tokens = excluded.tokens, wall_seconds = excluded.wall_seconds`
		}
		// COUNTED, like everything else: the export carries a track whose only
		// enrichment is what the pass cost, so the import needs somewhere to
		// put it other than "rejected".
		if _, err := s.DB().ExecContext(ctx, clause,
			id, rec.Cost.Tokens, rec.Cost.WallSeconds); err == nil {
			applied = true
		}
	}

	// THE TAGS AND THE DOSSIER ARE SEPARATE DECISIONS. Refusing to overwrite a
	// local tag edit is not a reason to throw away the enrichment that arrived
	// with it -- and returning here did exactly that, on precisely the tracks
	// an operator had cared enough about to file by hand.
	heldBack, refused := "", false
	if rec.Tags != nil {
		switch {
		case hasOverride(ctx, s, id):
			// NEVER. Somebody sat and typed the local one.
			heldBack = "override"
		default:
			// THE SAME GATE THE DOSSIER GOES THROUGH, and for a stronger
			// reason: the override outranks the enrichment everywhere in this
			// codebase, and a hand-written file could put anything in it. An
			// out-of-vocabulary tag is not cosmetic -- stations match on the
			// closed vocabulary, so the track would belong to no station at all.
			genres := filterVocab(rec.Tags.Genres, stationTagSet, maxTags)
			moods := filterVocab(rec.Tags.Moods, moodSet, maxTags)
			// NOTHING SURVIVED A LIST THAT HAD SOMETHING IN IT. Writing the
			// empty result would be an override saying "this is not anything",
			// which outranks a perfectly good dossier and removes the track
			// from every station. An override that ARRIVED empty is a different
			// statement and is honoured.
			// EITHER LIST. A mood list that survived nothing is the same
			// mistake with a smaller blast radius: the track drops out of every
			// mood-narrowed station instead of out of every station.
			if (len(genres) == 0 && len(rec.Tags.Genres) > 0) ||
				(len(moods) == 0 && len(rec.Tags.Moods) > 0) {
				refused = true
			} else if err := s.SetTrackTags(ctx, id, genres, moods); err == nil {
				applied = true
			}
		}
	}

	if rec.Dossier != nil {
		switch {
		case hasDossier(ctx, s, id) && !overwrite:
			if heldBack == "" {
				heldBack = "dossier"
			}
		case !storeImported(ctx, s, id, rec):
			rep.Rejected++
			return
		default:
			applied = true
		}
	}

	// ONE OUTCOME PER RECORD, and the one that says why something was left
	// alone wins: an operator reading the report needs to know their own edit
	// stood, not that some other part of the same record landed.
	// ONE OUTCOME PER RECORD, ordered by what the operator most needs to know.
	// A record can be partly applied -- a junk tag list refused while its
	// dossier lands -- and when that happens the REFUSAL is reported, because
	// the file is the thing they can go and look at. The data still landed.
	switch {
	case refused:
		rep.Rejected++
	case heldBack == "override":
		rep.HadOverride++
	case heldBack == "dossier":
		rep.HadDossier++
	case applied:
		rep.Applied++
	default:
		rep.Rejected++
	}
}

// applyAnalysis merges the measured audio.
//
// FILLS HOLES unless the operator asked to overwrite: a loudness measured HERE
// came from this copy of the file, and an imported one came from a different
// encoding of the same recording. The duration check proves it is the same
// song; it does not prove it is the same master.
func applyAnalysis(ctx context.Context, s *store.Store, id int64, a *PortAnalysis, overwrite bool) error {
	noFade := 0
	if a.NoCrossfadeNext {
		noFade = 1
	}
	// force is 1 when the operator asked for the incoming numbers; otherwise a
	// column that already holds a measurement keeps it.
	force := 0
	if overwrite {
		force = 1
	}
	// Two guards on every column: an export carrying a zero for something it
	// never measured must not erase a local measurement either.
	_, err := s.DB().ExecContext(ctx, `
		UPDATE tracks SET
			loudness_lufs   = CASE WHEN ? != 0  AND (loudness_lufs IS NULL   OR ? = 1) THEN ? ELSE loudness_lufs END,
			ramp_s          = CASE WHEN ? != 0  AND (ramp_s IS NULL          OR ? = 1) THEN ? ELSE ramp_s END,
			outro_s         = CASE WHEN ? != 0  AND (outro_s IS NULL         OR ? = 1) THEN ? ELSE outro_s END,
			ramp_confidence = CASE WHEN ? != '' AND (ramp_confidence IS NULL OR ? = 1) THEN ? ELSE ramp_confidence END,
			bpm             = CASE WHEN ? != 0  AND (bpm IS NULL             OR ? = 1) THEN ? ELSE bpm END,
			-- SET, NEVER CLEARED, like every column above it. A record that
			-- never measured the join carries false, and writing that flat
			-- turned a locally measured gapless album pair back into a
			-- crossfade -- audible, on exactly the records it matters for.
			no_crossfade_next = CASE WHEN ? = 1 THEN 1 ELSE no_crossfade_next END
		 WHERE id = ?`,
		a.LoudnessLUFS, force, a.LoudnessLUFS,
		a.RampS, force, a.RampS,
		a.OutroS, force, a.OutroS,
		a.RampConfidence, force, a.RampConfidence,
		a.BPM, force, a.BPM,
		noFade, id)
	return err
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

	// SANITISING LIVES IN validate NOW, which this calls below, so it is not
	// repeated here.
	//
	// It used to be done twice, and the copy here was load-bearing for a while:
	// LookedUpSource is computed from the raw sources on the next line, and
	// while the recall label was compared exactly, a padded or capitalised one
	// slipped past it. isRecallLabel canonicalises now, so the decision below
	// is safe on raw input and one owner is better than two that must agree.
	// The TrackInput validate needs, rebuilt from what the record actually
	// carries. Found is set from the dossier's own sources: a dossier that
	// arrives with them WAS looked up, on the other machine, and one that
	// arrives without them earns the same downgrade it would have earned here.
	//
	// THE RECALL LABEL IS NOT A LOOKUP. It names the model's own memory, and
	// counting it here would let a file carrying sources ["model"] and a list
	// of invented artist_facts past the grounding check -- on the one path in
	// this program that reads a document somebody else wrote.
	in := TrackInput{
		Artist: rec.Artist, Title: rec.Title, Album: rec.Album,
		DurationS:   rec.DurationS,
		ArtistFacts: ArtistFacts{Found: LookedUpSource(d.Sources)},
	}
	if rec.Analysis != nil && rec.Analysis.RampConfidence != "" {
		in.HasSyncedLyrics = true
	}
	// recognised is FALSE here and it does not matter: recall is off for an
	// import, so applyRecall never reads it. The other machine already applied
	// its own answer before this record was written.
	d = validate(d, false, in)

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
