// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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

	stats, err := library.Scan(ctx, s, cfg.LibraryPath)
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
		log.Info("no DJ: no persona configured", "fix", "start with -persona personas/midnight_vale.toml")
		return nil, nil, nil
	}
	persona, err := dj.LoadPersona(cfg.PersonaPath)
	if err != nil {
		log.Error("no DJ: the persona could not be loaded", "path", cfg.PersonaPath, "err", err)
		return nil, nil, nil
	}

	llm := enrich.NewLlamaCPP(cfg.LLMBaseURL, nil)
	if err := llm.Health(ctx); err != nil {
		log.Info("no DJ: the language model is not reachable",
			"url", cfg.LLMBaseURL, "err", err, "fix", "start llama-server, or set -llm-url")
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

// buildEnricher returns the background dossier worker, or nil.
func buildEnricher(ctx context.Context, cfg *config.Config, lib *app.Library, log *slog.Logger) *enrich.Queue {
	llm := enrich.NewLlamaCPP(cfg.LLMBaseURL, nil)
	if err := llm.Health(ctx); err != nil {
		log.Info("no enrichment: the language model is not reachable",
			"url", cfg.LLMBaseURL, "fix", "start llama-server, or set -llm-url")
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
