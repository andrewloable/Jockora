// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
)

// MOVING THE MOST EXPENSIVE THING THIS PROGRAM MAKES, and nothing else with it.

func TestEnrichPortAPIExportsAndImports(t *testing.T) {
	s, st, _ := playlistServer(t, 3)
	admin := adminCookie(t, s)

	rec := as(t, s, http.MethodGet, "/admin/enrichment/export", "", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", rec.Code, rec.Body)
	}
	// A FILE, named, so a browser saves it rather than rendering it.
	if got := rec.Header().Get("Content-Type"); got != "application/gzip" {
		t.Errorf("content type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Errorf("content disposition = %q, want an attachment", got)
	}
	body := rec.Body.Bytes()
	if _, err := gzip.NewReader(bytes.NewReader(body)); err != nil {
		t.Fatalf("the export is not gzip: %v", err)
	}

	// Round trip: the same server can read what it wrote. The dossiers are
	// already there, so every record reports as already having one.
	if _, err := st.DB().Exec(`DELETE FROM dossiers`); err != nil {
		t.Fatal(err)
	}
	up := as(t, s, http.MethodPost, "/admin/enrichment/import", string(body), admin)
	if up.Code != http.StatusOK {
		t.Fatalf("import = %d: %s", up.Code, up.Body)
	}
	var rep struct {
		Applied    int `json:"applied"`
		HadDossier int `json:"had_dossier"`
		Unmatched  int `json:"unmatched"`
	}
	if err := json.Unmarshal(up.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Applied != 3 {
		t.Errorf("report = %+v, want the three dossiers back", rep)
	}
}

func TestEnrichPortAPIRefusesAFileItCannotRead(t *testing.T) {
	// An import that silently does nothing is the failure to design against,
	// and so is one that answers 200 to a file it never understood.
	s, _, _ := playlistServer(t, 1)
	rec := as(t, s, http.MethodPost, "/admin/enrichment/import", "not an export at all", adminCookie(t, s))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("= %d, want 400: %s", rec.Code, rec.Body)
	}
}

func TestEnrichPortAPIRequiresAdmin(t *testing.T) {
	s, _, _ := playlistServer(t, 1)
	listener := listenerCookie(t, s)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/enrichment/export"},
		{http.MethodPost, "/admin/enrichment/import"},
	} {
		if rec := as(t, s, tc.method, tc.path, "x", listener); rec.Code != http.StatusForbidden {
			t.Errorf("%s as a listener = %d, want 403", tc.path, rec.Code)
		}
		if rec := as(t, s, tc.method, tc.path, "x", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s signed out = %d, want 401", tc.path, rec.Code)
		}
	}
}

func TestEnrichPortAPINeedsALibrary(t *testing.T) {
	bare, _, _ := authServer(t)
	admin := adminCookie(t, bare)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/admin/enrichment/export"},
		{http.MethodPost, "/admin/enrichment/import"},
	} {
		if rec := as(t, bare, tc.method, tc.path, "x", admin); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s with no library = %d, want 503", tc.path, rec.Code)
		}
	}
}

func TestEnrichPortAPIRejectsTheWrongMethod(t *testing.T) {
	s, _, _ := playlistServer(t, 1)
	admin := adminCookie(t, s)
	if rec := as(t, s, http.MethodPost, "/admin/enrichment/export", "", admin); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST to export = %d, want 405", rec.Code)
	}
	if rec := as(t, s, http.MethodGet, "/admin/enrichment/import", "", admin); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET to import = %d, want 405", rec.Code)
	}
	if rec := as(t, s, http.MethodGet, "/admin/enrichment/nonsense", "", admin); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown path = %d, want 404", rec.Code)
	}
}

func TestEnrichPortAPIOverwriteIsAskedForExplicitly(t *testing.T) {
	// Filling holes is the default; replacing what is already there is a
	// decision an operator has to make on purpose.
	s, st, _ := playlistServer(t, 1)
	admin := adminCookie(t, s)
	export := as(t, s, http.MethodGet, "/admin/enrichment/export", "", admin).Body.Bytes()

	if _, err := st.DB().Exec(
		`UPDATE dossiers SET json = '{"station_tags":["pop"],"subject_summary":"local","confidence":"high"}'`); err != nil {
		t.Fatal(err)
	}
	plain := as(t, s, http.MethodPost, "/admin/enrichment/import", string(export), admin)
	if !strings.Contains(plain.Body.String(), `"had_dossier":1`) {
		t.Errorf("without the flag = %s, want it skipped", plain.Body)
	}
	forced := as(t, s, http.MethodPost, "/admin/enrichment/import?overwrite=1", string(export), admin)
	if !strings.Contains(forced.Body.String(), `"applied":1`) {
		t.Errorf("with the flag = %s, want it applied", forced.Body)
	}
}

func TestEnrichPortAPIRefusesAFileTooBigToBeALibrary(t *testing.T) {
	// A cap so a mistaken upload cannot fill the disk of a machine whose whole
	// job is to keep playing.
	s, _, _ := playlistServer(t, 1)
	// The DECLARED length, not a quarter of a gigabyte of test fixture: the
	// point is that the refusal comes before the bytes do.
	req := httptest.NewRequest(http.MethodPost, "/admin/enrichment/import", strings.NewReader("x"))
	req.ContentLength = MaxImportBytes + 1
	req.AddCookie(adminCookie(t, s))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("= %d, want 413", rec.Code)
	}

	// And a request that does not declare one is stopped by the reader. The
	// body is gzip so it gets past the header check and is refused on length.
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(bytes.Repeat([]byte("junk that does not compress to nothing "), 64)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	streamed := httptest.NewRequest(http.MethodPost, "/admin/enrichment/import", bytes.NewReader(gz.Bytes()))
	streamed.ContentLength = -1
	streamed.AddCookie(adminCookie(t, s))
	rec = httptest.NewRecorder()
	s.importCap = 16
	s.Handler().ServeHTTP(rec, streamed)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("an undeclared oversize body = %d, want 413", rec.Code)
	}
}

// brokenPort is a library that stops answering half way through an export.
type brokenPort struct{}

func (brokenPort) ExportEnrichment(context.Context, io.Writer) error {
	return io.ErrUnexpectedEOF
}

func (brokenPort) ImportEnrichment(context.Context, io.Reader, bool) (enrich.PortReport, error) {
	return enrich.PortReport{}, io.ErrUnexpectedEOF
}

// TestEnrichPortAPISaysNothingUsefulWhenTheExportDiesMidStream: the status and
// the headers are already sent by the time a failure can happen, so all the
// operator can be given is a short file -- and all the server can do is say so
// in the log rather than pretend it succeeded.
func TestEnrichPortAPISaysNothingUsefulWhenTheExportDiesMidStream(t *testing.T) {
	s, _, _ := playlistServer(t, 1)
	s.SetEnrichmentPort(brokenPort{})
	rec := as(t, s, http.MethodGet, "/admin/enrichment/export", "", adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Errorf("= %d: the headers were already sent, so this cannot be an error code", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want nothing after the failure", rec.Body)
	}
}
