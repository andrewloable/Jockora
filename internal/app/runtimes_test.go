// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// The station manager reaches the server as a Runtimes INTERFACE.
//
// That makes the wiring order load-bearing in a way nothing about the types
// says out loud: a nil *station.Manager put into an interface is a NON-NIL
// interface holding a nil pointer, so the handler's `if s.runtimes != nil`
// guard passes and the very next line panics. New used to pass a.mgr to
// SetStations eleven lines before assigning it, so DELETE /admin/stations/N and
// the disable toggle both killed the request -- in every library deployment,
// on the first station an operator tried to remove.
//
// These drive the REAL handler rather than asserting on a field, because the
// field being non-nil is exactly the thing that was true while it was broken.

func runtimesApp(t *testing.T) (*App, func()) {
	t.Helper()

	path := v01Fixture(t)
	s := openUpgraded(t, path)
	ctx := context.Background()

	pool, err := station.PlayablePool(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	cfg.LibraryPath, cfg.PersonaPath = "/music", personaDir

	a, err := New(cfg, Options{
		Library:  &Library{Store: s, Selector: station.NewSelector(pool, 1)},
		Personas: roster(t),
		Log:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// New spawns the encoder under its own context, so it is run and stopped
	// rather than dropped on the floor.
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	return a, func() { cancel(); <-done }
}

// admin signs in and returns the cookie every guarded call needs.
func runtimesAdmin(t *testing.T, a *App, s *store.Store) *http.Cookie {
	t.Helper()

	// bcrypt.MinCost: this test signs in, it does not measure hashing, and the
	// real cost of 12 is a tenth of a second per call.
	hash, err := auth.HashPassword("correct horse battery", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}
	if _, err := s.CreateUser(context.Background(), "op", hash, auth.RoleAdmin); err != nil {
		t.Fatalf("creating the admin: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login",
		bytes.NewBufferString(`{"name":"op","password":"correct horse battery"}`))
	a.srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("signing in: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Value != "" {
			return c
		}
	}
	t.Fatal("signing in set no cookie")
	return nil
}

func runtimesDo(t *testing.T, a *App, method, path string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, http.NoBody)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	// A panic here is the bug. Without this the test still fails, but as a
	// process-wide crash whose message says nothing about stations.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s %s PANICKED: %v\n"+
				"the station manager reached the server as a nil pointer in a non-nil interface",
				method, path, r)
		}
	}()
	a.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestRuntimesDisableAStationDoesNotPanic(t *testing.T) {
	a, stop := runtimesApp(t)
	defer stop()
	cookie := runtimesAdmin(t, a, a.opts.Library.Store)

	sts, err := a.opts.Library.Store.ListStations(context.Background())
	if err != nil || len(sts) == 0 {
		t.Fatalf("no seeded station to disable: %v", err)
	}
	id := sts[0].ID

	rec := runtimesDo(t, a, http.MethodPost, "/admin/stations/1/disable", cookie)
	if rec.Code >= 500 {
		t.Errorf("POST /admin/stations/%d/disable = %d %s", id, rec.Code, rec.Body)
	}
}

func TestRuntimesDeleteAStationDoesNotPanic(t *testing.T) {
	a, stop := runtimesApp(t)
	defer stop()
	cookie := runtimesAdmin(t, a, a.opts.Library.Store)

	rec := runtimesDo(t, a, http.MethodDelete, "/admin/stations/1", cookie)
	if rec.Code >= 500 {
		t.Errorf("DELETE /admin/stations/1 = %d %s", rec.Code, rec.Body)
	}
}
