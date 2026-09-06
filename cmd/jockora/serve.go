// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
	log.Info("scanning library, this takes a while on a large one", "root", cfg.LibraryPath)
	started := time.Now()
	stats, err := library.ScanWithProgress(ctx, s, cfg.LibraryPath, func(found int, path string) {
		if found%500 == 0 {
			log.Info("scanning", "files", found, "elapsed", time.Since(started).Round(time.Second))
		}
	})
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", cfg.LibraryPath, err)
	}
	log.Info("library scanned", "root", cfg.LibraryPath,
		"found", stats.Found, "added", stats.Added, "updated", stats.Updated, "unplayable", stats.Unplayable)
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

	return &app.Library{
		Store:    s,
		Selector: station.NewSelector(pool, time.Now().UnixNano()),
	}, nil
}

// buildBreaks assembles the DJ, or explains why it could not and returns nil.
//
// NIL IS A COMPLETELY VALID RESULT and the calling path treats it that way. A
// library with no language model is a shuffle, which is a working product; a
// station that refuses to start because a model is down is not. Every reason
// for returning nil is logged at INFO with what to do about it, because the
// failure a listener notices is silence, and the failure an operator needs to
// see is "the DJ never started".
func buildBreaks(ctx context.Context, cfg *config.Config, lib *app.Library, log *slog.Logger) (*station.Pipeline, *station.BreakWriter, *tts.Sidecar) {
	if cfg.PersonaPath == "" {
		log.Info("no DJ: no persona configured", "fix", "start with -persona personas/ to pick a jock by genre")
		return nil, nil, nil
	}
	persona, err := choosePersona(ctx, cfg.PersonaPath, lib, log)
	if err != nil {
		log.Error("no DJ: the persona could not be loaded", "path", cfg.PersonaPath, "err", err)
		return nil, nil, nil
	}

	llm, _, err := newCompleter(ctx, cfg, log)
	if err != nil {
		log.Info("no DJ: no language model", "err", err,
			"fix", "point -llm-url at a running server, add -llm-api ollama for Ollama, or set -llm-model to have Jockora run llama-server itself")
		return nil, nil, nil
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
		return nil, nil, nil
	}

	writer := &station.BreakWriter{
		Persona:   persona,
		Validator: &dj.Validator{Writer: dj.NewWriter(llm, 0), Said: &dj.SaidLines{Store: lib.Store, JockID: persona.ID()}},
		Session:   dj.NewSession(),
		Store:     lib.Store,
	}

	pipeline := &station.Pipeline{
		// THIS is where config.BreakEveryNTracks finally has a consumer. It was
		// parsed, env-mapped and defaulted since the spine and read by nothing.
		Cadence:   station.NewCadence(cfg.BreakEveryNTracks),
		Lookahead: station.NewLookahead(0),
		Length: station.LengthCheck{
			Writer: writer,
			Renderer: station.RendererFunc(func(ctx context.Context, text, voice, outPath string) (float64, error) {
				return tts.Render(ctx, sidecar, text, voice, outPath)
			}),
			Dir:   cfg.SegmentDir,
			Voice: persona.VoiceID(),
		},
		FadeSeconds: cfg.CrossfadeSeconds,
		Clock:       clock.Real{},
		Log:         log,
	}
	log.Info("DJ ready", "persona", persona.Name(), "voice", persona.VoiceID(),
		"break_every_n_tracks", cfg.BreakEveryNTracks, "tts", sidecar.Addr())
	return pipeline, writer, sidecar
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

// buildEnricher returns the background dossier worker, or nil.
func buildEnricher(ctx context.Context, cfg *config.Config, lib *app.Library, log *slog.Logger) *enrich.Queue {
	llm, _, err := newCompleter(ctx, cfg, log)
	if err != nil {
		log.Info("no enrichment: no language model", "err", err,
			"fix", "set -llm-url (with -llm-api ollama if that is what you run) or -llm-model")
		return nil
	}
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
