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
	"regexp"
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

// GuestCookie names the presence key a guest carries while public listening is
// on. A SEPARATE NAME from the session, not a second value under the same one:
// presence needs to tell one browser from another and nothing more, and an
// unsigned random id sharing a name with a signed token is how the two get
// confused by the next person to read this.
const GuestCookie = "jockora_guest"

// GuestLife is how long that key lasts. A day, because it identifies a browser
// for as long as somebody might leave the radio on, and nothing is attributed
// to it that would be worth keeping longer.
const GuestLife = 24 * time.Hour

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
	// A GUEST UNLESS THE COOKIE SAYS OTHERWISE, and no 401 of its own: this
	// handler is behind require(RoleListener), which is the gate. Somebody with
	// no session only gets here while public listening is open, so a second
	// check on it would be a branch nothing can reach -- and an unreachable
	// guard is one nobody can prove still works.
	name, role := "", auth.RoleGuest
	if _, user, err := s.session(r); err == nil {
		name, role = user.Name, user.Role
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"name": name, "role": role,
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
			// THE LISTENER GATE ONLY, and only while the operator has opened
			// it. RoleAdmin never reaches this branch, so the console asks for
			// a login whatever public listening is set to -- which is the
			// whole point of the switch being safe to leave on.
			if role == auth.RoleListener && s.publicListening() {
				h(w, r)
				return
			}
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
	if err == nil {
		// The SIGN-IN, not the account: two devices on one household account
		// are two listeners, and keying on the user would make the second one
		// move the first one's station.
		return sess.SID, true
	}
	// A GUEST IS STILL A LISTENER as far as presence is concerned. Without a
	// key of their own they would stream a station that nobody was counted on,
	// and it would stop underneath them at the end of the grace period.
	if s.publicListening() {
		return s.guestSession(w, r), true
	}
	return "", false
}

// publicListening reports whether the listener half is open to anyone.
//
// FALSE WHENEVER THERE IS NO ADMIN SURFACE, which is the spike path and every
// test that wires only what it is testing. A missing capability must read as
// "closed": defaulting the other way would open a stream on any server that had
// not got round to answering the question.
func (s *Server) publicListening() bool {
	return s.admin != nil && s.admin.PublicListener()
}

// guestKey is the exact shape newSID mints: twelve random bytes as hex.
//
// THE VALUE IS THE CALLER'S TO CHOOSE, and without this it was taken verbatim
// as a presence identity -- a four-kilobyte cookie became a four-kilobyte map
// key in the tracker, once per request, held for the grace period. A signed
// listener's SID arrives inside a token nobody can forge; a guest's arrives in
// a plain cookie, so the shape is the only check there is.
//
// Anyone can still mint themselves many VALID keys. That is inherent to
// cookie-based presence and it is bounded by the grace period; what this stops
// is the content and the size being theirs as well.
var guestKey = regexp.MustCompile(`^[0-9a-f]{24}$`)

// guestSession returns this browser's presence key, minting one if it has none
// -- or if the one it brought is not a key this server ever issued.
func (s *Server) guestSession(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(GuestCookie); err == nil && guestKey.MatchString(c.Value) {
		return c.Value
	}
	id := newSID()
	http.SetCookie(w, &http.Cookie{
		Name: GuestCookie, Value: id, Path: "/", MaxAge: int(GuestLife.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil,
	})
	return id
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
