// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package app assembles the spine into a running program: decoders feeding a
// ring, one mixer goroutine writing to a supervised ffmpeg, and an HTTP server
// handing the result to listeners.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"database/sql"

	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/decode"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/library"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/obs"
	"github.com/andrewloable/jockora/internal/presence"
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

	// TTSAddr is where the sidecar ACTUALLY is, which is not always where the
	// config says. A managed sidecar is given a port by the supervisor and only
	// knows it once running, so anything that talks to it has to be told.
	// Empty falls back to the configured address, which is right for a sidecar
	// the operator runs themselves.
	TTSAddr string

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
	//
	// THE SPIKE PATH'S pair. With a library, every station builds its own
	// through NewBreaks; these two are the single-station fallback and the
	// default a station inherits nothing from.
	Writer *station.BreakWriter

	// NewBreaks builds ONE STATION'S break machinery.
	//
	// A pipeline and a writer PER STATION, because almost everything in them is
	// per-broadcast state that two stations cannot share: the track context a
	// break is written about, the cold open, the fact cooldown, the exemption
	// list of a station's own track names, and the drop counters. One shared
	// box meant station B's break was written about station A's records, its
	// first break was not a cold open because A had already aired one, and the
	// drop rate described neither.
	//
	// TWO STATIONS MAY SHARE A JOCK and that is legal -- a station has one
	// jock, a jock may have many stations -- so this is keyed on the station,
	// never on the persona.
	//
	// Nil means no DJ, or the spike path, which has exactly one station and for
	// which sharing is not sharing.
	NewBreaks func(stationID int64) (*station.Pipeline, *station.BreakWriter)

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
	return l.nextFrom(ctx, l.Selector)
}

// nextFrom draws from ONE STATION's selector. Each station is its own
// timeline, so each has its own shuffle and its own position in it.
func (l *Library) nextFrom(ctx context.Context, sel *station.Selector) (track, error) {
	id, err := sel.Next()
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

	// A remote track is stored as a locator, not as a URL, so the credential
	// stays out of the tracks table. It becomes an openable, signed URL HERE,
	// one row before the decoder needs it.
	if t.path, err = library.ResolvePath(ctx, l.Store, t.path); err != nil {
		return track{}, err
	}
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

type App struct {
	cfg  *config.Config
	opts Options
	log  *slog.Logger

	// rt is the SPIKE PATH's single pipeline, used when there is no library to
	// build stations from. With a library, the manager owns one runtime per
	// station that has a listener and rt stays nil.
	rt  *station.Runtime
	srv *server.Server

	// tracker counts listeners; mgr turns that into running stations. Both nil
	// on the spike path, which has exactly one station and no presence.
	tracker *presence.Tracker
	mgr     *station.Manager

	// breaks is one station's break machinery each, built on first use and kept
	// for the life of the process: a station that stops and starts again keeps
	// its cold-open state and its counters, which is what a listener coming
	// back to a station they were on expects.
	breaksMu sync.Mutex
	breaks   map[int64]*stationBreaks
	// cadence is the operator's live choice, which a station coming up
	// inherits. HERE rather than in cfg: cfg is the startup default, read
	// without a lock all over this package, and writing it from an HTTP
	// handler is a race this code has already met once -- see SetCadence.
	cadence int

	// stallUntil pauses the feeder, for the fault injection GATE 2 requires.
	// Guarded because the signal handler and the feeder are different goroutines.
	stallMu    sync.Mutex
	stallUntil time.Time

	// nextBreak is the sample the next repeating break is owed at. The spike
	// schedules one window at startup and topUpBreaks continues from here.
	nextBreak int64

	nowMu sync.Mutex
	now   map[int64]nowPlaying
	// lastBreaks is the most recent break PER STATION.
	lastBreaks map[int64]*server.LastBreak

	// enriching gates the background worker so an operator can hand the
	// machine back for an evening without stopping the station.
	enriching atomic.Bool

	// rescan is the on-demand library scan. Present whenever there is a
	// library to scan; the route that starts it lands with the rest of the
	// admin API.
	rescan *library.Rescan
}

// ringSeconds is how much decoded audio is buffered ahead of the mixer. Enough
// to ride out a slow disk or a decoder starting up, small enough that a track
// change is not delayed by a wall of buffered audio.
const ringSeconds = 10

// scheduleAhead bounds how far ahead repeating breaks are queued.
const scheduleAhead = 2 * time.Hour

// seedFromConfig fills the v0.2 tables on first run, in a FIXED ORDER.
//
// Sources, then jocks, then stations. Not a style choice: stations reference
// jocks by foreign key, and the proposal reads the library the sources
// describe. Any other order either fails on the key or proposes a dial for a
// library nothing has scanned yet.
//
// Each step is a no-op once its table has a row, so this runs on every start
// and does nothing on all but the first.
func seedFromConfig(ctx context.Context, s *store.Store, cfg *config.Config,
	personas []*dj.Persona, log *slog.Logger) error {
	n, err := library.SeedSources(ctx, s, cfg)
	if err != nil {
		return err
	}
	log.Info("seeded sources", "n", n)

	// An empty persona directory is NOT an error. A library with no jocks is a
	// shuffle, and that is the state Jockora degrades to rather than refusing
	// to boot -- and globbing an empty path would search the working directory.
	if cfg.PersonaPath != "" {
		if n, err = dj.SeedJocks(ctx, s, cfg.PersonaPath); err != nil {
			return err
		}
		log.Info("seeded jocks", "n", n)
	}

	if n, err = station.SeedStations(ctx, s, personas); err != nil {
		return err
	}
	log.Info("seeded stations", "n", n)
	return nil
}

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

	a := &App{cfg: cfg, opts: opts, log: log, now: map[int64]nowPlaying{},
		lastBreaks: map[int64]*server.LastBreak{},
		breaks:     map[int64]*stationBreaks{},
		cadence:    cfg.BreakEveryNTracks}

	// THE SPIKE PATH keeps one runtime, started directly: no database means no
	// stations, no listeners to count and nothing to reconcile. With a library
	// the manager owns one runtime per station that has a listener, and this
	// stays nil.
	var queue *sched.Queue
	if opts.Library == nil {
		deps := a.runtimeDeps(nil)
		deps.Feed = func(ctx context.Context, _ *mix.Ring, _ *sched.Queue) { a.feedTracks(ctx, a.rt) }
		a.rt = station.NewRuntime(deps, singleStationID,
			filepath.Join(cfg.SegmentDir, singleStationDir))
		queue = a.rt.Queue()
	}

	// The pipeline enqueues into the SAME queue the mixer drains, and reports
	// what it scheduled. Wired HERE rather than by the caller, because the
	// caller cannot have the queue -- it is created above. Leaving it to be
	// passed in produced a nil-pointer panic in the break goroutine that took
	// the whole station off air.
	// The single pair's pipeline gets its writer HERE, before anything runs, for
	// the same reason the factory sets it at construction: assigning it later
	// races with the goroutine that reads it.
	if opts.Breaks != nil && opts.Breaks.Writer == nil {
		opts.Breaks.Writer = opts.Writer
	}
	if opts.Breaks != nil && queue != nil {
		opts.Breaks.Queue = queue
		// The spike path has exactly one station, and it is this one.
		opts.Breaks.OnScheduled = func(text string, p mix.Placement) {
			a.recordBreakOn(singleStationID, text, p)
		}
	}

	if opts.BreakPath != "" && queue != nil {
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
			if err := queue.Enqueue(sched.Entry{
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
	// Every listener needs an identity before they can be served, because
	// presence is what starts and stops stations. Until sign-in lands this is
	// a per-browser cookie, which is the right granularity anyway: two phones
	// behind one NAT are two listeners.
	srv.SetSessions(anonymousSession)
	srv.SetStatusSource(a)
	srv.SetTuner(a)
	srv.SetAdmin(a)

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
		// BEFORE the dial is built, so the first refresh sees the seeded
		// tables rather than an empty database.
		if err := seedFromConfig(context.Background(), opts.Library.Store, cfg,
			opts.Personas, log); err != nil {
			return nil, fmt.Errorf("app: seeding on first run: %w", err)
		}
		key, err := sessionKey(context.Background(), opts.Library.Store, cfg.SessionKey)
		if err != nil {
			return nil, err
		}
		signer, err := auth.NewSigner(key, time.Now)
		if err != nil {
			return nil, fmt.Errorf("app: session key: %w", err)
		}
		// SetAuth also makes the signed session the stream's identity, so the
		// anonymous cookie above stops being used the moment accounts exist.
		srv.SetAuth(signer, opts.Library.Store)

		// Presence is what starts and stops stations, so the server must be
		// able to record a heartbeat before any of them can come up.
		// The stored cadence outranks the configured one. The config value is
		// the DEFAULT for a station nobody has tuned yet; once an operator has
		// chosen, that choice is the setting, and a restart must not quietly
		// undo it.
		a.applyStoredCadence(context.Background(), opts.Breaks)

		a.tracker = presence.New(clock.Real{}, ListenerGrace)
		// THE MANAGER IS BUILT BEFORE SetStations, and the order is not
		// cosmetic. SetStations takes it as a Runtimes INTERFACE, so passing
		// a.mgr while it is still a nil *station.Manager hands the server a
		// non-nil interface wrapping a nil pointer: the handler's nil check
		// passes and the call panics on its first line. Deleting or disabling
		// a station killed the request that way in every library deployment.
		a.mgr = station.NewManager(a.tracker, opts.Library.Store, a.newStationRuntime,
			clock.Real{}, ListenerGrace, MaxStations)

		a.rescan = library.NewRescan(opts.Library.Store, clock.Real{})
		srv.SetSources(opts.Library.Store, a.rescan)
		srv.SetStations(opts.Library.Store, func(ctx context.Context, id int64) (station.Diff, error) {
			return station.Regenerate(ctx, opts.Library.Store, id)
		}, a.mgr)
		// THE LIVE ADDRESS, not the configured one. A managed sidecar picks its
		// own port, so reading cfg.TTSAddr here made /admin/voices 502 in every
		// default deployment and left the console's voice picker empty -- the
		// same half-wired failure buildAnalyser's comment warns about, repeated
		// three functions away.
		ttsAddr := opts.TTSAddr
		if ttsAddr == "" {
			ttsAddr = cfg.TTSAddr
		}
		var voices server.Voices
		if ttsAddr != "" {
			voices = newSidecarVoices(ttsAddr)
		}
		srv.SetJocks(opts.Library.Store, voices, a)
		srv.SetPlaylists(opts.Library.Store)
		// The library's accumulated enrichment, out and in. Named as the
		// enrichment rather than as the database, because that distinction is
		// the whole design: the file an operator hands to a friend carries no
		// accounts, no stations and no said lines.
		srv.SetEnrichmentPort(enrich.NewPort(opts.Library.Store))
		srv.SetPresence(a.tracker)
	}

	// The encoder starts with the station, in Runtime.Start, rather than here.
	// It used to spawn in New, which left an ffmpeg running for any caller
	// that built an App and never ran it.
	return a, nil
}

// singleStationID and singleStationDir are the one station this build runs.
// 12d's manager replaces both with one runtime per active station.
const (
	singleStationID  = 1
	singleStationDir = "1"
)

// sessionKeySetting is where the generated signing key lives.
const sessionKeySetting = "session_key"

// applyStoredCadence restores the operator's chosen cadence at startup.
//
// Silent about everything except a value it cannot use: a fresh database has no
// setting, which is the normal case and not worth a line.
func (a *App) applyStoredCadence(ctx context.Context, breaks *station.Pipeline) {
	if breaks == nil || a.opts.Library == nil || a.opts.Library.Store == nil {
		return
	}
	raw, ok, err := a.opts.Library.Store.Setting(ctx, cadenceSetting)
	if err != nil || !ok {
		return
	}
	n, convErr := strconv.Atoi(raw)
	if convErr != nil || n < 1 || n > 100 {
		a.log.Warn("stored break cadence is not usable; keeping the configured one",
			"stored", raw, "using", a.cfg.BreakEveryNTracks)
		return
	}
	breaks.SetCadence(station.NewCadence(n))
	// BOTH, and safely: this runs at startup before any goroutine exists, so
	// the config default may be corrected here even though an HTTP handler
	// must never touch it.
	a.cfg.BreakEveryNTracks = n
	a.setLiveCadence(n)
	a.log.Info("break cadence restored", "every_n_tracks", n)
}

// cadenceSetting is where the operator's break cadence lives.
//
// IT HAS TO OUTLIVE A RESTART. It is a decision an operator makes about how
// their station sounds, and it was being kept only in memory and in a config
// field -- so every restart silently reverted it to whatever the compose file
// said, and the operator's setting was gone with nothing to say so. Reported
// after it went back to 4 twice.
const cadenceSetting = "break_cadence"

// sessionKey resolves the secret that signs listener sessions.
//
// Generated once and kept in the database when the operator supplies none: a
// key drawn fresh at every start would sign every listener out whenever the
// server restarted, which on a home box is often.
func sessionKey(ctx context.Context, s *store.Store, configured string) ([]byte, error) {
	if configured != "" {
		return []byte(configured), nil
	}

	stored, ok, err := s.Setting(ctx, sessionKeySetting)
	if err != nil {
		return nil, err
	}
	if !ok {
		var b [32]byte
		_, _ = rand.Read(b[:]) // documented never to fail; it crashes instead
		stored = hex.EncodeToString(b[:])
		if err := s.SetSetting(ctx, sessionKeySetting, stored); err != nil {
			return nil, err
		}
	}
	raw, err := hex.DecodeString(stored)
	if err != nil {
		return nil, fmt.Errorf("app: the stored session key is not readable: %w", err)
	}
	return raw, nil
}

// sessionCookie names the listener. A cookie rather than an address: two phones
// behind one NAT are two listeners, and one laptop that changed network is
// still one.
const sessionCookie = "jockora_session"

// anonymousSession identifies a listener without an account.
//
// A STOPGAP until sign-in lands, and deliberately not a security measure: it
// says which browser is listening, which is all presence needs to start and
// stop stations. Replaced wholesale when sessions become signed.
func anonymousSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		return c.Value, true
	}
	// crypto/rand.Read is documented never to fail -- it crashes the program
	// rather than handing back predictable bytes -- so there is no branch here
	// that a test could reach.
	var b [16]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: id, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	return id, true
}

// runtimeDeps is the pipeline configuration every station shares. sel is that
// station's own draw; everything else is the same box.
func (a *App) runtimeDeps(sel *station.Selector) station.Deps {
	return station.Deps{
		Log:            a.log,
		Clock:          clock.Real{},
		SampleRate:     a.cfg.SampleRate,
		Channels:       a.cfg.Channels,
		SegmentSeconds: a.cfg.SegmentSeconds,
		ListSize:       a.cfg.ListSize,
		Sel:            sel,
		BreakOut:       a.opts.Writer,
	}
}

// newStationRuntime builds one station's pipeline on demand.
func (a *App) newStationRuntime(ctx context.Context, id int64,
	state store.SelectorState, resume bool) (*station.Runtime, error) {
	// The feeder needs the runtime it is feeding, and the runtime needs the
	// feeder: the closure captures the variable, which is assigned before
	// Start can ever call it.
	var rt *station.Runtime
	deps := a.runtimeDeps(nil)
	deps.Feed = func(ctx context.Context, _ *mix.Ring, _ *sched.Queue) { a.feedTracks(ctx, rt) }

	rt, err := station.NewRuntimeForStation(ctx, a.opts.Library.Store, deps, id,
		filepath.Join(a.cfg.SegmentDir, strconv.FormatInt(id, 10)), state, resume)
	return rt, err
}

// PersonaChanged pushes an edited jock to the stations already airing it.
//
// On the NEXT BREAK rather than by restarting the station: the writer reads the
// persona per break already, so the listener hears the change without hearing
// a gap.
func (a *App) PersonaChanged(j store.Jock) {
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return
	}
	// EVERY station on that jock, not the first. Two stations may share one --
	// the invariant is that a station has one jock, not that a jock has one
	// station -- and returning after the first left the others speaking as the
	// persona the operator had just edited away.
	// EVERY STATION THAT HAS A WRITER, not only the ones on air. A station that
	// is off air keeps its machinery, so an edit that skipped it would come
	// back as the old persona the next time somebody tuned in.
	a.eachBreaks(func(id int64, br *stationBreaks) {
		if br.writer == nil {
			return
		}
		st, err := a.opts.Library.Store.GetStation(context.Background(), id)
		if err != nil || st.JockID != j.ID {
			return
		}
		br.writer.SetPersona(dj.FromRecord(j))
	})
}

// stationBreaks is one station's break machinery: the pipeline that decides
// when a break happens, and the writer that writes it.
//
// Both belong to the STATION rather than to the server. See Options.NewBreaks
// for the list of state that two stations sharing one of these got wrong.
type stationBreaks struct {
	pipeline *station.Pipeline
	writer   *station.BreakWriter
}

// breaksFor is one station's own break machinery, built on first use.
//
// Nil when there is no DJ at all -- no persona, no language model, no speech
// sidecar -- which is a supported way to run and must stay a shuffle rather
// than a crash.
func (a *App) breaksFor(id int64) *stationBreaks {
	a.breaksMu.Lock()
	defer a.breaksMu.Unlock()

	if br, ok := a.breaks[id]; ok {
		return br
	}
	// A zero-valued App is a legitimate thing to hold -- several tests build
	// one directly -- and writing into a nil map panics.
	if a.breaks == nil {
		a.breaks = map[int64]*stationBreaks{}
	}
	// THE FACTORY FIRST. Options.Breaks is the spike path's single pair, and
	// a deployment with a library never reaches it: one box shared by four
	// stations is the bug this exists to fix.
	if a.opts.NewBreaks != nil {
		pipeline, writer := a.opts.NewBreaks(id)
		if pipeline == nil {
			return nil
		}
		br := &stationBreaks{pipeline: pipeline, writer: writer}
		// The operator's cadence applies to every station, including one that
		// comes up long after they set it. Read under breaksMu, which this
		// function already holds.
		if n := a.cadence; n > 0 {
			pipeline.SetCadence(station.NewCadence(n))
		}
		a.breaks[id] = br
		return br
	}
	if a.opts.Breaks == nil {
		return nil
	}
	br := &stationBreaks{pipeline: a.opts.Breaks, writer: a.opts.Writer}
	a.breaks[id] = br
	return br
}

// setLiveCadence records the operator's choice for stations not yet built.
func (a *App) setLiveCadence(n int) {
	a.breaksMu.Lock()
	a.cadence = n
	a.breaksMu.Unlock()
}

// liveCadenceSetting is that choice, or zero if nobody has made one.
func (a *App) liveCadenceSetting() int {
	a.breaksMu.Lock()
	defer a.breaksMu.Unlock()
	return a.cadence
}

// hasDJ reports whether this deployment can write breaks at all.
//
// A build with no persona, no language model or no speech sidecar runs as a
// shuffle, which is a supported way to run: the question is asked here rather
// than by testing one pipeline for nil, because with a library the pipelines
// are built per station and none exists until one is on air.
func (a *App) hasDJ() bool { return a.opts.NewBreaks != nil || a.opts.Breaks != nil }

// existingBreaks is one station's machinery IF IT HAS ANY, and never builds it.
//
// The read-only half of breaksFor, for the paths a listener drives: polling a
// status page must not allocate anything.
func (a *App) existingBreaks(id int64) *stationBreaks {
	a.breaksMu.Lock()
	defer a.breaksMu.Unlock()
	return a.breaks[id]
}

// eachBreaks runs fn over every station that has break machinery, so a setting
// an operator changes once reaches all of them.
func (a *App) eachBreaks(fn func(id int64, br *stationBreaks)) {
	a.breaksMu.Lock()
	live := make(map[int64]*stationBreaks, len(a.breaks))
	for id, br := range a.breaks {
		live[id] = br
	}
	a.breaksMu.Unlock()
	for id, br := range live {
		fn(id, br)
	}
}

// applyStationJock puts the station's OWN jock on air.
//
// THE ASSIGNMENT HAD NO CONSUMER. station.jock_id was written by the console,
// read back by the console and captioned on the dial, while the persona at the
// microphone came from a genre vote over personas/ taken once at boot and never
// changed again. A station assigned Sunny Marchetti -- slow, warm, af_nicole --
// aired Dutch "The Hammer" Mahoney shouting in am_fenrir, under her name.
//
// Called where the break pipeline is already rebound to the station on air, so
// the jock arrives with the queue it will speak into. SetPersona moves the
// writing, the voice and the said-lines index together.
//
// A station with NO jock keeps whoever is speaking. That is the boot-time seed
// rather than anybody's decision, and it is better than the alternative, which
// is a station that plays music and never talks.
func (a *App) applyStationJock(ctx context.Context, stationID int64) {
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return
	}
	br := a.breaksFor(stationID)
	if br == nil || br.writer == nil {
		return
	}
	// ONE error branch for every way there is nobody to put on air: no such
	// station, no jock assigned, no database. All three keep the current jock.
	j, err := a.opts.Library.Store.StationJock(ctx, stationID)
	if err != nil {
		return
	}
	// READ AGAIN AT EVERY FEED, not only when the writer is built. A station
	// can be given a different jock while it is off air, and the next listener
	// must hear the new one.
	if p := br.writer.CurrentPersona(); p != nil && p.ID() == j.ID {
		return
	}
	br.writer.SetPersona(dj.FromRecord(j))
	a.log.Info("jock on air", "station", stationID, "jock", j.Name, "voice", j.VoiceID)
}

// ListenerGrace is how long a session counts as present after its last
// playlist fetch. Long enough to ride out a slow poll or a page reload, short
// enough that a closed tab does not hold a station on air.
const ListenerGrace = 30 * time.Second

// MaxStations caps how many run at once. A per-station ffmpeg is the one cost
// that scales with stations, and the target box is small.
const MaxStations = 4

// reconcileEvery is how often the manager compares listeners to runtimes. A
// second is fast enough that a first listener waits no longer than that, and
// slow enough that the work is invisible.
const reconcileEvery = time.Second

// refillEvery is how often every station's playlist is rebuilt from the
// dossiers that exist by then.
//
// Two minutes, not one second like reconcileEvery: this WRITES, once per
// station, and enrichment produces a track or two a minute at best. Any faster
// is churn for a result that has not changed.
const refillEvery = 2 * time.Minute

// refill grows the stations as enrichment classifies more of the library.
//
// A station's playlist is materialised when it is created, from whatever had a
// dossier at that moment -- which on a fresh library is almost nothing. Without
// this it stayed that size forever unless somebody opened the console and
// pressed Regenerate, so a station made on day one was still twelve tracks on
// day three while the library filled up around it.
//
// Pins and exclusions survive: ReplaceStationTracks only deletes the rows the
// filter owns.
func (a *App) refill(ctx context.Context) {
	t := time.NewTicker(refillEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.refillOnce(ctx)
		}
	}
}

func (a *App) refillOnce(ctx context.Context) {
	stations, err := a.opts.Library.Store.ListStations(ctx)
	if err != nil {
		a.log.Warn("refilling stations", "err", err)
		return
	}
	for _, st := range stations {
		diff, err := station.Regenerate(ctx, a.opts.Library.Store, st.ID)
		if err != nil {
			// Not fatal and not retried here: the next tick is the retry, and
			// one station whose filter is broken must not stop the others.
			a.log.Warn("refilling station", "station", st.ID, "err", err)
			continue
		}
		if diff.Added > 0 || diff.Removed > 0 {
			a.log.Info("station refilled", "station", st.ID, "name", st.Name,
				"added", diff.Added, "removed", diff.Removed, "now", diff.Kept+diff.Added)
		}
	}
}

// reconcile keeps the running stations in step with the listeners.
func (a *App) reconcile(ctx context.Context) {
	t := time.NewTicker(reconcileEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.mgr.Tick(ctx); err != nil && ctx.Err() == nil {
				// Not fatal and not retried in a loop here: the next tick is
				// the retry, and a station that cannot start says so once a
				// second rather than spinning.
				a.log.Warn("reconciling stations", "err", err)
			}
		}
	}
}

// primary is the runtime whose numbers /now.json reports.
//
// The one station on the spike path, or the lowest-numbered station on air.
// Nil when nothing is running, which is what a server with no listeners looks
// like and is not a fault.
func (a *App) primary() *station.Runtime {
	if a.mgr != nil {
		for _, id := range a.mgr.Running() {
			if rt := a.mgr.Runtime(id); rt != nil {
				return rt
			}
		}
		return nil
	}
	return a.rt
}

// primaryID is primary's station, or the single station when nothing runs.
func (a *App) primaryID() int64 {
	if rt := a.primary(); rt != nil {
		return rt.StationID()
	}
	return singleStationID
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

// recordBreakOn remembers what the DJ last said ON ONE STATION, for /now.json
// and for the thumbs-down that rates it.
func (a *App) recordBreakOn(stationID int64, text string, placement mix.Placement) {
	a.nowMu.Lock()
	// A zero-valued App is a legitimate thing to hold, and writing into a nil
	// map panics -- in the goroutine that airs breaks, which would take the
	// station off air.
	if a.lastBreaks == nil {
		a.lastBreaks = map[int64]*server.LastBreak{}
	}
	// PER STATION. /now.json carries the last thing the DJ said and it is what
	// the thumbs-down rates, so one global copy showed a listener on one
	// station a break that aired on another, in a different jock's voice, about
	// records they never heard.
	a.lastBreaks[stationID] = &server.LastBreak{
		Text:      text,
		Placement: placement.String(),
		AiredAt:   time.Now().Unix(),
	}
	a.nowMu.Unlock()

	// THIS STATION'S writer, not the shared one. Clearing the cold open on a
	// writer nothing else uses left every station permanently cold, writing
	// every break as the first thing a listener hears.
	if br := a.breaksFor(stationID); br != nil && br.writer != nil {
		br.writer.Aired()
	}
}

// Addr is the address the server is listening on.
func (a *App) Addr() string { return a.srv.Addr() }

// PendingBreaks reports how many scheduled items have not aired yet.
func (a *App) PendingBreaks() int {
	if rt := a.primary(); rt != nil {
		return rt.Queue().Pending()
	}
	return 0
}

// Metrics reports the run so far: inter-write gaps, ring occupancy, write count.
func (a *App) Metrics() mix.MetricsSnapshot { return a.snapshot() }

// snapshot is the primary station's mixer timing, or an empty record when no
// station is on air -- which is what a server with no listeners has produced.
func (a *App) snapshot() mix.MetricsSnapshot {
	if rt := a.primary(); rt != nil {
		return rt.Metrics().Snapshot()
	}
	return mix.MetricsSnapshot{}
}

// runtimeStatus is the primary station's pipeline, or a stopped one.
func (a *App) runtimeStatus() station.RuntimeStatus {
	if rt := a.primary(); rt != nil {
		return rt.Status()
	}
	return station.RuntimeStatus{}
}

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
	m := a.snapshot()
	rst := a.runtimeStatus()

	st := server.Status{
		Health: server.Health{
			// ffmpeg is proven working by the fact that segments exist at all,
			// and the supervisor reports restarts separately -- EXCEPT for the
			// one failure that neither of those makes visible. A full disk
			// keeps the mixer running and the restart counter climbing while
			// no new segment is ever written, which reads as healthy right up
			// until a listener says the stream stopped.
			FFmpeg: ffmpegHealth(a.encoderDegraded()),
			// Reported by what is actually WIRED, not by what the spike used
			// to do. These read "not used by the spike" long after both were
			// in use, which is a status endpoint inventing an answer.
			LLM: dependencyHealth(a.hasDJ() || a.opts.Enricher != nil),
			TTS: dependencyHealth(a.hasDJ()),
		},
		Metrics: server.Metrics{
			RingOccupancyS:    rst.RingOccupancyS,
			Underruns:         rst.Underruns,
			EncoderRestarts:   rst.EncoderRestarts,
			P99GapMs:          float64(m.P99Gap.Microseconds()) / 1000,
			MaxGapMs:          float64(m.MaxGap.Microseconds()) / 1000,
			MinRingOccupancyS: m.MinOccupancy.Seconds(),
			DriftMs:           float64(a.drift().Microseconds()) / 1000,
		},
	}

	a.nowMu.Lock()
	if np, ok := a.now[a.primaryID()]; ok && (np.artist != "" || np.title != "") {
		st.Now = &server.Track{Artist: np.artist, Title: np.title}
	}
	st.LastBreak = a.lastBreaks[a.primaryID()]
	a.nowMu.Unlock()

	// THE PRIMARY STATION'S, which is the spike path's only one. A listener
	// asks about a station by name; see StatusForStation.
	//
	// existing, not breaksFor: a status poll must not BUILD a station's DJ as a
	// side effect of being read. Every listener polls this every few seconds,
	// and a station nobody has tuned to should not acquire a writer, a session
	// and a row in the break stats because somebody looked at the page.
	if br := a.existingBreaks(a.primaryID()); br != nil {
		// Told what the mixer queue is holding, because the pipeline is handed
		// a queue only once a station is on air and must not reach for one.
		br.pipeline.SetScheduled(a.PendingBreaks())
		st.NextBreak = string(br.pipeline.Outlook())
	}

	st.Enrichment = a.enrichment()

	// Only once one has been asked for. A station that has never rescanned
	// should show nothing rather than a progress bar reading zero of zero.
	if a.rescan != nil {
		if p := a.rescan.Progress(); !p.StartedAt.IsZero() {
			st.Rescan = &p
		}
	}

	return st
}

// enrichment is how far the dossier worker has got.
//
// The field has existed since the spine and was never filled: /now.json has
// carried an enrichment block in its type and nothing in its body since the
// day it was written, so the operator page has always shown an empty bar. It
// costs two counts.
func (a *App) enrichment() *server.Enrichment {
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return nil
	}
	db := a.opts.Library.Store.DB()
	ctx := context.Background()

	var total, done int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM tracks WHERE playable = 1 AND missing_at IS NULL`).Scan(&total); err != nil {
		return nil
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM dossiers`).Scan(&done); err != nil {
		return nil
	}

	e := &server.Enrichment{Done: done, Total: total, Running: a.enriching.Load()}
	if total > 0 {
		// Rounded to a tenth: a bar that moves in ten-thousandths looks broken.
		e.Pct = math.Round(float64(done)/float64(total)*1000) / 10
	}

	// The confidence split is what says whether the enrichment is WORKING. A
	// library that is 90% enriched into "none" is 90% of nothing.
	e.ConfidenceCounts = map[string]int{}
	rows, err := db.QueryContext(ctx, `SELECT confidence, count(*) FROM dossiers GROUP BY confidence`)
	if err != nil {
		return e
	}
	defer rows.Close() //nolint:errcheck // read-only
	// A row that will not scan is skipped rather than branched on: confidence
	// is TEXT NOT NULL and the count is an integer, so there is nothing here a
	// working database can fail at, and a partial split is still worth showing.
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err == nil {
			e.ConfidenceCounts[name] = n
		}
	}
	return e
}

// StatusForStation reports what ONE station is airing.
//
// There is no global now-playing once stations are per-listener: four running
// stations are four different tracks, and naming one of them "now" would tell
// three quarters of the listeners something false.
func (a *App) StatusForStation(id int64) server.Status {
	st := a.Status()
	st.Now = nil

	a.nowMu.Lock()
	np, ok := a.now[id]
	st.LastBreak = a.lastBreaks[id]
	a.nowMu.Unlock()
	if ok && (np.artist != "" || np.title != "") {
		st.Now = &server.Track{Artist: np.artist, Title: np.title}
	}
	if a.mgr != nil {
		if rt := a.mgr.Runtime(id); rt != nil {
			rst := rt.Status()
			st.Metrics.RingOccupancyS = rst.RingOccupancyS
			st.Metrics.Underruns = rst.Underruns
			st.Metrics.EncoderRestarts = rst.EncoderRestarts
			// THIS STATION'S DJ, not the first one that happened to be built.
			// "The DJ speaks after this track" on a station whose DJ is not
			// writing anything is a promise the stream does not keep.
			if br := a.existingBreaks(id); br != nil {
				br.pipeline.SetScheduled(rt.Queue().Pending())
				st.NextBreak = string(br.pipeline.Outlook())
			}
		}
	}
	return st
}

// StartRescan begins an on-demand library scan.
//
// Returns library.ErrScanRunning if one is already going, which the admin route
// turns into a 409 rather than queueing: two walks marking missing against each
// other is a race with no correct answer.
func (a *App) StartRescan(ctx context.Context) error {
	if a.rescan == nil {
		return errors.New("app: there is no library to rescan")
	}
	return a.rescan.Start(ctx)
}

// drift reports how far behind schedule the mixer is, or zero before it starts.
func (a *App) drift() time.Duration {
	if rt := a.primary(); rt != nil {
		return rt.Drift()
	}
	return 0
}

// P99Gap and MaxGap expose the pacing tail for a soak harness.
func (a *App) P99Gap() time.Duration { return a.snapshot().P99Gap }
func (a *App) MaxGap() time.Duration { return a.snapshot().MaxGap }

// MinRingOccupancy is the lowest the ring has been all run.
func (a *App) MinRingOccupancy() time.Duration { return a.snapshot().MinOccupancy }

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
	// THE SPIKE PATH starts its one station immediately: there is nobody to
	// count, so there is nothing to wait for.
	if a.rt != nil {
		if err := a.rt.Start(ctx); err != nil {
			return err
		}
		defer a.rt.Stop() //nolint:errcheck // shutting down
	}
	if a.mgr != nil {
		defer a.mgr.StopAll() //nolint:errcheck // shutting down
		go a.reconcile(ctx)
	}
	if a.opts.Library != nil && a.opts.Library.Store != nil {
		go a.refill(ctx)
	}

	go func() {
		if err := a.srv.Run(ctx); err != nil && ctx.Err() == nil {
			a.log.Error("http server stopped", "err", err)
		}
	}()

	// Enrichment runs alongside the stream, never in front of it. A library of
	// ten thousand tracks takes many hours to enrich and the station has to be
	// listenable from the first minute -- an unenriched track simply gets
	// personality-only talk, which is what the empty dossier means everywhere
	// else in this design.
	if a.hasDJ() {
		// hasDJ, not one pipeline being non-nil: with a library the pipelines
		// are built PER STATION and none exists until one is on air, so asking
		// the old question would have left the ticker unstarted and the DJ
		// silent on a deployment that supplied only the factory.
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
		a.enriching.Store(true)
		a.opts.Enricher.Paused = a.enriching.Load
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

	// One mixer goroutine per station, owning that station's bus clock and
	// sole writer to its encoder -- inside the runtime. With a manager there
	// is no single mixer to wait for, so the context is what ends the run.
	var err error
	if a.rt != nil {
		err = a.rt.Wait()
	} else {
		<-ctx.Done()
	}

	// The four numbers GATE 2 asserts on, logged at shutdown so a long
	// unattended run needs no extra tooling to be judged.
	s := a.snapshot()
	st := a.runtimeStatus()
	frames := int64(0)
	if rt := a.primary(); rt != nil {
		frames = rt.SamplePos()
	}
	a.log.Info("off air",
		"frames", frames,
		"audio", mix.FramesToDuration(int(frames)).Round(time.Second),
		"p99_inter_write_gap", s.P99Gap.Round(time.Millisecond),
		"max_inter_write_gap", s.MaxGap.Round(time.Millisecond),
		"min_ring_occupancy", s.MinOccupancy.Round(time.Millisecond),
		"final_drift", a.drift().Round(time.Millisecond),
		"underruns", st.Underruns,
		"encoder_restarts", st.EncoderRestarts)

	return err
}

// feedTracks decodes the track list into the ring, looping forever. A track that
// will not decode is logged and skipped: one bad file must not end the stream.
func (a *App) feedTracks(ctx context.Context, rt *station.Runtime) {
	if a.opts.Library != nil {
		a.feedFromLibrary(ctx, rt)
		return
	}
	a.feedFromList(ctx, rt)
}

// feedFromLibrary plays the scanned database and schedules breaks between
// tracks.
//
// Every part of the break machinery is optional here and each degrades on its
// own: no pipeline is a shuffle, a failed selection is one skipped track, a
// dropped break is silence where speech would have been. None of them stops the
// music, which is the invariant this whole program is arranged around.
func (a *App) feedFromLibrary(ctx context.Context, rt *station.Runtime) {
	blocks := make(chan []mix.Frame, 8)
	var boundary int
	var prev, cur track

	// THIS STATION'S OWN break machinery. It used to be one box on the App,
	// rebound to whichever station was fed last -- which with two stations
	// airing meant B's breaks were written about A's records, in A's jock's
	// voice, into A's queue.
	if br := a.breaksFor(rt.StationID()); br != nil {
		// THE QUEUE OF THE STATION ACTUALLY ON AIR. Every station owns its own
		// ring and queue, and a runtime builds a fresh queue when it restarts,
		// so this is bound per feed rather than once.
		br.pipeline.SetQueue(rt.Queue(), func(text string, p mix.Placement) {
			a.recordBreakOn(rt.StationID(), text, p)
		})
		// THE WRITER IS NOT REBOUND HERE. It was, on every feed start, while
		// the break ticker read it on its own goroutine -- a data race on the
		// field the break's track context is written through. The pipeline is
		// built with its writer instead.
		a.applyStationJock(ctx, rt.StationID())

		// The boundary counter below starts at zero, so the cadence has to
		// start there too. A station restarts whenever a listener comes back,
		// and a cadence carrying the previous run's high-water mark rejects
		// every boundary of this one -- silently, forever.
		if br.pipeline.Cadence != nil {
			br.pipeline.Cadence.Reset()
		}
	}

	// One track is chosen AHEAD of the one playing. Without it the boundary is
	// only known at the instant it arrives, which leaves the lookahead no time
	// at all -- ShouldTrigger requires now < insertionAt, so a break announced
	// at its own boundary can never be generated and none would ever air.
	upcoming, err := a.opts.Library.nextFrom(ctx, rt.Selector())
	if err != nil {
		a.log.Error("could not choose a first track", "err", err)
		return
	}

	for ctx.Err() == nil {
		cur = upcoming
		var chooseErr error
		upcoming, chooseErr = a.opts.Library.nextFrom(ctx, rt.Selector())
		if chooseErr != nil {
			a.log.Error("could not choose the next track", "err", chooseErr)
			upcoming = track{}
		}

		// The boundary at the END of the track now starting. Announced here,
		// a whole track early, which is exactly the room the lookahead needs.
		if upcoming.path != "" {
			boundary++
			a.announceBoundary(ctx, rt, boundary, prev, cur, upcoming)
		}

		done := a.startFeeder(ctx, rt, blocks)
		a.setNowPlayingTrack(rt.StationID(), cur)
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
			a.tickBreaks(ctx)
		}
	}
}

// tickBreaks polls every airing station's own pipeline, on its own clock.
//
// One ticker rather than one goroutine per station: generation is serial by
// design -- the GPU holds one model at a time -- and a shared ticker makes that
// ordering explicit rather than leaving four goroutines to contend for it.
func (a *App) tickBreaks(ctx context.Context) {
	for _, rt := range a.airing() {
		br := a.breaksFor(rt.StationID())
		if br == nil {
			continue
		}
		if _, err := br.pipeline.Tick(ctx, secondsOn(rt)); err != nil && ctx.Err() == nil {
			a.log.Warn("break generation failed", "station", rt.StationID(), "err", err)
		}
	}
}

// airing is every station currently on air, or the spike path's single one.
func (a *App) airing() []*station.Runtime {
	if a.mgr == nil {
		if a.rt != nil {
			return []*station.Runtime{a.rt}
		}
		return nil
	}
	ids := a.mgr.Running()
	out := make([]*station.Runtime, 0, len(ids))
	for _, id := range ids {
		if rt := a.mgr.Runtime(id); rt != nil {
			out = append(out, rt)
		}
	}
	return out
}

// announceBoundary offers one transition to the cadence and lets the pipeline
// generate anything it decides to.
func (a *App) announceBoundary(ctx context.Context, rt *station.Runtime, boundary int, prev, cur, next track) {
	br := a.breaksFor(rt.StationID())
	if br == nil {
		return
	}
	curTrack := a.trackFor(ctx, cur)
	nextTrack := a.trackFor(ctx, next)

	// THIS STATION'S CLOCK. The boundary is when the track now STARTING will
	// end, measured from the mixer's position rather than the wall clock,
	// because that is the clock the break will be spliced against -- and every
	// station has its own. Read from the primary runtime, a break on the second
	// station was placed against the first station's position and landed
	// wherever that happened to be.
	now := secondsOn(rt)
	at := now + curTrack.DurationS
	took := br.pipeline.Announce(station.Boundary{
		Index:           boundary,
		Cur:             curTrack,
		Next:            nextTrack,
		InsertionAt:     at,
		InsertionSample: rt.SamplePos() + int64(curTrack.DurationS*mix.SampleRate),

		// CARRIED, not stashed in the writer. Generation happens up to a
		// minute later, by which time later boundaries have arrived; the
		// writer's shared context made the DJ announce one record and the
		// station play another.
		PrevID: prev.id, CurID: cur.id, NextID: next.id,
		PrevArtist: prev.artist, PrevTitle: prev.title, PrevAlbum: prev.album,
		CurArtist: cur.artist, CurTitle: cur.title, CurAlbum: cur.album,
		NextArtist: next.artist, NextTitle: next.title, NextAlbum: next.album,
	})
	// EVERY boundary, taken or not. A break that never airs is the failure this
	// product is least able to notice: nothing errors, no metric moves, health
	// stays green and the station simply plays music. The only way to tell
	// "the cadence said no" from "generation ran late" from "the slot was never
	// offered" is to say which one happened, at the moment it happens.
	a.log.Info("break slot offered",
		"station", rt.StationID(),
		"boundary", boundary, "taken", took,
		"cadence", br.pipeline.EveryN(),
		"now_s", math.Round(now),
		"insertion_at_s", math.Round(at),
		"track_s", math.Round(curTrack.DurationS),
		"pending", br.pipeline.Pending())
}

// secondsOn is one station's position on its own bus clock.
func secondsOn(rt *station.Runtime) float64 {
	return float64(rt.SamplePos()) / mix.SampleRate
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
func (a *App) feedFromList(ctx context.Context, rt *station.Runtime) {
	blocks := make(chan []mix.Frame, 8)

	for ctx.Err() == nil {
		played := 0
		for _, path := range a.opts.Tracks {
			if ctx.Err() != nil {
				return
			}

			done := a.startFeeder(ctx, rt, blocks)
			a.setNowPlaying(rt.StationID(), path)
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
func (a *App) startFeeder(ctx context.Context, rt *station.Runtime, blocks chan []mix.Frame) chan struct{} {
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
			if err := feedRing(ctx, rt.Ring(), b); err != nil {
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
func (a *App) setNowPlayingTrack(stationID int64, t track) {
	np := nowPlaying{artist: t.artist, title: t.title}
	if np.title == "" {
		// An untagged file still has to be called something, and its filename
		// is the only honest answer. Never the full path: the status endpoint
		// must not leak the library layout.
		np.title = strings.TrimSuffix(filepath.Base(t.path), filepath.Ext(t.path))
	}
	a.nowMu.Lock()
	defer a.nowMu.Unlock()
	a.now[stationID] = np
}

// secondsPlayed is the mixer's position on the bus clock, in seconds.
func (a *App) secondsPlayed() float64 {
	return float64(a.samplePos()) / mix.SampleRate
}

// samplePos is the mixer's absolute bus position, or zero before it starts.
func (a *App) samplePos() int64 {
	if rt := a.primary(); rt != nil {
		return rt.SamplePos()
	}
	return 0
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
		rt := a.primary()
		if rt == nil {
			return added
		}
		if err := rt.Queue().Enqueue(sched.Entry{
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
func (a *App) setNowPlaying(stationID int64, path string) {
	a.nowMu.Lock()
	defer a.nowMu.Unlock()
	a.now[stationID] = nowPlaying{
		title: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
	}
}

// nowPlaying is what one station is airing.
type nowPlaying struct{ artist, title string }

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
func (a *App) encoderPID() int {
	if rt := a.primary(); rt != nil {
		if enc := rt.Encoder(); enc != nil {
			return enc.PID()
		}
	}
	return 0
}

// encoderDegraded reports the encoder's health, treating "not started yet" as
// healthy: an App that has not been run has not failed at anything.
func (a *App) encoderDegraded() string {
	if rt := a.primary(); rt != nil {
		if enc := rt.Encoder(); enc != nil {
			return enc.Degraded()
		}
	}
	return ""
}

// Tune switches the station to a tag, and reports how many tracks it holds.
//
// The current track keeps playing: the mixer has already buffered it, and
// cutting audio mid-song to honour a click is the stutter this design exists to
// avoid. The change is heard at the next boundary, which is also how a real
// radio behaves when you turn the dial slowly.
// Dial lists the stations a listener may choose from.
//
// From the TABLE, not from a proposal. What the operator built is what the
// listener sees; the proposal is a seed, and re-deriving it here would show a
// dial nobody configured.
// enrichmentDone reports whether every playable track has a dossier.
//
// It decides whether the readiness gate applies at all: while enrichment runs a
// thin station is thin because the answer has not arrived, and once it finishes
// a thin station is simply small. Errors count as NOT done, which keeps the
// gate on -- the safe direction, since the alternative offers a station that
// may be three tracks long.
func (a *App) enrichmentDone(ctx context.Context) bool {
	var left int
	err := a.opts.Library.Store.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM tracks WHERE playable = 1 AND id NOT IN (SELECT track_id FROM dossiers)`).
		Scan(&left)
	return err == nil && left == 0
}

func (a *App) Dial(ctx context.Context) ([]server.DialStation, error) {
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return nil, fmt.Errorf("app: no library to build a dial from")
	}
	s := a.opts.Library.Store
	rows, err := s.ListStations(ctx)
	if err != nil {
		return nil, err
	}

	// Asked ONCE for the whole dial rather than per station: it is a count over
	// the library, and the answer is the same for every row.
	done := a.enrichmentDone(ctx)

	out := []server.DialStation{}
	for _, st := range rows {
		// DISABLED STATIONS ARE NOT ON THE DIAL. An operator switching one off
		// means listeners should stop seeing it, not see it and fail to tune.
		if !st.Enabled {
			continue
		}
		ids, err := s.StationTrackIDs(ctx, st.ID)
		if err != nil {
			return nil, err
		}
		d := server.DialStation{ID: st.ID, Name: st.Name, Genre: st.Genre,
			Mood: st.Mood, Tracks: len(ids)}
		d.Ready, d.Preparing = station.Ready(st.Genre, len(ids), done)
		if a.tracker != nil {
			d.Listeners = a.tracker.Count(st.ID)
		}
		if st.JockID != "" {
			if j, err := s.GetJock(ctx, st.JockID); err == nil {
				d.JockName = j.Name
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// Tune puts a session on a station and says where to listen.
//
// IT IS THE FIRST HEARTBEAT. The station starts because somebody tuned to it,
// so the touch has to happen here rather than waiting for the client's first
// playlist fetch -- which cannot succeed until the station is running.
func (a *App) Tune(ctx context.Context, session string, stationID int64) (string, error) {
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return "", fmt.Errorf("app: no library to tune")
	}
	st, err := a.opts.Library.Store.GetStation(ctx, stationID)
	if err != nil {
		return "", err
	}
	if !st.Enabled {
		// The same answer as a station that does not exist: a listener has no
		// business knowing which stations the operator has switched off.
		return "", store.ErrNotFound
	}
	// STILL FILLING is not the same as switched off, and it says so. The dial
	// already refuses to offer it, so this is the second line rather than the
	// first: a bookmarked station id, or a dial the listener has had open
	// since before the operator made it.
	ids, err := a.opts.Library.Store.StationTrackIDs(ctx, stationID)
	if err != nil {
		return "", err
	}
	if ok, why := station.Ready(st.Genre, len(ids), a.enrichmentDone(ctx)); !ok {
		return "", fmt.Errorf("%w: %s has %d tracks and is %s",
			server.ErrStationPreparing, st.Name, len(ids), why)
	}
	if a.tracker != nil {
		a.tracker.Touch(stationID, session)
	}
	a.log.Info("tuned", "station", stationID, "name", st.Name)
	return "/hls/" + strconv.FormatInt(stationID, 10) + "/stream.m3u8", nil
}

// Feedback records what a listener thought of the break that just aired.
//
// ATTRIBUTED to the person and the station: a household is several people with
// different taste, and "somebody disliked this" is much less useful than
// knowing who, on what.
func (a *App) Feedback(ctx context.Context, userID, stationID int64, verdict string) error {
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return fmt.Errorf("no store to record feedback in")
	}
	// THE BREAK THAT AIRED ON THE STATION THEY WERE LISTENING TO, and the jock
	// who said it. Both used to come from the shared writer, so a thumbs-down
	// on one station recorded another station's line against another station's
	// jock -- into the table the console reports as what listeners said.
	a.nowMu.Lock()
	last := a.lastBreaks[stationID]
	a.nowMu.Unlock()

	if last == nil || last.Text == "" {
		return fmt.Errorf("no break has aired yet")
	}
	jockID := ""
	if br := a.breaksFor(stationID); br != nil && br.writer != nil {
		if p := br.writer.CurrentPersona(); p != nil {
			jockID = p.ID()
		}
	}
	_, err := a.opts.Library.Store.DB().ExecContext(ctx,
		`INSERT INTO break_feedback (jock_id, text, verdict, aired_at, at, user_id)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		jockID, last.Text, verdict, last.AiredAt, time.Now().Unix(), userID)
	if err != nil {
		return fmt.Errorf("recording feedback: %w", err)
	}
	a.log.Info("break feedback", "verdict", verdict, "user", userID, "station", stationID)
	return nil
}

// Jock is one persona a listener may put on air.
type Jock struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Voice  string   `json:"voice"`
	Genres []string `json:"genres,omitempty"`
	Moods  []string `json:"moods,omitempty"`
	OnAir  bool     `json:"on_air"`
}

// Jocks lists the roster and marks whoever is on air.
//
// The roster was invisible until this existed: nine personas shipped, the
// station chose one from what the library sounded like, and a listener had no

// Overview is everything the operator page renders.
//
// Most of this the station already knew and never showed anyone: enrichment
// cost, which tracks failed analysis, the advert pool, and the thumbs-downs,
// which were RECORDED AND READ BY NOTHING until this existed.
func (a *App) Overview() any {
	out := map[string]any{}
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return out
	}
	db := a.opts.Library.Store.DB()
	ctx := context.Background()

	var tracks, playable, enriched, analysed, withBPM int
	_ = db.QueryRowContext(ctx, `SELECT count(*), sum(playable) FROM tracks`).Scan(&tracks, &playable)
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM dossiers`).Scan(&enriched)
	_ = db.QueryRowContext(ctx,
		`SELECT sum(loudness_lufs IS NOT NULL), sum(bpm IS NOT NULL) FROM tracks WHERE playable = 1`).
		Scan(&analysed, &withBPM)

	var tokens, wallSeconds float64
	var costed int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*), coalesce(sum(tokens),0), coalesce(sum(wall_seconds),0) FROM enrich_cost`).
		Scan(&costed, &tokens, &wallSeconds)

	out["library"] = map[string]any{
		"tracks": tracks, "playable": playable,
		"enriched": enriched, "loudness_measured": analysed, "bpm_measured": withBPM,
	}
	// Cost is reported per track as well as in total, because the total is
	// meaningless for planning and the per-track figure projects to a library.
	perTrack := 0.0
	if costed > 0 {
		perTrack = wallSeconds / float64(costed)
	}
	out["enrichment_cost"] = map[string]any{
		"tracks_measured": costed, "tokens": tokens,
		"wall_seconds": wallSeconds, "seconds_per_track": perTrack,
	}

	// Tracks the analyser could not measure. Stored as zero rather than NULL so
	// they are not retried for ever, which makes them findable exactly this way.
	var failed int
	_ = db.QueryRowContext(ctx,
		`SELECT count(*) FROM tracks WHERE playable = 1 AND loudness_lufs = 0`).Scan(&failed)
	out["analysis_failures"] = failed

	// HOW THE DJ IS DOING, which the validator has always counted and nothing
	// ever read. "Why is it quiet?" is answered by the drop reasons, and they
	// were in memory with no way to see them.
	if b := a.breakStats(); b != nil {
		out["breaks"] = b
	}

	out["said_lines"] = countOf(ctx, db, `SELECT count(*) FROM said_lines`)
	out["adverts"] = countOf(ctx, db, `SELECT count(*) FROM ads`)
	out["feedback"] = recentFeedback(ctx, db)
	out["cadence"] = a.liveCadence()
	out["enriching"] = a.enriching.Load()
	return out
}

// breakStats is how the DJ is doing, per station and in total.
//
// PER STATION, because a drop rate mixed across stations describes neither: two
// stations are two broadcasts, with their own jocks, their own tracks and their
// own reasons for dropping a break. The total is kept alongside it as the
// answer to "is the DJ working at all", which is the question the console asks
// first.
func (a *App) breakStats() map[string]any {
	perStation := map[string]any{}
	var aired, dropped, ungrounded int
	reasons := map[string]int{}
	next := ""

	a.eachBreaks(func(id int64, br *stationBreaks) {
		if br.writer == nil {
			return
		}
		st, ok := br.writer.Stats()
		if !ok {
			return
		}
		own := map[string]int{}
		for r, n := range st.Reasons {
			own[string(r)] = n
			reasons[string(r)] += n
		}
		aired += st.Breaks
		dropped += st.Drops
		ungrounded += st.Ungrounded
		// THE MOST INTERESTING ANSWER ACROSS THE DIAL, in that order: a station
		// writing a break beats one holding a finished break, which beats one
		// with nothing coming. Testing "next == ''" only worked for the first
		// station, because Outlook never returns an empty string -- so a
		// station that was ready could never displace another's "none".
		outlook := string(br.pipeline.Outlook())
		next = moreInteresting(next, outlook)
		perStation[strconv.FormatInt(id, 10)] = map[string]any{
			"aired": st.Breaks, "dropped": st.Drops,
			"drop_rate": dropRate(st.Breaks, st.Drops), "reasons": own,
			"mislabelled_facts": st.Ungrounded,
			"next":              outlook,
		}
	})
	if len(perStation) == 0 {
		return nil
	}
	return map[string]any{
		"aired": aired, "dropped": dropped,
		"drop_rate": dropRate(aired, dropped), "reasons": reasons,
		"mislabelled_facts": ungrounded,
		"next":              next,
		"by_station":        perStation,
	}
}

// moreInteresting picks the outlook an operator most needs to see.
func moreInteresting(a, b string) string {
	if outlookRank(b) > outlookRank(a) {
		return b
	}
	return a
}

func outlookRank(o string) int {
	switch o {
	case string(station.OutlookWriting):
		return 2
	case string(station.OutlookReady):
		return 1
	case "":
		// NOTHING YET, which every real outlook beats. Ranking it alongside
		// "none" left the summary empty when every station had nothing coming,
		// which reads as a missing field rather than as a quiet dial.
		return -1
	default:
		return 0
	}
}

func dropRate(aired, dropped int) float64 {
	total := aired + dropped
	if total == 0 {
		return 0
	}
	return float64(dropped) / float64(total)
}

// liveCadence is the cadence the station is running, for the console.
//
// The PIPELINE, not the config: an operator who sets 1 and reloads the page
// must see 1. Reported twice -- the first time the server really had forgotten
// it, the second time the server remembered and the console drew its own
// hardcoded default over the answer.
func (a *App) liveCadence() int {
	// ONE SETTING ACROSS EVERY STATION, so any live pipeline answers for all of
	// them. Reading the first that has one avoids reporting zero from a station
	// that has not started yet.
	n := 0
	a.eachBreaks(func(_ int64, br *stationBreaks) {
		if n == 0 && br.pipeline != nil {
			n = br.pipeline.EveryN()
		}
	})
	// Then the single pair, which is the spike path's and the one a station
	// inherits before any station has come up.
	if n == 0 && a.opts.Breaks != nil {
		n = a.opts.Breaks.EveryN()
	}
	if n == 0 {
		n = a.liveCadenceSetting()
	}
	if n > 0 {
		return n
	}
	return a.cfg.BreakEveryNTracks
}

func countOf(ctx context.Context, db *sql.DB, q string) int {
	var n int
	_ = db.QueryRowContext(ctx, q).Scan(&n)
	return n
}

// recentFeedback is the thumbs-down feed.
//
// It closes a loop that was open: /feedback has been writing verdicts into
// break_feedback and NOTHING READ THEM. A signal from a real ear that nobody
// can see is not a signal.
func recentFeedback(ctx context.Context, db *sql.DB) []map[string]any {
	rows, err := db.QueryContext(ctx,
		`SELECT verdict, coalesce(jock_id,''), text, at FROM break_feedback
		  ORDER BY at DESC LIMIT 20`)
	if err != nil {
		return nil
	}
	defer rows.Close() //nolint:errcheck // read-only

	// An empty slice rather than nil, so the JSON is [] and not null: every
	// consumer would otherwise need its own null check.
	out := []map[string]any{}
	for rows.Next() {
		var verdict, jock, text string
		var at int64
		if err := rows.Scan(&verdict, &jock, &text, &at); err != nil {
			return out
		}
		out = append(out, map[string]any{"verdict": verdict, "jock": jock, "text": text, "at": at})
	}
	return out
}

// SetCadence changes how many tracks pass between breaks, without a restart.
//
// The deferred month named this the single most likely thing to be
// misconfigured, and until now changing it meant editing a compose file and
// restarting the station.
func (a *App) SetCadence(n int) error {
	if n < 1 || n > 100 {
		return fmt.Errorf("cadence must be between 1 and 100 tracks, got %d", n)
	}
	if !a.hasDJ() {
		return fmt.Errorf("no DJ is running")
	}
	// EVERY STATION, and the default a later one inherits. Written under
	// breaksMu and released before the pipelines are touched, because
	// eachBreaks takes the same lock.
	a.setLiveCadence(n)
	a.eachBreaks(func(_ int64, br *stationBreaks) {
		br.pipeline.SetCadence(station.NewCadence(n))
	})
	if a.opts.Breaks != nil {
		a.opts.Breaks.SetCadence(station.NewCadence(n))
	}

	// THE CONFIG FIELD IS NOT UPDATED HERE. It is the startup default, read
	// without a lock across this package, and writing it from an HTTP handler
	// raced with the overview handler reading it. That race was reintroduced
	// once by a line placed directly above this comment; the guarded field is
	// where the live cadence lives now.

	// WRITTEN DOWN, not just applied. Everything else about a station survives
	// a restart; a cadence that does not is a setting the operator has to
	// remember to redo, and will not.
	if a.opts.Library != nil && a.opts.Library.Store != nil {
		if err := a.opts.Library.Store.SetSetting(
			context.Background(), cadenceSetting, strconv.Itoa(n)); err != nil {
			// Applied but not remembered. Worth saying, not worth refusing:
			// the operator asked for this cadence now.
			a.log.Warn("break cadence changed but could not be saved", "err", err)
		}
	}
	a.log.Info("break cadence changed", "every_n_tracks", n)
	return nil
}

// SetEnriching pauses or resumes the background enrichment worker.
//
// Pausing is a real need rather than a toggle for its own sake: enrichment is
// the heaviest thing this process does, and an operator who wants the machine
// back for an evening currently has to stop the station to get it.
func (a *App) SetEnriching(on bool) error {
	a.enriching.Store(on)
	a.log.Info("enrichment", "running", on)
	return nil
}
