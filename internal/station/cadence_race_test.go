// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"sync"
	"testing"
)

// THE CADENCE IS WRITTEN BY AN HTTP HANDLER AND READ BY THE MIXER. SetCadence
// takes the pipeline lock; Announce, which reads the very same field on every
// track boundary, did not. Neither did the log line that reports which cadence
// a slot was offered under. Both are unsynchronised reads of a pointer another
// goroutine replaces the moment an operator touches the console.
//
// Every test here is TestPipelineCadence*, which is the -run pattern for this
// fix. It is a -race test: without the detector it passes either way.

func TestPipelineCadenceSurvivesAConsoleChange(t *testing.T) {
	p := &Pipeline{Cadence: NewCadence(4)}

	var wg sync.WaitGroup
	wg.Add(3)

	// The mixer: one boundary after another, for ever.
	go func() {
		defer wg.Done()
		for i := 1; i <= 300; i++ {
			p.Announce(Boundary{Index: i})
		}
	}()
	// The log line that goes with it.
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			_ = p.EveryN()
		}
	}()
	// The operator, changing their mind.
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			p.SetCadence(NewCadence(1 + i%8))
		}
	}()
	wg.Wait()
}

func TestPipelineCadenceReportsItsOwnNumber(t *testing.T) {
	p := &Pipeline{Cadence: NewCadence(3)}
	if got := p.EveryN(); got != 3 {
		t.Errorf("EveryN = %d, want 3", got)
	}
	p.SetCadence(NewCadence(9))
	if got := p.EveryN(); got != 9 {
		t.Errorf("after SetCadence, EveryN = %d, want 9", got)
	}

	// A pipeline with no cadence has no number to give, and must say so rather
	// than panicking: the console asks before a DJ exists.
	if got := (&Pipeline{}).EveryN(); got != 0 {
		t.Errorf("EveryN with no cadence = %d, want 0", got)
	}
}
