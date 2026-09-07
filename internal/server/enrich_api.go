// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/andrewloable/jockora/internal/enrich"
)

// MaxImportBytes caps an upload.
//
// A gzipped export of a very large library is a few tens of megabytes; this is
// generous against that and small enough that a mistaken upload cannot fill the
// disk of a machine whose whole job is to keep playing.
var MaxImportBytes int64 = 256 << 20

// setMaxImport lowers the cap so a test can prove the streaming backstop
// without a quarter of a gigabyte of fixture.
func setMaxImport(n int64) { MaxImportBytes = n }

// EnrichmentPort moves enrichment between installs.
//
// An interface rather than the store, so the handler can be driven without a
// database and so the thing it calls is named: this is the enrichment, not a
// backup, and nothing here reaches the users table.
type EnrichmentPort interface {
	ExportEnrichment(ctx context.Context, w io.Writer) error
	ImportEnrichment(ctx context.Context, r io.Reader, overwrite bool) (enrich.PortReport, error)
}

// SetEnrichmentPort wires export and import.
func (s *Server) SetEnrichmentPort(p EnrichmentPort) { s.port = p }

// serveEnrichment handles /admin/enrichment/export and /import.
func (s *Server) serveEnrichment(w http.ResponseWriter, r *http.Request, path string) {
	action := strings.Trim(strings.TrimPrefix(path, "/admin/enrichment"), "/")
	if action != "export" && action != "import" {
		http.NotFound(w, r)
		return
	}
	if s.port == nil {
		http.Error(w, "no library to export", http.StatusServiceUnavailable)
		return
	}
	switch {
	case action == "export" && r.Method == http.MethodGet:
		s.exportEnrichment(w, r)
	case action == "import" && r.Method == http.MethodPost:
		s.importEnrichment(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// exportEnrichment streams the enrichment as a file.
func (s *Server) exportEnrichment(w http.ResponseWriter, r *http.Request) {
	name := fmt.Sprintf("jockora-enrichment-%s.jsonl.gz", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// no-store: an enrichment export is a snapshot, and a cached one handed to
	// somebody tomorrow is quietly the wrong library.
	w.Header().Set("Cache-Control", "no-store")

	// STREAMED, so a ten-thousand-track library is never a document in memory.
	// The status is already sent by the time a failure can happen mid-stream,
	// so there is nothing to do but log it: the operator sees a short file.
	if err := s.port.ExportEnrichment(r.Context(), w); err != nil {
		s.log.Error("enrichment export failed part-way", "err", err)
	}
}

// importEnrichment merges an uploaded export and reports what it did.
func (s *Server) importEnrichment(w http.ResponseWriter, r *http.Request) {
	// THE DECLARED LENGTH FIRST. Refusing a 4GB upload after streaming it is a
	// refusal that already cost what it was meant to prevent, and a browser
	// sending a file always says how big it is.
	if r.ContentLength > MaxImportBytes {
		http.Error(w, "that file is too large to be an enrichment export",
			http.StatusRequestEntityTooLarge)
		return
	}
	// And the reader as the backstop, for a request that does not say.
	body := http.MaxBytesReader(w, r.Body, MaxImportBytes)
	defer body.Close() //nolint:errcheck // request body

	// OVERWRITE IS ASKED FOR EXPLICITLY. Filling holes is the default; an
	// operator replacing dossiers they already have is making a decision.
	overwrite := r.URL.Query().Get("overwrite") == "1"

	report, err := s.port.ImportEnrichment(r.Context(), body, overwrite)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "that file is too large to be an enrichment export",
				http.StatusRequestEntityTooLarge)
			return
		}
		// A file this program did not write, or one from a newer version. Both
		// are the operator's to fix, so both are 400 with the reason.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// THE REPORT IS THE FEATURE. An import that silently does nothing is what
	// this endpoint exists to make impossible.
	s.writeJSON(w, http.StatusOK, report)
}
