// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

//go:build docker

// The image actually serves the app.
//
// Separately tagged from `gates` because it builds a container: minutes, a
// daemon, and a network. The thing it proves cannot be proven any other way --
// web/dist is gitignored build output, so an image that never runs `ng build`
// compiles, links, starts, and serves the "assets not built" notice to every
// listener. That failure is invisible to every other test in this repository.
package test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDockerServesApp(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("the docker daemon is not running: %v\n%s", err, out)
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is needed to make the one track the server refuses to start without")
	}

	const tag = "jockora-docker-test:latest"

	build := exec.Command("docker", "build", "-t", tag, "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}

	// ONE TRACK. `serve` refuses to start with nothing to play, which is the
	// right behaviour and not what this test is about, so the library is a
	// single second of a sine wave mounted read-only -- read-only because
	// Jockora must never be able to write to a music library even by accident.
	library := t.TempDir()
	audio := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=1",
		"-ac", "2", "-metadata", "artist=Docker", "-metadata", "title=One Second",
		filepath.Join(library, "one.mp3"))
	if out, err := audio.CombinedOutput(); err != nil {
		t.Fatalf("making the track: %v\n%s", err, out)
	}

	// A free port, taken and released, so two runs of this test do not collide.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	// CREATE, COPY, START rather than a bind mount. A --volume of a temp
	// directory is not portable: Docker Desktop shares only a fixed set of
	// host paths, and t.TempDir() is under none of them, so the mount silently
	// arrives EMPTY and the server exits saying there is nothing to play. Two
	// hours of "the image is broken" that were nothing of the kind.
	//
	// The cost is that the library is not mounted read-only here. The
	// never-write invariant is proven in the library tests, against a real
	// read-only directory; this test is about what is served.
	id, err := exec.Command("docker", "create",
		"--publish", fmt.Sprintf("127.0.0.1:%d:8080", port),
		// The image's default database path is /config, which is not mounted
		// here; /tmp is owned by the unprivileged user the image runs as.
		"--env", "JOCKORA_DB_PATH=/tmp/jockora.db",
		"--env", "JOCKORA_LIBRARY_PATH=/music",
		// An EMPTY persona path turns the llama-server and sidecar preflight
		// checks from hard failures into warnings: a station with no jock plays
		// music and says nothing, which is a perfectly good thing to serve and
		// is all this test needs. Neither model is in the image, on purpose.
		"--env", "JOCKORA_PERSONA=",
		// The image binds 0.0.0.0 because a container must, and Jockora refuses
		// a non-loopback address unless told to. docker-compose.yml sets this
		// for the same reason; it is a deliberate safety default, not a bug.
		"--env", "JOCKORA_ALLOW_LAN=1",
		// The ENTRYPOINT is the binary; the subcommand is the operator's, which
		// is how docker-compose.yml invokes it too.
		tag, "serve").Output()
	if err != nil {
		t.Fatalf("docker create: %v", err)
	}
	container := strings.TrimSpace(string(id))
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "--force", container).Run()
	})

	if out, err := exec.Command("docker", "cp", library, container+":/music").CombinedOutput(); err != nil {
		t.Fatalf("docker cp: %v\n%s", err, out)
	}
	if out, err := exec.Command("docker", "start", container).CombinedOutput(); err != nil {
		t.Fatalf("docker start: %v\n%s", err, out)
	}

	var body string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			body = string(b)
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(time.Second)
	}

	if !strings.Contains(body, "<app-root") {
		logs, _ := exec.Command("docker", "logs", container).CombinedOutput()
		t.Fatalf("the image does not serve the built app.\nbody:\n%s\nlogs:\n%s", body, logs)
	}
	if strings.Contains(body, "not built") {
		t.Errorf("the image serves the 'assets not built' notice: the Node stage did not run")
	}

	// THE BUNDLE THE SHELL ASKS FOR, fetched by name. Serving the shell is not
	// serving the app: the script tag is a relative url, and when only the page
	// paths were routed the shell arrived, 404ed its own bundle, and rendered
	// nothing at all. A test that reads the html and stops cannot see that.
	script := regexp.MustCompile(`<script[^>]+src="([^"]+)"`).FindStringSubmatch(body)
	if script == nil {
		t.Fatalf("the shell has no script tag to follow:\n%s", body)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/%s", port, strings.TrimPrefix(script[1], "/")))
	if err != nil {
		t.Fatalf("GET %s: %v", script[1], err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	js, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the app's own bundle %s answered %d; the page loads and then renders nothing",
			script[1], resp.StatusCode)
	}
	if len(js) < 10_000 {
		t.Errorf("bundle %s is %d bytes; that is not the app", script[1], len(js))
	}
}
