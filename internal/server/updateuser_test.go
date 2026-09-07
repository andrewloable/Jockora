// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/store"
)

// putUser is a signed-in PUT to one account.
func putUser(t *testing.T, s *Server, c *http.Cookie, id int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	return as(t, s, http.MethodPut, "/admin/users/"+strconv.FormatInt(id, 10), body, c)
}

// idOfUser finds an account by name, so a test names people rather than ids.
func idOfUser(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	u, err := st.GetUserByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// PUT /admin/users/{id} did not exist: a name or role was fixed at creation,
// and the only way to change either was to delete the account and build it
// again -- which for the last admin is refused outright.
//
// Every test here is TestUpdateUserAPI*, which is the -run pattern for this fix.

func TestUpdateUserAPIChangesNameAndRole(t *testing.T) {
	s, st, _ := authServer(t)
	admin := adminCookie(t, s)
	rec := putUser(t, s, admin, idOfUser(t, st, "listener"), `{"name":"kim","role":"admin"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d, want 204: %s", rec.Code, rec.Body)
	}
	u, err := st.GetUserByName(context.Background(), "kim")
	if err != nil || u.Role != auth.RoleAdmin {
		t.Errorf("stored %+v (%v)", u, err)
	}
}

func TestUpdateUserAPIRefusesAnInvalidRole(t *testing.T) {
	s, st, _ := authServer(t)
	rec := putUser(t, s, adminCookie(t, s), idOfUser(t, st, "listener"), `{"name":"kim","role":"wizard"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "role") {
		t.Errorf("body = %s, want it to name the field", rec.Body)
	}
}

func TestUpdateUserAPIRefusesAnEmptyName(t *testing.T) {
	s, st, _ := authServer(t)
	if rec := putUser(t, s, adminCookie(t, s), idOfUser(t, st, "listener"), `{"name":"  ","role":"listener"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT = %d, want 400", rec.Code)
	}
}

func TestUpdateUserAPIRefusesADuplicateName(t *testing.T) {
	s, st, _ := authServer(t)
	if rec := putUser(t, s, adminCookie(t, s), idOfUser(t, st, "listener"), `{"name":"andrew","role":"listener"}`); rec.Code != http.StatusConflict {
		t.Errorf("PUT = %d, want 409", rec.Code)
	}
}

func TestUpdateUserAPIRefusesSelfDemotion(t *testing.T) {
	// THE SAME LOCKOUT AS SELF-DISABLE, which this server already refuses: an
	// operator who makes themselves a listener cannot reach the console that
	// would put them back. The last-admin rule does not catch it while another
	// admin exists, which is exactly when somebody would try it.
	s, st, _ := authServer(t)
	rec := putUser(t, s, adminCookie(t, s), idOfUser(t, st, "andrew"), `{"name":"andrew","role":"listener"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT = %d, want 409: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "your own") {
		t.Errorf("body = %s, want it to say whose account it is", rec.Body)
	}
}

func TestUpdateUserAPIAllowsRenamingYourself(t *testing.T) {
	// Renaming is not demoting, and refusing it would be the guard overreaching.
	s, st, _ := authServer(t)
	if rec := putUser(t, s, adminCookie(t, s), idOfUser(t, st, "andrew"), `{"name":"chief","role":"admin"}`); rec.Code != http.StatusNoContent {
		t.Errorf("PUT = %d, want 204: %s", rec.Code, rec.Body)
	}
}

func TestUpdateUserAPIRefusesDemotingTheLastAdmin(t *testing.T) {
	// Not self-demotion: a SECOND admin demoted by the first, down to one, and
	// then that one demoted -- which only the store's last-admin rule refuses.
	s, st, _ := authServer(t)
	admin := adminCookie(t, s)
	other := idOfUser(t, st, "listener")
	if rec := putUser(t, s, admin, other, `{"name":"listener","role":"admin"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("promoting = %d", rec.Code)
	}
	if rec := putUser(t, s, admin, other, `{"name":"listener","role":"listener"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("demoting one of two admins = %d", rec.Code)
	}
	// One admin left, and it is the caller, so BOTH guards would refuse. The
	// self rule answers first; the store rule is proved directly in
	// TestUpdateUserRefusesToDemoteTheLastAdmin.
	if rec := putUser(t, s, admin, idOfUser(t, st, "andrew"), `{"name":"andrew","role":"listener"}`); rec.Code != http.StatusConflict {
		t.Errorf("demoting the last admin = %d, want 409", rec.Code)
	}
}

func TestUpdateUserAPIOnAMissingAccountIsNotFound(t *testing.T) {
	s, _, _ := authServer(t)
	if rec := putUser(t, s, adminCookie(t, s), 9999, `{"name":"ghost","role":"listener"}`); rec.Code != http.StatusNotFound {
		t.Errorf("PUT = %d, want 404", rec.Code)
	}
}

func TestUpdateUserAPIRejectsAMalformedBody(t *testing.T) {
	s, st, _ := authServer(t)
	if rec := putUser(t, s, adminCookie(t, s), idOfUser(t, st, "listener"), `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("PUT = %d, want 400", rec.Code)
	}
}

func TestUpdateUserAPIIsAdminOnly(t *testing.T) {
	s, st, _ := authServer(t)
	if rec := putUser(t, s, listenerCookie(t, s), idOfUser(t, st, "andrew"), `{"name":"x","role":"listener"}`); rec.Code != http.StatusForbidden {
		t.Errorf("a listener got %d, want 403", rec.Code)
	}
}
