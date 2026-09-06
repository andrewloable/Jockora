// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/andrewloable/jockora/internal/supervise"
)

// LLMServerConfig describes where the language model comes from.
//
// Two modes, and the operator picks by which field they set:
//
//	BaseURL   use a llama-server somebody else is running
//	ModelPath start one and supervise it, the way the speech sidecar is
//
// The second exists so Jockora can be ONE thing to run. It does not embed
// llama.cpp: that is C++, so linking it means cgo, and CGO_ENABLED=0 is what
// keeps this binary static and cross-compilable -- a CI gate asserts it. A
// supervised child gets the same experience without giving that up, and it
// keeps the model swappable without a rebuild.
type LLMServerConfig struct {
	// BaseURL is an existing server. Takes precedence over ModelPath.
	BaseURL string

	// ModelPath is a GGUF to serve. Requires Binary.
	ModelPath string
	// Binary is llama-server. Default "llama-server", found on PATH.
	Binary string
	// ContextSize is -c. Default 8192.
	ContextSize int
	// GPULayers is -ngl. Default 99, which offloads everything that fits and
	// silently falls back to CPU for the rest.
	GPULayers int
	// ExtraArgs are appended verbatim, so any llama-server flag is reachable
	// without this struct having to know about it.
	ExtraArgs []string

	// StartTimeout allows for a cold model load, which is minutes on a large
	// GGUF from spinning storage.
	StartTimeout time.Duration

	Log *slog.Logger
}

// LLMServer is a language model Jockora can talk to, managed or not.
type LLMServer struct {
	*supervise.Process
	client *LlamaCPP
}

// StartLLMServer attaches to a server or starts one.
func StartLLMServer(ctx context.Context, cfg LLMServerConfig) (*LLMServer, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.StartTimeout <= 0 {
		// A cold GGUF load is minutes on a large model, and failing at three
		// seconds would look like a broken server rather than a slow disk.
		cfg.StartTimeout = 5 * time.Minute
	}

	sc := supervise.Config{
		Name: "llama-server",
		// llama-server answers /health with 200 once the model is loaded, so
		// "healthy" means READY rather than merely listening.
		HealthPath:   "/health",
		StartTimeout: cfg.StartTimeout,
		Log:          cfg.Log,
	}

	switch {
	case cfg.BaseURL != "":
		sc.Addr = cfg.BaseURL
	case cfg.ModelPath != "":
		if _, err := os.Stat(cfg.ModelPath); err != nil {
			return nil, fmt.Errorf("enrich: model %s: %w", cfg.ModelPath, err)
		}
		sc.Command = buildLlamaArgs(cfg)
	default:
		return nil, fmt.Errorf("enrich: the language model needs either a URL or a model file")
	}

	p, err := supervise.Start(ctx, sc)
	if err != nil {
		return nil, err
	}
	return &LLMServer{Process: p, client: NewLlamaCPP(p.BaseURL(), nil)}, nil
}

// buildLlamaArgs assembles the argv, leaving the address as placeholders for
// the supervisor to fill in with the port it reserved.
func buildLlamaArgs(cfg LLMServerConfig) []string {
	binary := cfg.Binary
	if binary == "" {
		binary = "llama-server"
	}
	ctxSize := cfg.ContextSize
	if ctxSize == 0 {
		ctxSize = 8192
	}
	ngl := cfg.GPULayers
	if ngl == 0 {
		ngl = 99
	}

	args := []string{
		binary,
		"-m", cfg.ModelPath,
		"--host", supervise.PlaceholderHost,
		"--port", supervise.PlaceholderPort,
		"-c", fmt.Sprint(ctxSize),
		"-ngl", fmt.Sprint(ngl),
	}
	return append(args, cfg.ExtraArgs...)
}

// Completer is the client for this server.
func (s *LLMServer) Completer() Completer { return s.client }
