// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/obs"
	"github.com/andrewloable/jockora/internal/sched"
)

// DefaultBlockFrames is how much audio the mixer produces per iteration, about
// 85 ms on the bus.
const DefaultBlockFrames = 4096

// DefaultPrebufferFrames is how much decoded audio the mixer waits for before it
// goes on air. Without it every stream opens with a burst of silence-fill while
// the first decoder is still starting, and the minimum ring occupancy over a run
// is always zero no matter how healthy the run was.
const DefaultPrebufferFrames = 2 * SampleRate

// prebufferTimeout bounds that wait. Going on air late is worse than going on
// air thin, and a library where nothing decodes must still produce a stream
// rather than hanging silently before the first byte.
const prebufferTimeout = 5 * time.Second

// Mixer is the heart of the program: one goroutine that owns the bus sample
// clock, pulls music from the ring, drains the scheduler queue, runs the signal
// chain, paces itself to the wall clock, and writes f32le to the encoder.
//
// It is the SOLE writer to W. Nothing else may ever write to that pipe, from
// any goroutine, for any reason. Everything else hands the mixer finished
// buffers and may block only itself.
//
// That single-owner rule is what makes the timing reviewable: every real-time
// decision lives in one loop with one clock, so there is exactly one place to
// reason about it.
// Processor is the DSP stage the mixer drives.
type Processor interface {
	Process(out, musicA, musicB, speech []Frame, st State)
	LatencyFrames() int
}

// MaxConsecutivePanics is how many blocks in a row may panic before the mixer
// gives up.
//
// Recovering forever is worse than dying. One panic is a bug in a block of
// audio; five in a row is a structural fault, and a mixer that spins on it
// produces a stream of silence while every health check says it is running --
// which is the failure this program is least able to notice.
const MaxConsecutivePanics = 5

type Mixer struct {
	Ring  *Ring
	Queue *sched.Queue
	// Chain is the DSP. An interface with exactly one production implementation
	// (*Chain), which is normally a smell -- it exists because the mixer's
	// panic recovery cannot be tested without something that panics on demand,
	// and a recovery path nobody has ever exercised is not a recovery path.
	Chain Processor
	Pacer *Pacer
	W     io.Writer
	Log   *slog.Logger

	// BlockFrames is the production block size. Zero means DefaultBlockFrames.
	BlockFrames int

	// PrebufferFrames is how much audio to wait for before the first write.
	// Zero means DefaultPrebufferFrames; negative disables the wait.
	PrebufferFrames int

	// Metrics records inter-write gaps and ring occupancy. Optional; nil costs
	// nothing.
	Metrics *Metrics

	// onBlock, if set, is called with the sample position after each block. It
	// exists so a test can drive the loop deterministically.
	onBlock func(pos int64)

	samplePos   int64
	speech      []Frame // speech waiting to be mixed in
	speechPos   int     // frames of speech already mixed
	speechStart int64   // absolute input sample at which speech[0] belongs
}

// guard runs one block's work and turns a panic into a reported failure.
//
// The stack is logged, not swallowed. A recovered panic with no stack is a bug
// that has been made invisible rather than survivable.
func (m *Mixer) guard(log *slog.Logger, fn func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			log.Error("mixer panicked; dropping one block and continuing",
				"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	fn()
	return false
}

// SamplePos reports how many frames the mixer has produced. Safe to read only
// after Run has returned.
func (m *Mixer) SamplePos() int64 { return m.samplePos }

// Run produces audio until ctx is cancelled, and returns ctx.Err() when it is.
func (m *Mixer) Run(ctx context.Context) error {
	block := m.BlockFrames
	if block <= 0 {
		block = DefaultBlockFrames
	}
	log := m.Log
	if log == nil {
		log = slog.Default()
	}

	music := make([]Frame, block)
	speech := make([]Frame, block)
	out := make([]Frame, block)

	// One clock, the pacer's. Reading time.Now() here instead would make the
	// fake-clock tests measure nothing and let a long run's drift figure quietly
	// disagree with its gap figure.
	clk := m.Pacer.Clock()
	var lastUnderruns uint64
	var underrunOpen bool

	m.prebuffer(ctx, clk, log)

	last := clk.Now()

	var consecutivePanics int

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Everything from reading music to running the chain, wrapped so a
		// panic costs ONE BLOCK of audio instead of the whole stream. The mixer
		// is the one goroutine whose death is total silence and the only one
		// with nothing above it to restart it.
		panicked := m.guard(log, func() {
			// 1. Music. Read never blocks and pads silence, so a decoder that is
			// behind costs a gap, never the stream.
			m.Metrics.recordOccupancy(m.Ring.Occupancy())
			real := m.Ring.Read(music)

			// Report silence-fill AS IT HAPPENS, not only in the shutdown summary.
			// This is the system's signature failure: nothing sounds broken, so
			// without a record here a decoder falling behind is invisible until the
			// process stops. Logged once per episode rather than per block, because
			// a sustained stall would otherwise emit twelve lines a second.
			if n := m.Ring.UnderrunCount(); n > lastUnderruns {
				if !underrunOpen {
					obs.New(log).RingUnderrun(real, "block_frames", block, "total_underruns", n)
					underrunOpen = true
				}
				lastUnderruns = n
			} else {
				underrunOpen = false
			}

			// 2. Anything starting inside this block. The queue holds the sample at
			// which a break should be HEARD; the chain delays everything by its
			// latency, so a break must be fed in that many frames earlier. Draining
			// through the end of the block, rather than at its start, is what allows
			// sub-block placement below: a break due mid-block would otherwise wait
			// for the next boundary and land up to a whole block late.
			latency := int64(m.Chain.LatencyFrames())
			for _, e := range m.Queue.DrainDue(m.samplePos + int64(block) + latency - 1) {
				frames, err := ReadWAV(e.Path)
				if err != nil {
					// Breaks are optional, music is not. Log it and keep going.
					obs.New(log).BreakDropped(obs.ReasonSidecarDown, "path", e.Path, "err", err.Error())
					continue
				}
				if m.speechPos < len(m.speech) {
					// Still playing one. Queue this behind it rather than cutting
					// the DJ off mid-sentence.
					log.Warn("break arrived while one was still playing; queued behind it", "path", e.Path)
					m.speech = append(m.speech, frames...)
					continue
				}
				m.speech, m.speechPos = frames, 0
				m.speechStart = max(e.AfterSample-latency, m.samplePos)
			}

			// 3. Place this block's speech at its exact offset within the block.
			clear(speech)
			speaking := false
			if m.speechPos < len(m.speech) {
				off := int(m.speechStart + int64(m.speechPos) - m.samplePos)
				if off < 0 {
					off = 0 // enqueued late; start it now rather than losing it
				}
				if off < block {
					n := copy(speech[off:], m.speech[m.speechPos:])
					m.speechPos += n
					speaking = n > 0
				}
			}

			// 4. Run the chain. musicB is nil and no fade is in progress: track
			// sequencing and crossfading come with the track-source wiring, and
			// until then the ring carries one continuous music stream.
			m.Chain.Process(out, music, nil, speech, State{Speaking: speaking})
		})

		if panicked {
			consecutivePanics++
			if consecutivePanics >= MaxConsecutivePanics {
				return fmt.Errorf("mix: the mixer panicked %d blocks in a row; giving up rather than streaming silence",
					consecutivePanics)
			}
			// Emit silence for the lost block rather than whatever half-written
			// state the chain left behind, and keep the clock moving: the pacing
			// is what the encoder downstream depends on.
			clear(out)
		} else {
			consecutivePanics = 0
		}

		// 5. Write. This is the only place in the program that writes to W.
		if err := WriteF32LE(m.W, out); err != nil {
			return err
		}

		now := clk.Now()
		m.Metrics.recordWrite(now.Sub(last))
		last = now

		// 6. Pace to the wall clock, then 7. advance the sample clock.
		m.Pacer.Wait(len(out))
		m.samplePos += int64(len(out))

		if m.onBlock != nil {
			m.onBlock(m.samplePos)
		}
	}
}

// prebuffer waits for the ring to fill before the first block, so the stream
// does not open on silence-fill while the first decoder is still starting.
//
// The wait is bounded: if nothing arrives, going on air thin beats not going on
// air at all.
func (m *Mixer) prebuffer(ctx context.Context, clk clock.Clock, log *slog.Logger) {
	want := m.PrebufferFrames
	if want == 0 {
		want = DefaultPrebufferFrames
	}
	if want < 0 {
		return
	}

	target := FramesToDuration(want)
	deadline := clk.Now().Add(prebufferTimeout)
	for m.Ring.Occupancy() < target {
		if ctx.Err() != nil {
			return
		}
		if !clk.Now().Before(deadline) {
			log.Warn("going on air without a full prebuffer",
				"want", target, "have", m.Ring.Occupancy())
			return
		}
		clk.Sleep(20 * time.Millisecond)
	}
}
