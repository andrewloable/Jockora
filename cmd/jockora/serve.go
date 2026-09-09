// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrewloable/jockora/internal/app"
	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/library"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
	"github.com/andrewloable/jockora/internal/tts"
)

// buildLibrary opens the database, scans if asked, and returns a music source.
//
// The scan runs at startup rather than in the background because selection
// cannot begin without it, and an empty library is not a station. Enrichment,
// which takes hours, is the part that runs alongside the stream.
func buildLibrary(ctx context.Context, cfg *config.Config, log *slog.Logger) (*app.Library, error) {
	s, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, err
	}

	// Progress every few hundred files. A ten-thousand-file library takes
	// minutes -- every file is probed with ffprobe -- and a scan that says
	// nothing looks exactly like a hang, which is how it was first read.
	src := librarySource(cfg)
	log.Info("scanning library, this takes a while on a large one", "source", src.Name())
	started := time.Now()
	stats, err := src.Scan(ctx, s, func(found int, path string) {
		if found%500 == 0 {
			log.Info("scanning", "files", found, "elapsed", time.Since(started).Round(time.Second))
		}
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", src.Name(), err)
	}
	log.Info("library scanned", "source", src.Name(),
		"found", stats.Found, "added", stats.Added, "updated", stats.Updated, "skipped", stats.Skipped, "unplayable", stats.Unplayable)
	for _, rejected := range stats.Rejected {
		log.Warn("file rejected at scan time", "err", rejected.Error())
	}

	pool, err := station.PlayablePool(ctx, s)
	if err != nil {
		return nil, err
	}
	if len(pool) == 0 {
		return nil, fmt.Errorf("no playable tracks under %s", cfg.LibraryPath)
	}
	log.Info("station pool", "tracks", len(pool))

	selector := station.NewSelector(pool, time.Now().UnixNano())

	// Tempo smoothing, if any tempos are known. On a library the analyser has
	// not reached yet this is empty and the selector behaves exactly as it did
	// before -- absence means no opinion, never zero.
	if energy, err := station.LoadEnergy(ctx, s); err == nil && energy.Known() > 0 {
		selector.Energy = energy
		log.Info("tempo smoothing enabled", "tracks_with_bpm", energy.Known(), "of", len(pool))
	}

	return &app.Library{Store: s, Selector: selector}, nil
}

// buildBreaks assembles the DJ, or explains why it could not and returns nil.
//
// NIL IS A COMPLETELY VALID RESULT and the calling path treats it that way. A
// library with no language model is a shuffle, which is a working product; a
// station that refuses to start because a model is down is not. Every reason
// for returning nil is logged at INFO with what to do about it, because the
// failure a listener notices is silence, and the failure an operator needs to
// see is "the DJ never started".
func buildBreaks(ctx context.Context, cfg *config.Config, lib *app.Library, log *slog.Logger,
	llm *enrich.Switchable) (
	func(int64) (*station.Pipeline, *station.BreakWriter), *station.Pipeline, *station.BreakWriter, *tts.Sidecar) {
	if cfg.PersonaPath == "" {
		log.Info("no DJ: no persona configured", "fix", "start with -persona personas/ to pick a jock by genre")
		return nil, nil, nil, nil
	}
	// THE ROTATION READS THE DATABASE ON EVERY PICK, so an advert added,
	// edited or deleted in the console is heard on the next slot without
	// restarting the station. The pack's own adverts are imported once, as
	// ordinary rows, so they are editable like everything else.
	rotation := liveAdRotation(ctx, cfg.PersonaPath, lib, log)

	persona, err := choosePersona(ctx, cfg.PersonaPath, lib, log)
	if err != nil {
		log.Error("no DJ: the persona could not be loaded", "path", cfg.PersonaPath, "err", err)
		return nil, nil, nil, nil
	}

	sidecar, err := tts.Start(ctx, tts.Config{
		Python:       cfg.TTSPython,
		Script:       cfg.TTSScript,
		Voice:        persona.VoiceID(),
		StartTimeout: 3 * time.Minute,
		Log:          log,
	})
	if err != nil {
		log.Info("no DJ: the speech sidecar would not start",
			"err", err, "fix", "check -tts-python and -tts-script, and that kokoro-onnx is installed")
		return nil, nil, nil, nil
	}

	// ONE PER STATION, built on demand.
	//
	// Almost everything below is per-broadcast state: the track context a break
	// is written about, the cold open, the fact cooldown, the exemption list of
	// a station's own titles, the drop counters, the cadence position. Two
	// stations sharing one box meant B's break was written about A's records
	// and B's first break was not a cold open because A had already aired one.
	//
	// SHARED ON PURPOSE, and only these: the model client, the speech sidecar
	// and the store. All three are stateless per request, and the said-lines
	// index is keyed by JOCK -- a listener moving between two stations with the
	// same jock should not hear the same opening twice, which is the whole
	// reason that index exists.
	// The ROTATION is shared too, and deliberately: adverts are global now, so
	// two stations must share the cooldown or a listener flipping between them
	// hears the same advert twice. It holds no per-station state -- the pool
	// and the cooldown both live in the database.
	newBreaks := func(stationID int64) (*station.Pipeline, *station.BreakWriter) {
		return buildStationBreaks(cfg, lib, log, llm, sidecar, persona, rotation, stationID)
	}
	pipeline, writer := newBreaks(0)
	log.Info("DJ ready", "persona", persona.Name(), "voice", persona.VoiceID(),
		"break_every_n_tracks", cfg.BreakEveryNTracks, "tts", sidecar.Addr())
	return newBreaks, pipeline, writer, sidecar
}

// buildStationBreaks makes one station's pipeline and writer.
func buildStationBreaks(cfg *config.Config, lib *app.Library, log *slog.Logger,
	llm enrich.Completer, sidecar *tts.Sidecar, persona *dj.Persona,
	rotation *dj.AdRotation, stationID int64) (*station.Pipeline, *station.BreakWriter) {

	writer := &station.BreakWriter{
		// The station's own jock replaces this the moment it goes on air; the
		// library-wide pick is what a station with no jock assigned falls back
		// to, and what the spike path uses.
		Persona:   persona,
		Validator: &dj.Validator{Writer: dj.NewWriter(llm, 0), Said: &dj.SaidLines{Store: lib.Store, JockID: persona.ID()}},
		Session:   dj.NewSession(),
		Store:     lib.Store,
	}

	pipeline := &station.Pipeline{
		// SET HERE AND NEVER AGAIN. It used to be rebound on every feed, on the
		// feed goroutine, while the break ticker read it on its own -- a race
		// the detector finds in a few hundred iterations. A pipeline and its
		// writer are built together, so there is no window to race in.
		Writer: writer,
		// THIS is where config.BreakEveryNTracks finally has a consumer. It was
		// parsed, env-mapped and defaulted since the spine and read by nothing.
		Cadence: station.NewCadence(cfg.BreakEveryNTracks),
		// AND THE LEAD-IN, on the same line of reasoning: the flag is the
		// startup default and this is where a pipeline gets it. Bounded by each
		// track's measured outro at use, so a high value is never a break over
		// a vocal -- it just stops being reached on short tails.
		OverlapSeconds: cfg.BreakOverlapS,
		Lookahead:      station.NewLookahead(0),
		Length: station.LengthCheck{
			// The DJ, wrapped so one break slot in four becomes an advert when
			// the pack carries a pool. An advert then travels the SAME ladder a
			// break does -- render, length check, relocate, enqueue -- so
			// nothing proven about airing breaks has to be proven again.
			// BUILT ONCE, HERE, beside the writer, exactly where the old
			// pool-loading rotation was: the rotation OBJECT never changes,
			// only what it reads, so the race the comment above describes
			// cannot come back. Jockora-iyw.3.
			Writer: station.NewAdWriter(writer, rotation, clock.Real{}),
			Renderer: station.RendererFunc(func(ctx context.Context, text, voice, outPath string) (float64, error) {
				return tts.Render(ctx, sidecar, text, voice, outPath)
			}),
			Dir: cfg.SegmentDir,
			// Asked at render time, not captured here: a listener can change
			// the jock while the station runs, and a break must be spoken by
			// whoever is on air now.
			Voice:   persona.VoiceID(),
			VoiceOf: writer.Voice,
		},
		FadeSeconds: cfg.CrossfadeSeconds,
		Clock:       clock.Real{},
		Log:         log.With("station", stationID),
	}
	return pipeline, writer
}

// startLLM attaches to a model server or starts one, whichever is configured.
//
// -llm-model wins over -llm-url when both are set: asking Jockora to run a
// specific model is the more specific instruction.
// newCompleter returns the language model client, whichever kind is configured.
//
// Ollama is reached over HTTP like any other external server, so nothing is
// supervised: it has its own lifecycle and Jockora must not act as though it
// owns it.
func newCompleter(ctx context.Context, cfg *config.Config, log *slog.Logger) (enrich.Completer, func(), error) {
	if strings.EqualFold(cfg.LLMAPI, enrich.OpenAIAPI) {
		c := enrich.NewOpenAICompatible(cfg.LLMBaseURL, cfg.LLMModelPath, cfg.LLMAPIKey, nil)
		if err := c.Health(ctx); err != nil {
			return nil, nil, err
		}
		// Said at INFO every start, deliberately. This is the one configuration
		// where the library's metadata and lyrics leave the machine, and a
		// self-hoster should never discover that from a network capture.
		log.Info("using a hosted language model; TRACK METADATA AND LYRICS LEAVE THIS MACHINE",
			"url", cfg.LLMBaseURL, "model", cfg.LLMModelPath)
		return c, func() {}, nil
	}

	if strings.EqualFold(cfg.LLMAPI, enrich.OllamaAPI) {
		c := enrich.NewOllama(cfg.LLMBaseURL, cfg.LLMModelPath, nil)
		if err := c.Health(ctx); err != nil {
			return nil, nil, err
		}
		log.Info("using an Ollama server", "url", cfg.LLMBaseURL, "model", cfg.LLMModelPath)
		return c, func() {}, nil
	}

	server, err := startLLM(ctx, cfg, log)
	if err != nil {
		return nil, nil, err
	}
	return server.Completer(), func() { _ = server.Close() }, nil
}

func startLLM(ctx context.Context, cfg *config.Config, log *slog.Logger) (*enrich.LLMServer, error) {
	c := enrich.LLMServerConfig{
		Binary:      cfg.LLMBinary,
		ContextSize: cfg.LLMContextSize,
		GPULayers:   cfg.LLMGPULayers,
		Log:         log,
	}
	if cfg.LLMModelPath != "" {
		c.ModelPath = cfg.LLMModelPath
		log.Info("starting a language model", "model", cfg.LLMModelPath, "binary", cfg.LLMBinary)
	} else {
		c.BaseURL = cfg.LLMBaseURL
	}
	return enrich.StartLLMServer(ctx, c)
}

// buildLLM is the ONE switchable client the DJ and the enricher share.
//
// Shared so a change made in the console reaches both at once, and switchable
// so it reaches them without a restart. Seeded from the STORED choice when the
// operator has made one, because that is what they last said; the flags and the
// environment are the default underneath it, exactly as the cadence works.
//
// It may start EMPTY. A station whose model has gone away must still come up:
// the console is where the model is chosen, and refusing to start locks the
// operator out of the screen that fixes it.
func buildLLM(ctx context.Context, cfg *config.Config, lib *app.Library, log *slog.Logger) *enrich.Switchable {
	if lib != nil && lib.Store != nil {
		if s, ok, err := lib.Store.LLMSettings(ctx); err == nil && ok {
			c := enrich.LLMConfig{Provider: s.Provider, URL: s.URL,
				Account: s.Account, Model: s.Model, Key: s.Key}
			if client, err := c.Client(nil); err == nil {
				log.Info("language model from the console", "provider", s.Provider, "model", s.Model)
				return enrich.NewSwitchable(client)
			}
			log.Warn("the saved language model cannot be used; choose another in the console",
				"provider", s.Provider, "model", s.Model)
		}
	}
	client, _, err := newCompleter(ctx, cfg, log)
	if err != nil {
		log.Info("no language model yet; choose one in the operator console",
			"err", err, "where", "/admin, Language model")
		return enrich.NewSwitchable(nil)
	}
	return enrich.NewSwitchable(client)
}

// buildEnricher returns the background dossier worker, or nil.
func buildEnricher(llm *enrich.Switchable, lib *app.Library, log *slog.Logger) *enrich.Queue {
	return &enrich.Queue{
		Store:   lib.Store,
		LLM:     llm,
		Lyrics:  enrich.NewLRCLib(lrclibURL, nil),
		Artists: enrich.NewMusicBrainz(musicbrainzURL, &http.Client{Timeout: 20 * time.Second}, clock.Real{}),
		Log:     log,
	}
}

// musicbrainzURL is the public API. Throttled to one request a second by the
// client, which is MusicBrainz's published limit.
const musicbrainzURL = "https://musicbrainz.org"

// choosePersona picks the jock, from a single card or from a directory of them.
//
// A DIRECTORY is the interesting case: the station's own dossiers say what it
// plays, and the jock follows the music rather than a setting somebody has to
// remember to change. A rock library gets the rock presenter without being told
// it is a rock library.
//
// A library with no dossiers yet still gets a jock. Enrichment takes hours and
// the station has to be listenable from the first minute; the choice simply
// improves once there is something to choose on.
func choosePersona(ctx context.Context, path string, lib *app.Library, log *slog.Logger) (*dj.Persona, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return dj.LoadPersona(path)
	}

	personas, err := dj.LoadPersonas(path)
	if err != nil {
		return nil, err
	}

	taste, err := enrich.Taste(ctx, lib.Store, 6)
	if err != nil {
		return nil, err
	}
	if len(taste.Genres) == 0 && len(taste.Moods) == 0 {
		chosen, _ := dj.SelectPersona(personas, nil, nil)
		log.Info("jock chosen with nothing to go on yet",
			"jock", chosen.Name(), "of", len(personas),
			"why", "no dossiers yet; enrichment will not change the jock mid-session")
		return chosen, nil
	}

	chosen, scores := dj.SelectPersona(personas, taste.Genres, taste.Moods)
	log.Info("jock chosen from what the library plays",
		"jock", chosen.Name(), "voice", chosen.VoiceID(),
		"genres", taste.Genres, "moods", taste.Moods,
		"matched", matchedOf(scores, chosen.ID()), "of", len(personas))
	return chosen, nil
}

func matchedOf(scores []dj.PersonaScore, id string) []string {
	for _, s := range scores {
		if s.Persona.ID() == id {
			return s.Matched
		}
	}
	return nil
}

// loadRoster reads every persona available, for the DIAL rather than for the
// session's jock.
//
// The two are different questions. choosePersona picks ONE jock for the whole
// session from what the library sounds like overall; the dial matches a jock
// PER STATION, so it needs the whole roster. A single .toml is a roster of one.
// Failure is not fatal: a dial without jocks is still a dial.
func loadRoster(path string, log *slog.Logger) []*dj.Persona {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		p, err := dj.LoadPersona(path)
		if err != nil {
			return nil
		}
		return []*dj.Persona{p}
	}
	all, err := dj.LoadPersonas(path)
	if err != nil {
		log.Warn("could not load the persona roster for the dial", "path", path, "err", err)
		return nil
	}
	return all
}

// librarySource picks where the music comes from.
//
// An OpenSubsonic server wins when one is configured, because an operator who
// set -subsonic-url meant it. Both satisfy the same read-only interface, so
// nothing downstream knows or cares which one answered.
func librarySource(cfg *config.Config) library.Source {
	if cfg.SubsonicURL != "" {
		return library.Subsonic{
			BaseURL:  cfg.SubsonicURL,
			User:     cfg.SubsonicUser,
			Password: cfg.SubsonicPassword,
		}
	}
	return library.Folder{Root: cfg.LibraryPath}
}

// liveAdRotation is the rotation the station airs from.
//
// It reads the ads table on every pick rather than loading a pool at start,
// because a station runs while it has a listener -- a busy one for days -- and
// an operator who deletes an advert and keeps hearing it all afternoon reports
// it as broken. One small SELECT roughly every fourth break is nothing.
//
// A JockPack's own adverts are IMPORTED as rows the first time the pack is
// seen, which keeps the decision that a pack travels with its adverts while
// making them editable and deletable like everything the operator wrote.
func liveAdRotation(ctx context.Context, personaPath string, lib *app.Library, log *slog.Logger) *dj.AdRotation {
	if lib == nil || lib.Store == nil {
		return nil
	}
	if ads := packAds(personaPath); len(ads) > 0 {
		n, err := app.ImportPackAds(ctx, lib.Store, ads)
		if err != nil {
			// Not fatal: the rotation still reads whatever the table holds,
			// and an advert that failed to import is one advert.
			log.Warn("importing pack adverts", "err", err)
		} else if n > 0 {
			// ONCE, AT IMPORT, and it says the consequence rather than the
			// count alone: they are in the shared pool now, every jock reads
			// them, and the operator can edit or delete any of them.
			log.Info("pack adverts imported into the shared pool -- every jock reads them, "+
				"and you can edit or delete them in Ads",
				"adverts", n, "from", personaPath)
		}
	}
	r := dj.NewAdRotation(nil)
	r.Source = app.AdSource(lib.Store)
	r.Aired = app.AdAired(lib.Store)
	return r
}

// packAds reads every advert a persona directory's packs carry.
func packAds(personaPath string) []dj.Ad {
	if personaPath == "" {
		return nil
	}
	info, err := os.Stat(personaPath)
	if err != nil {
		return nil
	}
	paths := []string{personaPath}
	if info.IsDir() {
		found, err := filepath.Glob(filepath.Join(personaPath, "*.toml"))
		if err != nil || len(found) == 0 {
			return nil
		}
		paths = found
	}

	var ads []dj.Ad
	for _, path := range paths {
		pack, err := dj.LoadPack(path)
		if err != nil {
			// A pack that fails to load is already reported by the persona
			// loader; here it just contributes no adverts.
			continue
		}
		ads = append(ads, pack.Ads()...)
	}
	return ads
}

// buildAnalyser measures loudness and tempo in the background.
//
// Loudness is the audible half and needs only ffmpeg, so it runs whether or not
// a sidecar is configured. Tempo needs librosa in the speech sidecar, and its
// absence costs only the selector's smoothing.
// ttsAddr is where the sidecar actually is.
//
// A MANAGED sidecar knows its address only once it is running, so it is asked
// rather than read from config; a configured -tts-url is the fallback for one
// the operator runs themselves. ONE function because there are two callers --
// the analyser and the console's voice list -- and when the second read config
// directly instead, the voice picker was empty in every default deployment.
func ttsAddr(cfg *config.Config, sidecar *tts.Sidecar) string {
	if sidecar != nil && sidecar.Addr() != "" {
		return "http://" + sidecar.Addr()
	}
	return cfg.TTSAddr
}

func buildAnalyser(cfg *config.Config, lib *app.Library, sidecar *tts.Sidecar, log *slog.Logger) *library.Analyser {
	a := &library.Analyser{Store: lib.Store, Log: log}

	addr := ttsAddr(cfg, sidecar)
	if addr != "" {
		a.BPM = enrich.NewOnsetClient(addr, nil)
		log.Info("tempo measurement enabled", "sidecar", addr)
	}
	return a
}
