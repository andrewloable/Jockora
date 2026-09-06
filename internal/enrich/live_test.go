// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/tts"
)

// TestLiveDossier runs the real client against a real llama-server.
//
// Skipped unless JOCKORA_LIVE_LLM points at one, so the normal suite stays
// hermetic. Run it after ANY change to the prompt or schema: every serious
// defect in this file was found here and not by the unit tests.
//
//	llama-server -m <model.gguf> --port 8123 -c 4096 -ngl 99
//	JOCKORA_LIVE_LLM=http://127.0.0.1:8123 go test ./internal/enrich/ -run TestLive -v
func TestLiveDossier(t *testing.T) {
	base := os.Getenv("JOCKORA_LIVE_LLM")
	if base == "" {
		t.Skip("set JOCKORA_LIVE_LLM to run")
	}

	llm := NewLlamaCPP(base, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	if err := llm.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}

	in := TrackInput{
		Artist: "New Order", Title: "Blue Monday",
		Album: "Power, Corruption & Lies", Year: 1983, DurationS: 442,
		ArtistFacts: ArtistFacts{
			Found: true, Name: "New Order", Country: "GB", Type: "Group",
			BeginYear: 1980, Disambiguation: "UK band formed from Joy Division",
			Source: SourceMusicBrainz,
		},
		HasSyncedLyrics: true,
	}

	start := time.Now()
	d, err := GenerateDossier(ctx, llm, in)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GenerateDossier: %v", err)
	}

	raw, _ := json.MarshalIndent(d, "  ", "  ")
	t.Logf("generated in %v:\n  %s", elapsed.Round(time.Millisecond), raw)

	for _, tag := range d.StationTags {
		if !stationTagSet[tag] {
			t.Errorf("station tag %q is out of vocabulary", tag)
		}
	}
	for _, m := range d.Mood {
		if !moodSet[m] {
			t.Errorf("mood %q is out of vocabulary", m)
		}
	}
	if d.Confidence != ConfidenceHigh && d.Confidence != ConfidenceLow && d.Confidence != ConfidenceNone {
		t.Errorf("confidence %q is out of vocabulary", d.Confidence)
	}
	if len(d.NotableLine) > MaxNotableLine {
		t.Errorf("notable_line is %d chars", len(d.NotableLine))
	}

	// A non-English track must still produce English.
	in2 := in
	in2.Artist, in2.Title = "Eraserheads", "Ang Huling El Bimbo"
	in2.ArtistFacts.Name, in2.ArtistFacts.Country = "Eraserheads", "PH"
	d2, err := GenerateDossier(ctx, llm, in2)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	raw2, _ := json.MarshalIndent(d2, "  ", "  ")
	t.Logf("non-English source:\n  %s", raw2)
}

func TestLiveDossierWithLyrics(t *testing.T) {
	base := os.Getenv("JOCKORA_LIVE_LLM")
	if base == "" {
		t.Skip("set JOCKORA_LIVE_LLM to run")
	}
	llm := NewLlamaCPP(base, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	in := TrackInput{
		Artist: "Nobody Famous", Title: "Harbour Lights", Year: 1994, DurationS: 240,
		ArtistFacts: ArtistFacts{Found: true, Name: "Nobody Famous", Country: "IE",
			Type: "Group", BeginYear: 1991, Source: SourceMusicBrainz},
		HasSyncedLyrics: true,
		Lyrics: `I left the harbour lights behind me
the tide was running out
my brother said he'd write me
but the letters never came
I count the winters on my hands
and none of them were kind`,
	}

	d, err := GenerateDossier(ctx, llm, in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(d, "  ", "  ")
	t.Logf("WITH lyrics supplied:\n  %s", raw)

	if d.SubjectSummary == "" {
		t.Error("subject_summary is empty even with lyrics supplied")
	}
	if strings.Contains(strings.ToLower(d.SubjectSummary), "harbour lights") &&
		len(d.SubjectSummary) < 40 {
		t.Errorf("subject_summary is still just the title: %q", d.SubjectSummary)
	}
}

// TestLiveOnsetEndpoint exercises the real sidecar's /onset against a real
// file. The unit tests above run against a fake HTTP server and prove the Go
// arithmetic; they cannot tell whether librosa is installed, whether ffmpeg can
// decode the container, or whether the endpoint is even wired up.
//
//	JOCKORA_LIVE_TTS=/path/to/venv/bin/python \
//	JOCKORA_LIVE_ONSET_TRACK=/path/to/a/song.mp3 \
//	go test ./internal/enrich/ -run TestLiveOnset -v -timeout 10m
func TestLiveOnsetEndpoint(t *testing.T) {
	python := os.Getenv("JOCKORA_LIVE_TTS")
	track := os.Getenv("JOCKORA_LIVE_ONSET_TRACK")
	if python == "" || track == "" {
		t.Skip("set JOCKORA_LIVE_TTS and JOCKORA_LIVE_ONSET_TRACK to run")
	}

	s, err := tts.Start(context.Background(), tts.Config{
		Command:      []string{python, "../../sidecar/kokoro_server.py"},
		StartTimeout: 180 * time.Second,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("starting sidecar: %v", err)
	}
	defer s.Close() //nolint:errcheck // test teardown

	start := time.Now()
	ramp, err := NewOnsetClient("http://"+s.Addr(), nil).
		FetchRamp(context.Background(), track, 0)
	if err != nil {
		t.Fatalf("FetchRamp: %v", err)
	}
	t.Logf("LIVE ONSET: ramp %.2fs outro %.2fs confidence %q in %s",
		ramp.RampS, ramp.OutroS, ramp.Confidence, time.Since(start).Round(time.Millisecond))

	if ramp.Confidence != ConfidenceAnalysis && ramp.Confidence != ConfidenceNone {
		t.Errorf("confidence = %q, want %q or %q", ramp.Confidence, ConfidenceAnalysis, ConfidenceNone)
	}
	if ramp.Confidence == ConfidenceAnalysis {
		if ramp.RampS <= 0 || ramp.RampS > MaxAnalysisRamp {
			t.Errorf("RampS = %v, outside (0, %v]", ramp.RampS, MaxAnalysisRamp)
		}
		if ramp.OutroS <= 0 || ramp.OutroS > MaxAnalysisRamp {
			t.Errorf("OutroS = %v, outside (0, %v]", ramp.OutroS, MaxAnalysisRamp)
		}
	}
}
