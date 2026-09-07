// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/sched"
)

// MinWindowWords is the fewest words a break can be written in and still be a
// break: a persona, the record that just ended, the one coming up, and a
// handoff.
//
// MEASURED, NOT CHOSEN. The window becomes "LENGTH: N words MAXIMUM" in the
// writer's prompt, and at the old floor of three seconds that read "8 words
// MAXIMUM". Against the deployed model, from an otherwise identical prompt:
//
//	 8 words   "The You Get"
//	20 words   "Yeah, Bayside just destroyed that castle. Barry Gibb on the
//	            floor. That's the kind of pure, unadulterated rock and roll
//	            that got me through the 80s. And now..."
//	40 words   the prompt recited back, in capitals
//
// The first eight breaks the deployed station ever aired were all degenerate
// and none was a model fault: "nobody nobody, nobody, nobody, nobody, nobody,
// nobody nobody" is EXACTLY eight words, which is what it had been asked for.
// A window this short is not a short break, it is a broken one.
const MinWindowWords = 20

// MinBreakSeconds is the shortest window worth aiming a break at.
//
// Derived from the word floor rather than set beside it, so the two cannot
// drift apart: the prompt converts seconds to words, and this is the same
// conversion run backwards.
const MinBreakSeconds = MinWindowWords / dj.WordsPerSecond

// Boundary is one upcoming track transition, as it enters the buffer.
type Boundary struct {
	// Index counts boundaries from 1. Zero is before anything has played.
	Index int
	// Cur is the outgoing track, Next the incoming one.
	Cur, Next *Track
	// InsertionAt is when the break airs, in seconds on the mixer's timeline.
	InsertionAt float64
	// InsertionSample is the same instant as a bus sample, already compensated
	// for chain latency by whoever built it.
	InsertionSample int64

	// WHICH RECORDS THIS BREAK IS ABOUT, carried with the boundary rather than
	// left in the writer.
	//
	// Generation is ASYNCHRONOUS and slow -- 75 seconds, measured on the live
	// station -- while boundaries keep arriving. The writer's context used to
	// be set at announce time and read at generation time, so by the time a
	// break was written the names had been overwritten by a later boundary:
	// the DJ announced Everlong and Green Day played. Heard on the live
	// station. A forward reference has to be true, and this product leans on
	// them: there is no skip, precisely so a break can safely say what is
	// coming.
	PrevID, CurID, NextID            int64
	PrevArtist, PrevTitle            string
	CurArtist, CurTitle, CurAlbum    string
	NextArtist, NextTitle, NextAlbum string
	PrevAlbum                        string
}

// Pipeline connects the break machinery end to end:
//
//	Cadence.SlotAt -> PreferredWindow -> Lookahead.ShouldTrigger ->
//	Writer -> LengthCheck -> sched.Queue
//
// It is TWO PHASE on purpose. Announce decides whether a boundary gets a break
// as it enters the buffer; Tick generates it later, when the lookahead fires.
// Collapsing them would mean asking Cadence about a boundary at generation
// time, and Cadence.SlotAt advances state -- declining to generate would then
// silently consume the slot and the break would never air anywhere.
type Pipeline struct {
	Cadence   *Cadence
	Lookahead *Lookahead
	Length    LengthCheck
	Queue     *sched.Queue
	// Writer is the same BreakWriter LengthCheck writes through. Held here so
	// each boundary's track context can be applied at GENERATION time.
	Writer      *BreakWriter
	FadeSeconds float64
	Clock       clock.Clock
	Log         *slog.Logger

	// OnScheduled is called when a break is handed to the mixer. Optional, and
	// it exists so /now.json can show what the DJ said: the transcript was
	// already in the status schema with nothing to fill it.
	OnScheduled func(text string, placement mix.Placement)

	mu      sync.Mutex
	pending []Boundary
}

func (p *Pipeline) log() *slog.Logger {
	if p.Log == nil {
		return slog.Default()
	}
	return p.Log
}

func (p *Pipeline) clk() clock.Clock {
	if p.Clock == nil {
		return clock.Real{}
	}
	return p.Clock
}

// Announce offers a boundary to the cadence and remembers it if it gets a slot.
//
// Call it exactly once per boundary, in order, as the boundary is buffered.
// SetCadence changes the break cadence while the station is running.
//
// The pipeline is read from the mixer goroutine and written from an HTTP
// handler, so this takes the same lock Announce does.
// SetQueue points the pipeline at the queue a break should be scheduled into,
// and at the callback that records it.
//
// IT CANNOT BE SET AT CONSTRUCTION on a multi-station build. Every station owns
// its own ring and its own scheduler queue -- that separation is the whole
// point of a per-station runtime -- so the queue only exists once a station is
// actually on air, and it is a different queue for each one. Wiring this only
// where the single legacy runtime was built left the pipeline with a nil queue
// on every station-based deployment, and every break it generated was dropped
// with "break pipeline has no scheduler queue". The DJ was silent and nothing
// else reported a fault.
func (p *Pipeline) SetQueue(q *sched.Queue, onScheduled func(text string, placement mix.Placement)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Queue = q
	if onScheduled != nil {
		p.OnScheduled = onScheduled
	}
}

func (p *Pipeline) SetCadence(c *Cadence) {
	if c == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Cadence = c
}

func (p *Pipeline) Announce(b Boundary) bool {
	// THE LOCK COVERS THE CADENCE TOO, not just the pending list. Announce runs
	// on the mixer goroutine and SetCadence on an admin HTTP handler, so
	// reading p.Cadence outside the lock raced with every console change --
	// and SlotAt mutates the cadence it is called on, so the read had to be
	// held for the whole call, not just long enough to copy the pointer.
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.Cadence.SlotAt(b.Index, b.Cur != nil && b.Cur.NoCrossfadeNext) {
		return false
	}
	p.pending = append(p.pending, b)
	return true
}

// EveryN is the cadence the station is running RIGHT NOW.
//
// Zero when there is no cadence: a console can ask before a DJ exists, and
// making the caller nil-check a field it is not allowed to read unlocked is
// how the race got there in the first place.
func (p *Pipeline) EveryN() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Cadence == nil {
		return 0
	}
	return p.Cadence.EveryN()
}

// Pending is how many slots are waiting for their lookahead to fire.
func (p *Pipeline) Pending() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending)
}

// Tick generates every pending break whose lookahead has fired, and returns how
// many were enqueued.
//
// It never returns an error for a dropped break. Generation failing, running
// late, or producing something that will not fit are all NORMAL and all end the
// same way: no break at this boundary, music uninterrupted.
func (p *Pipeline) Tick(ctx context.Context, now float64) (int, error) {
	p.mu.Lock()
	due, keep := make([]Boundary, 0, len(p.pending)), p.pending[:0:0]
	for _, b := range p.pending {
		switch {
		case now >= b.InsertionAt:
			// The boundary went past without generation ever firing. Nothing to
			// do but say so; a silent disappearance is the one thing worse.
			p.log().Warn("break slot expired before generation", "boundary", b.Index, "insertion_at", b.InsertionAt)
		case p.Lookahead.ShouldTrigger(now, b.InsertionAt):
			due = append(due, b)
		default:
			keep = append(keep, b)
		}
	}
	p.pending = keep
	p.mu.Unlock()

	enqueued := 0
	for _, b := range due {
		ok, err := p.generate(ctx, now, b)
		if err != nil {
			return enqueued, err
		}
		if ok {
			enqueued++
		}
	}
	return enqueued, nil
}

// applyContext points the writer at the records THIS boundary is about.
//
// At generation time, not at announce time. Between those two moments -- 75
// seconds on the live station -- later boundaries arrive, and each one used to
// overwrite the writer's shared context. The break then described whichever
// pair had been announced most recently: the DJ said Everlong was next and the
// station played Green Day.
func (p *Pipeline) applyContext(b Boundary) {
	if p.Writer == nil {
		return
	}
	p.Writer.SetContext(b.PrevID, b.CurID, b.NextID, b.PrevArtist, b.PrevTitle)
	p.Writer.SetTrackNames(
		b.PrevArtist, b.PrevTitle, b.PrevAlbum,
		b.CurArtist, b.CurTitle, b.CurAlbum,
		b.NextArtist, b.NextTitle, b.NextAlbum,
	)
}

// generate runs one break all the way from placement to the mixer's queue.
func (p *Pipeline) generate(ctx context.Context, now float64, b Boundary) (bool, error) {
	p.applyContext(b)

	placement, window := PreferredWindow(b.Cur, b.Next, p.FadeSeconds)

	started := p.clk().Now()
	rendered, err := p.Length.Enforce(ctx, placement, window)
	elapsed := p.clk().Now().Sub(started)

	// Timed here, around the WHOLE ladder, so the sample includes rewrites and
	// their renders. Measuring only clean first passes understates T by an
	// entire LLM call plus a TTS render, on exactly the breaks most likely to
	// be late.
	p.Lookahead.RecordTiming(elapsed, rendered.Attempts)

	if err != nil {
		return false, err // context cancellation only
	}
	if rendered.Dropped {
		p.log().Info("break dropped", "boundary", b.Index, "reason", rendered.Reason, "elapsed", elapsed)
		return false, nil
	}
	if !p.Lookahead.InTime(now, b.InsertionAt, elapsed) {
		p.log().Warn("break generated too late", "boundary", b.Index, "elapsed", elapsed,
			"available", b.InsertionAt-now, "p95", p.Lookahead.P95(), "recommended_t", p.Lookahead.RecommendedT())
		_ = os.Remove(rendered.Path)
		return false, nil
	}

	p.mu.Lock()
	queue := p.Queue
	p.mu.Unlock()
	if queue == nil {
		// Constructed wrong. Said plainly rather than dereferenced: the pipeline
		// runs in its own goroutine, and a panic there ends the process.
		p.log().Error("break pipeline has no scheduler queue; dropping", "boundary", b.Index)
		_ = os.Remove(rendered.Path)
		return false, nil
	}
	if err := queue.Enqueue(sched.Entry{
		AfterSample: b.InsertionSample,
		Action:      sched.ActionSpliceAudio,
		Path:        rendered.Path,
		Placement:   rendered.Placement.String(),
	}); err != nil {
		// The mixer has already played past this point. Same outcome as any
		// other late break, and never a reason to stop the station.
		p.log().Warn("break rejected by the scheduler", "boundary", b.Index, "err", err)
		_ = os.Remove(rendered.Path)
		return false, nil
	}

	p.log().Info("break scheduled", "boundary", b.Index, "placement", rendered.Placement.String(),
		"seconds", rendered.Seconds, "attempts", rendered.Attempts, "elapsed", elapsed)
	if p.OnScheduled != nil {
		p.OnScheduled(rendered.Text, rendered.Placement)
	}
	return true, nil
}

// PreferredWindow picks where a break should AIM before anything is written.
//
// Placement is a chicken-and-egg problem: ChoosePlacement needs the speech
// length, and the speech cannot be written without a length to aim at. So this
// picks the preferred window up front and LengthCheck's ladder corrects it
// afterwards against the real rendered duration.
//
// Ramp first, because talking over an intro and landing on the downbeat is the
// signature move. Span is the widest window and comes LAST of the three for
// exactly that reason: taking it early would spend a segue where a clean intro
// would have done.
func PreferredWindow(cur, next *Track, fadeSeconds float64) (mix.Placement, float64) {
	transition := mix.TransitionFor(cur != nil && cur.NoCrossfadeNext, int(fadeSeconds*mix.SampleRate))

	var ramp, outro float64
	if next.usableRamp() {
		ramp = next.RampS
	}
	if cur.usableRamp() {
		outro = cur.OutroS
	}

	switch {
	case ramp >= MinBreakSeconds:
		return mix.PlaceBreak(mix.PlacementRamp, transition), windowOr(transition, ramp)
	case outro >= MinBreakSeconds:
		return mix.PlaceBreak(mix.PlacementOutro, transition), windowOr(transition, outro)
	case ramp+fadeSeconds+outro >= MinBreakSeconds && (ramp > 0 || outro > 0):
		return mix.PlaceBreak(mix.PlacementSpan, transition), windowOr(transition, ramp+fadeSeconds+outro)
	}
	return mix.PlaceBreak(mix.PlacementBetween, transition), BetweenWindowSeconds
}

// windowOr falls back to the between budget when the gapless rule relocated the
// break, since the window it was sized for no longer applies.
func windowOr(t mix.Transition, seconds float64) float64 {
	if t.Adjacent() {
		return BetweenWindowSeconds
	}
	return seconds
}
