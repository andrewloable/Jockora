// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/store"
)

func adminDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "j.db")
}

// users opens the database the subcommand wrote and reads it back. Asserting on
// the command's own output would prove only that it prints well.
func users(t *testing.T, path string) []store.User {
	t.Helper()
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestAdminCreateInsertsAdmin(t *testing.T) {
	db := adminDB(t)
	var out bytes.Buffer

	err := runAdmin(context.Background(), []string{"create", "-name", "andrew"}, db,
		stdinPassword(strings.NewReader("correct horse battery\n")), &out)
	if err != nil {
		t.Fatal(err)
	}

	list := users(t, db)
	if len(list) != 1 {
		t.Fatalf("%d users, want 1", len(list))
	}
	if list[0].Name != "andrew" {
		t.Errorf("created %q, want andrew", list[0].Name)
	}
	// ADMIN, not listener. An account created from the console command line is
	// the bootstrap operator; if it came out a listener there would be no way
	// to reach the console at all.
	if list[0].Role != auth.RoleAdmin {
		t.Errorf("role = %q, want %q", list[0].Role, auth.RoleAdmin)
	}
	if list[0].Disabled {
		t.Error("the bootstrap admin was created disabled")
	}

	// The stored value must be a HASH that verifies, never the password.
	if list[0].PwHash == "correct horse battery" {
		t.Fatal("the password was stored verbatim")
	}
	if !auth.CheckPassword(list[0].PwHash, "correct horse battery") {
		t.Error("the stored hash does not verify the password that was typed")
	}
	if auth.CheckPassword(list[0].PwHash, "something else") {
		t.Error("the stored hash verifies the wrong password")
	}
	if !strings.Contains(out.String(), "andrew") {
		t.Errorf("nothing said what was created: %q", out.String())
	}
	// The password must not be echoed back in the confirmation either.
	if strings.Contains(out.String(), "correct horse") {
		t.Errorf("the password was printed: %q", out.String())
	}
}

// TestAdminCreateTrimsTheTypedLine: a terminal sends the newline, and a
// password with a trailing \r or \n would be unreproducible at the login form.
func TestAdminCreateTrimsTheTypedLine(t *testing.T) {
	db := adminDB(t)
	var out bytes.Buffer
	if err := runAdmin(context.Background(), []string{"create", "-name", "andrew"}, db,
		stdinPassword(strings.NewReader("correct horse battery\r\n")), &out); err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword(users(t, db)[0].PwHash, "correct horse battery") {
		t.Error("the line ending became part of the password")
	}
}

func TestAdminCreateRefusesEmptyPassword(t *testing.T) {
	db := adminDB(t)
	var out bytes.Buffer
	err := runAdmin(context.Background(), []string{"create", "-name", "andrew"}, db,
		stdinPassword(strings.NewReader("\n")), &out)
	if err == nil {
		t.Fatal("an empty password was accepted")
	}
	if len(users(t, db)) != 0 {
		t.Error("a row was written for a refused password")
	}
}

func TestAdminCreateRefusesShortPassword(t *testing.T) {
	db := adminDB(t)
	var out bytes.Buffer
	err := runAdmin(context.Background(), []string{"create", "-name", "andrew"}, db,
		stdinPassword(strings.NewReader("short\n")), &out)
	if err == nil {
		t.Fatal("a five character password was accepted")
	}
	if !strings.Contains(err.Error(), "8") {
		t.Errorf("the error does not say how long it must be: %v", err)
	}
	if len(users(t, db)) != 0 {
		t.Error("a row was written for a refused password")
	}
}

func TestAdminCreateRefusesDuplicate(t *testing.T) {
	db := adminDB(t)
	var out bytes.Buffer
	first := func() error {
		return runAdmin(context.Background(), []string{"create", "-name", "andrew"}, db,
			stdinPassword(strings.NewReader("correct horse battery\n")), &out)
	}
	if err := first(); err != nil {
		t.Fatal(err)
	}
	err := first()
	if err == nil {
		t.Fatal("the same name was created twice")
	}
	if !errors.Is(err, store.ErrUserExists) {
		t.Errorf("error = %v, want ErrUserExists", err)
	}
	if len(users(t, db)) != 1 {
		t.Error("the duplicate was written anyway")
	}
}

func TestAdminCreateNeedsAName(t *testing.T) {
	db := adminDB(t)
	var out bytes.Buffer
	if err := runAdmin(context.Background(), []string{"create"}, db,
		stdinPassword(strings.NewReader("correct horse battery\n")), &out); err == nil {
		t.Fatal("an admin was created with no name")
	}
	if len(users(t, db)) != 0 {
		t.Error("a nameless row was written")
	}
}

// TestAdminCreateRejectsUnknownActions: "admin" on its own, and any verb that
// is not create, must be a usage error rather than a silent no-op that leaves
// the operator thinking an account exists.
func TestAdminCreateRejectsUnknownActions(t *testing.T) {
	db := adminDB(t)
	for _, args := range [][]string{nil, {"delete"}, {"-name", "andrew"}} {
		var out bytes.Buffer
		if err := runAdmin(context.Background(), args, db,
			stdinPassword(strings.NewReader("correct horse battery\n")), &out); err == nil {
			t.Errorf("jockora admin %v was accepted", args)
		}
	}
}

func TestAdminCreateSurfacesFailures(t *testing.T) {
	var out bytes.Buffer
	ctx := context.Background()

	t.Run("bad flag", func(t *testing.T) {
		if err := runAdmin(ctx, []string{"create", "-nope"}, adminDB(t),
			stdinPassword(strings.NewReader("correct horse battery\n")), &out); err == nil {
			t.Error("an unknown flag was accepted")
		}
	})

	t.Run("unreadable stdin", func(t *testing.T) {
		if err := runAdmin(ctx, []string{"create", "-name", "andrew"}, adminDB(t),
			stdinPassword(errReader{}), &out); err == nil {
			t.Error("a failed read was treated as a password")
		}
	})

	// bcrypt refuses anything over 72 bytes. It must be REFUSED and said so,
	// never silently truncated: an operator who types a 90 character passphrase
	// and is quietly given a 72 character one has a password that does not
	// match what they wrote down.
	t.Run("password past bcrypt's limit", func(t *testing.T) {
		db := adminDB(t)
		long := strings.Repeat("correct horse ", 8) // 112 bytes
		if err := runAdmin(ctx, []string{"create", "-name", "andrew"}, db,
			stdinPassword(strings.NewReader(long+"\n")), &out); err == nil {
			t.Error("a 112 byte password was accepted")
		}
		if len(users(t, db)) != 0 {
			t.Error("a row was written for a password that could not be hashed")
		}
	})

	t.Run("unopenable database", func(t *testing.T) {
		// A directory where the file should be: open fails rather than
		// reporting an admin that does not exist.
		if err := runAdmin(ctx, []string{"create", "-name", "andrew"}, t.TempDir(),
			stdinPassword(strings.NewReader("correct horse battery\n")), &out); err == nil {
			t.Error("a database that cannot be opened reported success")
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("stdin went away") }
