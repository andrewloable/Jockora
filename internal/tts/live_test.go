// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package tts

import (
	"context"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/mix"
)

// TestLiveKokoroEndurance is the half of the step 6 endurance gate that the fake
// sidecar structurally cannot cover: the leak, if there is one, lives in Python
// and onnxruntime, not in the Go manager. Run it on EVERY target platform.
//
//	JOCKORA_LIVE_TTS=/path/to/venv/bin/python \
//	JOCKORA_KOKORO_MODEL=~/.jockora/models/kokoro-v1.0.onnx \
//	JOCKORA_KOKORO_VOICES=~/.jockora/models/voices-v1.0.bin \
//	go test ./internal/tts/ -run TestLiveKokoro -v -timeout 20m
func TestLiveKokoroEndurance(t *testing.T) {
	python := os.Getenv("JOCKORA_LIVE_TTS")
	if python == "" {
		t.Skip("set JOCKORA_LIVE_TTS to a python3.10 interpreter with kokoro-onnx installed")
	}

	s, err := Start(context.Background(), Config{
		Command:      []string{python, "../../sidecar/kokoro_server.py"},
		StartTimeout: 120 * time.Second, // an ONNX cold load is the point of the sidecar
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close() //nolint:errcheck // test teardown

	// Varied text, because a single cached line would measure nothing. These are
	// the length of a real break: one or two sentences.
	lines := []string{
		"You are listening to Midnight Vale, and that was a long way from anywhere.",
		"Coming up, something with more teeth in it.",
		"Three in the morning is the only honest hour.",
		"That record came out the year everything got louder.",
		"Stay where you are. There is more.",
	}

	// 50 is the gate. A longer run is available because onnxruntime's arena
	// grows for a while before it settles, and telling that apart from a leak
	// needs more than 50 points.
	n := 50
	if v, err := strconv.Atoi(os.Getenv("JOCKORA_LIVE_TTS_N")); err == nil && v > 0 {
		n = v
	}

	rss1 := rssKB(t, s.Pid())
	var latencies []time.Duration
	var rssTrail []int
	wall := time.Now()
	for i := 0; i < n; i++ {
		start := time.Now()
		wav, err := s.Synthesize(context.Background(), lines[i%len(lines)], "")
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if len(wav) < 44 {
			t.Fatalf("request %d returned %d bytes, not a WAV", i+1, len(wav))
		}
		latencies = append(latencies, time.Since(start))
		if (i+1)%25 == 0 {
			rssTrail = append(rssTrail, rssKB(t, s.Pid()))
		}
	}
	total := time.Since(wall)
	rss50 := rssKB(t, s.Pid())

	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	t.Logf("KOKORO ENDURANCE (%s/%s)", osArch(), python)
	t.Logf("  total wall clock : %s", total.Round(time.Millisecond))
	t.Logf("  median latency   : %s", sorted[len(sorted)/2].Round(time.Millisecond))
	t.Logf("  p95 latency      : %s", sorted[int(0.95*float64(len(sorted)))].Round(time.Millisecond))
	t.Logf("  requests         : %d", n)
	t.Logf("  RSS req 1 / %-4d : %d KB / %d KB (%+d KB)", n, rss1, rss50, rss50-rss1)
	t.Logf("  RSS every 25     : %v KB", rssTrail)
	t.Logf("  respawns         : %d", s.RespawnCount())

	if n := s.RespawnCount(); n != 0 {
		t.Fatalf("RespawnCount = %d: a respawn during steady-state use hides a leak or a crash", n)
	}
	// Growth of a few MB is arena churn. A doubling is a leak, and the respawn
	// supervisor is exactly what would stop anyone noticing it.
	if rss1 > 0 && rss50 > 2*rss1 {
		t.Fatalf("RSS grew from %d KB to %d KB across 50 requests: leak", rss1, rss50)
	}
}

func osArch() string {
	out, _ := exec.Command("uname", "-sm").Output()
	return strings.TrimSpace(string(out))
}

// rssKB reads the child's resident set.
//
// /proc first, because the container this runs in on Linux is python:3.10-slim
// and has no ps. The first version of this used ps everywhere, silently
// returned 0 there, and turned the leak assertion below into a gate that could
// not fail -- so an unreadable RSS is now a test failure, not a zero.
func rssKB(t *testing.T, pid int) int {
	t.Helper()
	if pid == 0 {
		t.Fatal("sidecar has no pid; cannot measure RSS")
	}
	if b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				kb, err := strconv.Atoi(strings.Fields(line)[1])
				if err == nil {
					return kb
				}
			}
		}
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("cannot read RSS for pid %d (no /proc, no usable ps): %v", pid, err)
	}
	kb, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("cannot parse ps RSS %q: %v", out, err)
	}
	return kb
}

// TestLiveKokoroRender puts real synthesis through the real render pipeline.
// The fake sidecar emits a tone, and a tone normalises more obligingly than
// speech does: this is the check that the loudness target holds on the signal
// the product actually carries.
func TestLiveKokoroRender(t *testing.T) {
	python := os.Getenv("JOCKORA_LIVE_TTS")
	if python == "" {
		t.Skip("set JOCKORA_LIVE_TTS to a python3.10 interpreter with kokoro-onnx installed")
	}
	s, err := Start(context.Background(), Config{
		Command:      []string{python, "../../sidecar/kokoro_server.py"},
		StartTimeout: 120 * time.Second,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close() //nolint:errcheck // test teardown

	// Several utterances, not one. Speech loudness varies with how much silence
	// an utterance carries, and one lucky line would prove nothing about the
	// next one.
	lines := []string{
		"You are listening to Midnight Vale. That was the sound of somebody leaving town in a hurry.",
		"Three in the morning.",
		"That was recorded in a room with no windows, which you can hear, and which is the point.",
		"Coming up: something louder, then something that regrets it.",
		"Stay where you are.",
	}
	dir := t.TempDir()
	for i, line := range lines {
		out := filepath.Join(dir, "break"+strconv.Itoa(i)+".wav")
		d, err := Render(context.Background(), s, line, "", out)
		if err != nil {
			t.Fatalf("Render %d: %v", i, err)
		}
		got, measure := speechLoudness(t, out)
		t.Logf("REAL KOKORO RENDER %d: %.2fs  %-10s %+.2f LUFS  (%d words)", i, d, measure, got, len(strings.Fields(line)))
		if math.Abs(got-mix.SpeechLUFS) > 1.0 {
			t.Errorf("line %d %s loudness %.2f LUFS, want %.1f +/- 1.0", i, measure, got, mix.SpeechLUFS)
		}
		// The ceiling matters far more on real speech than on the fake's tone:
		// speech has a high crest factor, so bringing it to -16 LUFS is exactly
		// what pushes its consonants past full scale.
		frames, err := mix.ReadWAV(out)
		if err != nil {
			t.Fatalf("line %d ReadWAV: %v", i, err)
		}
		ceiling := float32(math.Pow(10, mix.TruePeakCeilingDBTP/20))
		for j, f := range frames {
			if abs32(f.L) > ceiling || abs32(f.R) > ceiling {
				t.Fatalf("line %d frame %d = (%.4f, %.4f) exceeds the %.0f dBTP ceiling", i, j, f.L, f.R, mix.TruePeakCeilingDBTP)
			}
		}

		// A break is seconds, not fractions of one and not a minute. Either
		// bound means the resample or the synthesis went wrong.
		if d < 0.5 || d > 20 {
			t.Errorf("line %d rendered %.2fs, which is not a break length", i, d)
		}
	}
}

// TestLiveKokoroSpeaksEveryPersonaVoice checks that every voice the roster names
// actually exists in the operator's voice pack.
//
// A voice id that Kokoro does not recognise fails the synthesis with a 503 and
// the break is dropped. That is invisible: the station stays on air, plays
// music, and simply never talks. It happened once, for twenty breaks out of
// twenty, and the test that was supposed to catch it passed throughout. So the
// check is by NAME, per persona, and it fails loudly.
//
// It cannot be an ordinary unit test: the voice pack is operator-supplied and
// deliberately not vendored, so it lives behind the same env var as the rest of
// the live tests.
func TestLiveKokoroSpeaksEveryPersonaVoice(t *testing.T) {
	python := os.Getenv("JOCKORA_LIVE_TTS")
	if python == "" {
		t.Skip("set JOCKORA_LIVE_TTS to a python3.10 interpreter with kokoro-onnx installed")
	}

	personas, err := dj.LoadPersonas("../../personas")
	if err != nil {
		t.Fatal(err)
	}
	if len(personas) == 0 {
		t.Fatal("no personas loaded; this test would pass by having nothing to check")
	}

	s, err := Start(context.Background(), Config{
		Command:      []string{python, "../../sidecar/kokoro_server.py"},
		StartTimeout: 120 * time.Second,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close() //nolint:errcheck // test teardown

	for _, p := range personas {
		wav, err := s.Synthesize(context.Background(), "Testing one two.", p.VoiceID())
		if err != nil {
			t.Errorf("%s: voice %q cannot speak: %v", p.ID(), p.VoiceID(), err)
			continue
		}
		if len(wav) == 0 {
			t.Errorf("%s: voice %q produced no audio", p.ID(), p.VoiceID())
		}
	}
}
