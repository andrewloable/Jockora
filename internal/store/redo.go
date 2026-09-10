// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"
)

// meaninglessBefore matches dossiers that say nothing about what a track is
// about AND were written before a given moment.
//
// json_extract rather than LIKE. The column is JSON and the two fields are the
// ones the DJ speaks from; matching on text would also catch a summary that
// happens to contain the words. A row with a NULL created_at is included: it
// predates the column being filled, so it certainly predates any setting.
//
// A ROW THAT IS NOT JSON READS AS AN EMPTY ONE, and that is not cosmetic.
// json_extract does not skip a malformed value, it RAISES -- so one bad row
// failed the count and the delete for the entire library, with "SQL logic
// error: malformed JSON" as the whole explanation. Overview swallows that
// error, so the console would quietly stop offering the redo and nothing would
// say why. StoreDossier always marshals valid JSON, so the row can only come
// from a hand-edited database or corruption -- which is exactly when an
// operator needs the button to work rather than to be the second thing broken.
// Substituting an empty object also gives the right answer: a dossier nothing
// can read says nothing, so it is due a redo.
const meaninglessBefore = `
	 WHERE coalesce(json_extract(readable, '$.subject_summary'), '') = ''
	   AND coalesce(json_array_length(readable, '$.themes'), 0) = 0
	   AND coalesce(created_at, 0) < ?`

// The comparison is STRICTLY less than, so a dossier written in the same second
// the setting changed is not offered. That is the conservative direction and
// the deliberate one: a dossier that may already have been written under the
// new setting must not be offered again, or the redo starts looping. At one
// dossier every few seconds, at most one row is ever on that boundary.

// readableJSON is the json column with anything unreadable replaced by an empty
// object, bound once so both queries cannot drift apart.
//
// json_array_length is used in its TWO-ARGUMENT form deliberately: given a path
// it answers 0 when the value there is not an array, where extracting first and
// measuring after RAISES on a themes field holding a string. Both shapes turn
// up in a hand-edited database and neither should break the button.
const readableJSON = `CASE WHEN json_valid(json) THEN json ELSE '{}' END AS readable`

// ClearMeaninglessDossiersBefore deletes the dossiers that say nothing about
// what a track is about and were written before the given unix second, so the
// enrichment queue picks those tracks up again.
//
// THE NARROWEST SET THAT MAKES THE POINT. The queue only ever looks at tracks
// with no dossier row, so a setting that changes how dossiers are written --
// model recall is the first -- is invisible on a library that is already
// enriched. This is the way to make it visible, and it deletes ONLY rows whose
// subject_summary and themes are both empty: a dossier built from real lyrics
// is never touched, so nothing grounded is thrown away to re-derive something
// weaker.
//
// THE TIMESTAMP IS WHAT STOPS AN ENDLESS LOOP. Recall does not rescue every
// track -- a model refuses what it does not know, which is the correct
// behaviour -- so re-enrichment writes a fresh meaningless dossier for every
// track it still cannot describe. Without a cutoff the console would offer to
// clear those too, the operator would spend hours of model time reproducing
// them exactly, and it would offer again. Only rows written under the OLD
// settings can be improved by a redo, and only those are ever offered.
//
// The station tags go with them, which is the cost. A track being re-enriched
// is off the dial until its new dossier lands, so this is a deliberate action
// with a count reported back, never something that happens on its own.
func (s *Store) ClearMeaninglessDossiersBefore(ctx context.Context, before int64) (int64, error) {
	// A DELETE cannot carry a derived column, so the matching rows are named by
	// a subquery that can.
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM dossiers WHERE track_id IN (
		   SELECT track_id FROM (SELECT track_id, created_at, `+readableJSON+
			` FROM dossiers)`+meaninglessBefore+`)`, before)
	if err != nil {
		return 0, fmt.Errorf("store: clearing dossiers with no meaning: %w", err)
	}
	// KEPT THOUGH NO TEST REACHES IT. modernc's sqlite driver returns a nil
	// error from RowsAffected after a successful DELETE, so this branch is
	// unreachable today -- but the error is part of the database/sql contract
	// and a driver change could start returning one. Discarding it to win a
	// coverage line would turn a future failure into a silent "cleared 0",
	// which reads exactly like success with nothing to do.
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: counting cleared dossiers: %w", err)
	}
	return n, nil
}

// CountMeaninglessDossiersBefore is the same set, counted rather than deleted,
// so the console can say how many tracks a redo would affect BEFORE it happens.
func (s *Store) CountMeaninglessDossiersBefore(ctx context.Context, before int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM (SELECT created_at, `+readableJSON+
			` FROM dossiers)`+meaninglessBefore, before).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: counting dossiers with no meaning: %w", err)
	}
	return n, nil
}
