// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"

	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/store"
)

// SeedSources turns the v0.1 command line into the first sources row, ONCE.
//
// A v0.1 config has to keep working unchanged across the upgrade, so the flags
// that used to drive the scan now seed a row instead. An EMPTY TABLE is the only
// trigger, disabled rows included: an operator who turned their only source off
// must not find it replaced by an old flag on the next restart.
//
// Neither flag set is NOT an error. Once sources are managed in the console
// that is the normal way to start, and refusing to boot would make the console
// unreachable exactly when it is needed to add one.
func SeedSources(ctx context.Context, s *store.Store, cfg *config.Config) (int, error) {
	existing, err := s.ListSources(ctx)
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		return 0, nil
	}

	var src store.Source
	switch {
	// Subsonic wins, exactly as librarySource in cmd/jockora/serve.go already
	// decides it. If the two disagreed, the first run after upgrading would
	// scan a different library than the last run before it.
	case cfg.SubsonicURL != "":
		src = store.Source{
			Kind:     store.SourceSubsonic,
			Locator:  cfg.SubsonicURL,
			Username: cfg.SubsonicUser,
			Password: cfg.SubsonicPassword,
			Enabled:  true,
		}
	case cfg.LibraryPath != "":
		src = store.Source{Kind: store.SourceFolder, Locator: cfg.LibraryPath, Enabled: true}
	default:
		return 0, nil
	}

	if _, err := s.CreateSource(ctx, src); err != nil {
		return 0, err
	}
	return 1, nil
}
