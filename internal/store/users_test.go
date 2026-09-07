// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func userStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUsersCreateAndGet(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	id, err := s.CreateUser(ctx, "andrew", "hash-1", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("CreateUser returned id 0")
	}

	byName, err := s.GetUserByName(ctx, "andrew")
	if err != nil {
		t.Fatal(err)
	}
	byID, err := s.GetUserByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []User{byName, byID} {
		if u.ID != id || u.Name != "andrew" || u.PwHash != "hash-1" || u.Role != "admin" {
			t.Errorf("got %+v", u)
		}
		if u.Disabled {
			t.Error("a new user is disabled")
		}
		if u.CreatedAt.IsZero() {
			t.Error("CreatedAt is zero")
		}
	}

	if _, err := s.GetUserByName(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUserByName(nobody) = %v, want ErrNotFound", err)
	}
	if _, err := s.GetUserByID(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUserByID(9999) = %v, want ErrNotFound", err)
	}
}

func TestUsersNameUnique(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "andrew", "h", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "andrew", "h2", "listener"); !errors.Is(err, ErrUserExists) {
		t.Errorf("a duplicate name gave %v, want ErrUserExists", err)
	}
	// A role outside the CHECK must surface as an error, not a panic.
	if _, err := s.CreateUser(ctx, "dj", "h", "dj"); err == nil {
		t.Error("role 'dj' was accepted")
	}
}

func TestUsersDisableEnable(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	// Two admins, so disabling one is allowed.
	if _, err := s.CreateUser(ctx, "a", "h", "admin"); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateUser(ctx, "b", "h", "admin")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DisableUser(ctx, id); err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUserByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Disabled {
		t.Error("the user is not disabled")
	}

	if err := s.EnableUser(ctx, id); err != nil {
		t.Fatal(err)
	}
	if u, _ = s.GetUserByID(ctx, id); u.Disabled {
		t.Error("the user is still disabled after EnableUser")
	}

	if err := s.DisableUser(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("disabling a missing user = %v, want ErrNotFound", err)
	}
	if err := s.EnableUser(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("enabling a missing user = %v, want ErrNotFound", err)
	}
}

func TestUsersDelete(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "keeper", "h", "admin"); err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateUser(ctx, "goer", "h", "listener")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUser(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUserByName(ctx, "goer"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the deleted user is still there: %v", err)
	}
	if err := s.DeleteUser(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a missing user = %v, want ErrNotFound", err)
	}
}

// TestUsersList also pins that the hash is NOT serialised. A user struct
// reaching an API response with its bcrypt hash attached is an offline attack
// handed out over HTTP.
func TestUsersList(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	for _, name := range []string{"carol", "alice", "bob"} {
		if _, err := s.CreateUser(ctx, name, "secret-hash", "listener"); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d users, want 3", len(list))
	}
	for i, want := range []string{"alice", "bob", "carol"} {
		if list[i].Name != want {
			t.Errorf("position %d is %q, want %q -- the list is not ordered by name", i, list[i].Name, want)
		}
	}

	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-hash") {
		t.Error("the password hash is serialised; tag it json:\"-\"")
	}
}

// TestUsersCountAdmins counts ENABLED admins only. A disabled admin cannot log
// in, so counting one would let the last usable admin be removed.
func TestUsersCountAdmins(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "a1", "h", "admin"); err != nil {
		t.Fatal(err)
	}
	id2, err := s.CreateUser(ctx, "a2", "h", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "l1", "h", "listener"); err != nil {
		t.Fatal(err)
	}
	if err := s.DisableUser(ctx, id2); err != nil {
		t.Fatal(err)
	}

	n, err := s.CountAdmins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("CountAdmins = %d, want 1 (enabled admins only)", n)
	}
}

// TestUsersCannotDeleteLastAdmin: a locked-out self-hosted server is a
// reinstall, so the rule lives at the lowest layer where no caller can skip it.
func TestUsersCannotDeleteLastAdmin(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	id, err := s.CreateUser(ctx, "only-admin", "h", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "listener", "h", "listener"); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUser(ctx, id); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleting the last admin gave %v, want ErrLastAdmin", err)
	}
	if _, err := s.GetUserByID(ctx, id); err != nil {
		t.Error("the admin was deleted despite the error")
	}

	// With a second admin it is allowed.
	if _, err := s.CreateUser(ctx, "second-admin", "h", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, id); err != nil {
		t.Errorf("deleting one of two admins failed: %v", err)
	}
}

func TestUsersCannotDisableLastAdmin(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	id, err := s.CreateUser(ctx, "only-admin", "h", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DisableUser(ctx, id); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disabling the last admin gave %v, want ErrLastAdmin", err)
	}
	u, err := s.GetUserByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.Disabled {
		t.Error("the admin was disabled despite the error")
	}
}

func TestUsersSetPassword(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	id, err := s.CreateUser(ctx, "andrew", "old-hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(ctx, id, "new-hash"); err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUserByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.PwHash != "new-hash" {
		t.Errorf("PwHash = %q, want new-hash", u.PwHash)
	}
	if err := s.SetPassword(ctx, 9999, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("setting a missing user's password = %v, want ErrNotFound", err)
	}
}

// TestUsersSurfaceDatabaseErrors covers the error paths, which are otherwise
// the least-exercised and most dangerous code here: a swallowed database error
// on DeleteUser looks exactly like a successful delete.
//
// A CLOSED store fails every operation, which is the cheapest way to reach them
// without a fake driver.
func TestUsersSurfaceDatabaseErrors(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := s.CreateUser(ctx, "a", "h", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "b", "h", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	for name, call := range map[string]func() error{
		"CreateUser":     func() error { _, e := s.CreateUser(ctx, "c", "h", "admin"); return e },
		"GetUserByName":  func() error { _, e := s.GetUserByName(ctx, "a"); return e },
		"GetUserByID":    func() error { _, e := s.GetUserByID(ctx, id); return e },
		"ListUsers":      func() error { _, e := s.ListUsers(ctx); return e },
		"CountAdmins":    func() error { _, e := s.CountAdmins(ctx); return e },
		"DisableUser":    func() error { return s.DisableUser(ctx, id) },
		"EnableUser":     func() error { return s.EnableUser(ctx, id) },
		"DeleteUser":     func() error { return s.DeleteUser(ctx, id) },
		"SetPassword":    func() error { return s.SetPassword(ctx, id, "h2") },
		"guardLastAdmin": func() error { return s.guardLastAdmin(ctx, id) },
	} {
		if err := call(); err == nil {
			t.Errorf("%s swallowed a database error and reported success", name)
		}
	}
}

// TestUsersListSurfacesARowError covers the rows.Err() branch, which a closed
// database cannot reach because the query fails before iteration starts.
//
// Cancelling mid-iteration is what a real failure looks like: the connection
// dropping halfway through a large list. Without this branch the caller gets a
// SHORT LIST reported as a complete one.
func TestUsersListSurfacesARowError(t *testing.T) {
	s := userStore(t)
	for i := 0; i < 50; i++ {
		if _, err := s.CreateUser(context.Background(), string(rune('a'+i%26))+string(rune('a'+i/26)), "h", "listener"); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead before the query runs

	if _, err := s.ListUsers(ctx); err == nil {
		t.Error("ListUsers returned a list from a cancelled context")
	}
}

// failingResult is an sql.Result whose RowsAffected fails. Real drivers rarely
// do this, but requireOneRow takes the INTERFACE, so the branch is reachable
// and therefore worth pinning: it decides ErrNotFound versus success.
type failingResult struct{}

func (failingResult) LastInsertId() (int64, error) { return 0, errors.New("no") }
func (failingResult) RowsAffected() (int64, error) { return 0, errors.New("driver lost count") }

func TestUsersRequireOneRowSurfacesACountError(t *testing.T) {
	err := requireOneRow(failingResult{}, 7)
	if err == nil {
		t.Fatal("a failed row count reported success")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("a failed row count was reported as ErrNotFound; that is a different thing")
	}
}

// TestUsersCreateSurfacesALastInsertIdError: the same interface trick for the
// other half of CreateUser.
func TestUsersCreateSurfacesALastInsertIdError(t *testing.T) {
	if _, err := idOf(failingResult{}, "andrew"); err == nil {
		t.Error("a failed LastInsertId reported success")
	}
}

// readOnly makes every WRITE fail while reads keep working, which is the only
// way to reach the write-error branches: a closed store fails the guard first
// and never reaches the statement it is guarding.
func readOnly(t *testing.T, s *Store) {
	t.Helper()
	// One connection, or the pragma lands on a connection the next query does
	// not use.
	s.DB().SetMaxOpenConns(1)
	if _, err := s.DB().Exec(`PRAGMA query_only = 1`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.DB().Exec(`PRAGMA query_only = 0`) })
}

// TestUsersSurfaceWriteErrors: a swallowed failure on DeleteUser or
// SetPassword looks exactly like success, and the caller then tells an operator
// their account is gone when it is not.
func TestUsersSurfaceWriteErrors(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	id, err := s.CreateUser(ctx, "a", "h", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "b", "h", "admin"); err != nil {
		t.Fatal(err)
	}

	readOnly(t, s)

	for name, call := range map[string]func() error{
		"DeleteUser":  func() error { return s.DeleteUser(ctx, id) },
		"DisableUser": func() error { return s.DisableUser(ctx, id) },
		"EnableUser":  func() error { return s.EnableUser(ctx, id) },
		"SetPassword": func() error { return s.SetPassword(ctx, id, "h2") },
		"CreateUser":  func() error { _, e := s.CreateUser(ctx, "c", "h", "admin"); return e },
	} {
		if err := call(); err == nil {
			t.Errorf("%s reported success against a read-only database", name)
		}
	}
}

// TestUsersListSurfacesACorruptRow covers the per-row Scan failure.
//
// SQLite has type AFFINITY, not strict types, so a non-numeric value can sit in
// an INTEGER column and only fails when something tries to read it as a number.
// That is a real corruption shape -- a hand-edited database, a bad restore --
// and a swallowed Scan error would return a SHORT LIST reported as complete,
// which for a user list means an account silently invisible to the operator.
func TestUsersListSurfacesACorruptRow(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, "good", "h", "admin"); err != nil {
		t.Fatal(err)
	}
	// created_at has INTEGER affinity, so this stores TEXT and scanning it into
	// an int64 fails.
	if _, err := s.DB().Exec(
		`INSERT INTO users (name, pw_hash, role, disabled, created_at)
		 VALUES ('corrupt', 'h', 'listener', 0, 'not-a-timestamp')`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ListUsers(ctx); err == nil {
		t.Error("a row that cannot be scanned was silently dropped from the list")
	}
	// The single-row path must report it too rather than returning a zero user.
	if _, err := s.GetUserByName(ctx, "corrupt"); err == nil {
		t.Error("GetUserByName returned a user from an unscannable row")
	}
}

// TestUsersWrapRowsErr covers the iteration-failure path directly.
//
// A connection dying mid-scan cannot be produced on demand without a fake
// driver, so the wrapping is a seam instead: what matters is that a non-nil
// iteration error becomes a package error rather than being dropped, because a
// dropped one turns a TRUNCATED list into an apparently complete one.
func TestUsersWrapRowsErr(t *testing.T) {
	if err := wrapErr(nil, "listing users"); err != nil {
		t.Errorf("wrapErr(nil) = %v, want nil", err)
	}

	underlying := errors.New("connection reset")
	err := wrapErr(underlying, "listing users")
	if err == nil {
		t.Fatal("an iteration failure was dropped; a short list would look complete")
	}
	if !errors.Is(err, underlying) {
		t.Error("the underlying cause was lost in wrapping")
	}
	if !strings.Contains(err.Error(), "listing users") {
		t.Errorf("the error does not say what failed: %v", err)
	}
}

// TestUsersDisabledAdminIsNotAWayIn is the lockout case my other tests missed.
//
// Found by falsification: making the guard count DISABLED admins passed every
// test I had, because none of them had a disabled admin sitting alongside an
// enabled one. That is precisely the configuration where the bug bites -- an
// operator disables an old admin account, then deletes or disables the working
// one, the guard sees "two admins" and allows it, and the server is now a
// reinstall. There is no password reset and nobody to call.
func TestUsersDisabledAdminIsNotAWayIn(t *testing.T) {
	s := userStore(t)
	ctx := context.Background()

	working, err := s.CreateUser(ctx, "working-admin", "h", "admin")
	if err != nil {
		t.Fatal(err)
	}
	retired, err := s.CreateUser(ctx, "retired-admin", "h", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DisableUser(ctx, retired); err != nil {
		t.Fatal(err)
	}

	// One enabled admin and one disabled one. The disabled account cannot log
	// in, so the enabled one is the ONLY way in.
	if err := s.DeleteUser(ctx, working); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("deleting the only enabled admin gave %v, want ErrLastAdmin "+
			"-- a disabled admin was counted as a way back in", err)
	}
	if err := s.DisableUser(ctx, working); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("disabling the only enabled admin gave %v, want ErrLastAdmin", err)
	}

	// The disabled one may still be deleted: removing it changes nothing about
	// how many ways in remain.
	if err := s.DeleteUser(ctx, retired); err != nil {
		t.Errorf("deleting an already-disabled admin was refused: %v", err)
	}
}
