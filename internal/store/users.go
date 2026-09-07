// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrUserExists means the name is taken. Names are the login identifier, so
	// uniqueness is enforced by the schema and reported here rather than left
	// as a raw constraint string.
	ErrUserExists = errors.New("store: a user with that name already exists")

	// ErrLastAdmin refuses to remove the last way in.
	//
	// A self-hosted server with no enabled admin is not a support ticket, it is
	// a reinstall: there is no password reset, no second channel and nobody to
	// call. The rule lives HERE, at the lowest layer, so no future API handler,
	// CLI command or migration can route around it.
	ErrLastAdmin = errors.New("store: refusing to remove the last enabled admin")
)

// User is an account. It never carries a password, only a hash, and this
// package never produces one -- hashing belongs to internal/auth.
type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// PwHash is json:"-" so a User can be handed straight to an API response
	// without leaking it. A bcrypt hash on the wire is an offline attack given
	// away for free.
	PwHash    string    `json:"-"`
	Role      string    `json:"role"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateUser stores an account. The hash is taken as given; see internal/auth.
func (s *Store) CreateUser(ctx context.Context, name, pwHash, role string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (name, pw_hash, role, disabled, created_at) VALUES (?, ?, ?, 0, ?)`,
		name, pwHash, role, time.Now().Unix())
	if err != nil {
		// The schema owns both rules -- UNIQUE on name and the role CHECK -- so
		// a duplicate is recognised by what SQLite says rather than by asking
		// first, which would race.
		if isUniqueViolation(err) {
			return 0, fmt.Errorf("%w: %q", ErrUserExists, name)
		}
		return 0, fmt.Errorf("store: creating user %q: %w", name, err)
	}
	return idOf(res, name)
}

// idOf reads the new row id. Split out so the failure path is reachable from a
// test through the sql.Result interface: a driver that loses the id would
// otherwise return 0 as if it were a real account.
func idOf(res sql.Result, name string) (int64, error) {
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: creating %q: %w", name, err)
	}
	return id, nil
}

func isUniqueViolation(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

const userColumns = `id, name, pw_hash, role, disabled, created_at`

func scanUser(row *sql.Row) (User, error) {
	var u User
	var created int64
	var disabled int
	if err := row.Scan(&u.ID, &u.Name, &u.PwHash, &u.Role, &disabled, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("store: reading user: %w", err)
	}
	u.Disabled = disabled != 0
	u.CreatedAt = time.Unix(created, 0)
	return u, nil
}

// GetUserByName looks an account up by its login name.
func (s *Store) GetUserByName(ctx context.Context, name string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE name = ?`, name))
}

// GetUserByID looks an account up by id.
func (s *Store) GetUserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// ListUsers returns every account, ordered by name so a list is stable between
// calls and readable without sorting at the caller.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("store: listing users: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	out := []User{}
	for rows.Next() {
		var u User
		var created int64
		var disabled int
		if err := rows.Scan(&u.ID, &u.Name, &u.PwHash, &u.Role, &disabled, &created); err != nil {
			return nil, fmt.Errorf("store: listing users: %w", err)
		}
		u.Disabled = disabled != 0
		u.CreatedAt = time.Unix(created, 0)
		out = append(out, u)
	}
	// rows.Err() reports a failure DURING iteration -- a connection dropped
	// halfway through -- which is the difference between a short list and a
	// complete one. Reaching it from a test needs the connection to die at a
	// precise moment, so the wrapping lives behind a seam a test can call
	// directly rather than behind a race.
	return out, wrapErr(rows.Err(), "listing users")
}

// wrapErr turns an iteration failure into a package error, and nil into
// nil, so a caller cannot mistake a truncated list for a whole one.
// wrapErr names the operation on a failure that a working database does not
// produce -- a rows error mid-iteration, a commit that does not land. Split out
// so those paths are reachable from a test by calling it directly.
func wrapErr(err error, what string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("store: %s: %w", what, err)
}

// CountAdmins counts admins who can actually log in.
//
// ENABLED ONLY. A disabled admin is not a way back in, so counting one would
// let the last usable account be removed while the guard looked satisfied.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: counting admins: %w", err)
	}
	return n, nil
}

// DisableUser stops an account logging in, refusing if it is the last admin.
func (s *Store) DisableUser(ctx context.Context, id int64) error {
	if err := s.guardLastAdmin(ctx, id); err != nil {
		return err
	}
	return s.setDisabled(ctx, id, true)
}

// EnableUser lets an account log in again.
func (s *Store) EnableUser(ctx context.Context, id int64) error {
	return s.setDisabled(ctx, id, false)
}

func (s *Store) setDisabled(ctx context.Context, id int64, disabled bool) error {
	v := 0
	if disabled {
		v = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, v, id)
	if err != nil {
		return fmt.Errorf("store: updating user %d: %w", id, err)
	}
	return requireOneRow(res, id)
}

// DeleteUser removes an account, refusing if it is the last admin.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	if err := s.guardLastAdmin(ctx, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: deleting user %d: %w", id, err)
	}
	return requireOneRow(res, id)
}

// SetPassword replaces the stored hash.
func (s *Store) SetPassword(ctx context.Context, id int64, pwHash string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET pw_hash = ? WHERE id = ?`, pwHash, id)
	if err != nil {
		return fmt.Errorf("store: setting password for user %d: %w", id, err)
	}
	return requireOneRow(res, id)
}

// guardLastAdmin refuses to remove the only enabled admin.
//
// It checks the TARGET as well as the count: removing a listener, or an admin
// who is already disabled, changes nothing about how many ways in remain.
func (s *Store) guardLastAdmin(ctx context.Context, id int64) error {
	// ONE query, not a lookup followed by a count. Two queries would leave an
	// error branch reachable only by a database that answers the first and
	// fails the second, which is a fixture nobody can write honestly -- and an
	// untestable branch guarding a lockout rule is worse than no branch.
	var name, role string
	var disabled, admins int
	err := s.db.QueryRowContext(ctx, `
		SELECT u.name, u.role, u.disabled,
		       (SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0)
		  FROM users u WHERE u.id = ?`, id).Scan(&name, &role, &disabled, &admins)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: checking admins before touching user %d: %w", id, err)
	}

	// Removing a listener, or an admin who is already disabled, changes nothing
	// about how many ways in remain.
	if role != "admin" || disabled != 0 {
		return nil
	}
	if admins <= 1 {
		return fmt.Errorf("%w: %q is the only one", ErrLastAdmin, name)
	}
	return nil
}

// requireOneRowByID is requireOneRow for a string key.
func requireOneRowByID(res sql.Result, id string) error {
	return requireOneRowNamed(res, id)
}

// requireOneRow turns "no rows changed" into ErrNotFound, so a caller can tell
// a missing account from a successful no-op.
func requireOneRow(res sql.Result, id int64) error {
	return requireOneRowNamed(res, fmt.Sprintf("%d", id))
}

func requireOneRowNamed(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: row %s: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
