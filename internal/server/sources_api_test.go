// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/andrewloable/jockora/internal/library"
	"github.com/andrewloable/jockora/internal/store"
)

// fakeRescan records what the route asked for without walking a library.
type fakeRescan struct {
	mu      sync.Mutex
	starts  int
	err     error
	running bool
	ctx     context.Context
}

func (f *fakeRescan) Start(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.starts++
	f.running = true
	f.ctx = ctx
	return nil
}
func (f *fakeRescan) Progress() library.RescanProgress {
	f.mu.Lock()
	defer f.mu.Unlock()
	return library.RescanProgress{Running: f.running}
}
func (f *fakeRescan) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

// pingable is an OpenSubsonic server that answers, or refuses.
func pingable(t *testing.T, ok bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if ok {
			fmt.Fprint(w, `{"subsonic-response":{"status":"ok"}}`)
			return
		}
		fmt.Fprint(w, `{"subsonic-response":{"status":"failed",
			"error":{"code":40,"message":"Wrong username or password"}}}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func sourcesServer(t *testing.T) (*Server, *store.Store, *fakeRescan) {
	t.Helper()
	s, st, _ := authServer(t)
	rescan := &fakeRescan{}
	s.SetSources(st, rescan)
	return s, st, rescan
}

func TestSourcesAPICreateFolder(t *testing.T) {
	s, st, _ := sourcesServer(t)
	admin := adminCookie(t, s)
	dir := t.TempDir()

	rec := as(t, s, http.MethodPost, "/admin/sources",
		`{"kind":"folder","locator":"`+dir+`"}`, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	rows, err := st.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Locator != dir || !rows[0].Enabled {
		t.Errorf("stored %+v", rows)
	}

	// A folder that is not there fails HERE, not silently in a background scan
	// where the operator sees a source, no tracks, and nothing saying why.
	missing := as(t, s, http.MethodPost, "/admin/sources",
		`{"kind":"folder","locator":"`+filepath.Join(dir, "nope")+`"}`, admin)
	if missing.Code != http.StatusBadRequest {
		t.Errorf("a folder that does not exist = %d, want 400", missing.Code)
	}
	var e struct{ Field, Error string }
	if err := json.Unmarshal(missing.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.Field != "locator" {
		t.Errorf("named field %q, want locator", e.Field)
	}

	// A FILE where a folder should be is the same mistake with a clearer cause.
	file := filepath.Join(dir, "a.mp3")
	if err := writeTestFile(file); err != nil {
		t.Fatal(err)
	}
	if rec := as(t, s, http.MethodPost, "/admin/sources",
		`{"kind":"folder","locator":"`+file+`"}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a file as a folder = %d, want 400", rec.Code)
	}

	// And a kind nothing can scan.
	if rec := as(t, s, http.MethodPost, "/admin/sources",
		`{"kind":"spotify","locator":"x"}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown kind = %d, want 400", rec.Code)
	}
	if rec := as(t, s, http.MethodPost, "/admin/sources", "not json", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON = %d, want 400", rec.Code)
	}
}

// TestSourcesAPICreateSubsonicPings: the credentials are checked while the
// operator is still looking at the form.
func TestSourcesAPICreateSubsonicPings(t *testing.T) {
	s, st, _ := sourcesServer(t)
	admin := adminCookie(t, s)

	good := pingable(t, true)
	rec := as(t, s, http.MethodPost, "/admin/sources",
		`{"kind":"subsonic","locator":"`+good+`","username":"andrew","password":"hunter2"}`, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("a reachable server = %d: %s", rec.Code, rec.Body)
	}
	rows, _ := st.ListSources(context.Background())
	if len(rows) != 1 || rows[0].Password != "hunter2" {
		t.Errorf("the credentials were not stored: %+v", rows)
	}

	bad := as(t, s, http.MethodPost, "/admin/sources",
		`{"kind":"subsonic","locator":"`+pingable(t, false)+`","username":"andrew","password":"wrong"}`, admin)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("wrong credentials = %d, want 400", bad.Code)
	}
	// The SERVER'S OWN WORDS, so the operator knows it was the password and
	// not the address.
	if !strings.Contains(bad.Body.String(), "username or password") {
		t.Errorf("the refusal does not carry the server's reason: %s", bad.Body)
	}

	unreachable := as(t, s, http.MethodPost, "/admin/sources",
		`{"kind":"subsonic","locator":"http://127.0.0.1:1","username":"a","password":"b"}`, admin)
	if unreachable.Code != http.StatusBadRequest {
		t.Errorf("an unreachable server = %d, want 400", unreachable.Code)
	}
	if rows, _ := st.ListSources(context.Background()); len(rows) != 1 {
		t.Errorf("%d sources stored; only the reachable one should be", len(rows))
	}
}

func TestSourcesAPIListRedactsPassword(t *testing.T) {
	s, st, _ := sourcesServer(t)
	admin := adminCookie(t, s)
	if _, err := st.CreateSource(context.Background(), store.Source{
		Kind: store.SourceSubsonic, Locator: "http://nas:4533",
		Username: "andrew", Password: "hunter2", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	rec := as(t, s, http.MethodGet, "/admin/sources", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "password") {
		t.Errorf("the list leaks the credential: %s", rec.Body)
	}
	var out []sourceView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// The username IS shown: an operator has to be able to tell two sources on
	// one server apart.
	if len(out) != 1 || out[0].Username != "andrew" || out[0].Locator != "http://nas:4533" {
		t.Errorf("list = %+v", out)
	}
}

// TestSourcesAPIDeleteMarksTracksMissing: the rows carry dossiers worth minutes
// of model time each. Deleting them to remove a source would throw that away,
// and a source added back should find them waiting.
func TestSourcesAPIDeleteMarksTracksMissing(t *testing.T) {
	s, st, _ := sourcesServer(t)
	admin := adminCookie(t, s)
	ctx := context.Background()

	id, err := st.CreateSource(ctx, store.Source{
		Kind: store.SourceFolder, Locator: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := st.DB().Exec(
			`INSERT INTO tracks (id, path, playable, source_id) VALUES (?, ?, 1, ?)`,
			i, "/m/"+strconv.Itoa(i)+".mp3", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB().Exec(
		`INSERT INTO dossiers (track_id, json, confidence) VALUES (1, '{}', 'high')`); err != nil {
		t.Fatal(err)
	}

	if rec := as(t, s, http.MethodDelete, "/admin/sources/"+strconv.FormatInt(id, 10), "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}

	var rows, marked, dossiers int
	if err := st.DB().QueryRow(`SELECT count(*) FROM tracks`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow(`SELECT count(*) FROM tracks WHERE missing_at IS NOT NULL`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow(`SELECT count(*) FROM dossiers`).Scan(&dossiers); err != nil {
		t.Fatal(err)
	}
	if rows != 3 || marked != 3 || dossiers != 1 {
		t.Errorf("rows=%d marked=%d dossiers=%d; want 3, 3, 1", rows, marked, dossiers)
	}
	if left, _ := st.ListSources(ctx); len(left) != 0 {
		t.Errorf("the source is still listed: %+v", left)
	}

	if rec := as(t, s, http.MethodDelete, "/admin/sources/9999", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("deleting a source that is gone = %d, want 404", rec.Code)
	}
}

func TestSourcesAPIEnableDisable(t *testing.T) {
	s, st, _ := sourcesServer(t)
	admin := adminCookie(t, s)
	id, err := st.CreateSource(context.Background(), store.Source{
		Kind: store.SourceFolder, Locator: t.TempDir(), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	path := "/admin/sources/" + strconv.FormatInt(id, 10)

	if rec := as(t, s, http.MethodPost, path+"/disable", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("disable = %d", rec.Code)
	}
	if on, _ := st.ListEnabledSources(context.Background()); len(on) != 0 {
		t.Error("a disabled source is still scanned")
	}
	if all, _ := st.ListSources(context.Background()); len(all) != 1 || all[0].Enabled {
		t.Errorf("disabling deleted it or did nothing: %+v", all)
	}

	if rec := as(t, s, http.MethodPost, path+"/enable", "", admin); rec.Code != http.StatusNoContent {
		t.Fatalf("enable = %d", rec.Code)
	}
	if on, _ := st.ListEnabledSources(context.Background()); len(on) != 1 {
		t.Error("enabling did not bring it back")
	}
	if rec := as(t, s, http.MethodPost, "/admin/sources/9999/enable", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("a source that is gone = %d, want 404", rec.Code)
	}
}

// TestSourcesAPIRescanStarts: 202 rather than 200, because a scan of a real
// library takes minutes and holding the request open would time out in every
// browser. Progress rides on /now.json.
func TestSourcesAPIRescanStarts(t *testing.T) {
	s, _, rescan := sourcesServer(t)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodPost, "/admin/rescan", "", admin)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	if rescan.count() != 1 {
		t.Errorf("the scan was started %d times", rescan.count())
	}
	if !strings.Contains(rec.Body.String(), "running") {
		t.Errorf("the answer carries no progress to poll: %s", rec.Body)
	}

	// Two walks at once is a race with no correct answer.
	// THE SCAN OUTLIVES THE REQUEST. A scan tied to the request's context
	// would be cancelled the moment the browser was answered, so the operator
	// would press rescan, get a 202, and nothing would ever happen.
	req := httptest.NewRequest(http.MethodPost, "/admin/rescan", nil)
	ctx, cancel := context.WithCancel(req.Context())
	fresh := &fakeRescan{}
	s.SetSources(nil, fresh)
	s.serveRescan(httptest.NewRecorder(), req.WithContext(ctx))
	cancel()
	fresh.mu.Lock()
	scanCtx := fresh.ctx
	fresh.mu.Unlock()
	if scanCtx == nil {
		t.Fatal("the scan was never started")
	}
	if scanCtx.Err() != nil {
		t.Errorf("the scan's context died with the request: %v", scanCtx.Err())
	}

	rescan.mu.Lock()
	rescan.err = library.ErrScanRunning
	rescan.mu.Unlock()
	s.SetSources(nil, rescan)
	if rec := as(t, s, http.MethodPost, "/admin/rescan", "", admin); rec.Code != http.StatusConflict {
		t.Errorf("a second scan = %d, want 409", rec.Code)
	}

	rescan.mu.Lock()
	rescan.err = errors.New("the disk went away")
	rescan.mu.Unlock()
	if rec := as(t, s, http.MethodPost, "/admin/rescan", "", admin); rec.Code != http.StatusInternalServerError {
		t.Errorf("a scan that could not start = %d, want 500", rec.Code)
	}
}

func TestSourcesAPIRoleMatrix(t *testing.T) {
	s, _, _ := sourcesServer(t)
	listener := listenerCookie(t, s)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/sources"},
		{http.MethodPost, "/admin/sources"},
		{http.MethodDelete, "/admin/sources/1"},
		{http.MethodPost, "/admin/sources/1/enable"},
		{http.MethodPost, "/admin/sources/1/disable"},
		{http.MethodPost, "/admin/rescan"},
	} {
		if rec := as(t, s, tc.method, tc.path, "{}", listener); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as a listener = %d, want 403", tc.method, tc.path, rec.Code)
		}
		if rec := as(t, s, tc.method, tc.path, "{}", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymously = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestSourcesAPIWithoutALibrary(t *testing.T) {
	// The spike path: accounts exist, a library does not.
	s, _, _ := authServer(t)
	admin := adminCookie(t, s)

	if rec := as(t, s, http.MethodGet, "/admin/sources", "", admin); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("= %d, want 503", rec.Code)
	}
	if rec := as(t, s, http.MethodPost, "/admin/rescan", "", admin); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("rescan = %d, want 503", rec.Code)
	}
}

func TestSourcesAPIBadTargets(t *testing.T) {
	s, _, _ := sourcesServer(t)
	admin := adminCookie(t, s)

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodDelete, "/admin/sources/abc", http.StatusNotFound},
		{http.MethodPost, "/admin/sources/1/a/b", http.StatusNotFound},
		{http.MethodPost, "/admin/sources/1/nonsense", http.StatusMethodNotAllowed},
		{http.MethodGet, "/admin/sources/1", http.StatusMethodNotAllowed},
		{http.MethodPut, "/admin/sources", http.StatusMethodNotAllowed},
	} {
		if rec := as(t, s, tc.method, tc.path, "", admin); rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

// brokenSources fails every call, which is what a store that has gone away
// looks like from up here.
type brokenSources struct {
	err error
	// retireOK lets a test reach the delete itself, with the retirement that
	// has to happen first out of the way.
	retireOK bool
	retired  int
	deleted  int
}

func (b *brokenSources) ListSources(context.Context) ([]store.Source, error) {
	return nil, b.err
}

func (b *brokenSources) CreateSource(context.Context, store.Source) (int64, error) {
	return 0, b.err
}

func (b *brokenSources) DeleteSource(context.Context, int64) error {
	b.deleted++
	return b.err
}

func (b *brokenSources) SetSourceEnabled(context.Context, int64, bool) error {
	return b.err
}

func (b *brokenSources) RetireSourceTracks(context.Context, int64) (int, error) {
	b.retired++
	if b.retireOK {
		return 0, nil
	}
	return 0, b.err
}

// TestSourcesAPISurfacesStoreFailures: a swallowed failure looks exactly like
// an empty source list or a source that was added, and an operator acts on both.
func TestSourcesAPISurfacesStoreFailures(t *testing.T) {
	s, _, rescan := sourcesServer(t)
	admin := adminCookie(t, s)
	dir := t.TempDir()
	// The session still resolves, so the request is authorised, while
	// everything about sources underneath it fails.
	broken := &brokenSources{err: errors.New("the database went away")}
	s.SetSources(broken, rescan)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/sources", ""},
		{http.MethodPost, "/admin/sources", `{"kind":"folder","locator":"` + dir + `"}`},
		{http.MethodDelete, "/admin/sources/1", ""},
		{http.MethodPost, "/admin/sources/1/enable", ""},
		{http.MethodPost, "/admin/sources/1/disable", ""},
	} {
		rec := as(t, s, tc.method, tc.path, tc.body, admin)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s %s with a failing store = %d, want 500", tc.method, tc.path, rec.Code)
		}
	}

	// A delete whose RETIREMENT fails must NOT go on to remove the source:
	// losing the source row while its tracks stay playable would leave the
	// library holding files nothing can rescan and nothing can retire.
	if broken.deleted != 0 {
		t.Errorf("the source was deleted %d times despite the retirement failing", broken.deleted)
	}

	// And a delete that gets past retirement still reports its own failure.
	broken.retireOK = true
	if rec := as(t, s, http.MethodDelete, "/admin/sources/1", "", admin); rec.Code != http.StatusInternalServerError {
		t.Errorf("a delete that failed = %d, want 500", rec.Code)
	}
	if broken.deleted != 1 {
		t.Errorf("the delete ran %d times, want once", broken.deleted)
	}

	s.listSources(&failingWriter{}, httptest.NewRequest(http.MethodGet, "/admin/sources", nil))
}
func writeTestFile(path string) error {
	return os.WriteFile(path, []byte("x"), 0o600)
}
