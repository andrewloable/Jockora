// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package app assembles the spine into a running program: decoders feeding a
// ring, one mixer goroutine writing to a supervised ffmpeg, and an HTTP server
// handing the result to listeners.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"database/sql"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/decode"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/encode"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/library"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/obs"
	"github.com/andrewloable/jockora/internal/sched"
	"github.com/andrewloable/jockora/internal/server"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// Options are the run-specific choices the config file does not cover.
type Options struct {
	Tracks     []string // music files, played in order and then looped
	BreakPath  string   // a WAV to splice, optional
	BreakAtSec int      // when the first one should be heard, seconds from start

	// BreakEverySec repeats the break at this interval. Zero means one only.
	//
	// A single short break is very hard to catch by ear: HLS runs 12-18 seconds
	// behind live, so a listener who presses play at the wrong moment simply
	// never hears it and cannot tell that from a broken splice. Repeating it is
	// what makes the seam actually observable.
	BreakEverySec int
	Log           *slog.Logger

	// Personas is the roster the dial matches jocks from. Optional: with no
	// personas the dial still proposes stations, they simply carry no jock.
	Personas []*dj.Persona

	// Analyser measures loudness and tempo in the background. Optional, and
	// nil leaves both unmeasured -- which is what every library was until it
	// existed: 0 of 7,595 tracks had a loudness value, so every one played at
	// unity gain and the -16 LUFS contract was enforced for speech only.
	Analyser *library.Analyser

	// Library, when set, replaces Tracks as the source of music: the app
	// selects from the scanned database instead of looping a list of files.
	//
	// Optional on purpose. The spike path is what GATE 2 and the room test run
	// against, and it must keep working with no database at all.
	Library *Library

	// Breaks, when set, generates and airs DJ breaks. Optional for the same
	// reason, and independently: a library without a language model is a
	// perfectly good shuffle, and that is the state Jockora must degrade to
	// whenever the model or the sidecar is not there.
	Breaks *station.Pipeline

	// Writer is told which tracks surround each break. Set with Breaks.
	Writer *station.BreakWriter

	// Enricher runs dossier generation in the background. Optional.
	Enricher *enrich.Queue
}

// Library is the scanned music source.
type Library struct {
	Store    *store.Store
	Selector *station.Selector
}

// track is one item chosen for the broadcast.
type track struct {
	id            int64
	path          string
	artist, title string
	// album is carried for one reason: it is a PROPER NOUN OF THE RECORD, so
	// saying it is announcing the track, not reusing a phrase.
	album string
	// noCrossfadeNext marks gapless material, which the cadence refuses to
	// interrupt.
	noCrossfadeNext bool
}

// next chooses the following track from the database.
func (l *Library) next(ctx context.Context) (track, error) {
	id, err := l.Selector.Next()
	if err != nil {
		return track{}, err
	}
	var t = track{id: id}
	var artist, title, album sql.NullString
	var gapless int
	err = l.Store.DB().QueryRowContext(ctx,
		`SELECT path, artist, title, album, no_crossfade_next FROM tracks WHERE id = ?`, id).
		Scan(&t.path, &artist, &title, &album, &gapless)
	if err != nil {
		return track{}, fmt.Errorf("reading track %d: %w", id, err)
	}
	t.artist, t.title, t.album, t.noCrossfadeNext = artist.String, title.String, album.String, gapless != 0
	return t, nil
}

// applyTuning pushes the operator's knobs into the packages that read them.
//
// Every value defaults to what the project was measured with, so an operator
// who sets nothing gets exactly the behaviour every gate was run against. Only
// a value that DIFFERS from its default is logged, because a startup line
// listing eight unchanged numbers teaches nobody anything.
func applyTuning(cfg *config.Config, log *slog.Logger) {
	mix.SetLoudness(cfg.MusicLUFS, cfg.MusicDuckedLUFS, cfg.SpeechLUFS, cfg.TruePeakCeiling)
	if cfg.MusicLUFS != 0 && cfg.MusicLUFS != mix.DefaultMusicLUFS {
		log.Info("loudness contract changed", "music_lufs", mix.MusicLUFS,
			"ducked_lufs", mix.MusicDuckedLUFS, "duck_depth_db", mix.DuckDepthDB)
	}
	if cfg.LookaheadSeconds > 0 {
		d := time.Duration(cfg.LookaheadSeconds * float64(time.Second))
		if d != station.DefaultLookahead {
			log.Info("lookahead changed", "seconds", cfg.LookaheadSeconds)
		}
		station.DefaultLookahead = d
	}
	if cfg.AdEveryNBreaks != 0 && cfg.AdEveryNBreaks != station.AdEveryNBreaks {
		log.Info("advert frequency changed", "one_slot_in", cfg.AdEveryNBreaks)
		station.AdEveryNBreaks = cfg.AdEveryNBreaks
	}
	if cfg.AdIntervalMin > 0 {
		d := time.Duration(cfg.AdIntervalMin * float64(time.Minute))
		if d != station.MinAdInterval {
			log.Info("advert interval changed", "minutes", cfg.AdIntervalMin)
		}
		station.MinAdInterval = d
	}
	if cfg.FactConfidence > 0 && cfg.FactConfidence != dj.FactConfidenceThreshold {
		log.Info("fact confidence threshold changed", "threshold", cfg.FactConfidence,
			"effect", "lower means a chattier DJ leaning on weaker dossiers")
		dj.FactConfidenceThreshold = cfg.FactConfidence
	}
}

// DialRefreshInterval is how often the dial is recomputed.
//
// Five minutes: enrichment classifies a few tracks a minute at best, so
// anything faster re-reads the whole dossier table to learn nothing, and
// anything slower leaves a new station invisible for most of an evening.
const DialRefreshInterval = 5 * time.Minute

// cachedDial holds the last computed dial and recomputes it on a timer.
type cachedDial struct {
	store    *store.Store
	personas []*dj.Persona
	log      *slog.Logger

	mu sync.RWMutex
	d  station.Dial
}

func (c *cachedDial) Dial() any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.d
}

func (c *cachedDial) refresh(ctx context.Context) {
	dial, err := station.ProposeDial(ctx, c.store, c.personas)
	if err != nil {
		// Not fatal, and the previous dial is kept: a stale dial is far better
		// than an empty one. A dial is a nicety; the stream is not.
		c.log.Warn("could not propose a dial", "err", err)
		return
	}

	c.mu.Lock()
	was := len(c.d.Stations)
	c.d = dial
	c.mu.Unlock()

	if was != len(dial.Stations) {
		c.log.Info("dial proposed", "stations", len(dial.Stations),
			"enriched", dial.Enriched, "of", dial.Total)
	}
}

// watch recomputes the dial until the context ends.
func (c *cachedDial) watch(ctx context.Context) {
	t := time.NewTicker(DialRefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.refresh(ctx)
		}
	}
}

// App is one running station.
type App struct {
	cfg  *config.Config
	opts Options
	log  *slog.Logger

	ring    *mix.Ring
	queue   *sched.Queue
	srv     *server.Server
	sup     *encode.Supervisor
	metrics mix.Metrics

	// stallUntil pauses the feeder, for the fault injection GATE 2 requires.
	// Guarded because the signal handler and the feeder are different goroutines.
	stallMu    sync.Mutex
	stallUntil time.Time

	// nextBreak is the sample the next repeating break is owed at. The spike
	// schedules one window at startup and topUpBreaks continues from here.
	nextBreak int64

	nowMu               sync.Mutex
	nowArtist, nowTitle string
	lastBreak           *server.LastBreak

	mixerMu sync.Mutex
	mixer   *mix.Mixer

	dial *cachedDial
}

// ringSeconds is how much decoded audio is buffered ahead of the mixer. Enough
// to ride out a slow disk or a decoder starting up, small enough that a track
// change is not delayed by a wall of buffered audio.
const ringSeconds = 10

// scheduleAhead bounds how far ahead repeating breaks are queued.
const scheduleAhead = 2 * time.Hour

// New validates everything that can be validated before anything starts, then
// opens the listener and spawns ffmpeg.
//
// Failures that would only appear at airtime are checked here instead: a missing
// break file, no tracks, a non-loopback listen address.
func New(cfg *config.Config, opts Options) (*App, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	// One source or the other. A Library carries its own tracks and they are
	// not stat-ed here: the scan already refused what ffprobe could not read,
	// and re-checking ten thousand paths at startup would add minutes to a boot
	// for a question already answered.
	switch {
	case opts.Library != nil:
		if opts.Library.Store == nil || opts.Library.Selector == nil {
			return nil, errors.New("app: the library has no store or no selector")
		}
	case len(opts.Tracks) == 0:
		return nil, errors.New("app: no tracks to play")
	default:
		for _, p := range opts.Tracks {
			if _, err := os.Stat(p); err != nil {
				return nil, fmt.Errorf("app: track %s: %w", p, err)
			}
		}
	}

	// The pipeline enqueues into the SAME queue the mixer drains, and it is
	// wired here rather than by the caller because there is no way for the
	// caller to have it: the queue is created below. Leaving it to be passed in
	// produced a nil-pointer panic in the break goroutine that took the whole
	// station off air.
	queue := &sched.Queue{}

	a := &App{
		cfg:   cfg,
		opts:  opts,
		log:   log,
		ring:  mix.NewRing(ringSeconds * mix.SampleRate),
		queue: queue,
	}

	// The pipeline enqueues into the SAME queue the mixer drains, and reports
	// what it scheduled. Wired HERE rather than by the caller, because the
	// caller cannot have the queue -- it is created above. Leaving it to be
	// passed in produced a nil-pointer panic in the break goroutine that took
	// the whole station off air.
	if opts.Breaks != nil {
		opts.Breaks.Queue = queue
		opts.Breaks.OnScheduled = a.recordBreak
	}

	if opts.BreakPath != "" {
		// Read it now rather than at airtime. A break that cannot be read is a
		// dropped break, and finding that out mid-stream is much harder to
		// diagnose than refusing to start.
		if _, err := mix.ReadWAV(opts.BreakPath); err != nil {
			return nil, fmt.Errorf("app: break audio: %w", err)
		}
		every := opts.BreakEverySec
		count := 1
		if every > 0 {
			// One window's worth now; topUpBreaks keeps it filled from here.
			count = int(scheduleAhead.Seconds()) / every
		}
		for i := 0; i < count; i++ {
			at := int64(opts.BreakAtSec+i*every) * mix.SampleRate
			if err := a.queue.Enqueue(sched.Entry{
				AfterSample: at,
				Action:      sched.ActionSpliceAudio,
				Path:        opts.BreakPath,
				Placement:   mix.PlacementBetween.String(),
			}); err != nil {
				return nil, fmt.Errorf("app: scheduling break: %w", err)
			}
			a.nextBreak = at + int64(every)*mix.SampleRate
		}
	}

	// APPLIED ONCE, HERE, before the mixer, the encoder or any goroutine
	// exists. These are read on the audio path without synchronisation, so
	// this is the only safe moment to change them.
	applyTuning(cfg, log)

	srv, err := server.New(server.Config{
		ListenAddr:       cfg.ListenAddr,
		SegmentDir:       cfg.SegmentDir,
		AllowNonLoopback: cfg.AllowLAN,
	}, log)
	if err != nil {
		return nil, err
	}
	a.srv = srv
	srv.SetStatusSource(a)
	srv.SetTuner(a)

	// The dial is cached and REFRESHED PERIODICALLY, never computed per
	// request: it reads every dossier in the library, which is fine every few
	// minutes and absurd on every poll of /stations.json.
	//
	// Refreshing matters more than it looks. On a fresh library every track
	// starts in the catch-all and stations only appear as enrichment
	// classifies them -- which takes days. A dial computed once at startup
	// would show "unsorted 7595" and nothing else until the operator happened
	// to restart, and the dial is the PRIMARY UI.
	if opts.Library != nil && opts.Library.Store != nil {
		a.dial = &cachedDial{store: opts.Library.Store, personas: opts.Personas, log: log}
		a.dial.refresh(context.Background())
		srv.SetDialSource(a.dial)
	}

	// The encoder comes up before the mixer, so the mixer never writes into a
	// pipe that does not exist yet.
	sup, err := encode.StartSupervisor(context.Background(), encode.Config{
		SegmentDir:     cfg.SegmentDir,
		SegmentSeconds: cfg.SegmentSeconds,
		ListSize:       cfg.ListSize,
		SampleRate:     cfg.SampleRate,
		Channels:       cfg.Channels,
	}, log)
	if err != nil {
		return nil, err
	}
	a.sup = sup

	return a, nil
}

// dependencyHealth says whether an optional dependency is in use.
//
// "not configured" is a real and common answer here: a library with no model is
// a working shuffle, and reporting that as a fault would send an operator
// looking for a problem they chose.
func dependencyHealth(inUse bool) string {
	if inUse {
		return "ok"
	}
	return "not configured"
}

// recordBreak remembers what the DJ last said, for /now.json and the page.
func (a *App) recordBreak(text string, placement mix.Placement) {
	a.nowMu.Lock()
	a.lastBreak = &server.LastBreak{
		Text:      text,
		Placement: placement.String(),
		AiredAt:   time.Now().Unix(),
	}
	a.nowMu.Unlock()

	if a.opts.Writer != nil {
		// Clears the cold open: the next break is no longer the first thing a
		// listener hears after pressing play.
		a.opts.Writer.Aired()
	}
}

// Addr is the address the server is listening on.
func (a *App) Addr() string { return a.srv.Addr() }

// PendingBreaks reports how many scheduled items have not aired yet.
func (a *App) PendingBreaks() int { return a.queue.Pending() }

// Metrics reports the run so far: inter-write gaps, ring occupancy, write count.
func (a *App) Metrics() mix.MetricsSnapshot { return a.metrics.Snapshot() }

// Status implements server.StatusSource, feeding /now.json.
//
// Everything here is a cached counter read. The page polls this every few
// seconds and a status endpoint that costs real work is one nobody can afford
// to look at.
//
// Fields the spike genuinely does not know are left EMPTY rather than filled
// with plausible-looking values: a status endpoint that invents its answers is
// worse than one that admits what it cannot see.
// ffmpegHealth turns the supervisor's degraded reason into a health string.
func ffmpegHealth(degraded string) string {
	if degraded == "" {
		return "ok"
	}
	return degraded
}

func (a *App) Status() server.Status {
	m := a.metrics.Snapshot()

	st := server.Status{
		Health: server.Health{
			// ffmpeg is proven working by the fact that segments exist at all,
			// and the supervisor reports restarts separately -- EXCEPT for the
			// one failure that neither of those makes visible. A full disk
			// keeps the mixer running and the restart counter climbing while
			// no new segment is ever written, which reads as healthy right up
			// until a listener says the stream stopped.
			FFmpeg: ffmpegHealth(a.sup.Degraded()),
			// Reported by what is actually WIRED, not by what the spike used
			// to do. These read "not used by the spike" long after both were
			// in use, which is a status endpoint inventing an answer.
			LLM: dependencyHealth(a.opts.Breaks != nil || a.opts.Enricher != nil),
			TTS: dependencyHealth(a.opts.Breaks != nil),
		},
		Metrics: server.Metrics{
			RingOccupancyS:    a.ring.Occupancy().Seconds(),
			Underruns:         a.ring.UnderrunCount(),
			EncoderRestarts:   a.sup.Restarts(),
			P99GapMs:          float64(m.P99Gap.Microseconds()) / 1000,
			MaxGapMs:          float64(m.MaxGap.Microseconds()) / 1000,
			MinRingOccupancyS: m.MinOccupancy.Seconds(),
			DriftMs:           float64(a.drift().Microseconds()) / 1000,
		},
	}

	a.nowMu.Lock()
	if a.nowArtist != "" || a.nowTitle != "" {
		st.Now = &server.Track{Artist: a.nowArtist, Title: a.nowTitle}
	}
	st.LastBreak = a.lastBreak
	a.nowMu.Unlock()

	return st
}

// drift reports how far behind schedule the mixer is, or zero before it starts.
func (a *App) drift() time.Duration {
	a.mixerMu.Lock()
	defer a.mixerMu.Unlock()
	if a.mixer == nil {
		return 0
	}
	return a.mixer.Pacer.Drift()
}

// P99Gap and MaxGap expose the pacing tail for a soak harness.
func (a *App) P99Gap() time.Duration { return a.metrics.Snapshot().P99Gap }
func (a *App) MaxGap() time.Duration { return a.metrics.Snapshot().MaxGap }

// MinRingOccupancy is the lowest the ring has been all run.
func (a *App) MinRingOccupancy() time.Duration { return a.metrics.Snapshot().MinOccupancy }

// StallFeeder pauses decoding for d, leaving the mixer to ride it out.
//
// This is fault injection, not a feature. It stalls the DECODER on purpose so
// the safety net can be seen firing: the ring drains, silence-fill covers it,
// UnderrunCount rises, and the stream keeps playing. A stall that ends the
// stream is a failure. Nothing stalls the mixer itself.
func (a *App) StallFeeder(d time.Duration) {
	a.stallMu.Lock()
	defer a.stallMu.Unlock()
	a.stallUntil = time.Now().Add(d)
	a.log.Warn("FAULT INJECTION: stalling the decoder", "for", d)
}

// feederStall reports how much longer the feeder should stay paused.
func (a *App) feederStall() time.Duration {
	a.stallMu.Lock()
	defer a.stallMu.Unlock()
	return time.Until(a.stallUntil)
}

// Run streams until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	defer a.sup.Stop()

	go func() {
		if err := a.srv.Run(ctx); err != nil && ctx.Err() == nil {
			a.log.Error("http server stopped", "err", err)
		}
	}()

	go a.feedTracks(ctx)

	// Enrichment runs alongside the stream, never in front of it. A library of
	// ten thousand tracks takes many hours to enrich and the station has to be
	// listenable from the first minute -- an unenriched track simply gets
	// personality-only talk, which is what the empty dossier means everywhere
	// else in this design.
	if a.opts.Breaks != nil {
		go a.generateBreaks(ctx)
	}

	// Keep the spike's repeating break schedule filled.
	//
	// It used to be enqueued ONCE at startup, covering scheduleAhead and no
	// further. A station left running simply went quiet after two hours, with
	// no log line and every health check still green -- which is precisely the
	// quiet degradation this program is least able to notice, and it happened
	// on the deployed station for three hours before anyone asked.
	if a.opts.BreakPath != "" && a.opts.BreakEverySec > 0 {
		go a.topUpBreaks(ctx)
	}

	if a.dial != nil {
		go a.dial.watch(ctx)
	}

	if a.opts.Analyser != nil {
		go func() {
			// Loudness and tempo, in the background beside enrichment. Slow on
			// purpose -- a decode per track -- and it shares a machine with a
			// station that must not stutter.
			if err := a.opts.Analyser.Run(ctx); err != nil && ctx.Err() == nil {
				a.log.Warn("track analysis stopped", "err", err)
			}
		}()
	}

	if a.opts.Enricher != nil {
		go func() {
			if err := a.opts.Enricher.Run(ctx); err != nil && ctx.Err() == nil {
				// Not fatal, and deliberately not retried in a loop here: the
				// worker already survives one bad track, and a failure that
				// reaches this line means the model or the lock is wrong,
				// which is an operator problem, not a stream problem.
				a.log.Error("enrichment stopped", "err", err)
			}
		}()
	}

	a.log.Info("on air",
		"listen", "http://"+a.Addr(),
		"segments", a.cfg.SegmentDir,
		"tracks", a.trackCount(),
		"source", a.sourceName(),
		"breaks", a.opts.Breaks != nil,
		"enriching", a.opts.Enricher != nil)

	// One mixer goroutine, owning the bus clock, sole writer to the encoder.
	m := &mix.Mixer{
		Ring:    a.ring,
		Queue:   a.queue,
		Chain:   mix.NewChain(),
		Pacer:   mix.NewPacer(clock.Real{}),
		W:       a.sup.Writer(),
		Log:     a.log,
		Metrics: &a.metrics,
	}
	a.mixerMu.Lock()
	a.mixer = m
	a.mixerMu.Unlock()

	err := m.Run(ctx)

	// The four numbers GATE 2 asserts on, logged at shutdown so a long
	// unattended run needs no extra tooling to be judged.
	s := a.metrics.Snapshot()
	a.log.Info("off air",
		"frames", m.SamplePos(),
		"audio", mix.FramesToDuration(int(m.SamplePos())).Round(time.Second),
		"p99_inter_write_gap", s.P99Gap.Round(time.Millisecond),
		"max_inter_write_gap", s.MaxGap.Round(time.Millisecond),
		"min_ring_occupancy", s.MinOccupancy.Round(time.Millisecond),
		"final_drift", m.Pacer.Drift().Round(time.Millisecond),
		"underruns", a.ring.UnderrunCount(),
		"encoder_restarts", a.sup.Restarts())

	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// feedTracks decodes the track list into the ring, looping forever. A track that
// will not decode is logged and skipped: one bad file must not end the stream.
func (a *App) feedTracks(ctx context.Context) {
	if a.opts.Library != nil {
		a.feedFromLibrary(ctx)
		return
	}
	a.feedFromList(ctx)
}

// feedFromLibrary plays the scanned database and schedules breaks between
// tracks.
//
// Every part of the break machinery is optional here and each degrades on its
// own: no pipeline is a shuffle, a failed selection is one skipped track, a
// dropped break is silence where speech would have been. None of them stops the
// music, which is the invariant this whole program is arranged around.
func (a *App) feedFromLibrary(ctx context.Context) {
	blocks := make(chan []mix.Frame, 8)
	var boundary int
	var prev, cur track

	// One track is chosen AHEAD of the one playing. Without it the boundary is
	// only known at the instant it arrives, which leaves the lookahead no time
	// at all -- ShouldTrigger requires now < insertionAt, so a break announced
	// at its own boundary can never be generated and none would ever air.
	upcoming, err := a.opts.Library.next(ctx)
	if err != nil {
		a.log.Error("could not choose a first track", "err", err)
		return
	}

	for ctx.Err() == nil {
		cur = upcoming
		var chooseErr error
		upcoming, chooseErr = a.opts.Library.next(ctx)
		if chooseErr != nil {
			a.log.Error("could not choose the next track", "err", chooseErr)
			upcoming = track{}
		}

		// The boundary at the END of the track now starting. Announced here,
		// a whole track early, which is exactly the room the lookahead needs.
		if upcoming.path != "" {
			boundary++
			a.announceBoundary(ctx, boundary, prev, cur, upcoming)
		}

		done := a.startFeeder(ctx, blocks)
		a.setNowPlayingTrack(cur)
		decodeErr := decode.Decode(ctx, cur.path, blocks)
		blocks = a.finishFeeder(ctx, blocks, done)

		if decodeErr != nil {
			if ctx.Err() != nil {
				return
			}
			obs.New(a.log).DecoderSkip(cur.path, decodeErr.Error())
			a.markUnplayable(ctx, cur)
			continue
		}
		prev = cur
	}
}

// generateBreaks polls the pipeline so a slot announced a track ago is acted on
// when its lookahead finally fires.
//
// A timer rather than the boundary loop, because those are the same thing only
// on a station whose tracks are all shorter than T. Announce decides WHICH
// boundary; this decides WHEN, and they are minutes apart by design.
func (a *App) generateBreaks(ctx context.Context) {
	// The break machinery must NEVER take the station off air, and a panic in
	// any goroutine ends the process regardless of where it happened. The mixer
	// has the same guard for the same reason; this one exists because a nil
	// queue here already did exactly that once.
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("break generation panicked and has been stopped; music continues",
				"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()

	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if _, err := a.opts.Breaks.Tick(ctx, a.secondsPlayed()); err != nil && ctx.Err() == nil {
				a.log.Warn("break generation failed", "err", err)
			}
		}
	}
}

// announceBoundary offers one transition to the cadence and lets the pipeline
// generate anything it decides to.
func (a *App) announceBoundary(ctx context.Context, boundary int, prev, cur, next track) {
	if a.opts.Breaks == nil {
		return
	}
	if a.opts.Writer != nil {
		a.opts.Writer.SetContext(prev.id, cur.id, next.id, prev.artist, prev.title)
		// Every name in play at this boundary. Repeating these is not
		// repetition, it is announcing the record.
		// The ALBUM is in here too. Dossier.Release names it, so every track
		// from one album ends up saying the same words -- measured, that
		// doubled the collision count from 9 to 17 and cost ten breaks that
		// would otherwise have aired. Naming the record you are playing is the
		// job; the collision index is for reused PHRASING.
		a.opts.Writer.SetTrackNames(
			prev.artist, prev.title, prev.album,
			cur.artist, cur.title, cur.album,
			next.artist, next.title, next.album,
		)
	}

	curTrack := a.trackFor(ctx, cur)
	nextTrack := a.trackFor(ctx, next)

	// The boundary is when the track now STARTING will end, not now. Measured
	// from the mixer's position rather than the wall clock, because that is the
	// clock the break will be spliced against.
	at := a.secondsPlayed() + curTrack.DurationS
	a.opts.Breaks.Announce(station.Boundary{
		Index:           boundary,
		Cur:             curTrack,
		Next:            nextTrack,
		InsertionAt:     at,
		InsertionSample: a.samplePos() + int64(curTrack.DurationS*mix.SampleRate),
	})
}

// trackFor reads the placement numbers a break needs about one track.
func (a *App) trackFor(ctx context.Context, t track) *station.Track {
	out := &station.Track{NoCrossfadeNext: t.noCrossfadeNext}
	if t.id == 0 || a.opts.Library == nil {
		return out
	}
	var ramp, outro, duration sql.NullFloat64
	var confidence sql.NullString
	err := a.opts.Library.Store.DB().QueryRowContext(ctx,
		`SELECT ramp_s, outro_s, duration_s, ramp_confidence FROM tracks WHERE id = ?`, t.id).
		Scan(&ramp, &outro, &duration, &confidence)
	if err != nil {
		// No placement data is a normal state mid-enrichment. It means the
		// break goes between the tracks rather than over an intro.
		return out
	}
	out.RampS, out.OutroS, out.DurationS = ramp.Float64, outro.Float64, duration.Float64
	out.RampConfidence = confidence.String
	return out
}

// markUnplayable records a track the decoder could not read, so selection never
// offers it again. This is the play-time half of the scan-time check.
func (a *App) markUnplayable(ctx context.Context, t track) {
	if t.id == 0 || a.opts.Library == nil {
		return
	}
	if _, err := a.opts.Library.Store.DB().ExecContext(ctx,
		`UPDATE tracks SET playable = 0 WHERE id = ?`, t.id); err != nil {
		a.log.Warn("could not mark a track unplayable", "id", t.id, "err", err)
	}
}

// feedFromList is the spike path: a fixed list of files, looped.
//
// Kept because GATE 2 and room test A run against it, and because it is the one
// mode that needs no database, no model and no sidecar.
func (a *App) feedFromList(ctx context.Context) {
	blocks := make(chan []mix.Frame, 8)

	for ctx.Err() == nil {
		played := 0
		for _, path := range a.opts.Tracks {
			if ctx.Err() != nil {
				return
			}

			done := a.startFeeder(ctx, blocks)
			a.setNowPlaying(path)
			err := decode.Decode(ctx, path, blocks)
			blocks = a.finishFeeder(ctx, blocks, done)

			if err != nil {
				if ctx.Err() != nil {
					return
				}
				obs.New(a.log).DecoderSkip(path, err.Error())
				continue
			}
			played++
		}
		if played == 0 {
			a.log.Error("no track in the list could be decoded; the stream will be silence")
			return
		}
	}
}

// startFeeder moves decoded blocks into the ring until the channel closes.
func (a *App) startFeeder(ctx context.Context, blocks chan []mix.Frame) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for b := range blocks {
			// Honour an injected stall here, between decode and the ring, so
			// the decoder is what pauses and the mixer is left to ride it out
			// on silence-fill.
			for d := a.feederStall(); d > 0; d = a.feederStall() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(min(d, 100*time.Millisecond)):
				}
			}
			if err := feedRing(ctx, a.ring, b); err != nil {
				return
			}
		}
	}()
	return done
}

// finishFeeder drains the current track and returns a fresh channel.
func (a *App) finishFeeder(ctx context.Context, blocks chan []mix.Frame, done chan struct{}) chan []mix.Frame {
	// The consumer must finish before the next track reuses the channel.
	for len(blocks) > 0 && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	close(blocks)
	<-done
	return make(chan []mix.Frame, 8)
}

// setNowPlayingTrack reports a track the library knows the tags for.
func (a *App) setNowPlayingTrack(t track) {
	a.nowMu.Lock()
	defer a.nowMu.Unlock()
	a.nowArtist, a.nowTitle = t.artist, t.title
	if a.nowTitle == "" {
		// An untagged file still has to be called something, and its filename
		// is the only honest answer. Never the full path: the status endpoint
		// must not leak the library layout.
		a.nowTitle = strings.TrimSuffix(filepath.Base(t.path), filepath.Ext(t.path))
	}
}

// secondsPlayed is the mixer's position on the bus clock, in seconds.
func (a *App) secondsPlayed() float64 {
	return float64(a.samplePos()) / mix.SampleRate
}

// samplePos is the mixer's absolute bus position, or zero before it starts.
func (a *App) samplePos() int64 {
	a.mixerMu.Lock()
	defer a.mixerMu.Unlock()
	if a.mixer == nil {
		return 0
	}
	return a.mixer.SamplePos()
}

// trackCount is how many tracks are available to play.
func (a *App) trackCount() int {
	if a.opts.Library != nil {
		return a.opts.Library.Selector.Len()
	}
	return len(a.opts.Tracks)
}

// sourceName says which of the two playback paths is running, because "why is
// it playing the same five files" is otherwise answered by reading the code.
func (a *App) sourceName() string {
	if a.opts.Library != nil {
		return "library"
	}
	return "file list"
}

// topUpBreaks keeps the repeating spike break schedule ahead of the mixer.
//
// The queue rejects anything already played past, so this only ever adds
// entries in the future; it can be called as often as it likes without
// double-booking a boundary.
func (a *App) topUpBreaks(ctx context.Context) {
	every := int64(a.opts.BreakEverySec) * mix.SampleRate
	if every <= 0 {
		return
	}

	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		horizon := a.samplePos() + int64(scheduleAhead.Seconds())*mix.SampleRate
		if added := a.topUpOnce(horizon, every); added > 0 {
			a.log.Info("break schedule topped up", "added", added,
				"through_seconds", float64(a.nextBreak)/mix.SampleRate)
		}
	}
}

// topUpOnce enqueues every repeating break owed before horizon, and reports how
// many were added.
//
// Separated from the ticker so the arithmetic can be tested without starting a
// mixer: this is the loop that silently stopped filling and took the DJ off a
// live station for three hours.
func (a *App) topUpOnce(horizon, every int64) int {
	if every <= 0 {
		return 0
	}
	added := 0
	for a.nextBreak < horizon {
		if err := a.queue.Enqueue(sched.Entry{
			AfterSample: a.nextBreak,
			Action:      sched.ActionSpliceAudio,
			Path:        a.opts.BreakPath,
			Placement:   mix.PlacementBetween.String(),
		}); err != nil {
			// Already played past. Skip it rather than retrying forever.
			a.log.Debug("break top-up skipped an elapsed slot", "sample", a.nextBreak, "err", err)
		} else {
			added++
		}
		a.nextBreak += every
	}
	return added
}

// setNowPlaying records what is being decoded, for /now.json.
//
// The spike is handed file paths rather than a scanned library, so the file name
// is all it honestly knows. It is deliberately NOT reported as a filesystem path
// -- the status endpoint must not leak the library layout.
func (a *App) setNowPlaying(path string) {
	a.nowMu.Lock()
	defer a.nowMu.Unlock()
	a.nowArtist = ""
	a.nowTitle = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// feedRing writes every frame into the ring, retrying while it is full.
//
// Ring.Write returns short when there is no space, by design. Treating a short
// write as complete would silently drop audio, which is the kind of bug that
// only shows up as an occasional glitch nobody can reproduce.
func feedRing(ctx context.Context, ring *mix.Ring, frames []mix.Frame) error {
	for len(frames) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := ring.Write(frames)
		frames = frames[n:]
		if n == 0 {
			// Full. The mixer drains at real-time speed, so a short wait is
			// exactly the right amount of patience.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	return nil
}

// encoderPID exposes the running ffmpeg so tests can prove it was reaped.
func (a *App) encoderPID() int { return a.sup.PID() }

// Tune switches the station to a tag, and reports how many tracks it holds.
//
// The current track keeps playing: the mixer has already buffered it, and
// cutting audio mid-song to honour a click is the stutter this design exists to
// avoid. The change is heard at the next boundary, which is also how a real
// radio behaves when you turn the dial slowly.
func (a *App) Tune(tag string) (int, error) {
	if a.opts.Library == nil || a.opts.Library.Store == nil || a.opts.Library.Selector == nil {
		return 0, fmt.Errorf("no library to tune")
	}
	pool, err := station.PoolForTag(context.Background(), a.opts.Library.Store, tag)
	if err != nil {
		return 0, err
	}
	if err := a.opts.Library.Selector.Tune(pool); err != nil {
		return 0, fmt.Errorf("station %q has no playable tracks", tag)
	}
	a.log.Info("tuned", "station", tag, "tracks", len(pool))
	return len(pool), nil
}

// Feedback records what a listener thought of the break that just aired.
//
// It stores the TEXT rather than an id, because a break is not a row: it is
// written, aired and gone. What a later prompt-tuning pass needs is the
// sentence somebody disliked, not a reference to something no longer there.
func (a *App) Feedback(verdict string) error {
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return fmt.Errorf("no store to record feedback in")
	}
	a.nowMu.Lock()
	last := a.lastBreak
	a.nowMu.Unlock()

	if last == nil || last.Text == "" {
		return fmt.Errorf("no break has aired yet")
	}
	jockID := ""
	if a.opts.Writer != nil && a.opts.Writer.Persona != nil {
		jockID = a.opts.Writer.Persona.ID()
	}
	_, err := a.opts.Library.Store.DB().ExecContext(context.Background(),
		`INSERT INTO break_feedback (jock_id, text, verdict, aired_at, at) VALUES (?, ?, ?, ?, ?)`,
		jockID, last.Text, verdict, last.AiredAt, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("recording feedback: %w", err)
	}
	a.log.Info("break feedback", "verdict", verdict, "text", last.Text)
	return nil
}
