// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/app"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/store"
)

func TestUnknownSubcommandExitsNonZero(t *testing.T) {
	var stderr bytes.Buffer
	code := dispatch(context.Background(), []string{"brodcast"}, &stderr)

	if code != usageExit {
		t.Errorf("exit code = %d, want %d", code, usageExit)
	}
	out := stderr.String()
	if !strings.Contains(out, "brodcast") {
		t.Errorf("usage does not name the unknown subcommand:\n%s", out)
	}
	for _, want := range subcommands {
		if !strings.Contains(out, want) {
			t.Errorf("usage does not list %q:\n%s", want, out)
		}
	}
}

func TestNoArgumentsPrintsUsage(t *testing.T) {
	var stderr bytes.Buffer
	if code := dispatch(context.Background(), nil, &stderr); code != usageExit {
		t.Errorf("exit code = %d, want %d", code, usageExit)
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("no usage printed:\n%s", stderr.String())
	}
}

func TestVersionPrintsSemver(t *testing.T) {
	var out bytes.Buffer
	if code := dispatch(context.Background(), []string{"version"}, &out); code != 0 {
		t.Fatalf("version exited %d: %s", code, out.String())
	}
	got := strings.TrimSpace(out.String())
	if !regexp.MustCompile(`^jockora \d+\.\d+\.\d+`).MatchString(got) {
		t.Errorf("version printed %q, want a semver", got)
	}
}

// TestServeRefusesNewerSchema protects hours of dossier work. A downgrade that
// opened a newer database and wrote to it would corrupt exactly the data that
// took longest to build.
func TestServeRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "j.db")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE schema_version SET version = 999`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	if _, err := store.Open(context.Background(), dbPath); err == nil {
		t.Fatal("opened a database written by a newer jockora")
	} else if !strings.Contains(err.Error(), "999") || !strings.Contains(strings.ToLower(err.Error()), "upgrade") {
		t.Errorf("error = %v, want it to name the version and tell the operator to upgrade", err)
	}
}

// TestFlagsHaveEnvEquivalents pins the promise that every flag has a
// JOCKORA_-prefixed environment variable, which is what makes the container
// deployment possible at all -- compose sets environment, not argv.
func TestFlagsHaveEnvEquivalents(t *testing.T) {
	cases := []struct {
		env, value string
		get        func(*config.Config) string
	}{
		{"JOCKORA_LIBRARY_PATH", "/music", func(c *config.Config) string { return c.LibraryPath }},
		{"JOCKORA_DB_PATH", "/tmp/x.db", func(c *config.Config) string { return c.DBPath }},
		{"JOCKORA_SEGMENT_DIR", "/tmp/seg", func(c *config.Config) string { return c.SegmentDir }},
		{"JOCKORA_LISTEN_ADDR", "0.0.0.0:9999", func(c *config.Config) string { return c.ListenAddr }},
		{"JOCKORA_LLM_URL", "http://example:1", func(c *config.Config) string { return c.LLMBaseURL }},
		{"JOCKORA_TTS_URL", "http://example:2", func(c *config.Config) string { return c.TTSAddr }},
		{"JOCKORA_PERSONA", "/p.toml", func(c *config.Config) string { return c.PersonaPath }},
		{"JOCKORA_STATION", "midnight", func(c *config.Config) string { return c.StationName }},
		{"JOCKORA_LOG_FORMAT", "json", func(c *config.Config) string { return c.LogFormat }},
		{"JOCKORA_SEGMENT_SECONDS", "6", func(c *config.Config) string { return strconv.Itoa(c.SegmentSeconds) }},
		{"JOCKORA_LIST_SIZE", "20", func(c *config.Config) string { return strconv.Itoa(c.ListSize) }},
		{"JOCKORA_BREAK_EVERY_N_TRACKS", "7", func(c *config.Config) string { return strconv.Itoa(c.BreakEveryNTracks) }},
		{"JOCKORA_CROSSFADE_SECONDS", "3.5", func(c *config.Config) string { return strconv.FormatFloat(c.CrossfadeSeconds, 'g', -1, 64) }},
		{"JOCKORA_ALLOW_LAN", "true", func(c *config.Config) string { return strconv.FormatBool(c.AllowLAN) }},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			cfg, err := config.LoadArgs(nil, nil)
			if err != nil {
				t.Fatalf("loading with %s set: %v", tc.env, err)
			}
			if got := tc.get(cfg); got != tc.value {
				t.Errorf("%s did not reach its flag: got %q, want %q", tc.env, got, tc.value)
			}
		})
	}
}

func TestEnvUnknownSubcommandDoesNotConsumeFlags(t *testing.T) {
	// A flag placed before the subcommand must not be silently swallowed.
	var stderr bytes.Buffer
	if code := dispatch(context.Background(), []string{"-db-path", "/tmp/x.db", "nonsense"}, &stderr); code != usageExit {
		t.Errorf("exit code = %d, want %d", code, usageExit)
	}
}

func TestDoctorReportsWithoutStartingAStream(t *testing.T) {
	var out bytes.Buffer
	dir := t.TempDir()
	t.Setenv("JOCKORA_SEGMENT_DIR", filepath.Join(dir, "seg"))
	t.Setenv("JOCKORA_DB_PATH", filepath.Join(dir, "j.db"))

	code := dispatch(context.Background(), []string{"doctor"}, &out)
	if code != 0 && code != 1 {
		t.Errorf("doctor exited %d, want 0 or 1", code)
	}
	if !strings.Contains(out.String(), "ffmpeg") {
		t.Errorf("doctor said nothing about ffmpeg:\n%s", out.String())
	}
}

func TestMain(m *testing.M) { os.Exit(m.Run()) }

// TestLogLevelDefaultIsWarn: Jockora-e9a.57. A fresh install recorded info and
// above, so the server wrote down more than twenty "scanning" lines for every
// warning worth reading, and the console -- which reads the same level --
// opened on the same wall of noise.
//
// The LevelVar's zero value is Info, so this was never a decision anybody made;
// it was the default of the type. The stored setting still wins, and an
// operator can still drop to info or debug from the console without a restart.
func TestLogLevelDefaultIsWarn(t *testing.T) {
	if got := newLevelVar().Level(); got != app.DefaultLogLevel {
		t.Errorf("the process starts at %v, want the compiled default %v", got, app.DefaultLogLevel)
	}
	if app.DefaultLogLevel != slog.LevelWarn {
		t.Errorf("the compiled default is %v, want warnings and errors", app.DefaultLogLevel)
	}

	// AND THAT MAIN ACTUALLY USES IT. Falsification caught this: replacing
	// newLevelVar() with new(slog.LevelVar) in runSubcommand killed no test at
	// all, because everything above tests the helper rather than the wiring.
	// runSubcommand parses flags, opens a store and starts a server, so it
	// cannot be called from here -- reading the source is what is left, and it
	// is the same trade test/console_css_test.go makes for the stylesheet.
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "level := newLevelVar()") {
		t.Error("main builds its LevelVar directly, so the compiled default is the type's Info")
	}
	if strings.Contains(string(src), "level := new(slog.LevelVar)") {
		t.Error("main still creates a bare LevelVar, whose zero value is info")
	}
}

// TestServeGivesThePipelineTheConfiguredOverlap: found reviewing Jockora-ugw.
//
// The configured lead-in was applied only inside the branch that runs when a
// store exists, so on the spike path, in tests and on any deployment without
// accounts the -break-overlap flag was parsed, defaulted and read by nothing --
// every station started at the boundary and the flag looked broken. The Cadence
// line two lines above it carries a comment about exactly this: config.
// BreakEveryNTracks was parsed, env-mapped and read by nothing since the spine.
//
// serve() opens a store and starts a server, so it cannot be called from here.
// Reading the source is what is left, and it is the trade main.go's own
// LevelVar test and test/console_css_test.go already make.
func TestServeGivesThePipelineTheConfiguredOverlap(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "OverlapSeconds: cfg.BreakOverlapS") {
		t.Error("the pipeline is built without the configured lead-in, so -break-overlap " +
			"is parsed and read by nothing and every break starts at the boundary")
	}
}
