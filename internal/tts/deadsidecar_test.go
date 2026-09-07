// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package tts

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A sidecar that DIED is not going to answer, and waiting out StartTimeout for
// it is three minutes of a dead HTTP port -- the DJ is built before the
// listener, so those minutes are the whole product being unreachable. Measured
// against the real container with a kokoro model file that could not be loaded:
// three minutes of nothing, then a bare timeout with the reason discarded.

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

func TestDeadSidecarFailsAtOnceAndSaysWhy(t *testing.T) {
	start := time.Now()
	_, err := Start(context.Background(), Config{
		Command:      []string{"sh", "-c", "echo 'failed to load kokoro-v1.0.onnx: bad magic' >&2; exit 2"},
		StartTimeout: 2 * time.Minute,
		Log:          quietLog(),
	})
	took := time.Since(start)

	if err == nil {
		t.Fatal("a sidecar that exited immediately was accepted")
	}
	if took > 30*time.Second {
		t.Errorf("waited %s for a sidecar that was already dead", took)
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("the error must say it died, not that it was slow; got: %v", err)
	}
	// The reason. Without it the operator sees a timeout and nothing else.
	if !strings.Contains(err.Error(), "bad magic") {
		t.Errorf("the error must carry the sidecar's stderr; got: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("the error must carry the exit status; got: %v", err)
	}
}

func TestDeadSidecarSlowOneStillGetsItsGrace(t *testing.T) {
	// A sidecar that is merely SLOW must not be cut off. Kokoro genuinely takes
	// tens of seconds to load on a cold start.
	start := time.Now()
	_, err := Start(context.Background(), Config{
		Command:      []string{"sleep", "30"},
		StartTimeout: 5 * time.Second,
		Log:          quietLog(),
	})
	took := time.Since(start)

	if err == nil {
		t.Fatal("a sidecar that never listened was accepted")
	}
	if strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("a live sidecar was reported as dead: %v", err)
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("want the start-timeout message; got: %v", err)
	}
	if took < 4*time.Second {
		t.Errorf("gave up after %s; a live sidecar gets the full StartTimeout", took)
	}
}
