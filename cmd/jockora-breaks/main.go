// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Command jockora-breaks generates a round of breaks against a real library and
// scores them against the STEP 5.5 rubric.
//
// THIS IS THE STEP WHERE THE PROJECT IS WON OR LOST, and the thing it produces
// that matters most is not the pass rate -- it is the markdown file. Fifty
// breaks get READ, in full, by a person. The rubric only removes the ones that
// are wrong for reasons a machine can prove, so that the reading is spent on
// whether they are any GOOD.
//
// TASTE IS NOT SCORED HERE. Whoever wrote the persona knows what it will say and
// cannot hear it fresh; a self-graded taste score would be worth nothing. That
// judgement is room test B's, by someone who has not read the prompt.
//
// A separate binary, like jockora-gate5: the product surface is five commands
// and a tuning harness is not one of them.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jockora-breaks:", err)
		os.Exit(1)
	}
}

func run() error {
	var dbPath, llmURL, personaPath, outDir string
	var n, round, nPredict int
	var temperature float64
	flag.StringVar(&dbPath, "db", "jockora.db", "database holding scanned tracks and dossiers")
	flag.StringVar(&llmURL, "llm-url", "http://127.0.0.1:8081", "llama-server base URL")
	flag.StringVar(&personaPath, "persona", "personas", "persona card or directory")
	flag.StringVar(&outDir, "out", ".", "directory to write the round's markdown into")
	flag.IntVar(&n, "n", 50, "breaks to generate")
	flag.IntVar(&round, "round", 1, "tuning round (1-3); step 5.5 allows at most three")
	flag.Float64Var(&temperature, "temperature", enrich.WritingTemperature, "sampling temperature")
	flag.IntVar(&nPredict, "n-predict", 0, "response token budget (0 = dj.BreakTokenBudget)")
	flag.Parse()

	if round < 1 || round > 3 {
		return fmt.Errorf("round %d: step 5.5 allows three rounds. A fourth means the prompt is not the problem", round)
	}

	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close() //nolint:errcheck // read-only

	// The jock is chosen from what the library SOUNDS LIKE, exactly as the
	// station chooses one on first run. Grading a persona the product would
	// never pick for this library measures the wrong writer.
	taste, err := enrich.Taste(ctx, s, 5)
	if err != nil {
		return err
	}
	persona, err := loadPersona(personaPath, taste)
	if err != nil {
		return err
	}
	dossiers, names, err := loadDossiers(ctx, s)
	if err != nil {
		return err
	}
	ramps, outros, err := measuredWindows(ctx, s)
	if err != nil {
		return err
	}
	// SHUFFLED, because loadDossiers returns them in track_id order and a
	// library is stored in album order. Round 1 walked straight into three Lord
	// of the Rings soundtracks and spent its first dozen breaks on Howard
	// Shore, which grades the writer on one composer. A station shuffles its
	// pool; so does this. The seed is fixed so a round is reproducible.
	shuffle(dossiers, names, int64(round))

	// A seeded draw from this library's own measured windows, used only for
	// tracks whose ramp and outro have not been analysed yet.
	borrow := rand.New(rand.NewSource(int64(round) * 7919))
	fallback := func(int) (float64, float64) {
		var r, o float64
		if len(ramps) > 0 {
			r = ramps[borrow.Intn(len(ramps))]
		}
		if len(outros) > 0 {
			o = outros[borrow.Intn(len(outros))]
		}
		return r, o
	}
	if len(dossiers) < 3 {
		return fmt.Errorf("only %d enriched tracks in %s; the writer needs previous, current and next", len(dossiers), dbPath)
	}

	checkable, human := dj.CheckableRules(persona)
	fmt.Printf("round %d: %s, %d enriched tracks, %d breaks, temperature %.1f\n",
		round, persona.Name(), len(dossiers), n, temperature)
	fmt.Printf("library reads as: %s / %s\n", strings.Join(taste.Genres, ", "), strings.Join(taste.Moods, ", "))
	fmt.Printf("persona rules: %d checkable here, %d only a listener can judge\n", len(checkable), len(human))
	fmt.Printf("windows: %d tracks measured (%d ramps, %d outros); the rest borrow from those\n\n",
		len(ramps), len(ramps), len(outros))

	// The index is NOT cleared between rounds. Round two colliding with round
	// one is real: on air the index is never empty either.
	said := &dj.SaidLines{Store: s, JockID: persona.ID()}
	writer := dj.NewWriterAt(enrich.NewLlamaCPP(llmURL, nil), nPredict, temperature)
	rubric := dj.Rubric{Said: said, Persona: persona}

	// The fact cooldown is part of how production BUILDS the schema, not part
	// of validation -- nothing is rejected by it. Leaving it out would let the
	// round lean on the same three facts fifty times and then report the
	// resulting sameness as a phrasing problem.
	usedAt := map[string]int{}

	// Counted rather than shrugged off: a round that quietly yields 41 breaks
	// instead of 50 is not the round the step asks for, and the pass rate would
	// be computed over whatever survived without saying so.
	failed := 0
	// rawLong is the spec's unassisted criterion (c); relocated counts what
	// production's ladder rescued. Reported separately so neither hides.
	rawLong, relocated := 0, 0

	var results []dj.RubricResult
	var rows []row
	for i := 0; i < n; i++ {
		prev, cur, next := pick(dossiers, i)
		placement, window := placementAt(names, i, fallback)

		var cooling []string
		for id, at := range usedAt {
			if i-at < dj.FactCooldown {
				cooling = append(cooling, id)
			}
		}
		schema := dj.BreakSchemaExcluding(prev, cur, next, cooling)

		mkPrompt := func(words int) (string, error) {
			return dj.BuildBreakPrompt(promptFor(persona, prev, cur, next, names, i,
				placement, float64(words)/dj.WordsPerSecond, schema))
		}

		// Built through the SAME helper the retry uses. Two copies of this
		// drifted immediately: the inline one here never gained the current and
		// next track NAMES, so the harness was still measuring a writer that
		// had to invent a title while production had been told it.
		prompt, err := mkPrompt(dj.WordTarget(window))
		if err != nil {
			return err
		}

		out, err := writer.WriteBreak(ctx, prompt, schema, 0)
		if err != nil {
			failed++
			fmt.Printf("  %2d  model error: %v\n", i+1, err)
			continue
		}
		b, err := dj.ParseBreak(out)
		if err != nil {
			failed++
			fmt.Printf("  %2d  unparseable: %v\n", i+1, err)
			continue
		}

		// PRODUCTION'S LADDER, not a single attempt.
		//
		// station.LengthCheck.Enforce does not drop an overlong break: it
		// rewrites once at 0.75x the target, and if that still does not fit it
		// RE-PLACES the break into the between-track gap, which is 120 words.
		// Scoring the first attempt against the original window measured a
		// configuration the product never runs -- the same mistake that made
		// rounds 1 and 2 worthless -- and reported 20 of 47 as overlong when
		// production would have aired nearly all of them.
		//
		// Both numbers are kept. `raw` is the spec's criterion (c) on the first
		// attempt, so the writer's unassisted aim stays visible and a
		// regression in it cannot hide behind the ladder.
		raw, err := rubric.Score(ctx, b, prev, cur, next, dj.WordTarget(window), namesAt(names, i))
		if err != nil {
			return err
		}
		res, laddered, err := enforceLength(ctx, rubric, writer, b, prev, cur, next,
			placement, window, schema, mkPrompt, namesAt(names, i))
		if err != nil {
			return err
		}
		if _, over := raw.Failures[dj.ReasonLength]; over {
			rawLong++
		}
		if laddered {
			relocated++
		}
		for _, id := range b.AssertedFacts {
			usedAt[id] = i
		}
		results = append(results, res)
		rows = append(rows, row{i + 1, placement.String(), window, b, res})

		mark := "pass"
		if !res.Pass {
			mark = strings.Join(reasons(res), "+")
		}
		fmt.Printf("  %2d  %-14s %s\n", i+1, mark, truncate(b.Text(), 80))

		// Only what would have AIRED enters the index, exactly as production
		// records it. Recording rejects too would make the next break collide
		// with lines no listener ever heard.
		//
		// Airing is NOT the same as passing the rubric. The validator rejects a
		// break for being ungrounded or repetitive; it does not reject one for
		// running long or sounding wrong, and those still go out and still
		// become prior art. Gating this on res.Pass would quietly shrink the
		// index and under-report the collision rate the rubric is measuring.
		_, ungrounded := res.Failures[dj.ReasonFact]
		_, collided := res.Failures[dj.ReasonPhrasing]
		if !ungrounded && !collided {
			if err := said.Record(ctx, b.Text()); err != nil {
				return err
			}
		}
	}

	summary := dj.Summarise(results)
	fmt.Printf("\n%s", summary)
	fmt.Printf("  %d of %d overran on the FIRST attempt (the spec's raw criterion (c));\n", rawLong, len(results))
	fmt.Printf("  %d were rescued by the retry or the between-gap, as production would.\n", relocated)
	if failed > 0 {
		fmt.Printf("  %d of %d requests produced nothing readable\n", failed, n)
	}

	path := filepath.Join(outDir, fmt.Sprintf("breaks-round-%d.md", round))
	if err := writeMarkdown(path, round, persona, temperature, summary, rows, checkable, human, failed, n, len(ramps)); err != nil {
		return err
	}
	fmt.Printf("\nwrote %s\n", path)
	fmt.Println("READ ALL OF IT. The pass rate says nothing about whether these are worth hearing.")
	return nil
}

type row struct {
	n         int
	placement string
	window    float64
	b         *dj.Break
	res       dj.RubricResult
}

func reasons(r dj.RubricResult) []string {
	var out []string
	for _, k := range []dj.RubricReason{dj.ReasonFact, dj.ReasonPhrasing, dj.ReasonLength, dj.ReasonTone} {
		if _, bad := r.Failures[k]; bad {
			out = append(out, string(k))
		}
	}
	return out
}

func writeMarkdown(path string, round int, p *dj.Persona, temp float64, s dj.RubricSummary, rows []row, checkable, human []string, failed, asked, measured int) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Break round %d — %s\n\n", round, p.Name())
	fmt.Fprintf(&b, "%s · temperature %.1f · %s\n\n", time.Now().Format(time.RFC3339), temp, p.VoiceID())
	fmt.Fprintf(&b, "```\n%s```\n\n", s)
	if failed > 0 {
		fmt.Fprintf(&b, "%d of %d requests produced nothing readable and are not below.\n\n", failed, asked)
	}

	fmt.Fprintf(&b, "Windows come from the tracks' own measured ramp and outro where they exist (%d tracks in this library); the rest borrow a window drawn from those, because inventing one graded 38 of 50 breaks against a length production would never have asked for.\n\n", measured)
	b.WriteString("The rubric checked: groundedness, collisions, length, and the persona rules quoted below.\n")
	b.WriteString("It did NOT check whether any of this is good. That is what the reading is for.\n\n")
	if len(human) > 0 {
		b.WriteString("Rules only a reader can judge:\n\n")
		for _, r := range human {
			fmt.Fprintf(&b, "- %s\n", r)
		}
		b.WriteString("\n")
	}
	b.WriteString("---\n\n")

	for _, r := range rows {
		verdict := "PASS"
		if !r.res.Pass {
			verdict = "FAIL — " + strings.Join(reasons(r.res), ", ")
		}
		fmt.Fprintf(&b, "### %d. %s (%s, %.0fs, %d words)\n\n",
			r.n, verdict, r.placement, r.window, len(strings.Fields(r.res.Text)))
		fmt.Fprintf(&b, "> %s\n\n", r.res.Text)
		if len(r.b.AssertedFacts) > 0 {
			fmt.Fprintf(&b, "asserted: `%s`\n\n", strings.Join(r.b.AssertedFacts, "`, `"))
		}
		for _, k := range []dj.RubricReason{dj.ReasonFact, dj.ReasonPhrasing, dj.ReasonLength, dj.ReasonTone} {
			if why, bad := r.res.Failures[k]; bad {
				fmt.Fprintf(&b, "- **%s** — %s\n", k, why)
			}
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// promptFor assembles one break request. It exists so the retry rung can build
// the SAME prompt aimed at a smaller word count, which is what production does.
func promptFor(p *dj.Persona, prev, cur, next *enrich.Dossier, names []trackName, i int,
	placement mix.Placement, window float64, schema map[string]any) dj.PromptInput {
	curN, nextN := names[(i+1)%len(names)], names[(i+2)%len(names)]
	return dj.PromptInput{
		Persona: p, Previous: prev, Current: cur, Next: next,
		PreviousArtist: names[i%len(names)].artist,
		PreviousTitle:  names[i%len(names)].title,
		CurrentArtist:  curN.artist, CurrentTitle: curN.title,
		NextArtist: nextN.artist, NextTitle: nextN.title,
		Placement:     placement.String(),
		WindowSeconds: window,
		Schema:        schema,
		IsColdOpen:    i == 0,
	}
}

// enforceLength walks the same ladder station.LengthCheck.Enforce walks, and
// scores the break that would actually have aired.
//
// It measures WORDS rather than synthesised seconds. Production measures the
// rendered WAV, which is more accurate and costs a TTS render per attempt; at
// fifty breaks a round that is the difference between a loop that can be run
// three times in an afternoon and one that cannot. The word target and the
// speaking rate are the same on both sides, so the rung chosen is the same for
// anything that is not within a word or two of a boundary.
func enforceLength(ctx context.Context, rubric dj.Rubric, w dj.Writer, b *dj.Break,
	prev, cur, next *enrich.Dossier, placement mix.Placement, window float64,
	schema map[string]any, buildPrompt func(words int) (string, error), names []string) (dj.RubricResult, bool, error) {

	res, err := rubric.Score(ctx, b, prev, cur, next, dj.WordTarget(window), names)
	if err != nil {
		return res, false, err
	}
	if _, over := res.Failures[dj.ReasonLength]; !over {
		return res, false, nil
	}

	// Rung two: one rewrite, aimed shorter. Only one, because two model calls
	// plus two renders is already the ceiling inside the lookahead window.
	// The prompt is REBUILT for the shorter target. Re-rolling the original
	// prompt would just be a second sample of the same request, which is not
	// what production does and would measure nothing about aiming shorter.
	shorter := int(float64(dj.WordTarget(window))*station.ShorterRetryFactor + 0.5)
	shortPrompt, err := buildPrompt(shorter)
	if err != nil {
		return res, false, err
	}
	if out, err := w.WriteBreak(ctx, shortPrompt, schema, 0); err == nil {
		if retry, perr := dj.ParseBreak(out); perr == nil {
			if r2, serr := rubric.Score(ctx, retry, prev, cur, next, shorter, names); serr == nil {
				if r2.Pass {
					return r2, true, nil
				}
				if _, stillOver := r2.Failures[dj.ReasonLength]; !stillOver {
					return r2, true, nil
				}
				res, b = r2, retry
			}
		}
	}

	// Rung three: re-place rather than rewrite again. The between gap is wider
	// than any ramp, and moving audio that already exists costs nothing.
	if placement != mix.PlacementBetween {
		moved, err := rubric.Score(ctx, b, prev, cur, next, station.BetweenWordBudget, names)
		if err != nil {
			return res, true, err
		}
		if _, stillOver := moved.Failures[dj.ReasonLength]; !stillOver {
			return moved, true, nil
		}
	}

	// Rung four: it does not fit anywhere. Production drops it; breaks are
	// optional, music is not.
	return res, true, nil
}

// The loaders below are deliberately duplicated from jockora-gate5 rather than
// lifted into a shared package. They are forty lines, the two harnesses measure
// different things, and a package that exists to serve two commands is a
// dependency both of them then have to agree with.

func loadPersona(path string, taste enrich.StationTaste) (*dj.Persona, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return dj.LoadPersona(path)
	}
	all, err := dj.LoadPersonas(path)
	if err != nil {
		return nil, err
	}
	chosen, _ := dj.SelectPersona(all, taste.Genres, taste.Moods)
	return chosen, nil
}

// trackName is one track's name and its measured speaking windows.
type trackName struct {
	artist, title string
	album         string
	rampS, outroS float64
}

func loadDossiers(ctx context.Context, s *store.Store) ([]*enrich.Dossier, []trackName, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT d.track_id, coalesce(t.artist,''), coalesce(t.title,''), coalesce(t.album,''),
		        coalesce(t.ramp_s,0), coalesce(t.outro_s,0)
		   FROM dossiers d JOIN tracks t ON t.id = d.track_id
		  ORDER BY d.track_id LIMIT 200`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only

	var out []*enrich.Dossier
	var names []trackName
	for rows.Next() {
		var id int64
		var n trackName
		if err := rows.Scan(&id, &n.artist, &n.title, &n.album, &n.rampS, &n.outroS); err != nil {
			return nil, nil, err
		}
		d, ok, err := enrich.LoadDossier(ctx, s, id)
		if err != nil || !ok {
			continue
		}
		out = append(out, &d)
		names = append(names, n)
	}
	return out, names, rows.Err()
}

// shuffle reorders dossiers and their names together, keeping them aligned --
// they are indexed in lockstep, so shuffling one alone silently attributes
// every break to the wrong record.
func shuffle(d []*enrich.Dossier, names []trackName, seed int64) {
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(d), func(i, j int) {
		d[i], d[j] = d[j], d[i]
		names[i], names[j] = names[j], names[i]
	})
}

func pick(d []*enrich.Dossier, i int) (prev, cur, next *enrich.Dossier) {
	return d[i%len(d)], d[(i+1)%len(d)], d[(i+2)%len(d)]
}

// placementAt chooses the placement and window the STATION would choose, from
// the incoming track's ramp and the outgoing track's outro.
//
// It replaces a fixed 12s ramp and 7s outro, which were invented rather than
// measured and were both far tighter than anything this library produces: the
// real measured mean is 23.4s of ramp and 29.4s of outro. Grading against the
// invented pair reported 38 of 50 breaks as overlong when most of them would
// have fitted the window they were actually going to be spoken over. That is
// the gate5 mistake again -- measuring a configuration the product never runs.
//
// The rule below is station.chooseePlacement's: prefer the incoming ramp, fall
// back to the outgoing outro, and when neither is usable take the between-track
// budget, which is what production does for a track it has not analysed yet.
func placementAt(names []trackName, i int, fallback func(int) (float64, float64)) (mix.Placement, float64) {
	cur, next := names[(i+1)%len(names)], names[(i+2)%len(names)]

	ramp, outro := next.rampS, cur.outroS
	if ramp <= 0 && outro <= 0 {
		// Unmeasured. Production would use the between-track budget here, but a
		// round made entirely of 120-word windows tests nothing: every break
		// passes on length by construction. So stand in a window drawn from
		// what THIS library actually measures, and say so in the report.
		ramp, outro = fallback(i)
	}

	switch {
	case ramp >= station.MinBreakSeconds:
		return mix.PlacementRamp, ramp
	case outro >= station.MinBreakSeconds:
		return mix.PlacementOutro, outro
	}
	return mix.PlacementBetween, station.BetweenWindowSeconds
}

// measuredWindows reads the ramp and outro lengths this library actually has,
// so an unmeasured track can borrow a plausible one instead of an invented one.
func measuredWindows(ctx context.Context, s *store.Store) (ramps, outros []float64, err error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT coalesce(ramp_s,0), coalesce(outro_s,0) FROM tracks
		  WHERE ramp_s > 0 OR outro_s > 0`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only

	for rows.Next() {
		var r, o float64
		if err := rows.Scan(&r, &o); err != nil {
			return nil, nil, err
		}
		if r > 0 {
			ramps = append(ramps, r)
		}
		if o > 0 {
			outros = append(outros, o)
		}
	}
	return ramps, outros, rows.Err()
}

func namesAt(names []trackName, i int) []string {
	if len(names) == 0 {
		return nil
	}
	var out []string
	for _, k := range []int{i, i + 1, i + 2} {
		n := names[k%len(names)]
		out = append(out, n.artist, n.title, n.album)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
