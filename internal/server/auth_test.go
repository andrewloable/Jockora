// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/store"
)

const testKey = "0123456789abcdef0123456789abcdef"

// authServer is a server with two accounts: one operator, one listener.
func authServer(t *testing.T) (*Server, *store.Store, *clock.Fake) {
	t.Helper()
	s, _ := newServer(t, 0)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	for _, u := range []struct{ name, role string }{
		{"andrew", auth.RoleAdmin}, {"listener", auth.RoleListener},
	} {
		hash, err := auth.HashPassword("correct horse battery", 4) // cheap on purpose
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateUser(ctx, u.name, hash, u.role); err != nil {
			t.Fatal(err)
		}
	}

	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	signer, err := auth.NewSigner([]byte(testKey), clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(signer, st)
	s.SetClock(clk)
	return s, st, clk
}

func disable(t *testing.T, st *store.Store, name string) {
	t.Helper()
	u, err := st.GetUserByName(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DisableUser(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
}

func login(t *testing.T, s *Server, name, password string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"name":"` + name + `","password":"` + password + `"}`
	return post(t, s, "/login", body)
}

// sessionCookie pulls the cookie out of a login response.
func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	return nil
}

func withCookie(t *testing.T, s *Server, method, path string, c *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAuthzLoginSetsCookie(t *testing.T) {
	s, _, _ := authServer(t)

	rec := login(t, s, "andrew", "correct horse battery")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body)
	}
	c := sessionCookie(t, rec)
	if c == nil {
		t.Fatal("login set no session cookie")
	}
	// HttpOnly so a cross-site script cannot read it; Lax so a link into the
	// station still arrives signed in.
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("cookie = %+v", c)
	}
	if strings.Contains(c.Value, "correct horse") {
		t.Error("the password is in the cookie")
	}

	me := withCookie(t, s, http.MethodGet, "/me", c)
	if me.Code != http.StatusOK {
		t.Fatalf("/me = %d: %s", me.Code, me.Body)
	}
	var who struct{ Name, Role string }
	if err := json.Unmarshal(me.Body.Bytes(), &who); err != nil {
		t.Fatal(err)
	}
	if who.Name != "andrew" || who.Role != auth.RoleAdmin {
		t.Errorf("/me = %+v", who)
	}
}

// TestAuthzLoginBadPassword and the two below must be indistinguishable: a
// difference between them turns the login form into a way to find out which
// accounts exist.
func TestAuthzLoginBadPassword(t *testing.T) {
	s, _, _ := authServer(t)

	rec := login(t, s, "andrew", "wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("= %d, want 401", rec.Code)
	}
	if sessionCookie(t, rec) != nil {
		t.Error("a failed login set a session cookie")
	}
}

func TestAuthzLoginUnknownUser(t *testing.T) {
	s, _, _ := authServer(t)

	unknown := login(t, s, "nobody", "correct horse battery")
	wrong := login(t, s, "andrew", "wrong")
	if unknown.Code != http.StatusUnauthorized {
		t.Errorf("= %d, want 401", unknown.Code)
	}
	if unknown.Body.String() != wrong.Body.String() {
		t.Errorf("an unknown name answers %q and a wrong password %q; the difference "+
			"lets the form enumerate accounts", unknown.Body, wrong.Body)
	}
}

func TestAuthzLoginDisabledUser(t *testing.T) {
	s, st, _ := authServer(t)
	disable(t, st, "listener")

	rec := login(t, s, "listener", "correct horse battery")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("= %d, want 401", rec.Code)
	}
	if sessionCookie(t, rec) != nil {
		t.Error("a disabled account was signed in")
	}
}

func TestAuthzLogoutClearsCookie(t *testing.T) {
	s, _, _ := authServer(t)
	c := sessionCookie(t, login(t, s, "andrew", "correct horse battery"))

	rec := post(t, s, "/logout", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	cleared := sessionCookie(t, rec)
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("logout did not clear the cookie: %+v", cleared)
	}

	if me := withCookie(t, s, http.MethodGet, "/me", nil); me.Code != http.StatusUnauthorized {
		t.Errorf("/me with no cookie = %d, want 401", me.Code)
	}
	_ = c
}

// TestAuthzLogoutEndsTheSessionServerSide REVERSES a decision this file used to
// record: "the old cookie still verifies -- signing it out is the browser's
// job". That was defensible while nothing in either app offered a sign-out
// control, so the weakness was unreachable. Jockora-e9a.66 added the control,
// and a session is a stateless token with a THIRTY-DAY life -- so a copy taken
// from a shared device went on working for a month after the person handed it
// back. Which is the case the control exists for.
func TestAuthzLogoutEndsTheSessionServerSide(t *testing.T) {
	s, _, _ := authServer(t)
	c := sessionCookie(t, login(t, s, "andrew", "correct horse battery"))
	if me := withCookie(t, s, http.MethodGet, "/me", c); me.Code != http.StatusOK {
		t.Fatalf("/me before signing out = %d", me.Code)
	}

	// Signed out WITH the cookie, the way a browser sends it.
	if rec := withCookie(t, s, http.MethodPost, "/logout", c); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}

	// THE OLD COOKIE IS THE WHOLE POINT: a copy kept by somebody else must stop
	// working, not merely be dropped by the browser that asked.
	if me := withCookie(t, s, http.MethodGet, "/me", c); me.Code != http.StatusUnauthorized {
		t.Errorf("/me with the signed-out cookie = %d, want 401", me.Code)
	}
}

// TestAuthzLogoutLeavesOtherDevicesAlone: revoked by SID, not by account. A
// household shares one account across two devices by design -- presence counts
// them separately -- so signing out on a phone must not sign out the tablet.
func TestAuthzLogoutLeavesOtherDevicesAlone(t *testing.T) {
	s, _, _ := authServer(t)
	phone := sessionCookie(t, login(t, s, "andrew", "correct horse battery"))
	tablet := sessionCookie(t, login(t, s, "andrew", "correct horse battery"))

	if withCookie(t, s, http.MethodPost, "/logout", phone).Code != http.StatusNoContent {
		t.Fatal("logout failed")
	}
	if me := withCookie(t, s, http.MethodGet, "/me", phone); me.Code != http.StatusUnauthorized {
		t.Errorf("the phone is still signed in: %d", me.Code)
	}
	if me := withCookie(t, s, http.MethodGet, "/me", tablet); me.Code != http.StatusOK {
		t.Errorf("signing out one device signed out the other: %d", me.Code)
	}
}

// TestAuthzLogoutClearsTheCookieEvenWhenRevokingFails: refusing to sign out
// because a write failed leaves somebody signed in on a device they are trying
// to hand over, which is worse than the residual risk.
func TestAuthzLogoutClearsTheCookieEvenWhenRevokingFails(t *testing.T) {
	s, _, clk := authServer(t)
	c := sessionCookie(t, login(t, s, "andrew", "correct horse battery"))
	signer, err := auth.NewSigner([]byte(testKey), clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(signer, fakeUsers{
		user:      store.User{ID: 1, Name: "andrew", Role: auth.RoleAdmin},
		revokeErr: errors.New("the database went away"),
	})

	rec := withCookie(t, s, http.MethodPost, "/logout", c)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	cleared := sessionCookie(t, rec)
	if cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("the cookie was not cleared: %+v", cleared)
	}
}

// TestAuthzDisabledAfterLoginIs401: revoking access must take effect on the
// next fetch, not whenever the session happens to expire a month later.
func TestAuthzDisabledAfterLoginIs401(t *testing.T) {
	s, st, _ := authServer(t)
	c := sessionCookie(t, login(t, s, "listener", "correct horse battery"))
	if me := withCookie(t, s, http.MethodGet, "/me", c); me.Code != http.StatusOK {
		t.Fatalf("/me before disabling = %d", me.Code)
	}

	disable(t, st, "listener")
	if me := withCookie(t, s, http.MethodGet, "/me", c); me.Code != http.StatusUnauthorized {
		t.Errorf("/me after disabling = %d, want 401", me.Code)
	}
}

func TestAuthzRequireRoleMatrix(t *testing.T) {
	s, _, _ := authServer(t)
	listenerRoute := s.require(auth.RoleListener, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	adminRoute := s.require(auth.RoleAdmin, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	admin := sessionCookie(t, login(t, s, "andrew", "correct horse battery"))
	listener := sessionCookie(t, login(t, s, "listener", "correct horse battery"))

	for _, tc := range []struct {
		who             string
		cookie          *http.Cookie
		onList, onAdmin int
	}{
		{"anonymous", nil, http.StatusUnauthorized, http.StatusUnauthorized},
		{"listener", listener, http.StatusOK, http.StatusForbidden},
		{"admin", admin, http.StatusOK, http.StatusOK},
	} {
		for _, route := range []struct {
			name string
			h    http.HandlerFunc
			want int
		}{
			{"listener route", listenerRoute, tc.onList},
			{"admin route", adminRoute, tc.onAdmin},
		} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			route.h(rec, req)
			if rec.Code != route.want {
				t.Errorf("%s on the %s = %d, want %d", tc.who, route.name, rec.Code, route.want)
			}
		}
	}
}

// TestAuthzLoginRateLimited: ten wrong guesses a minute is generous for a
// person and useless for a script.
func TestAuthzLoginRateLimited(t *testing.T) {
	s, _, clk := authServer(t)

	for i := 0; i < MaxLoginFailures; i++ {
		if rec := login(t, s, "andrew", "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i, rec.Code)
		}
	}
	// The RIGHT password, refused: the limit is on attempts, not on failures
	// alone, or a guesser simply keeps going once they land it.
	if rec := login(t, s, "andrew", "correct horse battery"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the eleventh attempt = %d, want 429", rec.Code)
	}
	// Another account is unaffected: the counter is per name, so one person
	// mistyping cannot lock out everyone else.
	if rec := login(t, s, "listener", "correct horse battery"); rec.Code != http.StatusNoContent {
		t.Errorf("a different account = %d, want 204", rec.Code)
	}

	clk.Advance(LoginWindow + time.Second)
	if rec := login(t, s, "andrew", "correct horse battery"); rec.Code != http.StatusNoContent {
		t.Errorf("after the window = %d, want 204", rec.Code)
	}
	// And a success clears the count, so the next mistake starts from zero.
	for i := 0; i < MaxLoginFailures-1; i++ {
		login(t, s, "andrew", "wrong")
	}
	if rec := login(t, s, "andrew", "correct horse battery"); rec.Code != http.StatusNoContent {
		t.Errorf("nine failures then the right password = %d, want 204", rec.Code)
	}
}

// TestAuthzSecureFlagWhenTLS: Secure on a plain-http LAN install would make the
// cookie unusable, and Jockora is usually reached over http at home.
func TestAuthzSecureFlagWhenTLS(t *testing.T) {
	s, _, _ := authServer(t)

	plain := sessionCookie(t, login(t, s, "andrew", "correct horse battery"))
	if plain == nil || plain.Secure {
		t.Errorf("plain http cookie Secure = %v, want false", plain)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader(`{"name":"andrew","password":"correct horse battery"}`))
	req.TLS = &tls.ConnectionState{}
	s.Handler().ServeHTTP(rec, req)
	secure := sessionCookie(t, rec)
	if secure == nil || !secure.Secure {
		t.Errorf("TLS cookie Secure = %v, want true", secure)
	}
}

// TestAuthzSessionKeyPersists: a key generated fresh on every start would sign
// everyone out whenever the operator restarted the server.
func TestAuthzSessionKeyPersists(t *testing.T) {
	s, st, clk := authServer(t)
	c := sessionCookie(t, login(t, s, "andrew", "correct horse battery"))

	// A SECOND server, as a restart would build, on the same key.
	other, _ := newServer(t, 0)
	signer, err := auth.NewSigner([]byte(testKey), clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	other.SetAuth(signer, st)
	other.SetClock(clk)

	if me := withCookie(t, other, http.MethodGet, "/me", c); me.Code != http.StatusOK {
		t.Errorf("a cookie from before the restart = %d, want 200", me.Code)
	}

	// A DIFFERENT key must not.
	stranger, _ := newServer(t, 0)
	otherSigner, err := auth.NewSigner([]byte("fedcba9876543210fedcba9876543210"), clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	stranger.SetAuth(otherSigner, st)
	if me := withCookie(t, stranger, http.MethodGet, "/me", c); me.Code != http.StatusUnauthorized {
		t.Errorf("a cookie signed with another key = %d, want 401", me.Code)
	}
}

func TestAuthzWithoutAccounts(t *testing.T) {
	// The spike path: no database, so no accounts to check against.
	s, _ := newServer(t, 0)

	if rec := login(t, s, "andrew", "x"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("login with no accounts configured = %d, want 503", rec.Code)
	}
	if me := withCookie(t, s, http.MethodGet, "/me", nil); me.Code != http.StatusUnauthorized {
		t.Errorf("/me = %d, want 401", me.Code)
	}
	// Signing out has to work even when there is nothing to sign out of.
	if rec := post(t, s, "/logout", ""); rec.Code != http.StatusNoContent {
		t.Errorf("logout = %d, want 204", rec.Code)
	}
}

func TestAuthzRejectsGarbage(t *testing.T) {
	s, _, _ := authServer(t)

	if rec := post(t, s, "/login", "not json"); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
	// A cookie that is not a session, and one signed for a user who is gone.
	bad := &http.Cookie{Name: SessionCookie, Value: "not.a.token"}
	if me := withCookie(t, s, http.MethodGet, "/me", bad); me.Code != http.StatusUnauthorized {
		t.Errorf("a forged cookie = %d, want 401", me.Code)
	}
	signer, err := auth.NewSigner([]byte(testKey), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ghost, err := signer.Sign(auth.Session{UserID: 9999, Role: auth.RoleAdmin,
		Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if me := withCookie(t, s, http.MethodGet, "/me",
		&http.Cookie{Name: SessionCookie, Value: ghost}); me.Code != http.StatusUnauthorized {
		t.Errorf("a validly signed cookie for a deleted account = %d, want 401", me.Code)
	}
}

// fakeUsers lets a test hand back an account the database could not hold, so
// the defences against a corrupt row are reachable.
type fakeUsers struct {
	user       store.User
	err        error
	revoked    bool
	revokedErr error
	revokeErr  error
}

func (f fakeUsers) GetUserByName(context.Context, string) (store.User, error) {
	return f.user, f.err
}
func (f fakeUsers) GetUserByID(context.Context, int64) (store.User, error) {
	return f.user, f.err
}

// The account-management half is not what these tests are about; a fake that
// silently succeeded would be a worse lie than one that refuses.
func (f fakeUsers) ListUsers(context.Context) ([]store.User, error) {
	return nil, errors.New("not implemented by this fake")
}
func (f fakeUsers) CreateUser(context.Context, string, string, string) (int64, error) {
	return 0, errors.New("not implemented by this fake")
}
func (f fakeUsers) UpdateUser(context.Context, int64, string, string) error {
	return errors.New("not implemented by this fake")
}
func (f fakeUsers) DisableUser(context.Context, int64) error {
	return errors.New("not implemented by this fake")
}
func (f fakeUsers) EnableUser(context.Context, int64) error {
	return errors.New("not implemented by this fake")
}
func (f fakeUsers) DeleteUser(context.Context, int64) error {
	return errors.New("not implemented by this fake")
}
func (f fakeUsers) SetPassword(context.Context, int64, string) error {
	return errors.New("not implemented by this fake")
}

// Signing out. revoked is what SessionRevoked answers, so a test can put a
// session on either side of the line without a store.
func (f fakeUsers) RevokeSession(context.Context, string, time.Time) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	return nil
}
func (f fakeUsers) SessionRevoked(context.Context, string) (bool, error) {
	return f.revoked, f.revokedErr
}
func (f fakeUsers) SweepRevokedSessions(context.Context, time.Time) error {
	return f.revokeErr
}

// failingWriter is a ResponseWriter whose body cannot be written, which is what
// a client hanging up mid-response looks like.
type failingWriter struct{ h http.Header }

func (f *failingWriter) Header() http.Header {
	if f.h == nil {
		f.h = http.Header{}
	}
	return f.h
}
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("client went away") }
func (f *failingWriter) WriteHeader(int)           {}

func TestAuthzEdgeCases(t *testing.T) {
	t.Run("no clock set", func(t *testing.T) {
		// A server built without SetClock still has to be able to sign someone
		// in; the fake clock is a test's convenience, not a requirement.
		s, st, _ := authServer(t)
		s.SetClock(nil)
		signer, err := auth.NewSigner([]byte(testKey), time.Now)
		if err != nil {
			t.Fatal(err)
		}
		s.SetAuth(signer, st)
		if rec := login(t, s, "andrew", "correct horse battery"); rec.Code != http.StatusNoContent {
			t.Errorf("= %d, want 204", rec.Code)
		}
	})

	t.Run("account with a role the signer refuses", func(t *testing.T) {
		// The schema's CHECK constraint keeps this out of the database, so the
		// only way in is a store that is not the database -- but the defence
		// has to hold, because a token signed for an unknown role is a token
		// no route knows how to refuse.
		s, _, clk := authServer(t)
		hash, err := auth.HashPassword("correct horse battery", 4)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := auth.NewSigner([]byte(testKey), clk.Now)
		if err != nil {
			t.Fatal(err)
		}
		s.SetAuth(signer, fakeUsers{user: store.User{ID: 1, Name: "andrew",
			Role: "wizard", PwHash: hash}})
		rec := login(t, s, "andrew", "correct horse battery")
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("= %d, want 500", rec.Code)
		}
		if sessionCookie(t, rec) != nil {
			t.Error("a session was issued for a role no route understands")
		}
	})

	t.Run("client hangs up mid-response", func(t *testing.T) {
		s, _, _ := authServer(t)
		c := sessionCookie(t, login(t, s, "andrew", "correct horse battery"))
		req := httptest.NewRequest(http.MethodGet, "/me", nil)
		req.AddCookie(c)
		// Must not panic; the failure is logged and the request ends.
		s.serveMe(&failingWriter{}, req)
	})

	// SetAuth must take the stream with it. Leaving the HLS routes on whatever
	// identity was installed before would let anyone stream from a server that
	// has accounts, and hold a station on air without one.
	t.Run("accounts gate the stream", func(t *testing.T) {
		s, _, _ := authServer(t)

		anon := withCookie(t, s, http.MethodGet, "/hls/1/stream.m3u8", nil)
		if anon.Code != http.StatusUnauthorized {
			t.Errorf("an anonymous listener = %d, want 401", anon.Code)
		}

		c := sessionCookie(t, login(t, s, "listener", "correct horse battery"))
		in := withCookie(t, s, http.MethodGet, "/hls/1/stream.m3u8", c)
		// 404, not 401: there is no such station on this bare server, which is
		// a different answer from "who are you".
		if in.Code == http.StatusUnauthorized {
			t.Error("a signed-in listener was refused")
		}
	})

	t.Run("presence identity comes from the session", func(t *testing.T) {
		s, _, _ := authServer(t)
		c := sessionCookie(t, login(t, s, "listener", "correct horse battery"))

		req := httptest.NewRequest(http.MethodGet, "/hls/1/stream.m3u8", nil)
		req.AddCookie(c)
		id, ok := s.sessionFromCookie(httptest.NewRecorder(), req)
		if !ok || id == "" {
			t.Fatalf("a signed-in listener has no presence identity: %q %v", id, ok)
		}
		// Stable across requests, or every poll would look like a new listener.
		again, _ := s.sessionFromCookie(httptest.NewRecorder(), req)
		if again != id {
			t.Errorf("the identity changed between polls: %q then %q", id, again)
		}
		// And an anonymous request has none.
		if _, ok := s.sessionFromCookie(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/hls/1/stream.m3u8", nil)); ok {
			t.Error("an anonymous request was given a presence identity")
		}
	})
}
