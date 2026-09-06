// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/supervise"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestLLMServerAttachesToAnExternalServer is the mode an operator uses when
// they already run llama-server, or run it on another machine, or want a
// different model. Jockora must NOT spawn or manage anything in that case.
func TestLLMServerAttachesToAnExternalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": `{"ok":true}`, "stop_type": "eos"})
	}))
	defer srv.Close()

	s, err := StartLLMServer(context.Background(), LLMServerConfig{BaseURL: srv.URL, Log: quietLog()})
	if err != nil {
		t.Fatalf("StartLLMServer: %v", err)
	}
	defer s.Close() //nolint:errcheck // test teardown

	if s.Managed() {
		t.Error("reported an operator's own server as managed; Jockora must never kill it")
	}
	if s.Pid() != 0 {
		t.Errorf("Pid = %d for an external server", s.Pid())
	}
	if !s.Healthy() {
		t.Error("a reachable external server is not healthy")
	}
	if _, err := s.Completer().Complete(context.Background(), CompletionRequest{Prompt: "hi"}); err != nil {
		t.Errorf("Complete against the attached server: %v", err)
	}
}

// TestLLMServerBuildsLlamaArgs pins the argv, because a wrong flag here is a
// server that starts and then cannot be talked to.
func TestLLMServerBuildsLlamaArgs(t *testing.T) {
	args := buildLlamaArgs(LLMServerConfig{
		ModelPath: "/models/qwen.gguf",
		ExtraArgs: []string{"--flash-attn"},
	})

	if args[0] != "llama-server" {
		t.Errorf("binary = %q", args[0])
	}
	for _, want := range []string{"-m", "/models/qwen.gguf", "--host", "--port", "-c", "8192", "-ngl", "99", "--flash-attn"} {
		if !slices.Contains(args, want) {
			t.Errorf("argv is missing %q: %v", want, args)
		}
	}
	// The address is left as placeholders for the supervisor to fill in with
	// the port it reserved; hardcoding one would collide with whatever else is
	// on the box.
	if !slices.Contains(args, supervise.PlaceholderPort) {
		t.Errorf("argv does not carry the port placeholder: %v", args)
	}

	// Overrides must reach the command line.
	custom := buildLlamaArgs(LLMServerConfig{
		ModelPath: "/m.gguf", Binary: "/opt/llama-server", ContextSize: 4096, GPULayers: 0,
	})
	if custom[0] != "/opt/llama-server" {
		t.Errorf("binary override ignored: %v", custom)
	}
	if !slices.Contains(custom, "4096") {
		t.Errorf("context size override ignored: %v", custom)
	}
}

// TestLLMServerNeedsAURLOrAModel: neither configured is a mistake worth naming,
// not a silent no-op.
func TestLLMServerNeedsAURLOrAModel(t *testing.T) {
	_, err := StartLLMServer(context.Background(), LLMServerConfig{Log: quietLog()})
	if err == nil {
		t.Fatal("started with neither a URL nor a model")
	}
	if !strings.Contains(err.Error(), "URL") || !strings.Contains(err.Error(), "model") {
		t.Errorf("error = %v, want it to name both options", err)
	}
}

// TestLLMServerRefusesAMissingModel: caught before a process is spawned, so the
// failure names the file rather than arriving as llama-server's exit status.
func TestLLMServerRefusesAMissingModel(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.gguf")
	_, err := StartLLMServer(context.Background(), LLMServerConfig{ModelPath: missing, Log: quietLog()})
	if err == nil {
		t.Fatal("accepted a model file that does not exist")
	}
	if !strings.Contains(err.Error(), "nope.gguf") {
		t.Errorf("error = %v, want it to name the file", err)
	}
}

// TestLLMServerSupervisesARealChild proves the managed path end to end with a
// stand-in server, so the supervision is exercised without a 5 GB model.
func TestLLMServerSupervisesARealChild(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-llama")
	body := `#!/bin/sh
# A stand-in for llama-server: parses --port and answers /health.
while [ $# -gt 0 ]; do
  case "$1" in --port) PORT="$2"; shift 2;; *) shift;; esac
done
exec python3 -c "
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.send_header('Content-Length','2'); self.end_headers(); self.wfile.write(b'ok')
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', int('$PORT')), H).serve_forever()
"
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(model, []byte("not a real gguf"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := StartLLMServer(context.Background(), LLMServerConfig{
		ModelPath: model, Binary: script, StartTimeout: 20 * time.Second, Log: quietLog(),
	})
	if err != nil {
		t.Skipf("could not start the stand-in server (python3 may be absent): %v", err)
	}
	defer s.Close() //nolint:errcheck // test teardown

	if !s.Managed() {
		t.Error("a server Jockora started reports as external")
	}
	if s.Pid() == 0 {
		t.Error("no pid for a managed server")
	}
	if !s.Healthy() {
		t.Error("managed server is not healthy after a successful start")
	}
	if !strings.HasPrefix(s.BaseURL(), "http://127.0.0.1:") {
		t.Errorf("BaseURL = %q", s.BaseURL())
	}
}
