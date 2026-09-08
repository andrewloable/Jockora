// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Ad is one advert the operator sells airtime for.
//
// THE TABLE HAS EXISTED SINCE MIGRATION 1 AND HAD NO READER AND NO WRITER --
// grep the repository before this and there is no INSERT INTO ads and no SELECT
// FROM ads anywhere. Adverts came from JockPack TOML loaded at station start.
// This is the store layer that never existed.
//
// jock_id, station_id and wav_path stay NULL and are deliberately absent from
// this struct: adverts are global, and a column in the struct is a column
// somebody eventually fills.
type Ad struct {
	ID int64 `json:"id"`
	// Brand is the name the advert says.
	Brand string `json:"brand"`
	// Brief is what the operator typed about the product; the model writes the
	// script from it.
	Brief string `json:"brief,omitempty"`
	// Delivery is how they asked for it to be read.
	Delivery string `json:"delivery,omitempty"`
	// Script is the text column, which already existed. There is no second
	// column for it.
	Script string `json:"script"`
	// LastAiredAt is the ZERO TIME when never aired, not the epoch: an advert
	// written a minute ago and one aired in 1970 must not sort together in the
	// rotation.
	LastAiredAt time.Time `json:"last_aired_at,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	// Enabled is the pause switch. NOT omitempty and NOT a pointer: false is
	// the interesting value here, and a flag that disappears from the JSON when
	// it is off is a flag the console cannot render.
	Enabled bool `json:"enabled"`
}

const adColumns = `id, brand, brief, delivery, text, last_aired_at, created_at, enabled`

// CreateAd stores an advert and returns its id.
func (s *Store) CreateAd(ctx context.Context, ad Ad) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO ads (brand, brief, delivery, text, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		nullable(ad.Brand), nullable(ad.Brief), nullable(ad.Delivery),
		ad.Script, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("store: creating advert %q: %w", ad.Brand, err)
	}
	return idOf(res, ad.Brand)
}

// GetAd reads one advert.
func (s *Store) GetAd(ctx context.Context, id int64) (Ad, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+adColumns+` FROM ads WHERE id = ?`, id)
	ad, err := scanAd(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Ad{}, ErrNotFound
	}
	if err != nil {
		return Ad{}, fmt.Errorf("store: reading advert %d: %w", id, err)
	}
	return ad, nil
}

// ListAds returns every advert, NEWEST FIRST.
//
// Ordered in the SQL rather than left to the table's own order, because the
// console shows this list on a poll: an unordered query is free to reshuffle
// under the operator's cursor every few seconds. Newest first because the
// advert somebody just wrote is the one they are looking for. The id is the
// tie-break, since two adverts written in the same second are common when a
// pack is imported.
func (s *Store) ListAds(ctx context.Context) ([]Ad, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+adColumns+` FROM ads ORDER BY coalesce(created_at, 0) DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: listing adverts: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []Ad{}
	for rows.Next() {
		ad, err := scanAd(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing adverts: %w", err)
		}
		out = append(out, ad)
	}
	// wrapErr, the idiom the jocks and users stores already use: it returns nil
	// for nil, so an iteration that ended cleanly is not a branch of its own.
	return out, wrapErr(rows.Err(), "listing adverts")
}

// UpdateAd changes what the operator wrote.
//
// created_at is NOT touched: rewriting it on every save would reshuffle the
// newest-first list every time somebody fixed a typo.
func (s *Store) UpdateAd(ctx context.Context, ad Ad) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE ads SET brand = ?, brief = ?, delivery = ?, text = ?
		 WHERE id = ?`,
		nullable(ad.Brand), nullable(ad.Brief), nullable(ad.Delivery), ad.Script, ad.ID)
	if err != nil {
		return fmt.Errorf("store: updating advert %d: %w", ad.ID, err)
	}
	return requireOneRow(res, ad.ID)
}

// SetAdEnabled pauses an advert or puts it back on air.
//
// SEPARATE FROM UpdateAd, for the reason MarkAdAired is separate: the console
// edits by writing the whole card back, so a flag carried on that form would
// re-enable a paused advert the moment somebody fixed a typo in it. One column,
// and only that column.
//
// DISABLING IS NOT DELETING. The copy is hand-written prose, which is exactly
// why an operator with a seasonal advertiser should not have to destroy it.
func (s *Store) SetAdEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE ads SET enabled = ? WHERE id = ?`, enabled, id)
	if err != nil {
		return fmt.Errorf("store: enabling advert %d: %w", id, err)
	}
	return requireOneRow(res, id)
}

// DeleteAd removes an advert, copy and all. SetAdEnabled is the reversible one.
func (s *Store) DeleteAd(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ads WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: deleting advert %d: %w", id, err)
	}
	return requireOneRow(res, id)
}

// MarkAdAired records that it played.
//
// SEPARATE FROM UpdateAd ON PURPOSE. This runs on a station goroutine at the
// moment of airing, while an operator may have the same advert open in the
// console -- and an airing must never write back a script the operator has just
// changed. One column, and only that column.
func (s *Store) MarkAdAired(ctx context.Context, id int64, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE ads SET last_aired_at = ? WHERE id = ?`, at.Unix(), id)
	if err != nil {
		return fmt.Errorf("store: marking advert %d aired: %w", id, err)
	}
	return requireOneRow(res, id)
}

// scanAd reads one row.
func scanAd(sc interface{ Scan(...any) error }) (Ad, error) {
	var ad Ad
	var brand, brief, delivery sql.NullString
	var aired, created sql.NullInt64
	if err := sc.Scan(&ad.ID, &brand, &brief, &delivery, &ad.Script,
		&aired, &created, &ad.Enabled); err != nil {
		return Ad{}, err
	}
	ad.Brand, ad.Brief, ad.Delivery = brand.String, brief.String, delivery.String
	// NULL and 0 both mean never, and both stay the zero time: an advert that
	// predates the created_at column must not claim to have been written in
	// 1970 and sort to the bottom for ever.
	if aired.Valid && aired.Int64 != 0 {
		ad.LastAiredAt = time.Unix(aired.Int64, 0)
	}
	if created.Valid && created.Int64 != 0 {
		ad.CreatedAt = time.Unix(created.Int64, 0)
	}
	return ad, nil
}
