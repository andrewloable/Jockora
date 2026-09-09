// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"slices"
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

// TestBriefGateLive is Jockora-g1t's live check: a real model, briefs written
// the way an operator writes them, and the vocabularies actually enforced.
//
// The dossier work paid for this three times -- three serious defects found
// here and none by the unit tests -- which is why it is part of the gate and
// not optional politeness.
//
//	JOCKORA_LIVE_LLM=http://127.0.0.1:8123 go test ./internal/enrich/ -run TestBriefGateLive -v
func TestBriefGateLive(t *testing.T) {
	base := os.Getenv("JOCKORA_LIVE_LLM")
	if base == "" {
		t.Skip("set JOCKORA_LIVE_LLM to run")
	}

	llm := NewLlamaCPP(base, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := llm.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}

	latest := time.Now().Year() + 1
	for _, brief := range []string{
		// A decade, stated the way people say it.
		"Loud eighties rock for driving at night.",
		// A FEELING WITH NO GENRE. The vocabulary has to carry this alone.
		"Something calm and a bit sad for the end of the evening.",
		// A GENRE THE VOCABULARY DOES NOT HAVE. "other" is the answer it
		// already has, and inventing "chiptune" is the drift the enums exist
		// to prevent.
		"Chiptune and vaporwave, nothing after 2010.",
		// TWO SENTENCES, which is how a real brief arrives.
		"This is the late-night station. Slow, moody, mostly nineties, " +
			"and nothing that sounds like a party.",
		// Terse, which is the other way it arrives.
		"angry guitars",
		// Jockora-g1t.14: A PACE AND NOTHING ELSE. The same brief the human
		// gate uses, so the two are testing the same claim.
		"something to run to",
		// AND A LENGTH AND NOTHING ELSE.
		"long ambient pieces, nothing short",
		// Jockora-2mu, REPORTED FROM THE DEPLOYMENT AND REPRODUCED HERE. This
		// brief came back with a tempo band of 192 to 193, a length band of
		// 1938 to 1939 seconds, and years of 1970 to 1979 -- a decade closed at
		// both ends, excluding everything the words "and below" were there to
		// include. The tempo and length were the schema forbidding the 0 the
		// prompt demands; the years were the decade rule firing on "70s" with
		// nothing covering the direction.
		"Oldies 70s and below",
	} {
		t.Run(brief[:min(len(brief), 30)], func(t *testing.T) {
			p, err := DeriveStationParams(ctx, llm, brief)
			if err != nil {
				t.Fatalf("DeriveStationParams(%q): %v", brief, err)
			}
			t.Logf("%q ->\n  name   %q\n  genres %v\n  moods  %v\n  years  %d..%d"+
				"\n  tempo  %g..%g BPM\n  length %g..%g s",
				brief, p.Name, p.Genres, p.Moods, p.YearMin, p.YearMax,
				p.TempoMin, p.TempoMax, p.DurationMinS, p.DurationMaxS)
			// READ THESE. A model that answers "something to run to" with a
			// plausible-looking 60 to 200 has answered nothing, and no
			// assertion catches that -- the same way an invented decade on a
			// date-free brief passed every check it had.
			if strings.Contains(brief, "run to") &&
				p.TempoMin == 0 && p.TempoMax == 0 {
				t.Error("a brief that is ABOUT pace came back with no tempo at all")
			}
			if strings.Contains(brief, "long ambient") &&
				p.DurationMinS == 0 && p.DurationMaxS == 0 {
				t.Error("a brief that is ABOUT length came back with no length at all")
			}
			// AND THE ONES THAT MENTION NEITHER MUST COME BACK UNBOUNDED. An
			// unbidden range silently shrinks a playlist, which is worse than a
			// missing one because nothing on screen explains it.
			// Jockora-2mu. A DECADE WITH A DIRECTION IS ONE-SIDED, and the
			// closed decade this used to return is the answer that excludes
			// everything the operator asked for.
			if brief == "Oldies 70s and below" {
				if p.YearMax != 1979 {
					t.Errorf("%q gave year_max %d, want 1979 -- the end of the decade named",
						brief, p.YearMax)
				}
				if p.YearMin != 0 {
					t.Errorf("%q gave year_min %d, want 0 -- \"and below\" is an open floor, "+
						"and a closed decade excludes everything those words were there to include",
						brief, p.YearMin)
				}
			}
			if brief == "angry guitars" || strings.HasPrefix(brief, "Loud eighties") ||
				brief == "Oldies 70s and below" {
				if p.TempoMin != 0 || p.TempoMax != 0 {
					t.Errorf("%q says nothing about pace and came back bounded at %g..%g",
						brief, p.TempoMin, p.TempoMax)
				}
				if p.DurationMinS != 0 || p.DurationMaxS != 0 {
					t.Errorf("%q says nothing about length and came back bounded at %g..%g",
						brief, p.DurationMinS, p.DurationMaxS)
				}
			}
			for _, r := range []struct {
				what     string
				min, max float64
				lo, hi   float64
			}{
				{"tempo", p.TempoMin, p.TempoMax, SlowestBPM, FastestBPM},
				{"length", p.DurationMinS, p.DurationMaxS, ShortestTrackS, LongestTrackS},
			} {
				for _, v := range []float64{r.min, r.max} {
					if v != 0 && (v < r.lo || v > r.hi) {
						t.Errorf("%s %g is outside %g..%g", r.what, v, r.lo, r.hi)
					}
				}
				if r.min != 0 && r.max != 0 && r.min > r.max {
					t.Errorf("%s %g..%g selects nothing", r.what, r.min, r.max)
				}
			}

			for _, g := range p.Genres {
				if !slices.Contains(StationTags, g) {
					t.Errorf("genre %q is not in the vocabulary", g)
				}
			}
			for _, m := range p.Moods {
				if !slices.Contains(Moods, m) {
					t.Errorf("mood %q is not in the vocabulary", m)
				}
			}
			for _, y := range []int{p.YearMin, p.YearMax} {
				if y != 0 && (y < 1900 || y > latest) {
					t.Errorf("year %d is outside 1900..%d", y, latest)
				}
			}
			if p.YearMin != 0 && p.YearMax != 0 && p.YearMin > p.YearMax {
				t.Errorf("years %d..%d select nothing", p.YearMin, p.YearMax)
			}
			if p.Name == "" {
				t.Error("no name, so the console pre-fills an empty box")
			}
			if n := len([]rune(p.Name)); n > MaxBriefName {
				t.Errorf("name is %d runes, over the %d cap", n, MaxBriefName)
			}
			// NOT EMPTY: a station selecting the whole library is legal as a
			// filter and never what a brief meant.
			if len(p.Genres) == 0 && len(p.Moods) == 0 && p.YearMin == 0 && p.YearMax == 0 {
				t.Error("the result selects the entire library")
			}
		})
	}
}
