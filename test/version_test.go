// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// The two places Jockora states its own version, kept in step.
//
// NOT build-tagged. This runs in `go test ./...` on every save, because the
// thing it guards against is silent: two numbers in two languages that nothing
// forces to agree, and the disagreement only shows up in a bug report where
// somebody has already wasted an hour on the wrong release.
package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// versionRE pulls the Go side's declaration out of the source.
//
// READ FROM SOURCE, because cmd/jockora is package main and cannot be imported
// from here. It is the same trade cmd/jockora/main_test.go makes for its
// LevelVar wiring and test/console_css_test.go makes for the stylesheet.
var versionRE = regexp.MustCompile(`var Version = "([^"]+)"`)

func TestVersionsAgree(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "cmd", "jockora", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := versionRE.FindSubmatch(src)
	if m == nil {
		t.Fatal("cmd/jockora/main.go no longer declares `var Version = \"...\"`; " +
			"this test cannot see the version the binary reports")
	}
	backend := string(m[1])

	raw, err := os.ReadFile(filepath.Join("..", "web", "app", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}

	if pkg.Version != backend {
		t.Errorf("the console says %q and the binary says %q.\n"+
			"One product, one version: `jockora version` is what an operator puts in a "+
			"bug report, and a console built from a different number makes that report "+
			"point at the wrong release.\n"+
			"Set both: cmd/jockora/main.go `var Version` and web/app/package.json `version`.",
			pkg.Version, backend)
	}

	// NOT THE ANGULAR DEFAULT. package.json ships with 0.0.0 and nothing ever
	// complains about it -- this project carried it through the whole of v0.1
	// and v0.2 -- so a check that only compares the two would pass happily with
	// both of them left at zero.
	if backend == "0.0.0" {
		t.Error("the version is still 0.0.0, which is the value `ng new` writes " +
			"and nobody chooses")
	}
}
