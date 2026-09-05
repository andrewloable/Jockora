// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package obs makes quiet degradation observable.
//
// This system's signature behaviour is failing quietly: a break drops, the ring
// runs dry, a decoder skips a file, and the listener hears nothing wrong. That
// is the design working. It also means that without logs, a silent DJ and a
// working DJ with a sparse library are indistinguishable.
//
// THE CONTRACT: every code path that silently degrades emits exactly ONE
// structured record naming the reason.
package obs

import (
	"log/slog"
	"strings"
)

// The events that make degradation visible. Every silently-degrading path in the
// program is on this list, and adding a new one means adding it here.
const (
	EventBreakDropped    = "break_dropped"
	EventRingUnderrun    = "ring_underrun"
	EventDecoderSkip     = "decoder_skip"
	EventEncoderRestart  = "encoder_restart"
	EventSidecarRespawn  = "sidecar_respawn"
	EventEnrichFailure   = "enrich_failure"
	EventLLMRefusal      = "llm_refusal"
	EventValidatorReject = "validator_reject"
)

// Reasons a break did not air.
const (
	ReasonValidatorReject = "validator_reject"
	ReasonLLMTimeout      = "llm_timeout"
	ReasonLLMRefusal      = "llm_refusal"
	ReasonSidecarDown     = "sidecar_down"
	ReasonOverBudget      = "over_budget"
	ReasonLate            = "late"
)

// forbiddenKeys must never appear as log attributes.
//
// Lyrics are not stored anywhere, so they must not appear here either; a log
// file is storage. Tokens and keys are the obvious other case.
var forbiddenKeys = []string{"lyric", "lyrics", "token", "key", "secret", "password", "api_key"}

// Logger wraps slog with the silent-path contract.
type Logger struct{ log *slog.Logger }

// New returns a Logger over an slog handler.
func New(l *slog.Logger) *Logger {
	if l == nil {
		l = slog.Default()
	}
	return &Logger{log: l}
}

// Slog exposes the underlying logger for code that needs it directly.
func (l *Logger) Slog() *slog.Logger { return l.log }

// BreakDropped records a break that did not air, and why.
//
// Breaks are optional by design, so this is never an error. It is, however,
// always logged: a rising drop rate is the failure mode that arrives slowly and
// silently.
func (l *Logger) BreakDropped(reason string, attrs ...any) {
	l.log.Warn(EventBreakDropped, append([]any{"event", EventBreakDropped, "reason", reason}, scrub(attrs)...)...)
}

// RingUnderrun records the mixer being handed silence because no audio was ready.
func (l *Logger) RingUnderrun(framesFilled int, attrs ...any) {
	l.log.Warn(EventRingUnderrun,
		append([]any{"event", EventRingUnderrun, "frames_filled", framesFilled}, scrub(attrs)...)...)
}

// DecoderSkip records a track that could not be decoded and was passed over.
func (l *Logger) DecoderSkip(path, reason string, attrs ...any) {
	l.log.Warn(EventDecoderSkip,
		append([]any{"event", EventDecoderSkip, "path", path, "reason", reason}, scrub(attrs)...)...)
}

// EncoderRestart records ffmpeg being replaced.
func (l *Logger) EncoderRestart(restartCount int, attrs ...any) {
	l.log.Warn(EventEncoderRestart,
		append([]any{"event", EventEncoderRestart, "restart_count", restartCount}, scrub(attrs)...)...)
}

// SidecarRespawn records the TTS sidecar being restarted.
func (l *Logger) SidecarRespawn(respawnCount int, attrs ...any) {
	l.log.Warn(EventSidecarRespawn,
		append([]any{"event", EventSidecarRespawn, "respawn_count", respawnCount}, scrub(attrs)...)...)
}

// EnrichFailure records a track whose enrichment did not produce a dossier.
func (l *Logger) EnrichFailure(path, reason string, attrs ...any) {
	l.log.Warn(EventEnrichFailure,
		append([]any{"event", EventEnrichFailure, "path", path, "reason", reason}, scrub(attrs)...)...)
}

// LLMRefusal records the model declining.
//
// Its own event because a refusal on every track means the persona prompt is
// tripping guardrails, and that diagnosis is invisible if it is folded into a
// generic failure.
func (l *Logger) LLMRefusal(where string, attrs ...any) {
	l.log.Warn(EventLLMRefusal,
		append([]any{"event", EventLLMRefusal, "where", where}, scrub(attrs)...)...)
}

// ValidatorReject records a break rejected for repetition or groundedness.
func (l *Logger) ValidatorReject(reason, detail string, attrs ...any) {
	l.log.Info(EventValidatorReject,
		append([]any{"event", EventValidatorReject, "reason", reason, "detail", detail}, scrub(attrs)...)...)
}

// scrub drops any attribute whose key looks like a secret or like lyric text.
//
// Enforced here rather than trusted to callers: a log line is storage, and the
// one place a discarded lyric could reappear is a debug attribute somebody added
// in a hurry.
func scrub(attrs []any) []any {
	out := make([]any, 0, len(attrs))
	for i := 0; i+1 < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if ok && IsForbiddenKey(key) {
			out = append(out, key, "[redacted]")
			continue
		}
		out = append(out, attrs[i], attrs[i+1])
	}
	if len(attrs)%2 == 1 {
		out = append(out, attrs[len(attrs)-1])
	}
	return out
}

// IsForbiddenKey reports whether an attribute name must never carry a value.
func IsForbiddenKey(key string) bool {
	low := strings.ToLower(key)
	for _, f := range forbiddenKeys {
		if low == f || strings.HasSuffix(low, "_"+f) || strings.HasPrefix(low, f+"_") {
			return true
		}
	}
	return false
}
