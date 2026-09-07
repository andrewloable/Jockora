// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/andrewloable/jockora/internal/store"
)

// SeedJocks copies persona cards from disk into the jocks table, ONCE EACH.
//
// INSERT WHERE ABSENT, NEVER OVERWRITE. The cards are a seed, not a source of
// truth: once an operator has edited a jock in the console, re-seeding must not
// quietly restore the shipped text on the next restart. An id already in the
// table is skipped entirely.
//
// A malformed card is SKIPPED rather than fatal. One bad file in a directory of
// nine must not leave a station with no jocks at all, so the error is collected
// and the rest are still inserted -- the count says what got in and the error
// says what did not.
func SeedJocks(ctx context.Context, s *store.Store, dir string) (int, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return 0, fmt.Errorf("dj: looking for persona cards in %s: %w", dir, err)
	}

	existing, err := s.ListJocks(ctx)
	if err != nil {
		return 0, err
	}
	known := make(map[string]bool, len(existing))
	for _, j := range existing {
		known[j.ID] = true
	}

	inserted := 0
	var problems []error
	for _, path := range paths {
		p, err := LoadPersona(path)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if known[p.ID()] {
			continue
		}
		if err := s.UpsertJock(ctx, p.Record()); err != nil {
			problems = append(problems, err)
			continue
		}
		known[p.ID()] = true
		inserted++
	}
	return inserted, errors.Join(problems...)
}
