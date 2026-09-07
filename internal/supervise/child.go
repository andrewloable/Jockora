// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package supervise

import (
	"bufio"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// maxLastWords is how many lines of a dead child's stderr are kept.
const maxLastWords = 8

// Child watches one spawned process: it drains stderr, reaps the process, and
// makes both its death and its final output readable by anyone.
//
// IT EXISTS BECAUSE WAITING FOR A CORPSE IS FREE TO GET WRONG. A supervisor
// that only polls a health endpoint cannot tell "still starting" from "died
// four seconds ago", so it waits out its whole start timeout for a failure that
// was decided immediately -- minutes of a dead port, because both the enricher
// and the DJ are built before the HTTP listener is. Both this package and
// internal/tts had that bug, separately, with near-identical code.
//
// ONE REAPER, ALWAYS. os/exec does not allow concurrent Wait, and Wait must not
// run until the stderr pipe is drained, since os/exec closes that pipe when it
// sees the child exit. Callers observe Dead instead of calling Wait themselves.
type Child struct {
	done chan struct{}
	err  error

	mu   sync.Mutex
	tail []string
}

// Watch starts draining stderr and reaping cmd, which must already be started.
// onLine, if given, sees every stderr line as it arrives.
func Watch(cmd *exec.Cmd, stderr io.Reader, onLine func(string)) *Child {
	c := &Child{done: make(chan struct{})}

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		sc := bufio.NewScanner(stderr)
		// Model servers emit very long lines; the default 64 KiB token limit
		// would end the drain early and stall the child in write().
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if onLine != nil {
				onLine(sc.Text())
			}
			c.say(sc.Text())
		}
	}()
	go func() {
		<-drained
		c.err = cmd.Wait()
		close(c.done)
	}()
	return c
}

// Dead closes when the child has exited and been reaped.
func (c *Child) Dead() <-chan struct{} { return c.done }

// Exited reports whether the child is already gone, without blocking.
func (c *Child) Exited() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// Err is why it exited. Only valid once Dead has closed.
func (c *Child) Err() error { return c.err }

// Reap blocks until the child has been reaped. Safe to call many times.
func (c *Child) Reap() { <-c.done }

func (c *Child) say(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tail = append(c.tail, line)
	if len(c.tail) > maxLastWords {
		c.tail = c.tail[len(c.tail)-maxLastWords:]
	}
}

// LastWords is the tail of what the child printed, ready to append to an error.
// Empty when it said nothing, so it can be interpolated unconditionally.
func (c *Child) LastWords() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tail) == 0 {
		return ""
	}
	return ": " + strings.Join(c.tail, " | ")
}
