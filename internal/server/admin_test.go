// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestEnrichmentRestartReportsWhatItDid: the Resume button used to report a
// bare 204 whatever happened, so restarting a dead worker and unpausing a live
// one were indistinguishable to the operator pressing the button -- and during
// the outage of 2026-09-08 the one that did nothing reported success.
func TestEnrichmentRestartReportsWhatItDid(t *testing.T) {
	s, _, _ := authServer(t)
	admin := &fakeAdmin{cadence: 4,
		said: "Enrichment restarted. It had stopped; watch the dossier count."}
	s.SetAdmin(admin)

	rec := as(t, s, http.MethodPost, "/admin/enriching", `{"enriching":true}`, adminCookie(t, s))
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Said string `json:"said"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	if !strings.Contains(body.Said, "restarted") {
		t.Errorf("said = %q, want it to say the worker was restarted", body.Said)
	}
	if !admin.enriching {
		t.Error("the flag was not set")
	}
}

// TestEnrichmentRestartStaysBehindAnOperatorAccount: it starts real work on the
// heaviest thing this process does, so it is a write like every other.
func TestEnrichmentRestartStaysBehindAnOperatorAccount(t *testing.T) {
	s, _, _ := authServer(t)
	admin := &fakeAdmin{cadence: 4}
	s.SetAdmin(admin)

	rec := post(t, s, "/admin/enriching", `{"enriching":true}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymously = %d, want 401: %s", rec.Code, rec.Body)
	}
	if admin.enriching {
		t.Error("an anonymous request restarted enrichment")
	}
}
