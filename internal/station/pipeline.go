// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"log/slog"
	"math"
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
	Cadence *Cadence
	// OverlapSeconds is how far BEFORE a transition the DJ may start talking,
	// when the outgoing track has an instrumental tail to talk over. Zero
	// keeps every break starting exactly at the boundary, which is what this
	// did before Jockora-ugw. Read and written under mu; see SetOverlap.
	OverlapSeconds float64
	Lookahead      *Lookahead
	Length         LengthCheck
	Queue          *sched.Queue
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
	// scheduled is how many rendered breaks the mixer queue is holding.
	scheduled int
	// gaps is how long the music pauses after a boundary, by boundary index,
	// waiting for the feed to come and collect it.
	//
	// RECORDED ONLY AFTER THE SCHEDULER HAS ACCEPTED THE BREAK. A gap held for
	// a break that never airs is dead air, which is the one outcome here that
	// is worse than no gap at all.
	gaps map[int]float64
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
	// A NEW FEED COUNTS BOUNDARIES FROM ZERO, so gaps claimed by the run that
	// just ended would be collected by the next one and hold the music for a
	// break nobody is about to hear. Same reason Cadence is reset here.
	p.gaps = nil
}

func (p *Pipeline) SetCadence(c *Cadence) {
	if c == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Cadence = c
}

// SetOverlap changes how far into the outgoing outro the DJ starts talking.
//
// GUARDED LIKE THE CADENCE AND FOR THE SAME REASON: this is written by an admin
// HTTP handler and read on the generation path. The comment above SetCadence
// records that reading an unguarded field there raced with every console
// change, and that the race was reintroduced once by a line placed directly
// above the comment warning about it.
func (p *Pipeline) SetOverlap(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.OverlapSeconds = seconds
}

// Overlap is the configured lead-in, read under the lock.
func (p *Pipeline) Overlap() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.OverlapSeconds
}

// setGap records how long the music should pause after one boundary.
func (p *Pipeline) setGap(index int, seconds float64) {
	if seconds <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gaps == nil {
		p.gaps = make(map[int]float64)
	}
	p.gaps[index] = seconds
}

// TakeGap is how long the music pauses after this boundary, and it can only be
// asked once.
//
// CONSUMED RATHER THAN READ, so a feed that asks twice does not hold the music
// twice. Zero is the ordinary answer: most boundaries carry no break at all.
func (p *Pipeline) TakeGap(index int) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	seconds := p.gaps[index]
	delete(p.gaps, index)
	return seconds
}

// gapFor is how long the music pauses between the two records, so that the
// break sits in a hole with a record overlapping each end of it.
//
// THE MEASURED OUTRO AND INTRO ARE NOT CONSULTED. They used to cap each end --
// the GTA rule, that a break never lands over a vocal -- and the operator has
// since said to disregard it: the configured overlap applies to every break at
// both ends, whatever the track does. So the DJ will talk over the closing
// seconds of a record that sings to the end, and over the opening seconds of
// one that starts singing immediately. That is the instruction, and the setting
// is the only thing that bounds it now; 0 turns the whole behaviour off.
//
// The two ends are the same number by construction, which is why there is no
// lead and no tail here to tell apart any more.
//
// A break shorter than its own two overlaps needs no hole at all, which is the
// clamp at zero -- and there the records simply overlap the break by less than
// was asked for, because there is not enough break to overlap.
func gapFor(seconds, overlap float64) float64 {
	return max(0, seconds-2*overlap)
}

// round1 keeps a log line readable. These are seconds of audio, and a
// nanosecond-precision float in a log is noise nobody reads past.
func round1(seconds float64) float64 { return math.Round(seconds*10) / 10 }

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
	// THE DEADLINE IS WHEN THE BREAK STARTS, NOT WHEN THE TRACK ENDS.
	//
	// A break placed on the outgoing outro now begins up to OverlapSeconds
	// BEFORE the boundary, so it has to be ready that much sooner. Checked
	// against the boundary instead, a break could pass this and then be handed
	// to the queue at a sample the mixer had already played -- rejected there,
	// deleted, and reported as "break rejected by the scheduler" rather than as
	// the late generation it actually was. Jockora-ugw introduced the lead-in;
	// this is the deadline moving with it.
	leadS := p.Overlap()
	startsAt := b.InsertionAt - leadS
	if !p.Lookahead.InTime(now, startsAt, elapsed) {
		p.log().Warn("break generated too late", "boundary", b.Index, "elapsed", elapsed,
			"available", startsAt-now, "lead_in_s", leadS,
			"p95", p.Lookahead.P95(), "recommended_t", p.Lookahead.RecommendedT())
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
	// THE BREAK STARTS BEFORE THE BOUNDARY WHEN IT IS ABOUT THE OUTGOING TRACK.
	//
	// This is the half of placement that was never implemented. The splice
	// position is fixed at announce time, and sched.Entry carries a Placement
	// string that nothing reads -- so outro and span granted a bigger word
	// budget and then spent all of it AFTER the transition, which is how a
	// break sized for a window straddling the boundary ends up entirely on one
	// side of it. Jockora-ugw.
	//
	// NEVER EARLIER THAN THE MIXER HAS ALREADY REACHED. The queue refuses an
	// entry at or before what it has drained and the mixer clamps to its own
	// position, but arriving there with a negative offset would be this code
	// asking for something impossible and calling the refusal someone else's
	// problem.
	at := b.InsertionSample - int64(leadS*mix.SampleRate)
	if at < 0 {
		at = 0
	}
	if err := queue.Enqueue(sched.Entry{
		AfterSample: at,
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

	// THE HOLE THE BREAK SITS IN, claimed only now that the scheduler has taken
	// the break. The feed collects it when it finishes decoding this boundary's
	// outgoing track and writes that much silence into the ring before starting
	// the next one, which is what puts the incoming record under the DJ's last
	// words instead of under all of them. Jockora-ey4.
	gap := gapFor(rendered.Seconds, leadS)
	p.setGap(b.Index, gap)

	p.log().Info("break scheduled", "boundary", b.Index, "placement", rendered.Placement.String(),
		"seconds", rendered.Seconds, "attempts", rendered.Attempts, "elapsed", elapsed,
		"overlap_s", round1(leadS), "music_gap_s", round1(gap))

	// WHAT THE DJ SAID, BESIDE THE RECORDS IT WAS WRITTEN ABOUT.
	//
	// A break can be perfectly written, correctly timed and about the wrong
	// pair. That has happened here: the writer's context was shared, later
	// boundaries overwrote it during the seventy-five seconds between announce
	// and generation, and the DJ said Everlong was next while the station
	// played Green Day. applyContext fixed the cause; this is how anybody
	// CHECKS it, on a running station, without a debugger.
	//
	// THE THREE ARE NAMED FOR WHAT A LISTENER HEARS, not for the struct
	// fields, because the struct's names are a trap: this break airs at the end
	// of CUR, so the record that just finished is cur and the one starting is
	// next. Prev is the one before that -- the DJ may backsell it, and reading
	// it as "the song before this break" is exactly the misreading that would
	// make a correct break look wrong.
	p.log().Info("break text",
		"boundary", b.Index,
		"placement", rendered.Placement.String(),
		"starts_before_boundary_s", round1(leadS),
		"just_played", song(b.CurArtist, b.CurTitle),
		"coming_up", song(b.NextArtist, b.NextTitle),
		"before_that", song(b.PrevArtist, b.PrevTitle),
		"said", rendered.Text)
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

// Outlook is what the DJ is about to do, for anyone who cannot see the logs.
//
// A break that is coming, one still being written, and one that was never
// scheduled are indistinguishable from outside -- so a quiet station and a
// broken one read the same, which is the failure this product is least able to
// notice.
type Outlook string

const (
	// OutlookNone: no break is scheduled after the current track.
	OutlookNone Outlook = "none"
	// OutlookWriting: a slot exists and the model has not answered yet. This
	// is the state that can still fail.
	OutlookWriting Outlook = "writing"
	// OutlookReady: the speech is rendered and queued. It will air.
	OutlookReady Outlook = "ready"
)

// SetScheduled records how many rendered breaks are waiting in the mixer's
// queue. Told rather than asked, because the queue belongs to the station
// runtime and the pipeline is handed one only once a station is on air.
func (p *Pipeline) SetScheduled(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scheduled = n
}

// Outlook reports whether the DJ is about to speak.
//
// WRITING OUTRANKS READY when both are true: the interesting state is the one
// that can still fail, and saying "ready" while a later break is unwritten
// would be the more comfortable half of the truth.
func (p *Pipeline) Outlook() Outlook {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case len(p.pending) > 0:
		return OutlookWriting
	case p.scheduled > 0:
		return OutlookReady
	default:
		return OutlookNone
	}
}

// song is one record as a person would name it, for a log line.
//
// EMPTY IS SAID OUT LOUD as "none" rather than left blank: the first break on a
// station has no previous record, and an empty value in a log line reads as a
// field that failed to populate rather than a fact.
func song(artist, title string) string {
	switch {
	case artist == "" && title == "":
		return "none"
	case artist == "":
		return title
	case title == "":
		return artist
	}
	return artist + " — " + title
}
