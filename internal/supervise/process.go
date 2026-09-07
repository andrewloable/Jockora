// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package supervise runs an external model server as a child process and keeps
// it alive.
//
// It exists because Jockora needs TWO of them -- Kokoro for speech and
// llama-server for writing -- and they differ only in what they are called and
// how they are told which port to use. Everything that matters is identical:
// start it, wait until it answers, poll it, replace it when it dies, reap it so
// the process table does not fill, and never let any of that block the mix bus.
//
// WHY NOT EMBED THE MODEL RUNTIME. llama.cpp and onnxruntime are both C++, so
// linking either means cgo -- and CGO_ENABLED=0 is load-bearing here. It is why
// the SQLite driver is modernc.org/sqlite rather than a cgo one, and a CI gate
// asserts all three targets still cross-compile without it. A supervised child
// buys the same one-command experience without giving that up, and it keeps the
// server SWAPPABLE: point it at a bigger model, or at another machine, without
// rebuilding anything.
package supervise

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrUnavailable means the supervised server is not usable right now.
//
// Callers drop whatever they were doing and carry on. Nothing in Jockora waits
// for a model: breaks are optional, music is not.
var ErrUnavailable = errors.New("supervise: server unavailable")

// Placeholders substituted into Command and Env before the child is started.
//
// A server is told its address in whatever way it happens to accept -- Kokoro
// reads an environment variable, llama-server takes --host and --port -- so the
// address is templated rather than assumed.
const (
	PlaceholderHost = "{{host}}"
	PlaceholderPort = "{{port}}"
	PlaceholderAddr = "{{addr}}"
)

const maxBackoff = 30 * time.Second

// Config describes one supervised server.
type Config struct {
	// Name appears in logs. "kokoro", "llama-server".
	Name string

	// Command is the argv, with {{host}}, {{port}} or {{addr}} substituted.
	Command []string
	// Env is extra environment, also substituted, as KEY=VALUE.
	Env []string

	// Addr is an EXTERNAL server to use instead of starting one.
	//
	// This is the whole point of the package being configurable: an operator
	// who already runs llama-server on another machine, or who wants a
	// different model, sets this and Jockora supervises nothing. Everything
	// downstream is unchanged.
	Addr string

	// HealthPath is polled to decide liveness. Default "/health".
	HealthPath string

	HealthInterval time.Duration // between polls; default 5s
	StartTimeout   time.Duration // to first healthy response; default 30s
	Backoff        time.Duration // first respawn delay, doubling to 30s; default 1s

	Log *slog.Logger
}

func (c *Config) applyDefaults() {
	if c.Name == "" {
		c.Name = "server"
	}
	if c.HealthPath == "" {
		c.HealthPath = "/health"
	}
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
}

// Process is one supervised server, or a handle to an external one.
type Process struct {
	cfg  Config
	addr string
	http *http.Client

	// external means an operator supplied the address and nothing is managed.
	external bool

	healthy  atomic.Bool
	respawns atomic.Uint64

	mu  sync.Mutex
	cmd *exec.Cmd
	// exit is how the ONE goroutine that reaps this child publishes the fact.
	// Single owner on purpose: os/exec forbids concurrent Wait, so kill waits
	// on this rather than calling Wait itself.
	exit *Child

	stop    chan struct{}
	stopped chan struct{}
	once    sync.Once
}

// Start launches the server, or attaches to an external one.
//
// It blocks until the server answers, so a caller holding a *Process has a warm
// server rather than a promise of one.
func Start(ctx context.Context, cfg Config) (*Process, error) {
	cfg.applyDefaults()

	p := &Process{
		cfg: cfg,
		// No client timeout: the caller's context is the only deadline that
		// knows how much of a budget is left.
		http:    &http.Client{},
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}

	if cfg.Addr != "" {
		// An operator-supplied server. Jockora does not own its lifecycle and
		// must never kill it, so nothing here spawns or reaps.
		p.external = true
		p.addr = strings.TrimPrefix(strings.TrimPrefix(cfg.Addr, "http://"), "https://")
		if err := p.waitHealthy(ctx); err != nil {
			return nil, err
		}
		p.healthy.Store(true)
		cfg.Log.Info("using an external server", "name", cfg.Name, "addr", p.addr)
		go p.supervise()
		return p, nil
	}

	if len(cfg.Command) == 0 {
		return nil, fmt.Errorf("supervise: %s has neither a command nor an address", cfg.Name)
	}
	addr, err := freeAddr()
	if err != nil {
		return nil, err
	}
	p.addr = addr

	if err := p.spawn(ctx); err != nil {
		return nil, err
	}
	go p.supervise()
	return p, nil
}

// freeAddr asks the kernel for an unused port and hands it to the child.
//
// ponytail: there is a window between closing this listener and the child
// binding it. Nothing else on a single-operator host is racing for ports, and
// losing the race merely fails the start with a clear bind error.
func freeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("supervise: reserving a port: %w", err)
	}
	defer l.Close() //nolint:errcheck // closing a probe listener
	return l.Addr().String(), nil
}

// expand substitutes the address placeholders.
func (p *Process) expand(in []string) []string {
	host, port, _ := strings.Cut(p.addr, ":")
	r := strings.NewReplacer(
		PlaceholderHost, host,
		PlaceholderPort, port,
		PlaceholderAddr, p.addr,
	)
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = r.Replace(v)
	}
	return out
}

// BaseURL is the http:// address callers should talk to.
func (p *Process) BaseURL() string { return "http://" + p.addr }

// Addr is host:port. Exposed because the protocol is plain HTTP specifically so
// an operator can curl it when a server goes quiet.
func (p *Process) Addr() string { return p.addr }

// Healthy reports whether the last poll succeeded.
func (p *Process) Healthy() bool { return p.healthy.Load() }

// Managed reports whether Jockora started this server and will restart it.
func (p *Process) Managed() bool { return !p.external }

// RespawnCount is how many times the child has been replaced after dying. In
// steady state it should stay at zero; a rising count is a leak or a crash the
// supervisor is hiding, not a system working as designed.
func (p *Process) RespawnCount() uint64 { return p.respawns.Load() }

// Pid is the child's process id, or 0 when nothing is managed or running.
func (p *Process) Pid() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *Process) spawn(ctx context.Context) error {
	argv := p.expand(p.cfg.Command)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // operator-configured
	cmd.Env = append(os.Environ(), p.expand(p.cfg.Env)...)

	// stderr MUST be drained. A child that fills the 64 KiB pipe buffer blocks
	// in write() and stops answering health checks, which looks exactly like a
	// hang inside the model.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("supervise: %s stderr pipe: %w", p.cfg.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("supervise: starting %s (%v): %w", p.cfg.Name, argv, err)
	}
	watch := Watch(cmd, stderr, func(line string) {
		p.cfg.Log.Debug("server output", "name", p.cfg.Name, "line", line)
	})

	p.mu.Lock()
	p.cmd = cmd
	p.exit = watch
	p.mu.Unlock()

	if err := p.waitHealthy(ctx); err != nil {
		p.kill()
		return err
	}
	p.healthy.Store(true)
	p.cfg.Log.Info("server ready", "name", p.cfg.Name, "addr", p.addr, "pid", cmd.Process.Pid)
	return nil
}

// connectGrace is how long an EXTERNAL server has to accept a TCP connection
// before Jockora stops waiting for it.
//
// StartTimeout is minutes, and rightly so: a cold GGUF load takes minutes and
// giving up early would look like a broken server rather than a slow disk. But
// that grace is for a server that is THERE AND LOADING. A server that is not
// running at all refuses the connection instantly and will go on refusing it,
// and waiting minutes for that costs the whole product: buildEnricher runs
// before the HTTP listener, so an operator with no llama-server -- a supported
// configuration, where the preflight check is deliberately soft -- got a dead
// port and no page explaining why, for five minutes, every start.
const connectGrace = 5 * time.Second

func (p *Process) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(p.cfg.StartTimeout)
	// Only meaningful for an external server. A managed one is a process we
	// just spawned, and it is entirely normal for it not to have bound its
	// port yet.
	connectBy := time.Now().Add(connectGrace)
	reached := false

	// The child this call is waiting for, if it manages one. Read once: a
	// respawn replaces it, and this loop belongs to the spawn that started it.
	p.mu.Lock()
	watch := p.exit
	p.mu.Unlock()

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ok, answered := p.poll()
		if ok {
			return nil
		}
		// A CHILD THAT HAS ALREADY DIED IS NOT GOING TO ANSWER. Waiting out
		// StartTimeout for it costs minutes of a dead port for a failure that
		// is already decided -- and buildBreaks runs before the HTTP listener,
		// so those minutes are the whole product being unreachable. The child's
		// last words come with it, because "did not answer within 3m0s" says
		// nothing an operator can act on.
		if watch != nil && watch.Exited() {
			return fmt.Errorf("supervise: %s exited during startup (%v)%s",
				p.cfg.Name, watch.Err(), watch.LastWords())
		}
		// ANSWERED AT ALL, at any status, means something is listening: it is
		// starting up, and it gets the full StartTimeout to finish.
		reached = reached || answered
		if p.external && !reached && time.Now().After(connectBy) {
			return fmt.Errorf("supervise: nothing is listening on %s (%s); waited %s",
				p.BaseURL(), p.cfg.Name, connectGrace)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("supervise: %s did not answer %s%s within %s",
		p.cfg.Name, p.BaseURL(), p.cfg.HealthPath, p.cfg.StartTimeout)
}

// poll reports whether the server is healthy, and separately whether it
// answered at all. The second value is what tells "loading" apart from "not
// running": both are unhealthy, and only one is worth waiting minutes for.
func (p *Process) poll() (healthy, answered bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL()+p.cfg.HealthPath, nil)
	if err != nil {
		return false, false
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close() //nolint:errcheck // health probe
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK, true
}

func (p *Process) kill() {
	p.mu.Lock()
	cmd, watch := p.cmd, p.exit
	p.cmd, p.exit = nil, nil
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	// Reaping matters: an unreaped child is a zombie, and a server that leaks
	// one per respawn eventually cannot fork at all. The reaping is done by the
	// goroutine spawn started; waiting for it here is what makes that a reap
	// rather than a hope, and it avoids a second concurrent Wait, which os/exec
	// does not allow.
	if watch != nil {
		watch.Reap()
	}
}

// supervise polls health and rebuilds the child when it stops answering.
func (p *Process) supervise() {
	defer close(p.stopped)
	backoff := p.cfg.Backoff
	tick := time.NewTicker(p.cfg.HealthInterval)
	defer tick.Stop()

	for {
		select {
		case <-p.stop:
			if p.cfg.Addr == "" {
				p.kill()
			}
			return
		case <-tick.C:
		}

		if healthy, _ := p.poll(); healthy {
			p.healthy.Store(true)
			backoff = p.cfg.Backoff
			continue
		}
		p.healthy.Store(false)

		if p.external {
			// Somebody else's process. Report it and keep watching; killing or
			// restarting a server Jockora does not own would be worse than the
			// outage.
			p.cfg.Log.Warn("external server is not answering", "name", p.cfg.Name, "addr", p.addr)
			continue
		}

		p.cfg.Log.Warn("server unhealthy, respawning", "name", p.cfg.Name, "backoff", backoff)
		p.kill()

		select {
		case <-p.stop:
			return
		case <-time.After(backoff):
		}

		if err := p.spawn(context.Background()); err != nil {
			p.cfg.Log.Error("respawn failed", "name", p.cfg.Name, "err", err)
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		p.respawns.Add(1)
		backoff = p.cfg.Backoff
	}
}

// Do sends a request to the server, failing fast when it is not healthy.
//
// It never waits for a sick server to recover: the caller's alternative --
// dropping whatever it was doing -- is always safe and always faster.
func (p *Process) Do(req *http.Request) (*http.Response, error) {
	if !p.healthy.Load() {
		return nil, ErrUnavailable
	}
	resp, err := p.http.Do(req)
	if err != nil {
		// Mark it down immediately so the next caller fails fast instead of
		// waiting for the health poll to notice.
		p.healthy.Store(false)
		if req.Context().Err() != nil {
			return nil, fmt.Errorf("supervise: %w", req.Context().Err())
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return resp, nil
}

// Close stops the supervisor and reaps the child.
func (p *Process) Close() error {
	p.once.Do(func() {
		p.healthy.Store(false)
		close(p.stop)
		<-p.stopped
		if !p.external {
			p.kill()
		}
	})
	return nil
}

// PortOf is a convenience for building a command that needs a bare port.
func PortOf(addr string) int {
	_, port, _ := strings.Cut(addr, ":")
	n, _ := strconv.Atoi(port)
	return n
}
