// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package library_test holds the tests that reach UP the dependency chain.
// internal/server imports internal/library for the progress type, so a test of
// the two together cannot live inside package library without a cycle.
package library_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/library"
	"github.com/andrewloable/jockora/internal/server"
)

// TestRescanSurfacesOnNow: the page already polls /now.json, and a second
// endpoint is a second thing to break.
func TestRescanSurfacesOnNow(t *testing.T) {
	prog := library.RescanProgress{Running: true, Found: 42, Skipped: 7, Missing: 1,
		StartedAt: time.Unix(1_700_000_000, 0)}
	st := server.Status{Rescan: &prog}

	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"rescan"`, `"found":42`, `"running":true`, `"missing":1`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("/now.json is missing %s: %s", want, raw)
		}
	}

	// Absent when there has never been one, so an operator who has not
	// rescanned does not see an empty progress bar claiming zero of zero.
	raw, err = json.Marshal(server.Status{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "rescan") {
		t.Errorf("a station that never rescanned reports one: %s", raw)
	}
}
