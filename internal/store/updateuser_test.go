// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"errors"
	"testing"
)

// AN ACCOUNT'S NAME AND ROLE WERE FIXED AT CREATION. A typo in a name, or a
// listener who should be an operator, meant deleting the account and building
// it again -- and deleting the last admin is refused, so for a one-admin
// install it meant no route at all.
//
// The dangerous half of this is the ROLE. Demoting the only admin locks
// everybody out of the console permanently, with no way back in short of the
// jockora admin command on the host.
//
// Every test here is TestUpdateUser*, which is the -run pattern for this fix.

func adminAndListener(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	s := userStore(t)
	ctx := context.Background()
	admin, err := s.CreateUser(ctx, "boss", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := s.CreateUser(ctx, "guest", "hash", "listener")
	if err != nil {
		t.Fatal(err)
	}
	return s, admin, listener
}

func TestUpdateUserChangesNameAndRole(t *testing.T) {
	s, _, listener := adminAndListener(t)
	ctx := context.Background()

	if err := s.UpdateUser(ctx, listener, "guest2", "admin"); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	got, err := s.GetUserByID(ctx, listener)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "guest2" || got.Role != "admin" {
		t.Errorf("user = %q/%q, want guest2/admin", got.Name, got.Role)
	}
}

func TestUpdateUserRefusesToDemoteTheLastAdmin(t *testing.T) {
	// THE LOCKOUT. One enabled admin, demoted to listener, and the console has
	// no way in for anybody ever again.
	s := userStore(t)
	ctx := context.Background()
	only, err := s.CreateUser(ctx, "boss", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateUser(ctx, only, "boss", "listener"); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("UpdateUser returned %v, want ErrLastAdmin", err)
	}
	// And it really did not happen.
	got, _ := s.GetUserByID(ctx, only)
	if got.Role != "admin" {
		t.Errorf("role = %q after a refused demotion, want admin", got.Role)
	}
}

func TestUpdateUserAllowsDemotionWhileAnotherAdminRemains(t *testing.T) {
	s, admin, listener := adminAndListener(t)
	ctx := context.Background()
	if err := s.UpdateUser(ctx, listener, "guest", "admin"); err != nil {
		t.Fatal(err)
	}
	// Two admins now, so stepping one down leaves a way in.
	if err := s.UpdateUser(ctx, admin, "boss", "listener"); err != nil {
		t.Errorf("UpdateUser: %v", err)
	}
}

func TestUpdateUserRenamingAnAdminIsNotADemotion(t *testing.T) {
	// The guard must key on the ROLE CHANGE, not on touching an admin at all:
	// renaming the only admin is safe and must be allowed.
	s := userStore(t)
	ctx := context.Background()
	only, err := s.CreateUser(ctx, "boss", "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateUser(ctx, only, "chief", "admin"); err != nil {
		t.Errorf("renaming the only admin was refused: %v", err)
	}
}

func TestUpdateUserRefusesADuplicateName(t *testing.T) {
	s, _, listener := adminAndListener(t)
	if err := s.UpdateUser(context.Background(), listener, "boss", "listener"); !errors.Is(err, ErrUserExists) {
		t.Errorf("UpdateUser returned %v, want ErrUserExists", err)
	}
}

func TestUpdateUserOnAMissingAccountIsNotFound(t *testing.T) {
	s, _, _ := adminAndListener(t)
	if err := s.UpdateUser(context.Background(), 9999, "ghost", "listener"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateUser returned %v, want ErrNotFound", err)
	}
}
