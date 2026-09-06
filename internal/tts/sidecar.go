// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package tts manages the speech sidecar: a long-lived Python process running
// Kokoro under onnxruntime, spoken to over local HTTP.
//
// It is a separate process on purpose. Kokoro is Python, onnxruntime has no
// cgo-free Go binding, and a cgo binding would end the static cross-compile
// that ships this server as one file. It is LONG-LIVED on purpose too: loading
// the ONNX model costs seconds, and seconds inside the break budget is the
// difference between a break airing and a break being dropped.
//
// DEATH POLICY. A dead sidecar drops the break that was in flight, the
// supervisor respawns it, and neither ever blocks the mix bus. Breaks are
// optional; music is not. Every path in this file returns ErrTTSUnavailable
// promptly rather than waiting for a process that may not come back.
package tts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrTTSUnavailable means no speech is available right now. Callers drop the
// break and let the music run; they never retry in a loop or wait.
var ErrTTSUnavailable = errors.New("tts: sidecar unavailable")

// Config describes how to run the sidecar. The zero value is usable apart from
// Command, which must name something to execute.
type Config struct {
	// Command is the argv of the child. Empty means Python + Script.
	Command []string
	// Python is the interpreter path. The sidecar PINS ITS OWN INTERPRETER:
	// onnxruntime has no wheels for the current system python3 on this host, and
	// inheriting whatever python3 resolves to is exactly what made this look
	// unbuildable. A 3.10 venv costs nothing when it is already another process.
	Python string
	// Script is the sidecar entry point.
	Script string
	// Voice is used when Synthesize is called with an empty voice.
	Voice string

	// Addr is an EXTERNAL sidecar to use instead of starting one, so an
	// operator can run Kokoro wherever they like -- another machine, a GPU
	// box, a container they manage -- or swap it for any server speaking the
	// same two endpoints.
	Addr string

	HealthInterval time.Duration // between health polls; default 5s
	StartTimeout   time.Duration // to first healthy response; default 30s
	Backoff        time.Duration // first respawn delay, doubling to 30s; default 1s

	Log *slog.Logger
}

func (c *Config) applyDefaults() {
	if c.HealthInterval <= 0 {
		c.HealthInterval = 5 * time.Second
	}
	if c.StartTimeout <= 0 {
		c.StartTimeout = 30 * time.Second
	}
	if c.Backoff <= 0 {
		c.Backoff = time.Second
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if len(c.Command) == 0 {
		python := c.Python
		if python == "" {
			python = "python3"
		}
		c.Command = []string{python, c.Script}
	}
}

const maxBackoff = 30 * time.Second

// Sidecar owns one child process and keeps it alive.
type Sidecar struct {
	cfg  Config
	addr string
	http *http.Client

	healthy  atomic.Bool
	respawns atomic.Uint64

	mu  sync.Mutex // guards cmd
	cmd *exec.Cmd

	// external means the operator runs the sidecar; Jockora only watches it.
	external bool

	stop     chan struct{}
	stopped  chan struct{}
	closeErr error
	once     sync.Once
}

// Start launches the sidecar and blocks until it answers /health, so a caller
// that gets a *Sidecar back has a warm model rather than a promise of one.
func Start(ctx context.Context, cfg Config) (*Sidecar, error) {
	cfg.applyDefaults()

	if cfg.Addr != "" {
		// Somebody else's process. Attach, never spawn, and never kill.
		return attach(ctx, cfg)
	}

	addr, err := freeAddr()
	if err != nil {
		return nil, err
	}

	s := &Sidecar{
		cfg:  cfg,
		addr: addr,
		// No global timeout: the caller's context is the only deadline that
		// knows how much of the break budget is left.
		http:    &http.Client{},
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}

	if err := s.spawn(ctx); err != nil {
		return nil, err
	}
	go s.supervise()
	return s, nil
}

// freeAddr asks the kernel for an unused port and hands it to the child.
//
// ponytail: there is a window between closing this listener and the child
// binding it. Nothing else on a single-operator host is racing for ports, and
// losing the race merely fails the start with a clear bind error. Move to
// passing an inherited fd only if that ever actually happens.
func freeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("tts: reserving a port: %w", err)
	}
	defer l.Close() //nolint:errcheck // closing a probe listener
	return l.Addr().String(), nil
}

// Addr is host:port. Exposed because the protocol is plain HTTP specifically so
// an operator can curl it when synthesis goes quiet.
func (s *Sidecar) Addr() string { return s.addr }

// Pid is the child's process id, or 0 when nothing is running. Exposed so an
// operator -- and the endurance measurement -- can watch its memory: the
// respawn supervisor would otherwise hide a slow leak behind a clean restart.
func (s *Sidecar) Pid() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// Healthy reports whether the last poll succeeded.
func (s *Sidecar) Healthy() bool { return s.healthy.Load() }

// RespawnCount is how many times the child has been restarted after dying. In
// steady state this should stay at zero; a rising count is a leak or a crash
// the supervisor is hiding, not a system working as designed.
func (s *Sidecar) RespawnCount() uint64 { return s.respawns.Load() }

func (s *Sidecar) spawn(ctx context.Context) error {
	cmd := exec.Command(s.cfg.Command[0], s.cfg.Command[1:]...) //nolint:gosec // operator-configured
	// The address travels in the environment rather than argv so the same
	// mechanism addresses the Python script and any other child without either
	// side parsing flags.
	cmd.Env = append(os.Environ(), "JOCKORA_TTS_ADDR="+s.addr)

	// stderr MUST be drained. A child that fills the 64 KiB pipe buffer blocks
	// in write() and stops answering health checks, which would look exactly
	// like a hang in the model.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("tts: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tts: starting %v: %w", s.cfg.Command, err)
	}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			s.cfg.Log.Debug("tts sidecar", "line", sc.Text())
		}
	}()

	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()

	if err := s.waitHealthy(ctx); err != nil {
		s.kill()
		return err
	}
	s.healthy.Store(true)
	s.cfg.Log.Info("tts sidecar ready", "addr", s.addr, "pid", cmd.Process.Pid)
	return nil
}

func (s *Sidecar) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(s.cfg.StartTimeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.poll() {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("tts: sidecar did not answer %s/health within %s", s.addr, s.cfg.StartTimeout)
}

func (s *Sidecar) poll() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.addr+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close() //nolint:errcheck // health probe
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func (s *Sidecar) kill() {
	s.mu.Lock()
	cmd := s.cmd
	s.cmd = nil
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	// Reaping matters: an unreaped child is a zombie, and a server that leaks
	// one per respawn eventually cannot fork at all.
	_ = cmd.Wait()
}

// supervise polls health and rebuilds the child when it stops answering.
func (s *Sidecar) supervise() {
	defer close(s.stopped)
	backoff := s.cfg.Backoff
	tick := time.NewTicker(s.cfg.HealthInterval)
	defer tick.Stop()

	for {
		select {
		case <-s.stop:
			if !s.external {
				s.kill()
			}
			return
		case <-tick.C:
		}

		if s.poll() {
			s.healthy.Store(true)
			backoff = s.cfg.Backoff
			continue
		}

		s.healthy.Store(false)
		if s.external {
			// Killing or restarting a server Jockora does not own would be
			// worse than the outage it is reporting.
			s.cfg.Log.Warn("external tts sidecar is not answering", "addr", s.addr)
			continue
		}
		s.cfg.Log.Warn("tts sidecar unhealthy, respawning", "addr", s.addr, "backoff", backoff)
		s.kill()

		select {
		case <-s.stop:
			return
		case <-time.After(backoff):
		}

		if err := s.spawn(context.Background()); err != nil {
			s.cfg.Log.Error("tts sidecar respawn failed", "err", err)
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		s.respawns.Add(1)
		backoff = s.cfg.Backoff
	}
}

// Engine is the prefix a Jockora voice id carries, as in "kokoro:am_michael".
//
// The namespace exists so a JockPack can name the engine its voice needs, and
// so that a second engine -- Orpheus is the expected one -- cannot have its
// voices silently handed to this sidecar.
const Engine = "kokoro"

// ErrWrongEngine means a voice id names an engine this sidecar cannot speak.
var ErrWrongEngine = errors.New("tts: voice belongs to another engine")

// voiceFor turns a Jockora voice id into the name Kokoro expects.
//
// This is not cosmetic. Kokoro's voice is "am_michael"; the persona file says
// "kokoro:am_michael"; and passing the namespaced form straight through makes
// the sidecar fail EVERY synthesis with a 503, which the caller correctly reads
// as an unavailable sidecar and turns into a dropped break. Measured end to
// end, that was 20 breaks out of 20 -- silent, and invisible to every unit test
// in this package, because the fake sidecar happily ignores the voice field.
func voiceFor(id string) (string, error) {
	engine, name, found := strings.Cut(id, ":")
	if !found {
		return id, nil
	}
	if engine != Engine {
		return "", fmt.Errorf("%w: %q wants %q", ErrWrongEngine, id, engine)
	}
	return name, nil
}

type synthRequest struct {
	Text  string `json:"text"`
	Voice string `json:"voice"`
}

// Synthesize returns WAV bytes for text, or ErrTTSUnavailable.
//
// It never waits for a sick sidecar to recover: an unhealthy manager fails on
// the spot, and a transport error mid-request is reported as unavailability
// rather than retried, because the caller's alternative -- dropping the break
// -- is always safe and always fast.
func (s *Sidecar) Synthesize(ctx context.Context, text, voice string) ([]byte, error) {
	if !s.healthy.Load() {
		return nil, ErrTTSUnavailable
	}
	if voice == "" {
		voice = s.cfg.Voice
	}
	voice, err := voiceFor(voice)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(synthRequest{Text: text, Voice: voice})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+s.addr+"/synth", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		// Mark it down immediately so the next caller fails fast instead of
		// waiting for the health poll to notice.
		s.healthy.Store(false)
		if ctx.Err() != nil {
			return nil, fmt.Errorf("tts: %w", ctx.Err())
		}
		return nil, fmt.Errorf("%w: %v", ErrTTSUnavailable, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response drained below

	wav, err := io.ReadAll(resp.Body)
	if err != nil {
		s.healthy.Store(false)
		return nil, fmt.Errorf("%w: reading audio: %v", ErrTTSUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: sidecar returned %s: %s", ErrTTSUnavailable, resp.Status, truncate(wav, 200))
	}
	if len(wav) == 0 {
		return nil, fmt.Errorf("%w: sidecar returned no audio", ErrTTSUnavailable)
	}
	return wav, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// Close stops the supervisor and reaps the child.
func (s *Sidecar) Close() error {
	s.once.Do(func() {
		s.healthy.Store(false)
		close(s.stop)
		<-s.stopped
		if !s.external {
			s.kill()
		}
	})
	return s.closeErr
}

// attach uses a sidecar the operator is running, instead of starting one.
//
// The rest of this file is unchanged by it: Synthesize, Healthy and the health
// poll all work the same way. What differs is that nothing is spawned, killed
// or respawned -- Jockora does not own the process and must not act as if it
// does.
func attach(ctx context.Context, cfg Config) (*Sidecar, error) {
	s := &Sidecar{
		cfg:      cfg,
		addr:     strings.TrimPrefix(strings.TrimPrefix(cfg.Addr, "http://"), "https://"),
		http:     &http.Client{},
		external: true,
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	if err := s.waitHealthy(ctx); err != nil {
		return nil, err
	}
	s.healthy.Store(true)
	cfg.Log.Info("using an external speech sidecar", "addr", s.addr)
	go s.supervise()
	return s, nil
}
