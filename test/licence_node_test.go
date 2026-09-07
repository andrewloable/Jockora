// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

//go:build gates

// The npm half of the dependency gate.
//
// The Angular BUNDLE is what ships: it is embedded in the binary and served to
// every browser, so a GPL package that reaches it is linked into the product
// exactly as surely as a Go module is. scripts/check-licences.sh is go-licenses;
// scripts/check-licences-node.sh is its opposite number, and this file proves it
// can actually fail -- a gate that has never been seen to reject anything is a
// gate nobody should trust.
package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run executes the node licence gate against the given web/app directory and
// returns its combined output and whether it succeeded.
func runNodeGate(t *testing.T, appDir string) (string, bool) {
	t.Helper()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}

	cmd := exec.Command("bash", filepath.Join(root, "scripts", "check-licences-node.sh"))
	cmd.Dir = root
	// JOCKORA_WEB_APP lets the test point the gate at a doctored copy of the
	// tree. Without it the only way to prove the gate rejects GPL would be to
	// install a GPL package into the real node_modules, which is a change to
	// the working tree that a failed test would leave behind.
	cmd.Env = append(os.Environ(), "JOCKORA_WEB_APP="+appDir)

	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// realApp is the working tree's own web/app.
func realApp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "web", "app"))
	if err != nil {
		t.Fatalf("resolving web/app: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
		t.Skipf("web/app/node_modules is absent; run npm ci first: %v", err)
	}
	return dir
}

// doctoredApp copies the real web/app -- by SYMLINKING node_modules rather than
// copying it, because it is hundreds of megabytes and the test would otherwise
// take minutes -- and returns the copy's path plus its node_modules, which is a
// real directory holding symlinks to each package.
func doctoredApp(t *testing.T) string {
	t.Helper()

	src := realApp(t)
	dst := t.TempDir()

	for _, name := range []string{"package.json", "package-lock.json"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	// One directory of symlinks, so the checker walks the real packages while
	// the test is free to add a fake one beside them.
	mods := filepath.Join(dst, "node_modules")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatalf("making node_modules: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(src, "node_modules"))
	if err != nil {
		t.Fatalf("reading node_modules: %v", err)
	}
	for _, e := range entries {
		if err := os.Symlink(filepath.Join(src, "node_modules", e.Name()), filepath.Join(mods, e.Name())); err != nil {
			t.Fatalf("linking %s: %v", e.Name(), err)
		}
	}
	return dst
}

// plant writes a package into the doctored tree and declares it a dependency,
// which is what makes the checker look at it at all.
func plant(t *testing.T, appDir, name, packageJSON string) {
	t.Helper()

	dir := filepath.Join(appDir, "node_modules", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("making %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatalf("writing %s/package.json: %v", name, err)
	}

	// The checker reports the tree reachable from package.json's dependencies,
	// so a package sitting in node_modules that nothing depends on is invisible
	// to it -- which is correct, since it does not ship either.
	manifest := filepath.Join(appDir, "package.json")
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("reading package.json: %v", err)
	}
	doctored := strings.Replace(string(b), `"dependencies": {`, `"dependencies": {`+"\n    \""+name+"\": \"1.0.0\",", 1)
	if doctored == string(b) {
		t.Fatalf("package.json has no dependencies block to plant %s into", name)
	}
	if err := os.WriteFile(manifest, []byte(doctored), 0o644); err != nil {
		t.Fatalf("writing package.json: %v", err)
	}
}

func TestLicenceNodeCleanTreePasses(t *testing.T) {
	out, ok := runNodeGate(t, realApp(t))
	if !ok {
		t.Fatalf("the node licence gate rejected the real tree:\n%s", out)
	}
	if !strings.Contains(out, "PASS") {
		t.Errorf("a passing gate should say so; got:\n%s", out)
	}
}

func TestLicenceNodeGPLDependencyFails(t *testing.T) {
	// The failure the gate exists for. One GPL package in the bundle kills the
	// commercial track, and it would be found by a licensee rather than by us.
	app := doctoredApp(t)
	plant(t, app, "evil", `{"name":"evil","version":"1.0.0","license":"GPL-3.0"}`)

	out, ok := runNodeGate(t, app)
	if ok {
		t.Fatalf("a GPL-3.0 dependency passed the gate:\n%s", out)
	}
	if !strings.Contains(out, "evil") {
		t.Errorf("the gate must NAME the package that failed it, or nobody can act on it; got:\n%s", out)
	}
}

func TestLicenceNodeUnknownLicenceFails(t *testing.T) {
	// A package with no licence field is not "probably fine". It is a package
	// whose terms nobody has read, which is the same risk as a bad one.
	app := doctoredApp(t)
	plant(t, app, "mystery", `{"name":"mystery","version":"1.0.0"}`)

	out, ok := runNodeGate(t, app)
	if ok {
		t.Fatalf("a package with no licence field passed the gate:\n%s", out)
	}
	if !strings.Contains(out, "mystery") {
		t.Errorf("the gate must name the package that failed it; got:\n%s", out)
	}
}

func TestLicenceNodeInGatesTarget(t *testing.T) {
	// A gate that exists and is never run is not a gate. This is the wiring
	// check: twelve subsystems in v0.1 were built and never called.
	out, err := exec.Command("make", "-n", "-C", "..", "gates").CombinedOutput()
	if err != nil {
		t.Fatalf("make -n gates: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "check-licences-node.sh") {
		t.Errorf("make gates does not run the node licence gate:\n%s", out)
	}
}
