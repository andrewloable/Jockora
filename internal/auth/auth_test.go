// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// testCost keeps bcrypt fast in tests. The cost is a PARAMETER rather than a
// constant precisely so this is possible without editing production code: at
// DefaultCost a table of hash tests takes seconds each.
const testCost = 4

func testKey() []byte { return []byte("0123456789abcdef0123456789abcdef") } // 32 bytes

func TestAuthHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse", testCost)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, "correct horse") {
		t.Fatal("the password is recoverable from its own hash")
	}
	if !CheckPassword(hash, "correct horse") {
		t.Error("the right password did not verify")
	}
	if CheckPassword(hash, "wrong horse") {
		t.Error("the wrong password verified")
	}
	if CheckPassword("not-a-hash", "correct horse") {
		t.Error("a malformed hash verified")
	}
}

// TestAuthHashIsSalted: two hashes of the same password must differ, or a
// stolen table tells an attacker which accounts share a password.
func TestAuthHashIsSalted(t *testing.T) {
	a, err := HashPassword("same", testCost)
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("same", testCost)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two hashes of one password are identical; the hash is unsalted")
	}
	if !CheckPassword(a, "same") || !CheckPassword(b, "same") {
		t.Error("a salted hash did not verify")
	}
}

func TestAuthSessionRoundTrip(t *testing.T) {
	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	s, err := NewSigner(testKey(), nil)
	if err != nil {
		t.Fatal(err)
	}

	token, err := s.Sign(Session{UserID: 7, Role: RoleListener, Expires: expires})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != 7 || got.Role != RoleListener || !got.Expires.Equal(expires) {
		t.Errorf("round trip = %+v, want {7 listener %v}", got, expires)
	}
}

// TestAuthSessionTamperFails: every byte of the token is attacker-controlled,
// so the signature is the only thing standing between a listener and admin.
func TestAuthSessionTamperFails(t *testing.T) {
	s, _ := NewSigner(testKey(), nil)
	token, err := s.Sign(Session{UserID: 7, Role: RoleListener, Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	for _, i := range []int{0, len(token) / 2, len(token) - 1} {
		b := []byte(token)
		b[i] ^= 0x01
		if _, err := s.Verify(string(b)); !errors.Is(err, ErrBadSignature) {
			t.Errorf("flipping byte %d gave %v, want ErrBadSignature", i, err)
		}
	}
	for _, bad := range []string{"", "nodot", "a.b.c", "!!!.???"} {
		if _, err := s.Verify(bad); err == nil {
			t.Errorf("Verify(%q) succeeded", bad)
		}
	}
}

func TestAuthSessionExpiredFails(t *testing.T) {
	s, _ := NewSigner(testKey(), nil)
	token, err := s.Sign(Session{UserID: 7, Role: RoleAdmin, Expires: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(token); !errors.Is(err, ErrExpired) {
		t.Errorf("an expired session gave %v, want ErrExpired", err)
	}
}

// TestAuthSessionWrongKeyFails: a token minted by one deployment must not open
// another, and rotating the key must invalidate everything.
func TestAuthSessionWrongKeyFails(t *testing.T) {
	a, _ := NewSigner(testKey(), nil)
	b, _ := NewSigner([]byte("ffffffffffffffffffffffffffffffff"), nil)

	token, err := a.Sign(Session{UserID: 7, Role: RoleAdmin, Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Verify(token); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a foreign key verified: %v", err)
	}
}

// TestAuthRoleIsClosedSet: the role decides what a request may do, so an
// unknown one must never be mintable.
func TestAuthRoleIsClosedSet(t *testing.T) {
	s, _ := NewSigner(testKey(), nil)

	for _, role := range []string{"dj", "", "Admin", "root"} {
		if _, err := s.Sign(Session{UserID: 1, Role: role, Expires: time.Now().Add(time.Hour)}); !errors.Is(err, ErrBadRole) {
			t.Errorf("Sign with role %q gave %v, want ErrBadRole", role, err)
		}
	}
	for _, role := range []string{RoleAdmin, RoleListener} {
		if _, err := s.Sign(Session{UserID: 1, Role: role, Expires: time.Now().Add(time.Hour)}); err != nil {
			t.Errorf("Sign with role %q failed: %v", role, err)
		}
	}
}

// TestAuthKeyTooShortRefused: a short key is a weak signature, and refusing at
// construction is the only moment anyone is looking.
func TestAuthKeyTooShortRefused(t *testing.T) {
	for _, n := range []int{0, 7, 31} {
		if _, err := NewSigner(make([]byte, n), nil); !errors.Is(err, ErrWeakKey) {
			t.Errorf("a %d-byte key gave %v, want ErrWeakKey", n, err)
		}
	}
	if _, err := NewSigner(make([]byte, 32), nil); err != nil {
		t.Errorf("a 32-byte key was refused: %v", err)
	}
}

// TestAuthVerifyUsesTheInjectedClock proves expiry is testable without sleeping
// and that Verify reads the clock it was given.
func TestAuthVerifyUsesTheInjectedClock(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	s, err := NewSigner(testKey(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Sign(Session{UserID: 1, Role: RoleAdmin, Expires: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(token); err != nil {
		t.Fatalf("a live session failed: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := s.Verify(token); !errors.Is(err, ErrExpired) {
		t.Errorf("the session did not expire when the clock moved: %v", err)
	}
}

// TestAuthTokenCarriesNothingSecret: the client can read every byte of this, so
// it must contain only what it is safe for them to see.
func TestAuthTokenCarriesNothingSecret(t *testing.T) {
	s, _ := NewSigner(testKey(), nil)
	hash, err := HashPassword("hunter2", testCost)
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Sign(Session{UserID: 7, Role: RoleAdmin, Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{hash, "hunter2", string(testKey())} {
		if strings.Contains(token, secret) {
			t.Errorf("the token carries a secret: %q", secret)
		}
	}
}

// The tests below reach Verify's defensive branches, which an attacker cannot:
// they sit AFTER the signature check, so getting to them requires a valid MAC
// over a broken payload. This file is in package auth precisely so it can mint
// one. They are not paranoia -- they are the branches a future bug in Sign
// would land on, and an untested branch there fails open or panics.

func TestAuthVerifyRejectsAValidlySignedBrokenPayload(t *testing.T) {
	s, _ := NewSigner(testKey(), nil)

	// Correctly signed, but the payload is not base64.
	bad := "!!!not-base64!!!"
	if _, err := s.Verify(bad + "." + s.mac(bad)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("undecodable payload gave %v, want ErrBadSignature", err)
	}

	// Correctly signed valid base64 that is not JSON.
	notJSON := base64.RawURLEncoding.EncodeToString([]byte("this is not json"))
	if _, err := s.Verify(notJSON + "." + s.mac(notJSON)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("non-JSON payload gave %v, want ErrBadSignature", err)
	}

	// Correctly signed JSON carrying a role outside the closed set. Sign can
	// never produce this; a bug, or a key leak, could.
	rogue, err := json.Marshal(Session{UserID: 1, Role: "root", Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(rogue)
	if _, err := s.Verify(payload + "." + s.mac(payload)); !errors.Is(err, ErrBadRole) {
		t.Errorf("a signed rogue role gave %v, want ErrBadRole", err)
	}
}

// TestAuthHashPasswordCostBounds covers the default and the refusal.
func TestAuthHashPasswordCostBounds(t *testing.T) {
	// Zero means DefaultCost. Verified by the cost recorded in the hash itself
	// rather than by timing it.
	h, err := HashPassword("x", 0)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost([]byte(h))
	if err != nil {
		t.Fatal(err)
	}
	if cost != DefaultCost {
		t.Errorf("cost 0 produced a hash at cost %d, want DefaultCost %d", cost, DefaultCost)
	}

	// bcrypt refuses a cost above 31, and that error must surface rather than
	// yielding an empty hash that CheckPassword would then reject for ever.
	if _, err := HashPassword("x", 99); err == nil {
		t.Error("an impossible cost produced a hash")
	}
}

// TestAuthComparesMACsInConstantTime enforces a requirement no behavioural test
// can reach.
//
// Swapping hmac.Equal for == produces IDENTICAL results for every input; the
// difference is only how long a rejection takes, and a wrong signature that
// fails a byte later than another leaks how much of a forgery was correct. A
// falsification proved the gap: replacing hmac.Equal with == passed every other
// test in this file.
//
// So the source is inspected instead. Unusual, and justified: the alternative
// is a security property with nothing whatsoever guarding it.
func TestAuthComparesMACsInConstantTime(t *testing.T) {
	src, err := os.ReadFile("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	if !strings.Contains(body, "hmac.Equal(") {
		t.Error("auth.go does not call hmac.Equal; a MAC compared with == leaks timing")
	}

	// And no line compares anything to a mac() result with == or !=, which is
	// exactly the shape the falsification introduced.
	for i, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "s.mac(") {
			continue
		}
		if strings.Contains(line, "==") || strings.Contains(line, "!=") {
			t.Errorf("auth.go:%d compares a MAC with an operator: %s",
				i+1, strings.TrimSpace(line))
		}
	}
}
