// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/store"
)

// as sends a request signed in as the given cookie.
func as(t *testing.T, s *Server, method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		req.AddCookie(c)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func adminCookie(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	return sessionCookie(t, login(t, s, "andrew", "correct horse battery"))
}

func listenerCookie(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	return sessionCookie(t, login(t, s, "listener", "correct horse battery"))
}

func TestUsersAPICreateAsAdmin(t *testing.T) {
	s, st, _ := authServer(t)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodPost, "/admin/users",
		`{"name":"kim","password":"correct horse battery","role":"listener"}`, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var made struct{ ID int64 }
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil || made.ID == 0 {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}

	u, err := st.GetUserByID(context.Background(), made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "kim" || u.Role != auth.RoleListener || u.Disabled {
		t.Errorf("stored %+v", u)
	}
	if !auth.CheckPassword(u.PwHash, "correct horse battery") {
		t.Error("the stored hash does not verify the password that was set")
	}
	if u.PwHash == "correct horse battery" {
		t.Fatal("the password was stored verbatim")
	}

	// The new account can sign in, which is the whole point of creating it.
	if rec := login(t, s, "kim", "correct horse battery"); rec.Code != http.StatusNoContent {
		t.Errorf("the new account cannot sign in: %d", rec.Code)
	}
	// And a duplicate name is refused, because the name IS the login.
	if rec := as(t, s, http.MethodPost, "/admin/users",
		`{"name":"kim","password":"correct horse battery","role":"listener"}`, admin); rec.Code != http.StatusConflict {
		t.Errorf("a duplicate name = %d, want 409", rec.Code)
	}
}

func TestUsersAPICreateAsListener403(t *testing.T) {
	s, _, _ := authServer(t)
	listener := listenerCookie(t, s)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/users", ""},
		{http.MethodPost, "/admin/users", `{"name":"kim","password":"correct horse battery","role":"listener"}`},
		{http.MethodPost, "/admin/users/1/disable", ""},
		{http.MethodPost, "/admin/users/1/enable", ""},
		{http.MethodPost, "/admin/users/1/password", `{"password":"correct horse battery"}`},
		{http.MethodDelete, "/admin/users/1", ""},
	} {
		if rec := as(t, s, tc.method, tc.path, tc.body, listener); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a listener = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

func TestUsersAPICreateAnonymous401(t *testing.T) {
	s, _, _ := authServer(t)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/users"},
		{http.MethodPost, "/admin/users"},
		{http.MethodDelete, "/admin/users/1"},
	} {
		if rec := as(t, s, tc.method, tc.path, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymously = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

// TestUsersAPIListHidesHashes: the hash is not in the view struct at all, so no
// future field or logging change can leak it.
func TestUsersAPIListHidesHashes(t *testing.T) {
	s, _, _ := authServer(t)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodGet, "/admin/users", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, leak := range []string{"pw_hash", "PwHash", "$2a$", "password"} {
		if strings.Contains(body, leak) {
			t.Errorf("the list leaks %q: %s", leak, body)
		}
	}
	var users []userView
	if err := json.Unmarshal(rec.Body.Bytes(), &users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("%d accounts listed, want 2", len(users))
	}
	if users[0].Name == "" || users[0].Role == "" {
		t.Errorf("the list is missing what a console needs: %+v", users[0])
	}
}

// TestUsersAPIDeleteLastAdmin409: 409 rather than 403, because the request was
// allowed and the STATE refuses it -- a console can say "promote someone first"
// to one and not the other.
func TestUsersAPIDeleteLastAdmin409(t *testing.T) {
	s, st, _ := authServer(t)
	admin := adminCookie(t, s)

	me, err := st.GetUserByName(context.Background(), "andrew")
	if err != nil {
		t.Fatal(err)
	}
	rec := as(t, s, http.MethodDelete, "/admin/users/"+strconv.FormatInt(me.ID, 10), "", admin)
	if rec.Code != http.StatusConflict {
		t.Errorf("deleting the last operator = %d, want 409: %s", rec.Code, rec.Body)
	}

	// With a second operator it goes through, and the door is still open.
	if rec := as(t, s, http.MethodPost, "/admin/users",
		`{"name":"kim","password":"correct horse battery","role":"admin"}`, admin); rec.Code != http.StatusCreated {
		t.Fatalf("creating a second operator = %d", rec.Code)
	}
	if rec := as(t, s, http.MethodDelete, "/admin/users/"+strconv.FormatInt(me.ID, 10), "", admin); rec.Code != http.StatusNoContent {
		t.Errorf("deleting one of two operators = %d, want 204", rec.Code)
	}
}

// TestUsersAPICannotDisableSelf409: an operator who switches their own account
// off is locked out of the only place that could switch it back on, and the
// last-admin rule does not catch it while another admin exists.
func TestUsersAPICannotDisableSelf409(t *testing.T) {
	s, st, _ := authServer(t)
	admin := adminCookie(t, s)
	if rec := as(t, s, http.MethodPost, "/admin/users",
		`{"name":"kim","password":"correct horse battery","role":"admin"}`, admin); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}

	me, err := st.GetUserByName(context.Background(), "andrew")
	if err != nil {
		t.Fatal(err)
	}
	if rec := as(t, s, http.MethodPost, "/admin/users/"+strconv.FormatInt(me.ID, 10)+"/disable", "", admin); rec.Code != http.StatusConflict {
		t.Errorf("disabling yourself = %d, want 409", rec.Code)
	}

	// Disabling somebody else is fine, and re-enabling them works.
	kim, err := st.GetUserByName(context.Background(), "kim")
	if err != nil {
		t.Fatal(err)
	}
	if rec := as(t, s, http.MethodPost, "/admin/users/"+strconv.FormatInt(kim.ID, 10)+"/disable", "", admin); rec.Code != http.StatusNoContent {
		t.Errorf("disabling another operator = %d, want 204", rec.Code)
	}
	if rec := as(t, s, http.MethodPost, "/admin/users/"+strconv.FormatInt(kim.ID, 10)+"/enable", "", admin); rec.Code != http.StatusNoContent {
		t.Errorf("enabling = %d, want 204", rec.Code)
	}
}

func TestUsersAPIValidation(t *testing.T) {
	s, _, _ := authServer(t)
	admin := adminCookie(t, s)

	for _, tc := range []struct{ body, field string }{
		{`{"name":"","password":"correct horse battery","role":"listener"}`, "name"},
		{`{"name":"   ","password":"correct horse battery","role":"listener"}`, "name"},
		{`{"name":"` + strings.Repeat("k", 65) + `","password":"correct horse battery","role":"listener"}`, "name"},
		{`{"name":"kim","password":"short","role":"listener"}`, "password"},
		{`{"name":"kim","password":"","role":"listener"}`, "password"},
		{`{"name":"kim","password":"` + strings.Repeat("long ", 20) + `","role":"listener"}`, "password"},
		{`{"name":"kim","password":"correct horse battery","role":"dj"}`, "role"},
		{`{"name":"kim","password":"correct horse battery","role":""}`, "role"},
	} {
		rec := as(t, s, http.MethodPost, "/admin/users", tc.body, admin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", tc.body, rec.Code)
			continue
		}
		var e struct{ Field string }
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		// The FIELD, so a console can highlight the box that was wrong rather
		// than leaving an operator to guess.
		if e.Field != tc.field {
			t.Errorf("%s named field %q, want %q", tc.body, e.Field, tc.field)
		}
	}

	if rec := as(t, s, http.MethodPost, "/admin/users", "not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
}

// TestUsersAPIRoleMustBeAdminOrListener is the validation half of the schema's
// own CHECK constraint: two roles is the whole vocabulary.
func TestUsersAPIRoleMustBeAdminOrListener(t *testing.T) {
	s, _, _ := authServer(t)
	admin := adminCookie(t, s)

	for _, role := range []string{"dj", "operator", "ADMIN", "root"} {
		rec := as(t, s, http.MethodPost, "/admin/users",
			`{"name":"kim","password":"correct horse battery","role":"`+role+`"}`, admin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("role %q = %d, want 400", role, rec.Code)
		}
	}
}

func TestUsersAPIResetPassword(t *testing.T) {
	s, st, _ := authServer(t)
	admin := adminCookie(t, s)

	kim, err := st.GetUserByName(context.Background(), "listener")
	if err != nil {
		t.Fatal(err)
	}
	rec := as(t, s, http.MethodPost, "/admin/users/"+strconv.FormatInt(kim.ID, 10)+"/password",
		`{"password":"a whole new passphrase"}`, admin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}

	if rec := login(t, s, "listener", "correct horse battery"); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old password still works: %d", rec.Code)
	}
	if rec := login(t, s, "listener", "a whole new passphrase"); rec.Code != http.StatusNoContent {
		t.Errorf("the new password does not work: %d", rec.Code)
	}

	// The same rules as creating one.
	if rec := as(t, s, http.MethodPost, "/admin/users/"+strconv.FormatInt(kim.ID, 10)+"/password",
		`{"password":"short"}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a short reset = %d, want 400", rec.Code)
	}
	if rec := as(t, s, http.MethodPost, "/admin/users/"+strconv.FormatInt(kim.ID, 10)+"/password",
		`{"password":"`+strings.Repeat("long ", 20)+`"}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a password past bcrypt's limit = %d, want 400", rec.Code)
	}
	if rec := as(t, s, http.MethodPost, "/admin/users/"+strconv.FormatInt(kim.ID, 10)+"/password",
		"not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
}

// TestUsersAPINoRegistrationRoute: accounts are admin-created. A self-service
// route would make the dial public the moment somebody found the port.
func TestUsersAPINoRegistrationRoute(t *testing.T) {
	s, _, _ := authServer(t)

	for _, path := range []string{"/register", "/signup", "/admin/register", "/users"} {
		if rec := as(t, s, http.MethodPost, path, `{"name":"kim"}`, nil); rec.Code == http.StatusOK ||
			rec.Code == http.StatusCreated || rec.Code == http.StatusNoContent {
			t.Errorf("POST %s = %d; there is no registration route", path, rec.Code)
		}
	}
}

func TestUsersAPIBadTargets(t *testing.T) {
	s, _, _ := authServer(t)
	admin := adminCookie(t, s)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodDelete, "/admin/users/abc", http.StatusNotFound},
		{http.MethodDelete, "/admin/users/0", http.StatusNotFound},
		{http.MethodDelete, "/admin/users/-1", http.StatusNotFound},
		{http.MethodPost, "/admin/users/1/a/b", http.StatusNotFound},
		{http.MethodPost, "/admin/users/1/nonsense", http.StatusMethodNotAllowed},
		{http.MethodGet, "/admin/users/1", http.StatusMethodNotAllowed},
		{http.MethodPut, "/admin/users", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/admin/users/9999", http.StatusNotFound},
		{http.MethodPost, "/admin/users/9999/disable", http.StatusNotFound},
		{http.MethodPost, "/admin/users/9999/enable", http.StatusNotFound},
	} {
		if rec := as(t, s, tc.method, tc.path, "", admin); rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
	// A password reset for an account that is gone.
	if rec := as(t, s, http.MethodPost, "/admin/users/9999/password",
		`{"password":"correct horse battery"}`, admin); rec.Code != http.StatusNotFound {
		t.Errorf("resetting a missing account = %d, want 404", rec.Code)
	}
}

// TestUsersAPISurfacesStoreFailures: a swallowed failure looks exactly like an
// empty account list or a rename that worked, and an operator acts on both.
func TestUsersAPISurfacesStoreFailures(t *testing.T) {
	s, _, clk := authServer(t)
	admin := adminCookie(t, s)
	hash, err := auth.HashPassword("correct horse battery", 4)
	if err != nil {
		t.Fatal(err)
	}
	// The session still resolves -- so the request is authorised -- while
	// every account-management call underneath it fails.
	signer, err := auth.NewSigner([]byte(testKey), clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(signer, fakeUsers{user: store.User{
		ID: 1, Name: "andrew", Role: auth.RoleAdmin, PwHash: hash}})

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/users", ""},
		{http.MethodPost, "/admin/users", `{"name":"kim","password":"correct horse battery","role":"listener"}`},
		{http.MethodDelete, "/admin/users/2", ""},
		{http.MethodPost, "/admin/users/2/disable", ""},
		{http.MethodPost, "/admin/users/2/enable", ""},
		{http.MethodPost, "/admin/users/2/password", `{"password":"correct horse battery"}`},
	} {
		rec := as(t, s, tc.method, tc.path, tc.body, admin)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s with a failing store = %d, want 500", tc.method, tc.path, rec.Code)
		}
	}

}

// TestUsersAPIClientHangsUp: a listing that cannot be written must be logged
// and dropped, not panic the server for every other listener on it.
func TestUsersAPIClientHangsUp(t *testing.T) {
	s, _, _ := authServer(t)
	s.listUsers(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/admin/users", nil))
}
