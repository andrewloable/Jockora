// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/store"
)

// SessionCookie names the signed session. Nothing else is trusted: a role in a
// header or a query string is a role the caller chose for themselves.
const SessionCookie = "jockora_session"

// SessionLife is how long a login lasts before it has to be repeated.
const SessionLife = 30 * 24 * time.Hour

// Login rate limiting. Small enough to stop a password being guessed, large
// enough that a person mistyping theirs is never locked out.
const (
	MaxLoginFailures = 10
	LoginWindow      = time.Minute
)

// badLogin is the ONE answer to every failed sign-in.
//
// Identical for an unknown name, a wrong password and a disabled account, so
// the form cannot be used to find out which accounts exist.
const badLogin = "wrong name or password"

// Users is the account lookup a request needs.
type Users interface {
	GetUserByName(ctx context.Context, name string) (store.User, error)
	GetUserByID(ctx context.Context, id int64) (store.User, error)
	ListUsers(ctx context.Context) ([]store.User, error)
	CreateUser(ctx context.Context, name, pwHash, role string) (int64, error)
	UpdateUser(ctx context.Context, id int64, name, role string) error
	DisableUser(ctx context.Context, id int64) error
	EnableUser(ctx context.Context, id int64) error
	DeleteUser(ctx context.Context, id int64) error
	SetPassword(ctx context.Context, id int64, pwHash string) error

	// Signing out. A session is a stateless HMAC token with a thirty-day life,
	// so clearing the cookie ends it on that device and nowhere else -- a
	// copied cookie went on authenticating for up to a month. These are what
	// stop it. Jockora-e9a.66.
	RevokeSession(ctx context.Context, sid string, expires time.Time) error
	SessionRevoked(ctx context.Context, sid string) (bool, error)
	SweepRevokedSessions(ctx context.Context, now time.Time) error
}

// SetAuth wires sign-in. Without it every authenticated route refuses.
func (s *Server) SetAuth(signer auth.Signer, users Users) {
	s.signer, s.users = &signer, users
	// And the stream follows the account. Once there is somewhere to sign in,
	// a listener IS their signed session -- an anonymous identity would let
	// anyone hold a station on air without one.
	s.sessions = s.sessionFromCookie
}

// SetClock replaces the clock the rate limiter counts against.
func (s *Server) SetClock(clk clock.Clock) { s.clk = clk }

func (s *Server) now() time.Time {
	if s.clk != nil {
		return s.clk.Now()
	}
	return time.Now()
}

// serveLogin exchanges a name and password for a signed cookie.
func (s *Server) serveLogin(w http.ResponseWriter, r *http.Request) {
	if s.signer == nil || s.users == nil {
		http.Error(w, "accounts are not configured", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, "expected a JSON object", http.StatusBadRequest)
		return
	}

	// Counted per NAME, before the password is checked, so a flood against one
	// account cannot be turned into a flood against the hash function.
	if s.rateLimited(body.Name) {
		http.Error(w, "too many attempts; wait a minute", http.StatusTooManyRequests)
		return
	}

	user, err := s.users.GetUserByName(r.Context(), body.Name)
	// The password is checked even when the name is unknown, against a hash
	// that cannot match, so the two paths take the same time.
	hash := user.PwHash
	if err != nil {
		hash = ""
	}
	if !auth.CheckPassword(hash, body.Password) || err != nil || user.Disabled {
		s.recordFailure(body.Name)
		http.Error(w, badLogin, http.StatusUnauthorized)
		return
	}

	token, signErr := s.signer.Sign(auth.Session{
		UserID: user.ID, Role: user.Role, SID: newSID(), Expires: s.now().Add(SessionLife),
	})
	if signErr != nil {
		http.Error(w, "could not sign in", http.StatusInternalServerError)
		return
	}
	s.clearFailures(body.Name)

	http.SetCookie(w, s.cookie(r, token, int(SessionLife.Seconds())))
	w.WriteHeader(http.StatusNoContent)
}

// serveLogout clears the cookie. It never fails: signing out has to work even
// when the session is already gone.
// serveLogout ends the session, not just the cookie.
//
// THE TOKEN OUTLIVES THE COOKIE. Sessions are stateless and last thirty days,
// so telling the browser to drop it left a copy able to authenticate for a
// month. That was a deliberate choice while nothing offered a sign-out control
// at all; Jockora-e9a.66 added the control, which made it reachable.
//
// The cookie is cleared even when the revocation cannot be written. Refusing to
// sign out because a write failed leaves the operator signed in on a device
// they are trying to hand over, which is worse than the residual risk.
func (s *Server) serveLogout(w http.ResponseWriter, r *http.Request) {
	if sess, _, err := s.session(r); err == nil && sess.SID != "" && s.users != nil {
		if err := s.users.RevokeSession(r.Context(), sess.SID, sess.Expires); err != nil {
			s.log.Warn("signing out did not revoke the session server-side", "err", err)
		}
		// HOUSEKEEPING ON THE RARE ACTION, rather than a goroutine and a
		// schedule for a table that gains a row per sign-out. A revocation only
		// has to outlive the token it names, and the server is the half that
		// owns a clock. Failure here costs nothing but a few stale rows.
		if err := s.users.SweepRevokedSessions(r.Context(), s.now()); err != nil {
			s.log.Warn("old sign-outs were not swept", "err", err)
		}
	}
	http.SetCookie(w, s.cookie(r, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

// cookie builds the session cookie.
//
// Secure ONLY under TLS: setting it on a plain-http LAN install would make the
// cookie unusable, and Jockora is usually reached over http on someone's home
// network.
func (s *Server) cookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: SessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
	}
}

// serveMe says who the caller is.
func (s *Server) serveMe(w http.ResponseWriter, r *http.Request) {
	_, user, err := s.session(r)
	if err != nil {
		http.Error(w, "sign in", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"name": user.Name, "role": user.Role,
	}); err != nil {
		s.log.Warn("writing /me", "err", err)
	}
}

// session verifies the cookie and re-reads the account behind it.
//
// THE ACCOUNT IS READ ON EVERY REQUEST, not trusted from the cookie. Disabling
// someone must take effect on their next fetch, not whenever their session
// happens to expire -- otherwise revoking access means waiting a month.
func (s *Server) session(r *http.Request) (auth.Session, store.User, error) {
	if s.signer == nil || s.users == nil {
		return auth.Session{}, store.User{}, errors.New("server: accounts are not configured")
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return auth.Session{}, store.User{}, err
	}
	sess, err := s.signer.Verify(c.Value)
	if err != nil {
		return auth.Session{}, store.User{}, err
	}
	// SIGNED OUT IS SIGNED OUT, checked before the account is even read: a
	// token whose sign-in was ended must not authenticate, however valid its
	// signature still is.
	revoked, err := s.users.SessionRevoked(r.Context(), sess.SID)
	if err != nil {
		return auth.Session{}, store.User{}, err
	}
	if revoked {
		return auth.Session{}, store.User{}, errors.New("server: session signed out")
	}
	user, err := s.users.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		return auth.Session{}, store.User{}, err
	}
	if user.Disabled {
		return auth.Session{}, store.User{}, errors.New("server: account disabled")
	}
	return sess, user, nil
}

// require refuses a handler to anyone without the role.
//
// ANONYMOUS IS 401 AND WRONG-ROLE IS 403, which are different questions: the
// first says "say who you are", the second says "you did, and it is not
// enough". Collapsing them makes a listener think their session broke.
func (s *Server) require(role string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, user, err := s.session(r)
		if err != nil {
			http.Error(w, "sign in", http.StatusUnauthorized)
			return
		}
		// An admin may do anything a listener may. The reverse is the point of
		// having roles at all.
		if role == auth.RoleAdmin && user.Role != auth.RoleAdmin {
			http.Error(w, "this needs an operator account", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// sessionFromCookie is the SessionSource the HLS routes use once accounts
// exist: a listener is their signed session, and nobody else is served.
func (s *Server) sessionFromCookie(w http.ResponseWriter, r *http.Request) (string, bool) {
	sess, _, err := s.session(r)
	if err != nil {
		return "", false
	}
	// The SIGN-IN, not the account: two devices on one household account are
	// two listeners, and keying on the user would make the second one move the
	// first one's station.
	return sess.SID, true
}

// newSID mints the identifier that distinguishes one sign-in from another.
// The error is discarded because crypto/rand.Read is documented never to
// return one -- it crashes the program rather than handing back short or
// predictable bytes. Handling it would add a branch no test can reach.
func newSID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// loginFailures counts recent failures per name.
//
// A map and a mutex, not a dependency: the whole feature is "ten in a minute",
// and a rate limiter library would be more code than the thing it limits.
type loginFailures struct {
	mu  sync.Mutex
	at  map[string][]time.Time
	now func() time.Time
}

func (s *Server) rateLimited(name string) bool {
	s.failures.mu.Lock()
	defer s.failures.mu.Unlock()
	s.pruneLocked(name)
	return len(s.failures.at[name]) >= MaxLoginFailures
}

func (s *Server) recordFailure(name string) {
	s.failures.mu.Lock()
	defer s.failures.mu.Unlock()
	s.pruneLocked(name)
	s.failures.at[name] = append(s.failures.at[name], s.now())
}

func (s *Server) clearFailures(name string) {
	s.failures.mu.Lock()
	defer s.failures.mu.Unlock()
	delete(s.failures.at, name)
}

// pruneLocked drops failures older than the window, so the limit is a rate and
// not a permanent lockout. Caller holds the lock.
func (s *Server) pruneLocked(name string) {
	if s.failures.at == nil {
		s.failures.at = map[string][]time.Time{}
	}
	cutoff := s.now().Add(-LoginWindow)
	kept := s.failures.at[name][:0]
	for _, at := range s.failures.at[name] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(s.failures.at, name)
		return
	}
	s.failures.at[name] = kept
}
