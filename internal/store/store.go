// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package store owns the SQLite database: the library index, the dossiers that
// cost hours of LLM time to build, and the state the DJ remembers.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure Go, no cgo, BSD-3-Clause
)

// driverName is modernc.org/sqlite's registration.
//
// The driver is pure Go on purpose. mattn/go-sqlite3 is cgo, which breaks
// cross-compilation, and a self-hosted product has to build for a Raspberry Pi
// from a laptop.
const driverName = "sqlite"

// ErrSchemaTooNew means the database was written by a newer Jockora.
var ErrSchemaTooNew = errors.New("store: database schema is newer than this build")

// ErrNotFound means the row asked for is not there.
//
// PACKAGE-WIDE on purpose. Every store added from here on -- users, sources,
// jocks, stations -- reports a missing row the same way, so a caller writes one
// errors.Is and not one per table. The task spec for the users store assumed
// this already existed; it did not, and adding it here rather than privately in
// users.go is what stops the next store inventing a second one.
var ErrNotFound = errors.New("store: not found")

// Store is an open database.
type Store struct {
	db *sql.DB
}

// Open opens or creates the database at path and brings it up to the current
// schema version.
//
// It refuses a database written by a newer Jockora rather than migrating
// backwards or writing into tables whose shape it does not know. Dossiers cost
// hours of LLM time; corrupting them is the most expensive self-inflicted wound
// available here.
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: creating %s: %w", dir, err)
		}
	}

	// Pragmas go in the DSN so every pooled connection gets them. Set per-query
	// instead and a second connection silently ignores foreign keys.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: opening %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: opening %s: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the connection pool for queries that live in other packages.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// SchemaVersion reports the schema version recorded in the database.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("store: reading schema version: %w", err)
	}
	return v, nil
}
