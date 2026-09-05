// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import "sync"

// Session tracks what the DJ knows about the current broadcast.
//
// v0.1 serves ONE SHARED session, so "session start" means the stream starting,
// not a listener connecting. Tying it to an HTTP connection would give every
// listener their own cold open on a stream that is already mid-sentence for
// everyone else.
type Session struct {
	mu        sync.Mutex
	coldOpen  bool
	breaksOut int
}

// NewSession begins a broadcast. The first break is a cold open.
func NewSession() *Session { return &Session{coldOpen: true} }

// IsColdOpen reports whether the next break is the first of the session.
func (s *Session) IsColdOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.coldOpen
}

// BreakAired records that a break went out, clearing the cold-open flag.
func (s *Session) BreakAired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coldOpen = false
	s.breaksOut++
}

// Breaks reports how many breaks have aired this session.
func (s *Session) Breaks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.breaksOut
}

// Restart begins a new broadcast, so the next break is a cold open again.
func (s *Session) Restart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.coldOpen = true
	s.breaksOut = 0
}
