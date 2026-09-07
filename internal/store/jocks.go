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

// Jock is a persona card as a row.
//
// THE AUTHORING SURFACE, not the runtime type. dj.Persona stays immutable with
// private fields and is what the break writer reads; this is what the operator
// console edits, and dj.FromRecord converts one into the other. Keeping them
// apart is what stops an admin edit reaching a half-written persona mid-break.
type Jock struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	VoiceID       string    `json:"voice_id"`
	GoodForGenres []string  `json:"good_for_genres"`
	GoodForMoods  []string  `json:"good_for_moods"`
	SpeechStyle   string    `json:"speech_style"`
	Personality   string    `json:"personality"`
	Forbidden     []string  `json:"forbidden"`
	UpdatedAt     time.Time `json:"updated_at"`
}

const jockColumns = `id, name, voice_id, good_for_genres, good_for_moods,
	speech_style, personality, forbidden, updated_at`

// UpsertJock writes a jock, replacing any row with the same id.
//
// Upsert rather than separate insert and update because the console edits by
// writing the whole card back, and a caller should not have to know whether the
// row already exists.
func (s *Store) UpsertJock(ctx context.Context, j Jock) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO jocks (`+jockColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name            = excluded.name,
			voice_id        = excluded.voice_id,
			good_for_genres = excluded.good_for_genres,
			good_for_moods  = excluded.good_for_moods,
			speech_style    = excluded.speech_style,
			personality     = excluded.personality,
			forbidden       = excluded.forbidden,
			updated_at      = excluded.updated_at`,
		j.ID, j.Name, j.VoiceID, encodeList(j.GoodForGenres), encodeList(j.GoodForMoods),
		j.SpeechStyle, j.Personality, encodeList(j.Forbidden), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("store: upserting jock %q: %w", j.ID, err)
	}
	return nil
}

// GetJock reads one jock.
func (s *Store) GetJock(ctx context.Context, id string) (Jock, error) {
	var j Jock
	var genres, moods, forbidden string
	var updated int64

	err := s.db.QueryRowContext(ctx, `SELECT `+jockColumns+` FROM jocks WHERE id = ?`, id).
		Scan(&j.ID, &j.Name, &j.VoiceID, &genres, &moods,
			&j.SpeechStyle, &j.Personality, &forbidden, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Jock{}, ErrNotFound
	}
	if err != nil {
		return Jock{}, fmt.Errorf("store: reading jock %q: %w", id, err)
	}
	if err := decodeJockLists(&j, genres, moods, forbidden); err != nil {
		return Jock{}, fmt.Errorf("store: jock %q: %w", id, err)
	}
	j.UpdatedAt = time.Unix(updated, 0)
	return j, nil
}

// ListJocks returns every jock, ordered by name.
func (s *Store) ListJocks(ctx context.Context) ([]Jock, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jockColumns+` FROM jocks ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: listing jocks: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []Jock{}
	for rows.Next() {
		var j Jock
		var genres, moods, forbidden string
		var updated int64
		if err := rows.Scan(&j.ID, &j.Name, &j.VoiceID, &genres, &moods,
			&j.SpeechStyle, &j.Personality, &forbidden, &updated); err != nil {
			return nil, fmt.Errorf("store: listing jocks: %w", err)
		}
		// A corrupt list is an ERROR, never a jock with no genres: silently
		// emptying it would stop that jock matching any station and look like
		// a configuration mistake rather than a broken row.
		if err := decodeJockLists(&j, genres, moods, forbidden); err != nil {
			return nil, fmt.Errorf("store: jock %q: %w", j.ID, err)
		}
		j.UpdatedAt = time.Unix(updated, 0)
		out = append(out, j)
	}
	return out, wrapErr(rows.Err(), "listing jocks")
}

// DeleteJock removes a jock. Stations referencing it are UNASSIGNED by the
// schema's ON DELETE SET NULL rather than deleted with it.
func (s *Store) DeleteJock(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM jocks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: deleting jock %q: %w", id, err)
	}
	// The same helper the users store uses, so "no rows changed means not
	// found" is decided in one place rather than re-implemented per table.
	return requireOneRowByID(res, id)
}

// encodeList stores a string slice as a JSON array. A nil slice becomes "[]"
// rather than "null", so a reader never has to handle both.
//
// No error return: json.Marshal of a []string cannot fail. Returning one would
// add four branches across this file that no test can reach and no bug can
// produce, and an unreachable error path is untested code wearing the costume
// of safety.
func encodeList(in []string) string {
	if in == nil {
		in = []string{}
	}
	b, _ := json.Marshal(in)
	return string(b)
}

func decodeJockLists(j *Jock, genres, moods, forbidden string) error {
	for _, f := range []struct {
		raw  string
		into *[]string
		name string
	}{
		{genres, &j.GoodForGenres, "good_for_genres"},
		{moods, &j.GoodForMoods, "good_for_moods"},
		{forbidden, &j.Forbidden, "forbidden"},
	} {
		if err := json.Unmarshal([]byte(f.raw), f.into); err != nil {
			return fmt.Errorf("decoding %s: %w", f.name, err)
		}
	}
	return nil
}
