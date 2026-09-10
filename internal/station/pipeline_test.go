// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/sched"
)

// All named TestPipeline*.

// pacedRenderer takes a fixed wall-clock time per render, on a fake clock, so
// the retry-inclusive timing can be asserted exactly.
type pacedRenderer struct {
	*scriptedRenderer
	clk     *clock.Fake
	perCall time.Duration
}

func (p *pacedRenderer) Render(ctx context.Context, text, voice, outPath string) (float64, error) {
	p.clk.Advance(p.perCall)
	return p.scriptedRenderer.Render(ctx, text, voice, outPath)
}

func newPipeline(t *testing.T, clk *clock.Fake, perCall time.Duration, seconds ...float64) (*Pipeline, *scriptedRenderer, *sched.Queue) {
	t.Helper()
	s := &scriptedRenderer{seconds: seconds}
	r := &pacedRenderer{scriptedRenderer: s, clk: clk, perCall: perCall}
	q := &sched.Queue{}
	return &Pipeline{
		Cadence:     NewCadence(4),
		Lookahead:   NewLookahead(150 * time.Second),
		Length:      LengthCheck{Writer: s, Renderer: r, Dir: t.TempDir()},
		Queue:       q,
		FadeSeconds: 3,
		Clock:       clk,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, s, q
}

func lrcTrack(rampS, outroS float64) *Track {
	return &Track{RampS: rampS, OutroS: outroS, DurationS: 200, RampConfidence: enrich.ConfidenceLRC}
}

// TestPipelineEndToEnd walks the whole chain: cadence decides a slot, the
// lookahead holds it, generation fires, the length ladder accepts it and the
// scheduler receives an entry the mixer can splice.
func TestPipelineEndToEnd(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	p, s, q := newPipeline(t, clk, 20*time.Second, 6.0)

	// Four boundaries at 60-second intervals; only the fourth gets a slot.
	for i := 1; i <= 4; i++ {
		at := float64(i) * 60
		got := p.Announce(Boundary{
			Index: i, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4),
			InsertionAt: at, InsertionSample: int64(at * mix.SampleRate),
		})
		if got != (i == 4) {
			t.Fatalf("boundary %d slot = %v, want %v", i, got, i == 4)
		}
	}
	if p.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1", p.Pending())
	}

	// NOT too early any more. The insertion is at 240s and generation starts
	// the moment the slot exists, so the first tick writes it -- which is the
	// whole point: every second between here and the boundary is slack against
	// a slow model.
	if n, err := p.Tick(context.Background(), 60); err != nil || n != 1 {
		t.Fatalf("Tick at t=60 enqueued %d (err %v), want 1", n, err)
	}
	if len(s.targets) != 1 {
		t.Errorf("wrote %d breaks at the first opportunity, want 1", len(s.targets))
	}

	// Nothing pending left to do: it was written at the first opportunity.
	n, err := p.Tick(context.Background(), 100)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 0 {
		t.Fatalf("enqueued %d breaks on a second tick, want 0", n)
	}
	if q.Pending() != 1 {
		t.Fatalf("scheduler holds %d entries, want 1", q.Pending())
	}
	if p.Pending() != 0 {
		t.Errorf("pipeline still holds %d slots after generating them", p.Pending())
	}

	entries := q.DrainDue(int64(240 * mix.SampleRate))
	if len(entries) != 1 {
		t.Fatalf("drained %d entries, want 1", len(entries))
	}
	if entries[0].Placement != "ramp" {
		t.Errorf("placement = %q, want ramp: a 12s ramp holds a 6s break", entries[0].Placement)
	}
	if entries[0].Path == "" {
		t.Error("scheduled entry has no audio path")
	}
}

// TestPipelineTimingCoversTheWholeLadder is the measurement the lookahead turns
// on: the sample is taken around everything Enforce does, not around a clean
// first pass.
//
// There is no longer a second pass to include. The writer gets ONE generation
// and a break that does not fit is re-placed into the wider gap rather than
// rewritten, so a retried render is now structurally impossible -- which is
// what the RetriedCount assertion here guards.
func TestPipelineTimingCoversTheWholeLadder(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	// The render overruns the 12s ramp and is moved to the between gap. 20s of
	// wall clock, once.
	p, _, _ := newPipeline(t, clk, 20*time.Second, 30.0, 6.0)

	for i := 1; i <= 4; i++ {
		p.Announce(Boundary{Index: i, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4),
			InsertionAt: 240, InsertionSample: int64(240 * mix.SampleRate)})
	}
	if _, err := p.Tick(context.Background(), 100); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if n := p.Lookahead.Count(); n != 1 {
		t.Fatalf("recorded %d timings, want 1", n)
	}
	if n := p.Lookahead.RetriedCount(); n != 0 {
		t.Errorf("RetriedCount = %d, want 0: a second generation is no longer possible", n)
	}
	if got := p.Lookahead.P50(); got != 20*time.Second {
		t.Errorf("recorded %s, want the 20s the one render took", got)
	}
}

// TestPipelineLateGenerationIsDroppedNotAired: a break that finishes after its
// slot must not be handed to the mixer, and must not stop the music.
func TestPipelineLateGenerationIsDroppedNotAired(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	p, _, q := newPipeline(t, clk, 200*time.Second, 6.0)

	for i := 1; i <= 4; i++ {
		p.Announce(Boundary{Index: i, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4),
			InsertionAt: 240, InsertionSample: int64(240 * mix.SampleRate)})
	}
	// Generation starts at t=100 and takes 200s, finishing at 300s for a slot
	// at 240s.
	n, err := p.Tick(context.Background(), 100)
	if err != nil {
		t.Fatalf("a late break is not an error: %v", err)
	}
	if n != 0 {
		t.Errorf("enqueued %d late breaks, want 0", n)
	}
	if q.Pending() != 0 {
		t.Errorf("scheduler holds %d entries after a late generation", q.Pending())
	}
	// And it was still measured: late generations are the ones T exists to price.
	if p.Lookahead.Count() != 1 {
		t.Error("a late generation was not recorded in the timing sample")
	}
}

func TestPipelineExpiredSlotIsDroppedNotGenerated(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	p, s, _ := newPipeline(t, clk, 20*time.Second, 6.0)

	for i := 1; i <= 4; i++ {
		p.Announce(Boundary{Index: i, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4),
			InsertionAt: 240, InsertionSample: int64(240 * mix.SampleRate)})
	}
	if n, err := p.Tick(context.Background(), 300); err != nil || n != 0 {
		t.Fatalf("Tick past the insertion point enqueued %d (err %v)", n, err)
	}
	if len(s.targets) != 0 {
		t.Error("generated a break for a boundary the stream had already passed")
	}
	if p.Pending() != 0 {
		t.Error("expired slot is still pending")
	}
}

func TestPipelinePreferredWindow(t *testing.T) {
	cases := []struct {
		name      string
		cur, next *Track
		want      mix.Placement
		wantSec   float64
	}{
		{"ramp when the intro is long enough", lrcTrack(4, 4), lrcTrack(12, 4), mix.PlacementRamp, 12},
		{"outro when the intro is not", lrcTrack(4, 14), lrcTrack(2, 4), mix.PlacementOutro, 14},
		{"span when neither alone is enough", lrcTrack(4, 2.5), lrcTrack(2.5, 4), mix.PlacementSpan, 8},
		{"between with no usable data", &Track{DurationS: 200}, &Track{DurationS: 200}, mix.PlacementBetween, BetweenWindowSeconds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, sec := PreferredWindow(tc.cur, tc.next, 3)
			if got != tc.want {
				t.Errorf("placement = %v, want %v", got, tc.want)
			}
			if sec != tc.wantSec {
				t.Errorf("window = %v, want %v", sec, tc.wantSec)
			}
		})
	}
}

// TestPipelineGaplessBoundaryNeverGetsASlot ties the two rules together: the
// cadence refuses the boundary, so the break moves to the next one rather than
// being repositioned inside a gapless pair.
func TestPipelineGaplessBoundaryNeverGetsASlot(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	p, _, _ := newPipeline(t, clk, 20*time.Second, 6.0)

	gapless := &Track{RampS: 4, OutroS: 4, DurationS: 200, RampConfidence: enrich.ConfidenceLRC, NoCrossfadeNext: true}
	for i := 1; i <= 4; i++ {
		cur := lrcTrack(4, 4)
		if i == 4 {
			cur = gapless
		}
		if p.Announce(Boundary{Index: i, Cur: cur, Next: lrcTrack(12, 4), InsertionAt: float64(i) * 60}) && i == 4 {
			t.Fatal("gapless boundary 4 was given a break slot")
		}
	}
	if !p.Announce(Boundary{Index: 5, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4), InsertionAt: 300}) {
		t.Error("the slot refused at the gapless boundary never reappeared")
	}
}

// EVERY STATION OWNS ITS OWN QUEUE, so the pipeline cannot be wired to one when
// it is built. Wiring it only where the single legacy runtime was created left
// it nil on every station-based deployment: each break was generated and then
// dropped with "break pipeline has no scheduler queue", the DJ never spoke, and
// nothing else reported a fault. Heard on the live station as music with no
// talking, at a cadence of one.

func TestPipelineQueueCanBeSetAfterConstruction(t *testing.T) {
	p := &Pipeline{Cadence: NewCadence(1), Lookahead: NewLookahead(0)}
	if p.Queue != nil {
		t.Fatal("a fresh pipeline should have no queue")
	}

	q := &sched.Queue{}
	var recorded string
	p.SetQueue(q, func(text string, _ mix.Placement) { recorded = text })

	if p.Queue != q {
		t.Error("SetQueue did not take")
	}
	if p.OnScheduled == nil {
		t.Error("SetQueue did not record the callback")
	}
	p.OnScheduled("hello", mix.Placement(0))
	if recorded != "hello" {
		t.Errorf("callback = %q, want hello", recorded)
	}
}

func TestPipelineQueueSurvivesANilCallback(t *testing.T) {
	// A caller that only wants to move the queue must not clear the recorder.
	q := &sched.Queue{}
	called := false
	p := &Pipeline{Cadence: NewCadence(1), Lookahead: NewLookahead(0)}
	p.SetQueue(q, func(string, mix.Placement) { called = true })
	p.SetQueue(&sched.Queue{}, nil)

	if p.OnScheduled == nil {
		t.Fatal("a nil callback wiped the existing one")
	}
	p.OnScheduled("x", mix.Placement(0))
	if !called {
		t.Error("the original callback was replaced")
	}
}

// A FORWARD REFERENCE HAS TO BE TRUE. This product has no skip precisely so a
// break can safely say what is coming, and the DJ announced Everlong while the
// station played Green Day.
//
// Generation is asynchronous and slow -- 75 seconds, measured live -- while
// boundaries keep arriving. The writer's track context was set at ANNOUNCE time
// and read at GENERATION time, so a later boundary overwrote it first and the
// break described the wrong pair of records.

func TestPipelineWritesAboutItsOwnBoundary(t *testing.T) {
	w := &BreakWriter{}
	// OBSERVED AT WRITE TIME, not afterwards. Generation now starts the moment
	// a slot exists, so several boundaries generate in one tick and the
	// writer's final state is only ever the last one's -- which would say
	// nothing about whether each break was written about its own. This records
	// the context as each write is attempted.
	seen := &contextRecorder{w: w}
	p := &Pipeline{
		Cadence: NewCadence(1), Lookahead: NewLookahead(0), Writer: w,
		Length: LengthCheck{Writer: seen},
	}

	first := Boundary{
		Index: 1, InsertionAt: 200,
		Cur: &Track{DurationS: 200}, Next: &Track{DurationS: 200},
		PrevID: 9, CurID: 1, NextID: 2,
		CurArtist: "Foo Fighters", CurTitle: "Everlong",
		NextArtist: "Green Day", NextTitle: "Basket Case",
	}
	later := Boundary{
		Index: 2, InsertionAt: 400,
		Cur: &Track{DurationS: 200}, Next: &Track{DurationS: 200},
		PrevID: 1, CurID: 3, NextID: 4,
		CurArtist: "Oasis", CurTitle: "Live Forever",
		NextArtist: "Blur", NextTitle: "Song 2",
	}

	// Both announced before either generates, which is what a slow model does.
	p.Announce(first)
	p.Announce(later)

	// Both generate in this one tick. Generation itself fails (there is no
	// model here) and that is fine: the context must already be right by the
	// time writing is attempted, which is what this test is about.
	_, _ = p.Tick(context.Background(), first.InsertionAt-1)

	want := [][2]int64{{1, 2}, {3, 4}}
	if len(seen.pairs) != len(want) {
		t.Fatalf("wrote %d breaks, want %d: %v", len(seen.pairs), len(want), seen.pairs)
	}
	for i, w := range want {
		if seen.pairs[i] != w {
			t.Errorf("break %d wrote about cur=%d next=%d, want %d and %d: a later "+
				"boundary overwrote the context and the DJ announced the wrong record",
				i+1, seen.pairs[i][0], seen.pairs[i][1], w[0], w[1])
		}
	}
}

// contextRecorder refuses to write, and notes which records it was asked about.
type contextRecorder struct {
	w     *BreakWriter
	pairs [][2]int64
}

func (c *contextRecorder) Write(context.Context, int, mix.Placement) (string, error) {
	c.pairs = append(c.pairs, [2]int64{c.w.curID, c.w.nextID})
	return "", errors.New("no model in this test")
}

func TestPipelineContextWithoutAWriterIsHarmless(t *testing.T) {
	// The spike path has no writer at all.
	p := &Pipeline{
		Cadence: NewCadence(1), Lookahead: NewLookahead(0),
		Length: LengthCheck{Writer: refusingWriter{}},
	}
	p.Announce(Boundary{
		Index: 1, InsertionAt: 10, CurID: 1,
		Cur: &Track{DurationS: 10}, Next: &Track{DurationS: 10},
	})
	_, _ = p.Tick(context.Background(), 9)
}

// refusingWriter stands in for the DJ when the test is about something else.
type refusingWriter struct{}

func (refusingWriter) Write(context.Context, int, mix.Placement) (string, error) {
	return "", errors.New("no model in this test")
}

// TestPipelineWriterIsNotRebound: the writer is read on the goroutine that
// GENERATES a break, and used to be assigned on the goroutine that FEEDS the
// station, with nothing between them -- a data race the detector finds in a few
// hundred iterations of this.
//
// The fix is structural rather than a mutex: the writer belongs to the pipeline
// from the moment it is built, so nothing reassigns it and there is no window.
// Run this under -race or it proves nothing.
func TestPipelineWriterIsNotRebound(t *testing.T) {
	p := &Pipeline{Cadence: NewCadence(1), Lookahead: NewLookahead(0), Writer: &BreakWriter{}}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// The FEED goroutine: it announces boundaries as tracks start.
		for i := 0; i < 200; i++ {
			p.Announce(Boundary{Index: i, InsertionAt: float64(i + 5), Cur: &Track{DurationS: 10}})
		}
	}()
	go func() {
		defer wg.Done()
		// The TICKER goroutine: it generates, which reads the writer.
		for i := 0; i < 200; i++ {
			_, _ = p.Tick(context.Background(), float64(i))
			_ = p.Outlook()
		}
	}()
	wg.Wait()
}

// TestPipelineWithNoWriterDeclinesRatherThanPanicking: a nil writer used to
// dereference inside the break goroutine. The panic is caught by a recover that
// stops break generation for the life of the process, so the station would keep
// playing music and never speak again, with one line in the log.
func TestPipelineWithNoWriterDeclinesRatherThanPanicking(t *testing.T) {
	p := &Pipeline{Cadence: NewCadence(1), Lookahead: NewLookahead(0)}
	p.Announce(Boundary{Index: 1, InsertionAt: 100, Cur: &Track{DurationS: 100}})

	n, err := p.Tick(context.Background(), 99)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 0 {
		t.Errorf("scheduled %d breaks with no writer", n)
	}
}

// TestBreakTextLogsTheRecordsItIsAbout: asked for 2026-09-09 so the DJ's words
// can be checked against the records they are supposed to be about.
//
// THIS IS THE ONE FAILURE THE REST OF THE PIPELINE CANNOT SEE. A break can be
// well written, correctly timed, the right length and about the WRONG PAIR --
// every counter green, every test passing, and the DJ naming a record the
// station is not playing. It has happened here: the writer's context was
// shared, later boundaries overwrote it during the seventy-five seconds between
// announce and generation, and the DJ said Everlong was next while Green Day
// played. applyContext fixed the cause; this line is how anybody CHECKS it on a
// running station.
func TestBreakTextLogsTheRecordsItIsAbout(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	var logged bytes.Buffer
	p, s, _ := newPipeline(t, clk, 20*time.Second, 6.0)
	p.Log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo}))
	_ = s

	at := 240.0
	for i := 1; i <= 4; i++ {
		p.Announce(Boundary{
			Index: i, Cur: lrcTrack(4, 4), Next: lrcTrack(12, 4),
			InsertionAt: float64(i) * 60, InsertionSample: int64(float64(i) * 60 * mix.SampleRate),
			PrevArtist: "Blur", PrevTitle: "Song 2",
			CurArtist: "Nine Inch Nails", CurTitle: "Hurt",
			NextArtist: "Green Day", NextTitle: "21 Guns",
		})
	}
	if _, err := p.Tick(context.Background(), at-150); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	out := logged.String()
	if !strings.Contains(out, "break text") {
		t.Fatalf("no break text line was logged:\n%s", out)
	}
	// THE WORDS THEMSELVES, or the line says a break happened and not what it
	// said, which is the whole point of it. This is what the fake writer
	// produces, so the assertion is on the script that actually flowed through
	// rather than on a string the test planted at the other end.
	if !strings.Contains(out, "script for a target of some words") {
		t.Errorf("the line does not carry what the DJ said:\n%s", out)
	}
	// NAMED FOR WHAT A LISTENER HEARS. The break airs at the end of Cur, so the
	// record that just finished is cur and the one starting is next -- reading
	// the struct's Prev as "the song before this break" is exactly the
	// misreading that makes a correct break look wrong.
	for _, want := range []string{
		`just_played="Nine Inch Nails — Hurt"`,
		`coming_up="Green Day — 21 Guns"`,
		`before_that="Blur — Song 2"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the line is missing %s:\n%s", want, out)
		}
	}
}

func TestBreakTextNamesAnAbsentRecordRatherThanLeavingItBlank(t *testing.T) {
	// The first break on a station has no record before the one just played.
	// An empty value in a log line reads as a field that failed to populate.
	for _, c := range []struct{ artist, title, want string }{
		{"Blur", "Song 2", "Blur — Song 2"},
		{"", "", "none"},
		{"", "Song 2", "Song 2"},
		{"Blur", "", "Blur"},
	} {
		if got := song(c.artist, c.title); got != c.want {
			t.Errorf("song(%q, %q) = %q, want %q", c.artist, c.title, got, c.want)
		}
	}
}

// TestOverlapStartsTheBreakBeforeTheBoundary: Jockora-ugw. Placement used to
// set a word budget and a label and never a position, so a break sized for a
// window straddling the transition was spliced entirely after it.
func TestOverlapStartsTheBreakBeforeTheBoundary(t *testing.T) {
	// A four-second outro on the outgoing track and a three-second setting: the
	// break should start three seconds early, not four and not zero.
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	p, _, q := newPipeline(t, clk, 20*time.Second, 6.0)
	p.SetOverlap(3)

	at := 240.0
	for i := 1; i <= 4; i++ {
		p.Announce(Boundary{
			// A LONG OUTRO AND NO USABLE RAMP, so the placement chooser picks
			// the outro rather than the ramp -- a ramp break is about the
			// incoming record and must NOT start early.
			Index: i, Cur: lrcTrack(0, 20), Next: lrcTrack(0, 4),
			InsertionAt: float64(i) * 60, InsertionSample: int64(float64(i) * 60 * mix.SampleRate),
		})
	}
	if _, err := p.Tick(context.Background(), at-150); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	entries := q.DrainDue(1 << 62)
	if len(entries) != 1 {
		t.Fatalf("got %d scheduled entries, want 1", len(entries))
	}
	boundary := int64(at * mix.SampleRate)
	want := boundary - int64(3*mix.SampleRate)
	if entries[0].AfterSample != want {
		t.Errorf("spliced at %d, want %d (%g seconds before the boundary at %d)",
			entries[0].AfterSample, want, 3.0, boundary)
	}
	if entries[0].Placement != "outro" {
		t.Errorf("placement = %q, want outro", entries[0].Placement)
	}

	// AND NO HOLE, WHICH IS CORRECT HERE AND WORTH PINNING. This break is six
	// seconds and the overlap is three at each end, so the two records meet in
	// the middle of it with nothing left over. A hole would mean silence that
	// the break is not long enough to pay for.
	//
	// THE BOUNDARY INDEX MATTERS AS MUCH AS THE NUMBER: a gap filed against the
	// wrong boundary is silence in front of a record nobody is talking over.
	if got := p.TakeGap(4); got != 0 {
		t.Errorf("a %gs break with %gs at each end left a %gs hole; there is "+
			"nothing left of it to hold the music with", 6.0, 3.0, got)
	}
	if got := p.TakeGap(3); got != 0 {
		t.Errorf("boundary 3 claimed a gap of %g, and no break was scheduled there", got)
	}
}

func TestOverlapIgnoresWhatTheTrackIsDoing(t *testing.T) {
	// THE GTA RULE IS OFF, BY INSTRUCTION. The overlap used to be capped by the
	// outgoing track's measured instrumental outro and the incoming track's
	// measured intro, so that a break could never land over singing. The
	// operator has said to disregard that: the configured number applies to
	// every break at both ends, whatever the track does.
	//
	// So a record that sings to its final second gets talked over for three of
	// them, and so does one that starts singing immediately. That is the
	// instruction rather than an oversight, and this test is the record of it.
	// The setting is the only bound left; 0 turns the behaviour off entirely.
	for _, c := range []struct {
		name  string
		track *Track
	}{
		{"a long measured tail", lrcTrack(0, 20)},
		// The case the old cap existed for: one second of instrumental, or
		// none at all, at a confidence the placement code trusts.
		{"one second of tail, measured", lrcTrack(1, 1)},
		{"no tail at all, measured", lrcTrack(0, 0)},
		{"never measured", &Track{DurationS: 200}},
		{"measured but not trusted", &Track{DurationS: 200, OutroS: 20, RampConfidence: enrich.ConfidenceNone}},
		{"no track at all", nil},
	} {
		if got := gapFor(15, 3); got != 9 {
			t.Errorf("%s: gapFor = %g, want 9", c.name, got)
		}
		// The track is not consulted at all any more, which is the assertion:
		// there is no longer a function that takes one.
		_ = c.track
	}
}

func TestGapIsWhatIsLeftOfTheBreak(t *testing.T) {
	// The outgoing record ends one overlap into the break and the incoming one
	// starts one overlap before it finishes, so the hole is the remainder.
	for _, c := range []struct {
		name             string
		seconds, overlap float64
		want             float64
	}{
		{"the ordinary case", 15, 3, 9},
		{"no overlap configured leaves the whole break in the hole", 15, 0, 15},
		{"a wider overlap eats into it", 15, 6, 3},
		// A break shorter than its own two overlaps needs no hole: the records
		// simply overlap it by less than was asked for, because there is not
		// enough break to overlap. Negative silence is not a thing the feed
		// could act on even if it were asked to.
		{"break shorter than its overlaps", 5, 3, 0},
		{"exactly its overlaps", 6, 3, 0},
	} {
		if got := gapFor(c.seconds, c.overlap); got != c.want {
			t.Errorf("%s: gapFor = %g, want %g", c.name, got, c.want)
		}
	}
}

func TestTakeGapAnswersOnceAndOnlyForABreakThatWasScheduled(t *testing.T) {
	p := &Pipeline{}
	// A boundary nobody scheduled a break for is the ordinary case, and it
	// holds the music for exactly no time.
	if got := p.TakeGap(7); got != 0 {
		t.Errorf("an unclaimed boundary gave a gap of %g, want 0", got)
	}

	p.setGap(7, 9)
	if got := p.TakeGap(7); got != 9 {
		t.Errorf("TakeGap = %g, want 9", got)
	}
	// CONSUMED. A feed that asks twice must not hold the music twice.
	if got := p.TakeGap(7); got != 0 {
		t.Errorf("TakeGap answered twice: %g", got)
	}

	// Zero and negative are not recorded at all, so the map does not fill up
	// with boundaries that mean nothing.
	p.setGap(8, 0)
	p.setGap(9, -1)
	if got := p.TakeGap(8) + p.TakeGap(9); got != 0 {
		t.Errorf("an empty gap was recorded: %g", got)
	}

	// A NEW FEED COUNTS BOUNDARIES FROM ZERO. A gap left by the run that just
	// ended would be collected by the next one and hold the music for a break
	// nobody is about to hear.
	p.setGap(3, 9)
	p.SetQueue(nil, nil)
	if got := p.TakeGap(3); got != 0 {
		t.Errorf("a gap survived the feed restart: %g", got)
	}
}

func TestOverlapIsClampedAndReadable(t *testing.T) {
	p := &Pipeline{}
	p.SetOverlap(-1)
	if got := p.Overlap(); got != 0 {
		t.Errorf("a negative overlap was kept as %g; it must clamp to 0", got)
	}
	p.SetOverlap(2.5)
	if got := p.Overlap(); got != 2.5 {
		t.Errorf("Overlap() = %g, want 2.5", got)
	}
}

// TestOverlapDeadlineMovesWithTheLeadIn: found reviewing Jockora-ugw.
//
// The lead-in moved WHERE a break is spliced and left the timing check pointed
// at the boundary, so a break that finished after its own start time still
// passed InTime -- and was then handed to the queue at a sample the mixer had
// already played. The queue refuses that, so the break was deleted and reported
// as "break rejected by the scheduler": a late generation wearing the costume
// of a scheduler fault, on exactly the breaks nearest the edge.
func TestOverlapDeadlineMovesWithTheLeadIn(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	var logged bytes.Buffer
	// Generation takes 8 seconds; the boundary is 10 seconds away; the break
	// starts 3 seconds before it. So it finishes at +8 against a start at +7
	// and is late -- while against the boundary at +10 it would look fine.
	p, _, q := newPipeline(t, clk, 8*time.Second, 6.0)
	p.SetOverlap(3)
	p.Log = slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo}))

	at := 240.0
	for i := 1; i <= 4; i++ {
		p.Announce(Boundary{
			Index: i, Cur: lrcTrack(0, 20), Next: lrcTrack(0, 4),
			InsertionAt: float64(i) * 60, InsertionSample: int64(float64(i) * 60 * mix.SampleRate),
		})
	}
	if _, err := p.Tick(context.Background(), at-10); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := len(q.DrainDue(1 << 62)); got != 0 {
		t.Errorf("%d entries reached the scheduler; a break that cannot start on time "+
			"must be dropped as late, not handed over to be refused", got)
	}
	out := logged.String()
	if !strings.Contains(out, "break generated too late") {
		t.Errorf("the drop was not reported as late generation:\n%s", out)
	}
	// AND THE LINE SAYS HOW MUCH OF THE DEADLINE THE LEAD-IN TOOK, or the next
	// person tuning the lookahead cannot see why the budget shrank.
	if !strings.Contains(out, "lead_in_s=3") {
		t.Errorf("the late line does not carry the lead-in:\n%s", out)
	}
}

// TestPipelineWhatTheListenerActuallyHears: Jockora-ey4, and the assertion the
// first pass at it was missing.
//
// The earlier test proved a gap was CLAIMED and nothing more. A gap of the
// wrong size still passes that, and the wrong size is the whole failure mode:
// the operator asked for three seconds of the outgoing record under the start
// of the break and three seconds of the incoming record under its end, and
// "some gap exists" is not that. This reconstructs the three positions the way
// a listener meets them and measures both overlaps.
func TestPipelineWhatTheListenerActuallyHears(t *testing.T) {
	const (
		boundaryS = 240.0 // where the outgoing record's audio runs out
		breakS    = 15.0  // what the renderer produced
		overlap   = 3.0
	)
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	p, _, q := newPipeline(t, clk, 20*time.Second, breakS)
	p.SetOverlap(overlap)

	for i := 1; i <= 4; i++ {
		p.Announce(Boundary{
			Index: i,
			// NEITHER TRACK IS MEASURED, which is the fixture that matters
			// now. The old rule gave a pair like this no overlap at all -- it
			// is two thirds of the reporting library -- and the operator has
			// said to disregard that rule, so the full three seconds must land
			// on both ends of a break between two records nothing is known
			// about.
			Cur: &Track{DurationS: 200}, Next: &Track{DurationS: 200},
			InsertionAt:     float64(i) * 60,
			InsertionSample: int64(float64(i) * 60 * mix.SampleRate),
		})
	}
	if _, err := p.Tick(context.Background(), boundaryS-150); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	entries := q.DrainDue(1 << 62)
	if len(entries) != 1 {
		t.Fatalf("got %d scheduled entries, want 1", len(entries))
	}

	// The three things a listener meets, all on the mixer's timeline.
	spliceS := float64(entries[0].AfterSample) / mix.SampleRate
	gapS := p.TakeGap(4)
	nextStartsS := boundaryS + gapS
	breakEndsS := spliceS + breakS

	// THE OUTGOING RECORD plays until the boundary, and the DJ starts before
	// it. What overlaps is the distance between those.
	if got := boundaryS - spliceS; got != overlap {
		t.Errorf("the outgoing record overlaps the break by %gs, want %gs\n"+
			"the break is spliced at %gs and the record runs out at %gs",
			got, overlap, spliceS, boundaryS)
	}

	// THE INCOMING RECORD starts once the hole closes, and the DJ is still
	// talking. What overlaps is the distance between those.
	if got := breakEndsS - nextStartsS; got != overlap {
		t.Errorf("the break overlaps the incoming record by %gs, want %gs\n"+
			"the break ends at %gs and the record starts at %gs (a %gs hole "+
			"after the boundary at %gs)",
			got, overlap, breakEndsS, nextStartsS, gapS, boundaryS)
	}

	// AND THE HOLE IS THE REMAINDER, which is the number an operator hears as
	// silence and the one worth naming in a failure.
	if want := breakS - 2*overlap; gapS != want {
		t.Errorf("the music pauses for %gs, want %gs", gapS, want)
	}

	// NOTHING IS PLAYED TWICE AND NOTHING IS SKIPPED: the outgoing record ends
	// exactly where the hole begins, and the incoming one begins exactly where
	// it ends. Stated as a total because that is the property that breaks if
	// any one of the three terms drifts.
	if got := (boundaryS - spliceS) + gapS + (breakEndsS - nextStartsS); got != breakS {
		t.Errorf("the three pieces sum to %gs of break, but the audio is %gs; "+
			"the record and the hole disagree about where the boundary is", got, breakS)
	}
}
