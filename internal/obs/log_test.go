// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func capture(t *testing.T) (*Logger, func() []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	l := New(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	return l, func() []map[string]any {
		var out []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("log line is not JSON: %q", line)
			}
			out = append(out, rec)
		}
		return out
	}
}

func TestBreakDroppedLogsReason(t *testing.T) {
	valid := map[string]bool{
		ReasonValidatorReject: true, ReasonLLMTimeout: true, ReasonLLMRefusal: true,
		ReasonSidecarDown: true, ReasonOverBudget: true, ReasonLate: true,
	}

	for reason := range valid {
		l, records := capture(t)
		l.BreakDropped(reason, "station", "midnight")

		recs := records()
		if len(recs) != 1 {
			t.Fatalf("%s: %d records, want exactly 1", reason, len(recs))
		}
		if recs[0]["event"] != EventBreakDropped {
			t.Errorf("event = %v, want %s", recs[0]["event"], EventBreakDropped)
		}
		got, _ := recs[0]["reason"].(string)
		if !valid[got] {
			t.Errorf("reason = %q, not one of the enumerated reasons", got)
		}
	}
}

func TestUnderrunLogs(t *testing.T) {
	l, records := capture(t)
	l.RingUnderrun(1024)

	recs := records()
	if len(recs) != 1 || recs[0]["event"] != EventRingUnderrun {
		t.Fatalf("records = %v", recs)
	}
	if recs[0]["frames_filled"] != float64(1024) {
		t.Errorf("frames_filled = %v, want 1024", recs[0]["frames_filled"])
	}
}

func TestDecoderSkipLogs(t *testing.T) {
	l, records := capture(t)
	l.DecoderSkip("/music/broken.mp3", "decode failed")

	recs := records()
	if len(recs) != 1 || recs[0]["event"] != EventDecoderSkip {
		t.Fatalf("records = %v", recs)
	}
	if recs[0]["path"] != "/music/broken.mp3" {
		t.Errorf("path = %v", recs[0]["path"])
	}
	if recs[0]["reason"] != "decode failed" {
		t.Errorf("reason = %v", recs[0]["reason"])
	}
}

func TestSupervisorRestartLogs(t *testing.T) {
	l, records := capture(t)
	l.EncoderRestart(3)

	recs := records()
	if len(recs) != 1 || recs[0]["event"] != EventEncoderRestart {
		t.Fatalf("records = %v", recs)
	}
	if recs[0]["restart_count"] != float64(3) {
		t.Errorf("restart_count = %v, want 3", recs[0]["restart_count"])
	}
}

// TestNoSecretsOrLyricsInLogs: a log file is storage, and lyrics are not stored
// anywhere. The one place a discarded lyric could reappear is a debug attribute
// somebody added in a hurry, so the redaction is enforced here rather than
// trusted to callers.
func TestNoSecretsOrLyricsInLogs(t *testing.T) {
	l, records := capture(t)

	l.BreakDropped(ReasonLate,
		"lyrics", "I left the harbour lights behind me",
		"api_key", "sk-secret-value",
		"token", "bearer-abc",
		"path", "/music/a.mp3")

	recs := records()
	raw, _ := json.Marshal(recs)
	for _, leak := range []string{"harbour lights", "sk-secret-value", "bearer-abc"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("a forbidden value reached the log: %q in %s", leak, raw)
		}
	}
	// A legitimate attribute survives.
	if recs[0]["path"] != "/music/a.mp3" {
		t.Errorf("a normal attribute was redacted: %v", recs[0]["path"])
	}
}

func TestForbiddenKeyMatching(t *testing.T) {
	for _, k := range []string{"lyrics", "Lyrics", "LYRIC", "api_key", "token", "secret",
		"synced_lyrics", "auth_token", "password"} {
		if !IsForbiddenKey(k) {
			t.Errorf("%q is not treated as forbidden", k)
		}
	}
	for _, k := range []string{"path", "reason", "station", "keyboard", "tokens_predicted"} {
		if IsForbiddenKey(k) {
			t.Errorf("%q was wrongly treated as forbidden", k)
		}
	}
}

// TestEverySilentPathHasAnEvent is the contract itself: the list of paths that
// degrade quietly is enumerated, so adding one without a log line is a test
// failure rather than an oversight discovered months later.
func TestEverySilentPathHasAnEvent(t *testing.T) {
	l, records := capture(t)

	l.BreakDropped(ReasonLate)
	l.RingUnderrun(1)
	l.DecoderSkip("/p", "r")
	l.EncoderRestart(1)
	l.SidecarRespawn(1)
	l.EnrichFailure("/p", "r")
	l.LLMRefusal("dossier")
	l.ValidatorReject("repetitive", "gram")

	want := []string{EventBreakDropped, EventRingUnderrun, EventDecoderSkip,
		EventEncoderRestart, EventSidecarRespawn, EventEnrichFailure,
		EventLLMRefusal, EventValidatorReject}

	recs := records()
	if len(recs) != len(want) {
		t.Fatalf("%d records, want %d", len(recs), len(want))
	}
	for i, w := range want {
		if recs[i]["event"] != w {
			t.Errorf("record %d event = %v, want %s", i, recs[i]["event"], w)
		}
	}
}

// TestNoBarePrintingInLogic enforces the DO NOT where it matters: one
// fmt.Println in a degradation path and the record it should have emitted is
// gone forever.
//
// cmd/ is deliberately NOT covered. That is the CLI surface, and `jockora
// doctor` printing a preflight table to stdout is exactly its job -- routing a
// human-readable report through a JSON log handler would make it unreadable for
// the one person it is written for. Everything with logic in it lives under
// internal/, and that is where the rule bites.
func TestNoBarePrintingInLogic(t *testing.T) {
	out, err := exec.Command("grep", "-rn",
		"-e", "fmt.Print", "-e", `[^/]log\.Print`, "--include=*.go", "../../internal").CombinedOutput()
	if err != nil && len(out) == 0 {
		return // grep found nothing
	}

	var offenders []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || strings.Contains(line, "_test.go") {
			continue
		}
		offenders = append(offenders, line)
	}
	if len(offenders) > 0 {
		t.Errorf("bare printing found outside tests:\n  %s", strings.Join(offenders, "\n  "))
	}
	_ = os.Stdout
}
