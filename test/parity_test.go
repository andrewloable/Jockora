// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// The row 15 gate: parity, then deletion.
//
// Two UIs is two of everything -- two bug reports, two places a fix has to
// land, two answers to "what does the console do". The hand-written pages go.
// What must not go with them is any behaviour a listener or an operator could
// reach yesterday, and the way that is proven is a checklist this test parses
// rather than a memory: a forgotten row fails here.
//
// NOT build-tagged. This one runs in `go test ./...` on every save, because the
// thing it guards against -- a deleted page taking a behaviour with it -- is
// silent, and a gate nobody runs would not have caught it.
package test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/web"
)

type parityRow struct {
	page, line, behaviour, specFile, specName string
}

func parityChecklist(t *testing.T) []parityRow {
	t.Helper()

	body, err := os.ReadFile("parity_checklist.md")
	if err != nil {
		t.Fatalf("reading the checklist: %v", err)
	}

	var rows []parityRow
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 5 {
			continue
		}
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		// The header and its separator.
		if cells[0] == "page" || strings.HasPrefix(cells[0], "---") {
			continue
		}
		rows = append(rows, parityRow{cells[0], cells[1], cells[2], cells[3], cells[4]})
	}
	return rows
}

func TestParityChecklistComplete(t *testing.T) {
	rows := parityChecklist(t)

	// Every fetch( and every id=" from both pages. A checklist that lost half
	// its rows would otherwise pass by having nothing to check.
	if len(rows) < 25 {
		t.Fatalf("the checklist has %d rows; both pages together have more behaviours than that", len(rows))
	}

	specs := filepath.Join("..", "web", "app", "src", "app")
	dropped := 0

	for _, row := range rows {
		if row.specFile == "DROPPED" {
			dropped++
			// A dropped behaviour needs an ARGUMENT, not a shrug. The reason
			// column is what a person reads when they ask where the control
			// went, and "no longer needed" answers nothing.
			if len(row.specName) < 80 {
				t.Errorf("%s:%s %s is DROPPED with a reason of %d characters; say why it went",
					row.page, row.line, row.behaviour, len(row.specName))
			}
			continue
		}

		path := filepath.Join(specs, filepath.FromSlash(row.specFile))
		spec, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s:%s %s names spec file %s, which does not exist: %v",
				row.page, row.line, row.behaviour, row.specFile, err)
			continue
		}
		if !strings.Contains(string(spec), row.specName) {
			t.Errorf("%s:%s %s names spec %q, which is not in %s",
				row.page, row.line, row.behaviour, row.specName, row.specFile)
		}
	}

	// Deleting a behaviour is a decision. Making it by marking rows DROPPED
	// until the checklist is empty is not.
	if dropped > 3 {
		t.Errorf("%d rows are DROPPED; parity means carrying behaviour over, not writing it off", dropped)
	}
}

func TestParityOldPagesGone(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "web", "index.html"),
		filepath.Join("..", "web", "admin.html"),
		filepath.Join("..", "web", "vendor"),
	} {
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s still exists; the Angular app replaced it", path)
		}
	}
}

func TestParityOldPagesAreNotServed(t *testing.T) {
	srv := httptest.NewServer(web.Handler())
	defer srv.Close()

	for _, path := range []string{"/index.html", "/vendor/hls.light.min.js", "/admin"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body := make([]byte, 4096)
		n, _ := resp.Body.Read(body)
		_ = resp.Body.Close()
		got := string(body[:n])

		// Either answer is fine -- a 404, or the SPA shell for a route the
		// Angular router owns. What must never come back is the old page.
		for _, gone := range []string{"Jockora — operator", `id="dial"`, `id="writewarn"`} {
			if strings.Contains(got, gone) {
				t.Errorf("GET %s still serves the old page: it contains %q", path, gone)
			}
		}
	}
}
