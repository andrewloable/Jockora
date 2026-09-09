// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Jockora-e9a.66. Sessions are STATELESS HMAC tokens with a thirty-day life,
// so clearing the cookie ends the session on that device and nowhere else: a
// copied cookie went on authenticating for up to a month after sign-out.
//
// Revoked BY SID, which the token already carries to tell one sign-in from
// another. Revoking the user instead would sign out the household's other
// device, which the presence model exists to keep separate.

func TestRevokedSessionStopsAuthenticating(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "r.db"))
	ctx := context.Background()
	until := time.Now().Add(24 * time.Hour)

	if yes, err := s.SessionRevoked(ctx, "sid-a"); err != nil || yes {
		t.Fatalf("SessionRevoked = %v, %v before anything was revoked", yes, err)
	}
	if err := s.RevokeSession(ctx, "sid-a", until); err != nil {
		t.Fatal(err)
	}
	if yes, err := s.SessionRevoked(ctx, "sid-a"); err != nil || !yes {
		t.Errorf("SessionRevoked = %v, %v after revoking it", yes, err)
	}
	// ONE SIGN-IN, NOT ONE ACCOUNT. The other device stays signed in.
	if yes, err := s.SessionRevoked(ctx, "sid-b"); err != nil || yes {
		t.Errorf("revoking one session revoked another: %v, %v", yes, err)
	}
}

// TestRevokedSessionIsIdempotent: pressing sign out twice, or a retry, must not
// fail. A logout that errors is a logout the operator does not trust.
func TestRevokedSessionIsIdempotent(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "r.db"))
	ctx := context.Background()
	until := time.Now().Add(time.Hour)
	if err := s.RevokeSession(ctx, "sid-a", until); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(ctx, "sid-a", until); err != nil {
		t.Errorf("revoking twice: %v", err)
	}
}

// TestRevokedSessionForgetsExpiredEntries: the row only has to outlive the
// token it revokes. Keeping them for ever would grow a table that is never
// read, one row per sign-out, for the life of the install.
func TestRevokedSessionForgetsExpiredEntries(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "r.db"))
	ctx := context.Background()
	if err := s.RevokeSession(ctx, "old", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(ctx, "live", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// The read consults no clock -- see SessionRevoked -- so an expired row
	// still answers yes until it is swept, which is harmless: the token it
	// names cannot verify either.
	if yes, _ := s.SessionRevoked(ctx, "old"); !yes {
		t.Error("a revocation stopped answering before it was swept")
	}
	if err := s.SweepRevokedSessions(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM revoked_sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d rows left after the sweep, want only the live one", n)
	}
	if yes, _ := s.SessionRevoked(ctx, "live"); !yes {
		t.Error("the sweep took a revocation that had not expired")
	}
	if yes, _ := s.SessionRevoked(ctx, "old"); yes {
		t.Error("the swept revocation is still there")
	}
}

func TestRevokedSessionReportsADatabaseThatWillNotAnswer(t *testing.T) {
	s := openAt(t, filepath.Join(t.TempDir(), "r.db"))
	ctx := context.Background()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeSession(ctx, "sid", time.Now()); err == nil {
		t.Error("RevokeSession reported success against a closed database")
	}
	if _, err := s.SessionRevoked(ctx, "sid"); err == nil {
		t.Error("SessionRevoked reported success against a closed database")
	}
	if err := s.SweepRevokedSessions(ctx, time.Now()); err == nil {
		t.Error("SweepRevokedSessions reported success against a closed database")
	}
}
