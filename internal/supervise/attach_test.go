// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package supervise

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestAttach covers waiting on an EXTERNAL server -- one an operator supplied,
// which Jockora attaches to and never owns.
//
// The distinction these tests draw is the whole point: "listening but not
// ready" is a model loading and is worth minutes, "nothing is listening" is a
// server that was never started and is worth seconds. Before this, both waited
// five minutes -- and because buildEnricher runs before the HTTP listener, the
// second case meant a dead port and no page explaining why on every start of a
// perfectly supported configuration.

func TestAttachHealthyReturnsAtOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := Start(context.Background(), Config{
		Name: "ready", Addr: srv.URL, HealthPath: "/health",
	})
	if err != nil {
		t.Fatalf("attaching to a healthy server: %v", err)
	}
	defer p.Close() //nolint:errcheck // test cleanup
	if !p.Healthy() {
		t.Error("a server that answered 200 is not marked healthy")
	}
}

func TestAttachNothingListeningFailsFast(t *testing.T) {
	// A port with nothing on it. Taken and released so the address is real and
	// certain to refuse.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	start := time.Now()
	_, err = Start(context.Background(), Config{
		Name: "llama-server", Addr: "http://" + addr, HealthPath: "/health",
		// The real default. The point is that it is NOT waited out.
		StartTimeout: 5 * time.Minute,
	})
	took := time.Since(start)

	if err == nil {
		t.Fatal("attaching to a dead address succeeded")
	}
	if took > time.Minute {
		t.Errorf("waited %s for a port that was refusing connections; the whole "+
			"point is to give up in about %s", took, connectGrace)
	}
	if !strings.Contains(err.Error(), "nothing is listening") {
		t.Errorf("the error must say the server is not running, so the operator "+
			"starts one instead of debugging Jockora; got: %v", err)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("the error must name the address it tried; got: %v", err)
	}
}

func TestAttachStillLoadingKeepsWaiting(t *testing.T) {
	// llama-server binds its port and answers 503 while the GGUF loads. That is
	// worth minutes, and is exactly what must NOT be cut off at the grace.
	var probes atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if probes.Add(1) < 80 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 80 probes at 100ms is ~8 seconds, comfortably past the 5-second grace.
	p, err := Start(context.Background(), Config{
		Name: "loading", Addr: srv.URL, HealthPath: "/health",
		StartTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("gave up on a server that was listening and loading: %v", err)
	}
	defer p.Close() //nolint:errcheck // test cleanup
	if probes.Load() < 80 {
		t.Errorf("returned after %d probes, before the server said it was ready", probes.Load())
	}
}

func TestAttachNeverReadyHitsTheStartTimeout(t *testing.T) {
	// Listening, answering, never healthy. This one IS waited out, up to
	// StartTimeout, and the message names the timeout rather than the grace.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := Start(context.Background(), Config{
		Name: "stuck", Addr: srv.URL, HealthPath: "/health",
		StartTimeout: 2 * time.Second,
	})
	if err == nil {
		t.Fatal("a server that never became healthy was accepted")
	}
	if !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("want the start-timeout message; got: %v", err)
	}
}

func TestAttachCancelledContextStops(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Start(ctx, Config{
		Name: "cancelled", Addr: "http://" + addr, HealthPath: "/health",
		StartTimeout: time.Minute,
	}); err == nil {
		t.Fatal("a cancelled context still attached")
	}
}

func TestAttachManagedProcessKeepsTheLongGrace(t *testing.T) {
	// A MANAGED process is one Jockora just spawned, and it is entirely normal
	// for it not to have bound its port for a moment. The grace must not apply
	// to it, or every slow-starting sidecar becomes a startup failure.
	//
	// sleep binds nothing ever, so this reaches the StartTimeout rather than
	// the grace -- which is the assertion.
	start := time.Now()
	_, err := Start(context.Background(), Config{
		Name: "sidecar", Command: []string{"sleep", "30"}, HealthPath: "/health",
		StartTimeout: 7 * time.Second,
	})
	took := time.Since(start)

	if err == nil {
		t.Fatal("a command that never listens was accepted")
	}
	if strings.Contains(err.Error(), "nothing is listening") {
		t.Errorf("the external grace was applied to a managed process: %v", err)
	}
	if took < 6*time.Second {
		t.Errorf("gave up after %s; a managed process gets the full StartTimeout", took)
	}
}

func TestAttachMalformedHealthPathFailsLikeAnythingElse(t *testing.T) {
	// A health path that cannot go into a URL at all. It fails the same way an
	// unreachable server does rather than panicking, which is the only sensible
	// answer: the probe is a probe, and a bad one means "not healthy".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if _, err := Start(context.Background(), Config{
		Name: "malformed", Addr: srv.URL, HealthPath: "/he\nalth",
		StartTimeout: 30 * time.Second,
	}); err == nil {
		t.Fatal("a health path that cannot be a URL was accepted")
	}
}

// A MANAGED child that dies is not going to answer. These cover the other half
// of the same failure the attach tests cover: a server that cannot start should
// cost seconds, not the whole StartTimeout, because buildBreaks and
// buildEnricher both run before the HTTP listener does.

func TestAttachManagedChildThatDiesFailsAtOnce(t *testing.T) {
	start := time.Now()
	_, err := Start(context.Background(), Config{
		Name: "sidecar", Command: []string{"sh", "-c", "echo 'cannot load model: bad magic' >&2; exit 3"},
		HealthPath: "/health", StartTimeout: 2 * time.Minute,
	})
	took := time.Since(start)

	if err == nil {
		t.Fatal("a child that exited immediately was accepted")
	}
	if took > 30*time.Second {
		t.Errorf("waited %s for a child that was already dead", took)
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("the error must say the child died; got: %v", err)
	}
	// ITS LAST WORDS. Without them the operator gets a timeout and no reason,
	// which is what a missing kokoro model looked like: three minutes, then
	// "did not answer", and the actual explanation discarded at Debug.
	if !strings.Contains(err.Error(), "bad magic") {
		t.Errorf("the error must carry the child's stderr; got: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("the error must carry the exit status; got: %v", err)
	}
}

func TestAttachManagedChildThatLivesStillGetsItsGrace(t *testing.T) {
	// The guard above must not fire for a child that is merely SLOW. This one
	// stays up and never binds, so it reaches StartTimeout the old way.
	start := time.Now()
	_, err := Start(context.Background(), Config{
		Name: "slow", Command: []string{"sleep", "30"},
		HealthPath: "/health", StartTimeout: 6 * time.Second,
	})
	took := time.Since(start)

	if err == nil {
		t.Fatal("a child that never listened was accepted")
	}
	if strings.Contains(err.Error(), "exited during startup") {
		t.Errorf("a live child was reported as dead: %v", err)
	}
	if took < 5*time.Second {
		t.Errorf("gave up after %s; a live child gets the full StartTimeout", took)
	}
}

func TestAttachManagedChildReportsWhatItPrinted(t *testing.T) {
	// Only the LAST few lines: a child that fails after a thousand lines of
	// banner should still put the reason in the error rather than the banner.
	_, err := Start(context.Background(), Config{
		Name: "noisy",
		Command: []string{"sh", "-c",
			"i=0; while [ $i -lt 40 ]; do echo \"banner line $i\" >&2; i=$((i+1)); done; echo 'the real reason' >&2; exit 1"},
		HealthPath: "/health", StartTimeout: 30 * time.Second,
	})
	if err == nil {
		t.Fatal("a child that exited was accepted")
	}
	if !strings.Contains(err.Error(), "the real reason") {
		t.Errorf("the last line must survive; got: %v", err)
	}
	if strings.Contains(err.Error(), "banner line 0") {
		t.Errorf("the whole log was included, not the tail; got: %v", err)
	}
}
