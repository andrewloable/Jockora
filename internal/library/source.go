// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package library

import (
	"context"

	"github.com/andrewloable/jockora/internal/store"
)

// Source is a library Jockora can read.
//
// READ-ONLY BY CONSTRUCTION, which is the point of the interface rather than a
// property of its implementations: there is no method here that writes, renames
// or retags, so no provider can acquire one without changing this file. Jockora
// layers on top of a library somebody already has -- Navidrome, Jellyfin, a
// folder -- and must never become a competing writer to it.
//
// A Source enumerates INTO THE STORE rather than returning tracks, because the
// store is what deduplicates, preserves enrichment across rescans, and lets a
// scan be resumed. Two providers producing rows the same way is what makes the
// rest of the system indifferent to where music came from.
type Source interface {
	// Name identifies the source in logs and in the doctor's output.
	Name() string

	// Scan brings the store up to date with the library. It is idempotent:
	// running it twice must not duplicate rows or discard dossiers.
	Scan(ctx context.Context, s *store.Store, progress ScanProgress) (Stats, error)
}

// Folder is the local-directory Source: the original scanner, behind the
// interface.
type Folder struct{ Root string }

func (f Folder) Name() string { return "folder " + f.Root }

func (f Folder) Scan(ctx context.Context, s *store.Store, progress ScanProgress) (Stats, error) {
	return ScanWithProgress(ctx, s, f.Root, progress)
}

// compile-time proof that the local scanner satisfies the interface a remote
// provider has to meet.
var _ Source = Folder{}
