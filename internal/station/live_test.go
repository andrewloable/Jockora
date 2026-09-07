// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/store"
	"github.com/andrewloable/jockora/internal/tts"
)

// NOTE ON CORPUS SIZE. This used to read 12 dossiers however many the database
// held, which made the repetition drop rate a property of the TEST rather than
// of the model: a DJ given twelve tracks and asked for twenty breaks genuinely
// runs out of things to say, and the said-lines validator correctly refuses
// them. Measured on the same model, 15 tracks gave 6 repetition drops and 60
// gave 8 only because the extra 48 were never loaded.
//
// TestLiveBreakTiming is the MEASUREMENT PROCEDURE from Jockora-96e.5: run real
// generations end to end and record how long they take, RETRY INCLUSIVE, so
// that T can be set from evidence instead of from the 150-second guess.
//
// Retry-inclusive is the whole point. A sample of clean first passes understates
// T by an entire LLM call plus a TTS render, on exactly the breaks most likely
// to be late -- the ones the validator rejected because the DJ had already said
// something like it.
//
//	JOCKORA_LIVE_LLM=http://127.0.0.1:8123 \
//	JOCKORA_LIVE_TTS=/path/to/venv/bin/python \
//	JOCKORA_KOKORO_MODEL=... JOCKORA_KOKORO_VOICES=... \
//	JOCKORA_LIVE_DB=/path/to/a/db/with/dossiers.db \
//	go test ./internal/station/ -run TestLiveBreakTiming -v -timeout 60m
func TestLiveBreakTiming(t *testing.T) {
	llmURL := os.Getenv("JOCKORA_LIVE_LLM")
	python := os.Getenv("JOCKORA_LIVE_TTS")
	dbPath := os.Getenv("JOCKORA_LIVE_DB")
	if llmURL == "" || python == "" || dbPath == "" {
		t.Skip("set JOCKORA_LIVE_LLM, JOCKORA_LIVE_TTS and JOCKORA_LIVE_DB to run")
	}

	n := 20
	if v, err := strconv.Atoi(os.Getenv("JOCKORA_LIVE_BREAKS")); err == nil && v > 0 {
		n = v
	}

	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close() //nolint:errcheck // read-mostly

	dossiers, names := loadEnrichedTracks(t, ctx, s)
	if len(dossiers) < 3 {
		t.Fatalf("only %d enriched tracks in %s; the writer needs previous, current and next", len(dossiers), dbPath)
	}
	t.Logf("using %d enriched tracks", len(dossiers))

	personaPath := os.Getenv("JOCKORA_LIVE_PERSONA")
	if personaPath == "" {
		personaPath = "../../personas/midnight_vale.toml"
	}
	persona, err := dj.LoadPersona(personaPath)
	if err != nil {
		t.Fatalf("loading persona: %v", err)
	}

	sidecar, err := tts.Start(ctx, tts.Config{
		Command:      []string{python, "../../sidecar/kokoro_server.py"},
		StartTimeout: 180 * time.Second,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("starting the TTS sidecar: %v", err)
	}
	defer sidecar.Close() //nolint:errcheck // test teardown

	validator := &dj.Validator{
		Writer: dj.NewWriter(enrich.NewLlamaCPP(llmURL, nil), 0),
		Said:   &dj.SaidLines{Store: s, JockID: persona.ID()},
	}
	session := dj.NewSession()

	lookahead := NewLookahead(DefaultLookahead)
	dir := t.TempDir()
	clk := clock.Real{}

	var aired, dropped int
	for i := 0; i < n; i++ {
		prev, cur, next := dossiers[i%len(dossiers)], dossiers[(i+1)%len(dossiers)], dossiers[(i+2)%len(dossiers)]
		prevName := names[i%len(names)]

		placement, window := PreferredWindow(
			&Track{RampS: 8, OutroS: 8, DurationS: 200, RampConfidence: enrich.ConfidenceLRC},
			&Track{RampS: 12, OutroS: 6, DurationS: 200, RampConfidence: enrich.ConfidenceLRC}, 3)

		lc := LengthCheck{
			Writer: &liveWriter{
				t: t, validator: validator, persona: persona, session: session,
				prev: prev, cur: cur, next: next,
				prevArtist: prevName.artist, prevTitle: prevName.title,
			},
			Renderer: RendererFunc(func(ctx context.Context, text, voice, outPath string) (float64, error) {
				return tts.Render(ctx, sidecar, text, voice, outPath)
			}),
			Dir:   filepath.Join(dir, strconv.Itoa(i)),
			Voice: persona.VoiceID(),
		}
		if err := os.MkdirAll(lc.Dir, 0o750); err != nil {
			t.Fatal(err)
		}

		started := clk.Now()
		rendered, err := lc.Enforce(ctx, placement, window)
		elapsed := clk.Now().Sub(started)
		if err != nil {
			t.Fatalf("break %d: %v", i+1, err)
		}

		// Recorded whether it aired or not. A break that was written, rendered
		// and then dropped still consumed the whole budget, and dropping it is
		// what T exists to make rare.
		lookahead.RecordTiming(elapsed, rendered.Attempts)

		if rendered.Dropped {
			dropped++
			t.Logf("  %2d  DROPPED   %6s  attempts %d  -- %s", i+1, elapsed.Round(time.Millisecond), rendered.Attempts, rendered.Reason)
			continue
		}
		aired++
		session.BreakAired()
		t.Logf("  %2d  %-7s  %6s  speech %4.1fs  attempts %d", i+1,
			rendered.Placement.String(), elapsed.Round(time.Millisecond), rendered.Seconds, rendered.Attempts)
	}

	stats := validator.Stats()
	t.Logf("")
	t.Logf("BREAK GENERATION TIMING, n=%d (retry-inclusive)", lookahead.Count())
	t.Logf("  aired / dropped     %d / %d", aired, dropped)
	t.Logf("  needed a rewrite    %d", lookahead.RetriedCount())
	t.Logf("  validator drops     %d of %d attempts, reasons %v", stats.Drops, stats.Attempts, stats.Reasons)
	t.Logf("  p50                 %s", lookahead.P50().Round(time.Millisecond))
	t.Logf("  p95                 %s", lookahead.P95().Round(time.Millisecond))
	t.Logf("  RECOMMENDED T       %s   (p95 x %.1f)", lookahead.RecommendedT().Round(time.Second), LookaheadSafetyFactor)
	t.Logf("  current default     %s", DefaultLookahead)

	// A run where nothing airs is not a timing measurement, it is a broken
	// pipeline reporting healthy. The first version of this test passed with
	// 0 of 20 aired, because every drop is individually legitimate -- which is
	// exactly how a wrong voice id stayed invisible.
	if aired == 0 {
		t.Fatalf("0 of %d breaks aired; the timings below measure a pipeline that produces nothing", n)
	}
	// The DROP RATE is deliberately not asserted here. It is a WRITING quality
	// measurement and it belongs to Jockora-9ax.6 (GATE 5: raw pre-validator
	// collision and drop rate) and Jockora-9ax.7 (the 50-break rubric loop).
	// This test measures TIMING, and a timing number is not made wrong by a
	// chatty model -- p95 has landed within a second across every run here
	// regardless of how many breaks aired. Failing this test on drop rate would
	// only mean a permanently red timing gate owned by nobody.
	t.Logf("  drop rate           %.0f%%  -- belongs to GATE 5 (Jockora-9ax.6), not to this measurement", 100*float64(dropped)/float64(n))

	if lookahead.Count() < MinTimingSample {
		t.Errorf("only %d generations timed; the minimum for a recommendation is %d", lookahead.Count(), MinTimingSample)
	}
	if lookahead.RecommendedT() > DefaultLookahead {
		t.Errorf("measured T %s exceeds the %s default: the lookahead is too short and breaks will be late",
			lookahead.RecommendedT(), DefaultLookahead)
	}
}

type trackName struct{ artist, title string }

func loadEnrichedTracks(t *testing.T, ctx context.Context, s *store.Store) ([]*enrich.Dossier, []trackName) {
	t.Helper()
	rows, err := s.DB().QueryContext(ctx, `
		SELECT t.id, coalesce(t.artist,''), coalesce(t.title,'')
		  FROM tracks t JOIN dossiers d ON d.track_id = t.id
		 WHERE t.playable = 1 LIMIT 200`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck // test helper

	var out []*enrich.Dossier
	var names []trackName
	for rows.Next() {
		var id int64
		var artist, title string
		if err := rows.Scan(&id, &artist, &title); err != nil {
			t.Fatal(err)
		}
		d, ok, err := enrich.LoadDossier(ctx, s, id)
		if err != nil || !ok {
			continue
		}
		out = append(out, &d)
		names = append(names, trackName{artist, title})
	}
	return out, names
}

// liveWriter assembles a real prompt and drives the real validator.
//
// It lives in the test rather than in the package because deciding where the
// persona, the session and the prohibition list come from is integration
// design, and that belongs to Jockora-q4q. This is the smallest thing that can
// produce a real measurement.
type liveWriter struct {
	t                     *testing.T
	validator             *dj.Validator
	persona               *dj.Persona
	session               *dj.Session
	prev, cur, next       *enrich.Dossier
	prevArtist, prevTitle string
}

func (w *liveWriter) Write(ctx context.Context, wordTarget int, placement mix.Placement) (string, error) {
	prompt, err := dj.BuildBreakPrompt(dj.PromptInput{
		Persona:        w.persona,
		Previous:       w.prev,
		Current:        w.cur,
		Next:           w.next,
		PreviousArtist: w.prevArtist,
		PreviousTitle:  w.prevTitle,
		Placement:      placement.String(),
		WindowSeconds:  float64(wordTarget) / dj.WordsPerSecond,
		Schema:         dj.BreakSchema(w.prev, w.cur, w.next),
		IsColdOpen:     w.session.IsColdOpen(),
	})
	if err != nil {
		return "", err
	}
	b, err := w.validator.Generate(ctx, prompt, 0, w.prev, w.cur, w.next)
	if err != nil {
		return "", err
	}
	// Validator.Generate already recorded it; recording again would double-count
	// the n-grams and make the DJ look more repetitive than it is.
	return b.Text(), nil
}
