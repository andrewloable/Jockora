// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"
	"time"
)

// Sessions that have been signed out.
//
// A session is a stateless HMAC token with a thirty-day life, so clearing the
// cookie ends it on that device and nowhere else: a copied cookie goes on
// authenticating until the token expires. This is the list that stops it.
//
// KEYED ON THE SID, which the token already carries to tell one sign-in from
// another, so signing out on a phone does not sign out the tablet. That
// distinction is the presence model's, not this table's invention.

// RevokeSession marks one sign-in as over, until the token would have expired.
//
// IDEMPOTENT: pressing sign out twice, or retrying, must not fail. A logout
// that errors is a logout nobody trusts.
func (s *Store) RevokeSession(ctx context.Context, sid string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO revoked_sessions (sid, expires) VALUES (?, ?)
		ON CONFLICT(sid) DO UPDATE SET expires = excluded.expires`,
		sid, expires.Unix())
	if err != nil {
		return fmt.Errorf("store: revoking session: %w", err)
	}
	return nil
}

// SessionRevoked reports whether this sign-in has been signed out.
//
// NO CLOCK. The expires column is for SWEEPING, not for answering: a revoked
// token that has expired cannot verify either, so the row's presence is the
// whole answer and reading a clock here only invents a way to disagree with
// the one that signed the token. It did: the server can run on an injected
// clock, and comparing a fake expiry against the wall clock made a
// just-revoked session read as still valid.
func (s *Store) SessionRevoked(ctx context.Context, sid string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM revoked_sessions WHERE sid = ?`, sid).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: reading revoked sessions: %w", err)
	}
	return n > 0, nil
}

// SweepRevokedSessions drops revocations whose tokens have expired anyway.
//
// The row only has to outlive the token it revokes. Without this the table
// grows one row per sign-out for the life of the install, and is never read.
func (s *Store) SweepRevokedSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM revoked_sessions WHERE expires <= ?`, now.Unix())
	if err != nil {
		return fmt.Errorf("store: sweeping revoked sessions: %w", err)
	}
	return nil
}
