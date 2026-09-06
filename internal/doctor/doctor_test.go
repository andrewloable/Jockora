// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package doctor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func healthyConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	lib := filepath.Join(dir, "music")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	return Config{
		LibraryPath: lib,
		SegmentDir:  filepath.Join(dir, "segments"),
		DBPath:      filepath.Join(dir, "jockora.db"),
	}
}

func find(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %v", name, names(checks))
	return Check{}
}

func names(checks []Check) []string {
	var out []string
	for _, c := range checks {
		out = append(out, c.Name)
	}
	return out
}

func TestDetectsMissingFFmpeg(t *testing.T) {
	cfg := healthyConfig(t)
	cfg.FFmpegPath = "definitely-not-ffmpeg-anywhere"

	c := find(t, Run(context.Background(), cfg), "ffmpeg")

	if c.OK {
		t.Fatal("a nonexistent ffmpeg passed")
	}
	if !strings.Contains(c.Detail, "definitely-not-ffmpeg-anywhere") {
		t.Errorf("detail %q does not name the binary", c.Detail)
	}
	if c.Fix == "" || !strings.Contains(c.Fix, "install") {
		t.Errorf("Fix = %q, want an install command", c.Fix)
	}
	if !c.Hard {
		t.Error("a missing ffmpeg is not marked as a hard failure")
	}
}

func TestDetectsMissingFilter(t *testing.T) {
	// A stand-in ffmpeg that runs but lists no filters.
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'Filters:'\necho ' anull  A->A  Pass audio.'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := healthyConfig(t)
	cfg.FFmpegPath = fake

	c := find(t, Run(context.Background(), cfg), "ffmpeg filters")

	if c.OK {
		t.Fatal("an ffmpeg without loudnorm passed")
	}
	if !strings.Contains(c.Detail, "loudnorm") {
		t.Errorf("detail %q does not name the missing filter", c.Detail)
	}
	if c.Fix == "" {
		t.Error("no Fix for a missing filter")
	}
}

func TestDetectsUnreachableLLM(t *testing.T) {
	cfg := healthyConfig(t)
	cfg.LLMBaseURL = "http://127.0.0.1:1" // nothing listens on port 1
	cfg.RequireLLM = true

	c := find(t, Run(context.Background(), cfg), "llama-server")

	if c.OK {
		t.Fatal("an unreachable LLM passed")
	}
	if !strings.Contains(c.Detail, "127.0.0.1:1") {
		t.Errorf("detail %q does not name the URL", c.Detail)
	}
	if !strings.Contains(c.Fix, "llama-server -m") {
		t.Errorf("Fix = %q, want the start command", c.Fix)
	}
}

// TestDetectsOpenAIOnlyServer: a 404 on /completion means something is
// listening, but not llama.cpp's native API.
func TestDetectsOpenAIOnlyServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cfg := healthyConfig(t)
	cfg.LLMBaseURL = srv.URL
	cfg.HTTPClient = srv.Client()

	c := find(t, Run(context.Background(), cfg), "llama-server")

	if c.OK {
		t.Fatal("a 404 on /completion passed")
	}
	if !strings.Contains(c.Fix, "native /completion") {
		t.Errorf("Fix = %q, want it to explain the OpenAI-only case", c.Fix)
	}
}

// TestDetectsSchemaIgnored is the check that matters most: a server that is UP
// but ignores json_schema fails at dossier time, hours later, not at startup.
func TestDetectsSchemaIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reachable, answers, and completely ignores the schema.
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"content": "Sure! Here is some prose instead of JSON.", "stop_type": "eos",
		})
	}))
	defer srv.Close()

	cfg := healthyConfig(t)
	cfg.LLMBaseURL = srv.URL
	cfg.HTTPClient = srv.Client()

	c := find(t, Run(context.Background(), cfg), "llama-server")

	if c.OK {
		t.Fatal("a server that ignored json_schema passed a reachability-only check")
	}
	if !strings.Contains(c.Fix, "upgrade llama.cpp") {
		t.Errorf("Fix = %q, want the upgrade instruction", c.Fix)
	}
}

func TestLLMRoundTripPasses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"content": `{"ok":true}`, "stop_type": "eos",
		})
	}))
	defer srv.Close()

	cfg := healthyConfig(t)
	cfg.LLMBaseURL = srv.URL
	cfg.HTTPClient = srv.Client()

	if c := find(t, Run(context.Background(), cfg), "llama-server"); !c.OK {
		t.Errorf("a schema-honouring server failed: %s", c.Detail)
	}
}

func TestDetectsUnwritableSegmentDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write anywhere")
	}
	dir := t.TempDir()
	readonly := filepath.Join(dir, "ro")
	if err := os.Mkdir(readonly, 0o555); err != nil {
		t.Fatal(err)
	}

	cfg := healthyConfig(t)
	cfg.SegmentDir = filepath.Join(readonly, "segments")

	c := find(t, Run(context.Background(), cfg), "segment dir")

	if c.OK {
		t.Fatal("an unwritable segment dir passed")
	}
	if c.Fix == "" {
		t.Error("no Fix for an unwritable segment dir")
	}
}

func TestDetectsMissingLibrary(t *testing.T) {
	cfg := healthyConfig(t)
	cfg.LibraryPath = filepath.Join(t.TempDir(), "no-such-library")
	cfg.RequireLibrary = true

	c := find(t, Run(context.Background(), cfg), "library path")

	if c.OK {
		t.Fatal("a nonexistent library path passed")
	}
	if c.Fix == "" {
		t.Error("no Fix for a missing library")
	}
}

// TestTTSFailureIsSoft: speech is optional, music is not. A missing sidecar
// costs breaks, never the stream.
func TestTTSFailureIsSoft(t *testing.T) {
	cfg := healthyConfig(t)
	cfg.TTSAddr = "http://127.0.0.1:1"

	checks := Run(context.Background(), cfg)
	c := find(t, checks, "tts sidecar")

	if c.OK {
		t.Fatal("an unreachable sidecar passed")
	}
	if c.Hard {
		t.Error("a missing TTS sidecar is marked hard; that would refuse to serve music over a speech problem")
	}
	for _, f := range Failed(checks) {
		if f.Name == "tts sidecar" {
			t.Error("the TTS check appears in the hard-failure list")
		}
	}
}

// TestLLMIsHardOnlyWhenRequired: the splice spike plays tracks and a
// pre-rendered WAV and calls no model at all, so requiring one would refuse to
// start a run that does not need it. Once the DJ brain is wired it becomes hard.
func TestLLMIsHardOnlyWhenRequired(t *testing.T) {
	cfg := healthyConfig(t)
	cfg.LLMBaseURL = "http://127.0.0.1:1"

	checks := Run(context.Background(), cfg)
	if c := find(t, checks, "llama-server"); c.OK || c.Hard {
		t.Errorf("LLM check: ok=%v hard=%v, want a soft failure when not required", c.OK, c.Hard)
	}
	for _, f := range Failed(checks) {
		if f.Name == "llama-server" {
			t.Error("the LLM appears in the hard-failure list without RequireLLM")
		}
	}

	cfg.RequireLLM = true
	if c := find(t, Run(context.Background(), cfg), "llama-server"); !c.Hard {
		t.Error("RequireLLM did not make the check hard")
	}
}

// TestNoLibraryIsFineWhenTracksAreGivenDirectly: the splice spike is handed
// track files and has no library at all.
func TestNoLibraryIsFineWhenTracksAreGivenDirectly(t *testing.T) {
	cfg := healthyConfig(t)
	cfg.LibraryPath = ""

	if c := find(t, Run(context.Background(), cfg), "library path"); !c.OK {
		t.Errorf("an absent library was a failure for a run that does not need one: %s", c.Detail)
	}

	cfg.RequireLibrary = true
	if c := find(t, Run(context.Background(), cfg), "library path"); c.OK {
		t.Error("an absent library passed when it was required")
	}
}

func TestEveryFailureCarriesAFix(t *testing.T) {
	cfg := Config{RequireLLM: true, RequireLibrary: true} // nothing configured at all
	for _, c := range Run(context.Background(), cfg) {
		if !c.OK && c.Fix == "" {
			t.Errorf("check %q failed with no Fix: %q", c.Name, c.Detail)
		}
		if !c.OK && c.Detail == "" {
			t.Errorf("check %q failed with no Detail", c.Name)
		}
	}
}

// TestNoFailureSaysDependencyError: naming the binary, the filter, the URL or
// the path is the whole point.
func TestNoFailureSaysDependencyError(t *testing.T) {
	for _, c := range Run(context.Background(), Config{RequireLibrary: true}) {
		if strings.EqualFold(strings.TrimSpace(c.Detail), "dependency error") {
			t.Errorf("check %q reports a generic failure", c.Name)
		}
	}
}

func TestReportIsReadable(t *testing.T) {
	out := Report(Run(context.Background(), Config{RequireLibrary: true}))

	if !strings.Contains(out, "JOCKORA PREFLIGHT") {
		t.Error("the report has no heading")
	}
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "fix:") {
		t.Errorf("the report does not show failures and fixes:\n%s", out)
	}
}

func TestHealthyConfigHasNoHardFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/health") {
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"content": `{"ok":true}`}) //nolint:errcheck
	}))
	defer srv.Close()

	cfg := healthyConfig(t)
	cfg.LLMBaseURL = srv.URL
	cfg.TTSAddr = srv.URL
	cfg.HTTPClient = srv.Client()

	checks := Run(context.Background(), cfg)
	if failed := Failed(checks); len(failed) > 0 {
		t.Errorf("a healthy configuration reported hard failures: %v\n%s", names(failed), Report(checks))
	}
}

// TestDoctorCatchesMissingKokoroWeights.
//
// Importing kokoro_onnx proves the library is installed and says nothing about
// whether there is a model to load. With the weights missing the sidecar
// starts, fails, and the supervisor waits out its full three-minute start
// timeout before the station goes on air with no DJ. It degrades correctly and
// it reads like a hang, so the preflight has to catch it.
func TestDoctorCatchesMissingKokoroWeights(t *testing.T) {
	python := pythonThatImports(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "sidecar.py")
	if err := os.WriteFile(script, []byte("pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("JOCKORA_KOKORO_MODEL", filepath.Join(dir, "absent.onnx"))
	t.Setenv("JOCKORA_KOKORO_VOICES", filepath.Join(dir, "absent.bin"))

	got := checkTTSInterpreter(context.Background(), Config{
		TTSPython: python, TTSScript: script,
	}, Check{Name: "tts sidecar"})

	if got.OK {
		t.Fatal("the check passed with no weights on disk; the DJ would never speak")
	}
	if !strings.Contains(got.Detail, "absent.onnx") {
		t.Errorf("the failure does not name the missing file: %q", got.Detail)
	}
	if !strings.Contains(got.Fix, "JOCKORA_KOKORO_MODEL") {
		t.Errorf("the fix does not say how to point at the weights: %q", got.Fix)
	}
}

// TestDoctorPassesWithKokoroWeightsPresent is the other half: the new check
// must not fail a correct install.
func TestDoctorPassesWithKokoroWeightsPresent(t *testing.T) {
	python := pythonThatImports(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "sidecar.py")
	model := filepath.Join(dir, "kokoro.onnx")
	voices := filepath.Join(dir, "voices.bin")
	for _, f := range []string{script, model, voices} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("JOCKORA_KOKORO_MODEL", model)
	t.Setenv("JOCKORA_KOKORO_VOICES", voices)

	got := checkTTSInterpreter(context.Background(), Config{
		TTSPython: python, TTSScript: script,
	}, Check{Name: "tts sidecar"})
	if !got.OK {
		t.Errorf("a correct install failed the check: %s", got.Detail)
	}
}

// pythonThatImports returns an interpreter that can import kokoro_onnx, or
// skips. The weights check is what is under test, not the import.
func pythonThatImports(t *testing.T) string {
	t.Helper()
	for _, p := range []string{".venv-tts/bin/python", "../../.venv-tts/bin/python"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no venv with kokoro-onnx available")
	return ""
}
