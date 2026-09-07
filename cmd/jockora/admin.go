// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/store"
)

// MinPasswordLength is the shortest password the bootstrap admin may have.
//
// Eight, and no complexity rules. A long passphrase beats a short one with a
// digit stapled on, and rules that reject "correct horse battery staple" while
// accepting "Pa55w0rd!" teach the wrong lesson. There is no rate limit on the
// login form yet, which is why there is a floor at all.
const MinPasswordLength = 8

// passwordFunc supplies the password. A FUNCTION rather than an io.Reader
// because reading it without echo means talking to the terminal, and that has
// to stay out of here: this file is the part that must be testable without a
// pseudo-terminal.
type passwordFunc func() (string, error)

// stdinPassword reads one line. Used when stdin is a pipe -- a script, a test,
// a container entrypoint -- where there is no echo to suppress.
func stdinPassword(r io.Reader) passwordFunc {
	return func() (string, error) {
		line, err := bufio.NewReader(r).ReadString('\n')
		// io.EOF with content is a password typed without a trailing newline,
		// which is what a here-string produces. Only an empty read is a
		// failure worth reporting.
		if err != nil && line == "" {
			return "", fmt.Errorf("reading the password: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
}

// runAdmin is the admin subcommand: account management from the command line.
//
// This exists because accounts are ADMIN-CREATED and there is no
// self-registration, so a fresh install has nobody who can log in to create
// anybody. The command line is the only way in.
func runAdmin(ctx context.Context, args []string, dbPath string, pw passwordFunc, out io.Writer) error {
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: jockora admin create -name NAME")
	}

	fs := flag.NewFlagSet("admin create", flag.ContinueOnError)
	fs.SetOutput(out)
	name := fs.String("name", "", "the account name to create")
	// Defaulted from the environment and overridable here, so the command
	// writes to the same database the server will read.
	db := fs.String("db-path", dbPath, "SQLite database file")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("admin create needs a name: jockora admin create -name NAME")
	}

	password, err := pw()
	if err != nil {
		return err
	}
	// Checked BEFORE the database is opened, so a refused password never
	// creates a file as a side effect of being rejected.
	if len(password) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}

	hash, err := auth.HashPassword(password, auth.DefaultCost)
	if err != nil {
		return err
	}

	s, err := store.Open(ctx, *db)
	if err != nil {
		return err
	}
	defer s.Close() //nolint:errcheck // nothing left to write

	if _, err := s.CreateUser(ctx, *name, hash, auth.RoleAdmin); err != nil {
		return err
	}
	fmt.Fprintf(out, "created admin %q\n", *name)
	return nil
}
