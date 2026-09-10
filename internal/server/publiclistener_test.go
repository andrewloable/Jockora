// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andrewloable/jockora/internal/auth"
)

// publicServer is matrixServer with the switch thrown.
//
// The SAME fake and the same wiring, so a difference between the two suites is
// the switch and nothing else.
func publicServer(t *testing.T) (*Server, *fakeAdmin) {
	t.Helper()
	s, st, _ := authServer(t)
	admin := &fakeAdmin{cadence: 4, public: true}
	s.SetAdmin(admin)
	s.SetSources(st, &fakeRescan{})
	s.SetStations(st, nil, nil)
	s.SetJocks(st, &voiceList{names: []string{"am_fenrir"}}, nil)
	s.SetPlaylists(st)
	s.SetTuner(&listenTuner{stations: []DialStation{{ID: 1, Name: "ROCK"}}})
	s.SetStatusSource(&StaticStatus{})
	return s, admin
}

// TestPublicListenerOpensTheListenerRoutesAndNothingElse is the whole feature
// in one assertion, run over the SAME matrix the closed server is checked
// against: a listener route answers a caller with no cookie at all, and every
// operator route still says 401.
//
// Feedback is the deliberate exception and it is named here rather than left
// to be noticed: a verdict is attributed to an account, and a guest has none.
func TestPublicListenerOpensTheListenerRoutesAndNothingElse(t *testing.T) {
	s, _ := publicServer(t)

	for _, e := range matrix {
		rec := as(t, s, e.verb(), e.requestPath(), e.body, nil)
		switch {
		case e.path == "/feedback":
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("a guest left feedback: %s = %d, want 401",
					e.requestPath(), rec.Code)
			}
		case e.role == auth.RoleAdmin:
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s reached the console anonymously = %d, want 401",
					e.verb(), e.requestPath(), rec.Code)
			}
		default:
			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
				t.Errorf("%s %s is public but answered %d: %s",
					e.verb(), e.requestPath(), rec.Code, rec.Body)
			}
		}
	}
}

// TestPublicListenerClosedStillRefusesEverything: the switch is the only thing
// that opened those routes, so turning it back off closes them again on the
// next request rather than at the next restart.
func TestPublicListenerClosedStillRefusesEverything(t *testing.T) {
	s, admin := publicServer(t)
	admin.setPublic(false)

	for _, e := range matrix {
		if e.role == "" {
			continue
		}
		if rec := as(t, s, e.verb(), e.requestPath(), e.body, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with the switch off = %d, want 401",
				e.verb(), e.requestPath(), rec.Code)
		}
	}
}

// TestPublicListenerWithNoAdminSurfaceIsClosed: a server that was never given
// an admin surface -- the spike path, and every test that wires only what it is
// testing -- must read as closed rather than as unanswered.
func TestPublicListenerWithNoAdminSurfaceIsClosed(t *testing.T) {
	s, _, _ := authServer(t)
	s.SetTuner(&listenTuner{stations: []DialStation{{ID: 1, Name: "ROCK"}}})
	if s.publicListening() {
		t.Fatal("a server with no admin surface reported public listening")
	}
	if rec := as(t, s, http.MethodGet, "/stations.json", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("the dial = %d, want 401", rec.Code)
	}
}

// TestPublicListenerMeReportsAGuest: the browser decides what to render from
// this one answer, so a guest has to get a 200 naming a role that is not an
// account -- and the role must not be one a token could ever carry.
func TestPublicListenerMeReportsAGuest(t *testing.T) {
	s, admin := publicServer(t)

	rec := as(t, s, http.MethodGet, "/me", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me as a guest = %d, want 200", rec.Code)
	}
	var got struct{ Name, Role string }
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding /me: %v", err)
	}
	if got.Role != auth.RoleGuest {
		t.Errorf("role = %q, want %q", got.Role, auth.RoleGuest)
	}
	if got.Name != "" {
		t.Errorf("name = %q, want empty: a guest has no account to name", got.Name)
	}

	// A SIGNED-IN CALLER IS STILL THEMSELVES. The guest answer must not
	// overwrite a real one, or an operator would lose the console link.
	rec = as(t, s, http.MethodGet, "/me", "", adminCookie(t, s))
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decoding /me signed in: %v", err)
	}
	if got.Role != auth.RoleAdmin || got.Name != "andrew" {
		t.Errorf("signed in /me = %q/%q, want andrew/admin", got.Name, got.Role)
	}

	// And with the switch off /me refuses again -- from require, which is the
	// single gate serveMe sits behind rather than duplicating.
	admin.setPublic(false)
	if rec := as(t, s, http.MethodGet, "/me", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("/me with the switch off = %d, want 401", rec.Code)
	}
}

// TestPublicListenerGuestKeepsOnePresenceKey: presence starts and stops
// stations, so a guest needs an identity that is stable across requests. One
// that changed per request would count one browser as a crowd; a missing one
// would stop the station underneath them.
func TestPublicListenerGuestKeepsOnePresenceKey(t *testing.T) {
	s, _ := publicServer(t)

	rec := as(t, s, http.MethodPost, "/tune", `{"station_id":1}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("a guest tuning = %d: %s", rec.Code, rec.Body)
	}
	var minted *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == GuestCookie {
			minted = c
		}
	}
	if minted == nil || minted.Value == "" {
		t.Fatal("tuning as a guest minted no presence key")
	}
	// NOT THE SESSION COOKIE. An unsigned random id under the name a signed
	// token uses is how the two get confused by the next reader.
	if minted.Name == SessionCookie {
		t.Fatal("the guest key was written into the session cookie")
	}
	if !minted.HttpOnly {
		t.Error("the guest key is readable from script")
	}
	// AND IT EXPIRES. GuestLife is declared and documented; without this the
	// constant means nothing and a MaxAge of 0 -- a cookie that dies with the
	// browser, or one that never dies, depending on how it is written -- would
	// read as correct.
	if minted.MaxAge != int(GuestLife.Seconds()) {
		t.Errorf("the guest key lasts %ds, want %ds", minted.MaxAge, int(GuestLife.Seconds()))
	}

	// Carried back, it is reused rather than replaced.
	again := as(t, s, http.MethodPost, "/tune", `{"station_id":1}`, minted)
	for _, c := range again.Result().Cookies() {
		if c.Name == GuestCookie && c.Value != minted.Value {
			t.Errorf("a returning guest was given a second key %q, had %q", c.Value, minted.Value)
		}
	}
	if _, got := s.tuner.(*listenTuner).tuned(); got != minted.Value {
		t.Errorf("tuned under %q, want the key it carried %q", got, minted.Value)
	}
}

// TestPublicListenerGuestStreamsHLS: the point of the whole switch. A guest
// with no account fetches a playlist and is counted for the station, because a
// stream nobody is counted on is a station that stops underneath them.
func TestPublicListenerGuestStreamsHLS(t *testing.T) {
	s, admin := publicServer(t)
	touched := &stationTouch{}
	s.SetPresence(touched)
	dir := filepath.Join(s.cfg.SegmentDir, "1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("making a station directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatalf("writing a playlist: %v", err)
	}

	rec := as(t, s, http.MethodGet, "/hls/1/stream.m3u8", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("a guest fetching the playlist = %d: %s", rec.Code, rec.Body)
	}
	if touched.count() != 1 || touched.sess == "" {
		t.Fatalf("presence recorded %d beats under %q, want one under a real key",
			touched.count(), touched.sess)
	}

	admin.setPublic(false)
	if rec := as(t, s, http.MethodGet, "/hls/1/stream.m3u8", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("the stream with the switch off = %d, want 401", rec.Code)
	}
}

// TestPublicListenerAdminWriteTogglesIt: the operator's control, and the shape
// of the body it refuses.
func TestPublicListenerAdminWriteTogglesIt(t *testing.T) {
	s, admin := publicServer(t)
	admin.setPublic(false)

	post := func(body string) int {
		return as(t, s, http.MethodPost, "/admin/public-listener", body, adminCookie(t, s)).Code
	}
	if code := post(`{"public_listener":true}`); code != http.StatusNoContent {
		t.Fatalf("opening = %d, want 204", code)
	}
	if !admin.public {
		t.Error("the switch reported 204 and did not move")
	}
	if code := post(`{"public_listener":false}`); code != http.StatusNoContent || admin.public {
		t.Errorf("closing = %d, public=%v", code, admin.public)
	}
	// A MISSING FIELD IS NOT FALSE. Reading an absent key as "close it" would
	// let a malformed request quietly shut a station's listeners out.
	if code := post(`{}`); code != http.StatusBadRequest {
		t.Errorf("an empty body = %d, want 400", code)
	}
	admin.err = errors.New("no DJ is running")
	if code := post(`{"public_listener":true}`); code != http.StatusBadRequest {
		t.Errorf("a refused write = %d, want 400", code)
	}
	admin.err = nil
}

// TestRedoDossiersReportsTheRightKindOfFailure.
//
// SAME STATUSES AS serveRescan, the other route that sets a long library job
// going. Both of these were 400, which tells an operator they sent something
// wrong when the truth is either "this server has no library" or "the delete
// failed" -- and 400 is the one status a monitor will not page on.
func TestRedoDossiersReportsTheRightKindOfFailure(t *testing.T) {
	s, admin := publicServer(t)
	post := func() *httptest.ResponseRecorder {
		return as(t, s, http.MethodPost, "/admin/redo-dossiers", `{}`, adminCookie(t, s))
	}

	admin.cleared = 7
	rec := post()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cleared":7`) {
		t.Errorf("a good redo = %d: %s", rec.Code, rec.Body)
	}

	// No library is a capability the server does not have.
	admin.err = fmt.Errorf("%w to re-enrich", ErrNoLibrary)
	if rec := post(); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no library = %d, want 503", rec.Code)
	}

	// Anything else went wrong at our end.
	admin.err = errors.New("database is locked")
	if rec := post(); rec.Code != http.StatusInternalServerError {
		t.Errorf("a failed delete = %d, want 500", rec.Code)
	}
	admin.err = nil

	// And a listener cannot reach the switch that would remove their login.
	if code := as(t, s, http.MethodPost, "/admin/public-listener",
		`{"public_listener":true}`, listenerCookie(t, s)).Code; code != http.StatusForbidden {
		t.Errorf("a listener toggling it = %d, want 403", code)
	}
}

// TestPublicListenerGuestRoleIsNotSignable: the guest label must never become
// an authority. If Sign ever accepts it, a forged-looking role becomes a real
// session, so this is asserted against the signer rather than assumed.
func TestPublicListenerGuestRoleIsNotSignable(t *testing.T) {
	signer, err := auth.NewSigner([]byte(strings.Repeat("k", 32)), nil)
	if err != nil {
		t.Fatalf("building a signer: %v", err)
	}
	if _, err := signer.Sign(auth.Session{UserID: 1, Role: auth.RoleGuest, SID: "s"}); err == nil {
		t.Fatal("the guest role was signed into a session token")
	}
}

// TestListenerKeyIsTheSameAnswerForTuningAndStreaming.
//
// A REGRESSION GUARD on the paired paths. /hls asked the configured
// SessionSource and /tune called sessionFromCookie directly; nothing diverged
// while the app installs one as the other, but SetSessions is exported and two
// authorities for "which listener is this" is how one of them gets forgotten.
//
// The failure it prevents is specific: a listener tunes under one key and is
// counted under another, so presence never sees them and the station they are
// listening to stops underneath them.
func TestListenerKeyIsTheSameAnswerForTuningAndStreaming(t *testing.T) {
	s, _ := publicServer(t)
	touched := &stationTouch{}
	s.SetPresence(touched)
	dir := filepath.Join(s.cfg.SegmentDir, "1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A SOURCE OF ITS OWN, which is what SetSessions is for. If /tune ignores
	// it, the two keys differ and this fails.
	s.SetSessions(func(http.ResponseWriter, *http.Request) (string, bool) {
		return "the-one-key", true
	})

	if rec := as(t, s, http.MethodPost, "/tune", `{"station_id":1}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("tuning = %d: %s", rec.Code, rec.Body)
	}
	_, tunedAs := s.tuner.(*listenTuner).tuned()

	if rec := as(t, s, http.MethodGet, "/hls/1/stream.m3u8", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("streaming = %d: %s", rec.Code, rec.Body)
	}
	if tunedAs != touched.sess {
		t.Errorf("tuned as %q but counted as %q: presence will never see this listener",
			tunedAs, touched.sess)
	}
	if tunedAs != "the-one-key" {
		t.Errorf("the installed session source was ignored: got %q", tunedAs)
	}
}

// TestNoSessionSourceRefusesEvenWithPublicListeningOn.
//
// A REGRESSION GUARD on a mistake made while fixing something else. Unifying
// /tune and /hls onto one listener key, I added a "helpful" fallback to the
// cookie when no SessionSource was installed -- which would have served HLS on
// a server that installed no listener identity at all, quietly undoing the
// invariant documented one line above it.
//
// The existing bare-server test could not catch it: without an admin surface
// the fallback refuses anyway. It takes public listening ON to expose it.
func TestNoSessionSourceRefusesEvenWithPublicListeningOn(t *testing.T) {
	s, _ := publicServer(t)
	dir := filepath.Join(s.cfg.SegmentDir, "1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.SetSessions(nil)

	if rec := as(t, s, http.MethodGet, "/hls/1/stream.m3u8", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("the stream = %d with no session source, want 401", rec.Code)
	}
	// AND TUNING TOO, which is the half that shares the call now. A listener
	// who can tune but never be counted holds a station that stops underneath
	// them.
	if rec := as(t, s, http.MethodPost, "/tune", `{"station_id":1}`, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("tuning = %d with no session source, want 401", rec.Code)
	}
}

// TestAGuestKeyTheServerNeverIssuedIsReplaced.
//
// The guest cookie is a PLAIN cookie, so its value is the caller's to choose.
// Taken verbatim it became a presence identity: a four-kilobyte cookie became a
// four-kilobyte map key in the tracker, once per request, held for the whole
// grace period. A signed listener's SID arrives inside a token nobody can
// forge; a guest's has only its shape.
func TestAGuestKeyTheServerNeverIssuedIsReplaced(t *testing.T) {
	s, _ := publicServer(t)

	for _, bad := range []string{
		strings.Repeat("A", 4096),          // size is theirs
		"../../etc/passwd",                 // content is theirs
		"0123456789ab",                     // right alphabet, too short
		"0123456789abcdef0123456789abcdef", // right alphabet, too long
		"0123456789ABCDEF01234567",         // right length, wrong case
		"",                                 // empty
	} {
		rec := as(t, s, http.MethodPost, "/tune", `{"station_id":1}`,
			&http.Cookie{Name: GuestCookie, Value: bad})
		if rec.Code != http.StatusOK {
			t.Fatalf("tuning with %.20q = %d: a bad key must be replaced, not refused", bad, rec.Code)
		}
		_, used := s.tuner.(*listenTuner).tuned()
		if used == bad {
			t.Errorf("a caller-chosen key was used as an identity: %.40q", used)
		}
		if !guestKey.MatchString(used) {
			t.Errorf("the replacement is not a key this server issues: %q", used)
		}
	}

	// A key this server DID issue is kept, or every request would mint a new
	// identity and one listener would count as a crowd.
	minted := as(t, s, http.MethodPost, "/tune", `{"station_id":1}`, nil)
	var key string
	for _, c := range minted.Result().Cookies() {
		if c.Name == GuestCookie {
			key = c.Value
		}
	}
	if key == "" {
		t.Fatal("no key was minted")
	}
	as(t, s, http.MethodPost, "/tune", `{"station_id":1}`, &http.Cookie{Name: GuestCookie, Value: key})
	if _, got := s.tuner.(*listenTuner).tuned(); got != key {
		t.Errorf("a key this server issued was replaced: had %q, used %q", key, got)
	}
}

// TestTheGuestPathHoldsUnderConcurrentRequests.
//
// THE ONE CLASS NOTHING HERE EXERCISED. Every listener route is reached from
// many request goroutines at once, and public listening added state on that
// path -- the switch itself, and a cookie minted mid-request. Nothing ran two
// of those at the same time, because the shared test doubles were unguarded and
// any attempt reported a race in the DOUBLE rather than in the code.
//
// Run this with -race. It asserts no outcome beyond "nothing tore": the point
// is the detector, and a quiet run is the result.
func TestTheGuestPathHoldsUnderConcurrentRequests(t *testing.T) {
	s, admin := publicServer(t)
	s.SetPresence(&stationTouch{})
	dir := filepath.Join(s.cfg.SegmentDir, "1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte("#EXTM3U\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	// Guests arriving with no cookie, so every one of them mints a key.
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			as(t, s, http.MethodPost, "/tune", `{"station_id":1}`, nil)
			as(t, s, http.MethodGet, "/hls/1/stream.m3u8", "", nil)
			as(t, s, http.MethodGet, "/me", "", nil)
			as(t, s, http.MethodGet, "/stations.json", "", nil)
		}()
	}
	// And the operator throwing the switch underneath them, which is the
	// interleaving that actually worries: a request reading publicListening
	// while it changes.
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			admin.setPublic(n%2 == 0)
		}(i)
	}
	wg.Wait()

	// Left open, so the server is in a known state for whatever runs next.
	admin.setPublic(true)
	if rec := as(t, s, http.MethodGet, "/me", "", nil); rec.Code != http.StatusOK {
		t.Errorf("the server did not survive the load: /me = %d", rec.Code)
	}
}
