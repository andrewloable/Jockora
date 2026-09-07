// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package auth is password hashing and signed sessions, and NOTHING ELSE.
//
// No HTTP, no cookies, no database. Keeping the crypto in a package with no
// transport in it means it can be reasoned about and tested on its own, and it
// is why every case below is exercised without a server.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// The role vocabulary. A CLOSED SET, checked when a session is minted: the role
// decides what a request may do, so an unknown one must never become signable.
const (
	RoleAdmin    = "admin"
	RoleListener = "listener"
)

// DefaultCost is the bcrypt cost for real use.
//
// 12, not bcrypt's own default of 10: this is a self-hosted service whose
// database sits in a file beside the music, so an offline attack on a stolen
// file is the realistic threat and the extra factor of four is cheap on a login
// that happens once a session. It is a PARAMETER rather than a constant in the
// call so tests can drop it; at cost 12 a table of hash cases takes seconds
// each and the tests stop being run.
const DefaultCost = 12

// MinKeyBytes is the shortest signing key accepted.
//
// 32 bytes, matching HMAC-SHA256's block-relevant size. Refused at
// CONSTRUCTION, because that is the only moment anybody is looking: a weak key
// discovered at verification time is a weak key that has already been signing.
const MinKeyBytes = 32

var (
	// ErrBadSignature covers every way a token fails to be one this signer
	// made: a flipped byte, a foreign key, a truncated or malformed token. They
	// are deliberately NOT distinguished -- telling an attacker which part of
	// their forgery was wrong is telling them how to fix it.
	ErrBadSignature = errors.New("auth: bad session signature")
	ErrExpired      = errors.New("auth: session expired")
	ErrBadRole      = errors.New("auth: unknown role")
	ErrWeakKey      = fmt.Errorf("auth: signing key must be at least %d bytes", MinKeyBytes)
)

// HashPassword returns a salted bcrypt hash.
func HashPassword(pw string, cost int) (string, error) {
	if cost <= 0 {
		cost = DefaultCost
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), cost)
	if err != nil {
		return "", fmt.Errorf("auth: hashing password: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether pw produced hash.
//
// A bool rather than an error, and every failure looks the same to the caller:
// a malformed hash and a wrong password are both "no". bcrypt's own comparison
// is constant time with respect to the password.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// Session is what a signed token carries.
//
// ONLY WHAT THE CLIENT MAY SEE. The token is handed to a browser, so it holds
// an id, a role and an expiry and nothing else -- no password hash, no email,
// no name. Anything added here is published.
type Session struct {
	// SID distinguishes one SIGN-IN from another for the same account.
	//
	// Presence counts listeners, and a household sharing one account is two
	// listeners on two devices. Keyed on the user alone, the second device to
	// tune would move the first one's station out from under them.
	SID string `json:"sid,omitempty"`

	UserID  int64     `json:"uid"`
	Role    string    `json:"role"`
	Expires time.Time `json:"exp"`
}

// Signer mints and checks session tokens.
//
// STATELESS BY DESIGN: there is no session table, so there is nothing to leak,
// nothing to clean up, and nothing to go stale. Logout is deleting the cookie,
// and a disabled account is caught per request by looking the user up, which is
// the only check a stateless token genuinely cannot make for itself.
type Signer struct {
	key []byte
	now func() time.Time
}

// NewSigner returns a Signer, refusing a key too short to be worth having.
// A nil clock means time.Now.
func NewSigner(key []byte, now func() time.Time) (Signer, error) {
	if len(key) < MinKeyBytes {
		return Signer{}, ErrWeakKey
	}
	if now == nil {
		now = time.Now
	}
	return Signer{key: key, now: now}, nil
}

// Sign returns base64url(json) "." base64url(hmac-sha256).
func (s Signer) Sign(sess Session) (string, error) {
	if sess.Role != RoleAdmin && sess.Role != RoleListener {
		return "", fmt.Errorf("%w: %q", ErrBadRole, sess.Role)
	}
	// The error is discarded because it CANNOT occur: Session holds an int64,
	// a string and a time.Time, all of which marshal unconditionally. Handling
	// it would add a branch no test can reach and no bug can produce, which is
	// worse than not handling it -- an unreachable error path is untested code
	// pretending to be safety.
	body, _ := json.Marshal(sess)
	payload := base64.RawURLEncoding.EncodeToString(body)
	return payload + "." + s.mac(payload), nil
}

// Verify checks the signature and the expiry, in that order.
//
// THE SIGNATURE FIRST, ALWAYS. Reading the payload before proving it is ours
// would mean parsing attacker-controlled JSON and, worse, deciding anything
// from it.
func (s Signer) Verify(token string) (Session, error) {
	var sess Session

	payload, sig, found := strings.Cut(token, ".")
	if !found || payload == "" || sig == "" {
		return sess, ErrBadSignature
	}
	// hmac.Equal, never ==: a byte-by-byte string comparison leaks how much of
	// a forged signature was right through how long it took to reject.
	if !hmac.Equal([]byte(sig), []byte(s.mac(payload))) {
		return sess, ErrBadSignature
	}

	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return sess, ErrBadSignature
	}
	if err := json.Unmarshal(body, &sess); err != nil {
		return sess, ErrBadSignature
	}
	// Re-checked on the way out as well as on the way in: a role that somehow
	// reached a signed token must still not be honoured.
	if sess.Role != RoleAdmin && sess.Role != RoleListener {
		return Session{}, fmt.Errorf("%w: %q", ErrBadRole, sess.Role)
	}
	if !sess.Expires.After(s.now()) {
		return Session{}, ErrExpired
	}
	return sess, nil
}

func (s Signer) mac(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
