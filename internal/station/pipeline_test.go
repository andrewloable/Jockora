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
