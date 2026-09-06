// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"flag"
	"os"
	"strconv"
	"strings"
	"testing"
)

// withArgs replaces os.Args for the duration of the test. Load() reads the real
// process args, and `go test` passes its own flags, so every test must supply a
// clean set or flag parsing fails on -test.timeout and friends.
func withArgs(t *testing.T, args ...string) {
	t.Helper()
	old := os.Args
	os.Args = append([]string{"jockora"}, args...)
	t.Cleanup(func() { os.Args = old })
}

// clearEnv blanks every JOCKORA_ variable so a developer's shell cannot change
// the result of a defaults test. t.Setenv restores them afterwards.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, envPrefix) {
			t.Setenv(k, "")
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	withArgs(t)
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.SampleRate != 48000 {
		t.Errorf("SampleRate = %d, want 48000", cfg.SampleRate)
	}
	if cfg.Channels != 2 {
		t.Errorf("Channels = %d, want 2", cfg.Channels)
	}
	if cfg.SegmentSeconds != 4 {
		t.Errorf("SegmentSeconds = %d, want 4", cfg.SegmentSeconds)
	}
	if cfg.ListSize != 10 {
		t.Errorf("ListSize = %d, want 10", cfg.ListSize)
	}
	if cfg.BreakEveryNTracks != 4 {
		t.Errorf("BreakEveryNTracks = %d, want 4", cfg.BreakEveryNTracks)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("ListenAddr = %q, want 127.0.0.1:8080", cfg.ListenAddr)
	}
}

// TestListenAddrNeverWildcard guards the security decision: v0.1 has no
// authentication, so the default must never be reachable off-host.
func TestListenAddrNeverWildcard(t *testing.T) {
	withArgs(t)
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if strings.HasPrefix(cfg.ListenAddr, ":") || strings.HasPrefix(cfg.ListenAddr, "0.0.0.0") {
		t.Errorf("default ListenAddr %q binds every interface", cfg.ListenAddr)
	}
}

func TestEnvOverridesFlagDefault(t *testing.T) {
	withArgs(t)
	clearEnv(t)
	t.Setenv("JOCKORA_LISTEN_ADDR", "127.0.0.1:9999")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9999" {
		t.Errorf("ListenAddr = %q, want 127.0.0.1:9999", cfg.ListenAddr)
	}
}

// TestFlagBeatsEnv pins the precedence rule: env fills in unset flags only.
func TestFlagBeatsEnv(t *testing.T) {
	withArgs(t, "-listen-addr", "127.0.0.1:7777")
	clearEnv(t)
	t.Setenv("JOCKORA_LISTEN_ADDR", "127.0.0.1:9999")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:7777" {
		t.Errorf("ListenAddr = %q, want the flag value 127.0.0.1:7777", cfg.ListenAddr)
	}
}

// TestEnvOverridesTypedFlag proves the env path goes through flag parsing, so
// ints and floats are not silently left at their defaults.
func TestEnvOverridesTypedFlag(t *testing.T) {
	withArgs(t)
	clearEnv(t)
	t.Setenv("JOCKORA_BREAK_EVERY_N_TRACKS", "7")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.BreakEveryNTracks != 7 {
		t.Errorf("BreakEveryNTracks = %d, want 7", cfg.BreakEveryNTracks)
	}
}

func TestBadEnvValueIsAnError(t *testing.T) {
	withArgs(t)
	clearEnv(t)
	t.Setenv("JOCKORA_SEGMENT_SECONDS", "not-a-number")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a non-numeric JOCKORA_SEGMENT_SECONDS")
	}
}

// TestLoadWithExtraFlags covers the failure that only appears when the binary is
// actually run: a command registering its own flags on flag.CommandLine has them
// rejected by Load's private flag set as unknown.
func TestLoadWithExtraFlags(t *testing.T) {
	withArgs(t, "-break", "voice.wav", "-break-at", "42", "-listen-addr", "127.0.0.1:7000", "a.mp3", "b.mp3")
	clearEnv(t)

	var breakPath string
	var breakAt int
	cfg, err := LoadWith(func(fs *flag.FlagSet) {
		fs.StringVar(&breakPath, "break", "", "")
		fs.IntVar(&breakAt, "break-at", 30, "")
	})
	if err != nil {
		t.Fatalf("LoadWith: %v", err)
	}

	if breakPath != "voice.wav" {
		t.Errorf("break = %q, want voice.wav", breakPath)
	}
	if breakAt != 42 {
		t.Errorf("break-at = %d, want 42", breakAt)
	}
	if cfg.ListenAddr != "127.0.0.1:7000" {
		t.Errorf("ListenAddr = %q, want 127.0.0.1:7000", cfg.ListenAddr)
	}
	if len(cfg.Args) != 2 || cfg.Args[0] != "a.mp3" || cfg.Args[1] != "b.mp3" {
		t.Errorf("Args = %v, want [a.mp3 b.mp3]", cfg.Args)
	}
}

func TestLoadCapturesPositionalArgs(t *testing.T) {
	withArgs(t, "one.mp3", "two.mp3")
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Args) != 2 {
		t.Errorf("Args = %v, want two tracks", cfg.Args)
	}
}

// TestConfigModelServerFlagsHaveEnvEquivalents. The container deployment sets
// environment, not argv, so a flag without its JOCKORA_ variable is a flag the
// deployed station cannot use.
func TestConfigModelServerFlagsHaveEnvEquivalents(t *testing.T) {
	cases := []struct {
		env, value string
		get        func(*Config) string
	}{
		{"JOCKORA_LLM_MODEL", "/models/qwen.gguf", func(c *Config) string { return c.LLMModelPath }},
		{"JOCKORA_LLM_BINARY", "/opt/llama-server", func(c *Config) string { return c.LLMBinary }},
		{"JOCKORA_LLM_CONTEXT", "4096", func(c *Config) string { return strconv.Itoa(c.LLMContextSize) }},
		{"JOCKORA_LLM_GPU_LAYERS", "20", func(c *Config) string { return strconv.Itoa(c.LLMGPULayers) }},
		{"JOCKORA_TTS_PYTHON", "/venv/bin/python", func(c *Config) string { return c.TTSPython }},
		{"JOCKORA_TTS_SCRIPT", "/opt/kokoro.py", func(c *Config) string { return c.TTSScript }},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv(tc.env, tc.value)
			cfg, err := LoadArgs(nil, nil)
			if err != nil {
				t.Fatalf("loading with %s set: %v", tc.env, err)
			}
			if got := tc.get(cfg); got != tc.value {
				t.Errorf("%s did not reach its flag: got %q, want %q", tc.env, got, tc.value)
			}
		})
	}
}

// TestTuningKnobsDefaultToTheMeasuredValues.
//
// The whole safety property of these knobs: an operator who sets none of them
// gets exactly the behaviour every gate in this project was measured against.
// A default that drifts from the measured value silently invalidates the gates.
func TestTuningKnobsDefaultToTheMeasuredValues(t *testing.T) {
	withArgs(t)
	clearEnv(t)

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		got  float64
		want float64
	}{
		{"music LUFS", c.MusicLUFS, -16},
		{"ducked LUFS", c.MusicDuckedLUFS, -28},
		{"speech LUFS", c.SpeechLUFS, -16},
		{"true-peak ceiling", c.TruePeakCeiling, -1},
		{"lookahead seconds", c.LookaheadSeconds, 150},
		{"advert interval minutes", c.AdIntervalMin, 90},
		{"fact confidence", c.FactConfidence, 0.6},
	} {
		if tc.got != tc.want {
			t.Errorf("%s defaults to %v, want the measured %v", tc.name, tc.got, tc.want)
		}
	}
	if c.AdEveryNBreaks != 4 {
		t.Errorf("advert frequency defaults to %d, want 4", c.AdEveryNBreaks)
	}
}

// TestTuningKnobsReadFromTheEnvironment: every flag has a JOCKORA_ equivalent,
// and these are the settings most likely to be set in a compose file rather
// than typed.
func TestTuningKnobsReadFromTheEnvironment(t *testing.T) {
	withArgs(t)
	clearEnv(t)
	t.Setenv("JOCKORA_MUSIC_DUCKED_LUFS", "-24")
	t.Setenv("JOCKORA_AD_EVERY_N_BREAKS", "8")
	t.Setenv("JOCKORA_FACT_CONFIDENCE", "0.9")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MusicDuckedLUFS != -24 {
		t.Errorf("ducked LUFS = %v, want -24 from the environment", c.MusicDuckedLUFS)
	}
	if c.AdEveryNBreaks != 8 {
		t.Errorf("ad frequency = %d, want 8 from the environment", c.AdEveryNBreaks)
	}
	if c.FactConfidence != 0.9 {
		t.Errorf("fact confidence = %v, want 0.9 from the environment", c.FactConfidence)
	}
}
