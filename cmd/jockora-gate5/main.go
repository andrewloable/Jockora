// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Command jockora-gate5 measures GATE 5 against a real library.
//
// A SEPARATE BINARY, not a sixth subcommand. The product surface is
// deliberately five commands (serve, scan, enrich, doctor, version) and a
// measurement harness is not one of them.
//
// It runs the writer TWICE over the same requests: once with the validator
// disabled, to see what the model RAW output collides on, and once with it
// enabled, to price what the validator costs. Measuring aired breaks alone
// would report a zero collision rate by construction.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "jockora-gate5:", err)
		os.Exit(1)
	}
}

func run() error {
	var dbPath, llmURL, personaPath string
	var sample int
	flag.StringVar(&dbPath, "db", "jockora.db", "database holding scanned tracks and dossiers")
	flag.StringVar(&llmURL, "llm-url", "http://127.0.0.1:8081", "llama-server base URL")
	flag.StringVar(&personaPath, "persona", "personas/midnight_vale.toml", "persona card or directory")
	flag.IntVar(&sample, "n", 20, "breaks to generate in each pass")
	flag.Parse()

	ctx := context.Background()

	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer s.Close() //nolint:errcheck // read-only

	persona, err := loadPersona(personaPath)
	if err != nil {
		return err
	}
	dossiers, names, err := loadDossiers(ctx, s)
	if err != nil {
		return err
	}
	if len(dossiers) < 3 {
		return fmt.Errorf("only %d enriched tracks in %s; the writer needs previous, current and next", len(dossiers), dbPath)
	}
	fmt.Printf("persona %s, %d enriched tracks, %d breaks per pass\n\n", persona.Name(), len(dossiers), sample)

	writer := dj.NewWriter(enrich.NewLlamaCPP(llmURL, nil), 0)

	// PASS ONE: validator disabled. Nothing is recorded in said_lines, so this
	// pass cannot influence the next one.
	fmt.Println("pass 1 of 2: raw output, validator DISABLED")
	raw, rawNames, factsAsserted, factsResolved, err := rawPass(ctx, writer, persona, dossiers, names, sample)
	if err != nil {
		return err
	}

	// PASS TWO: validator enabled, against a CLEAN index. Reusing the index
	// from pass one would count pass one's phrases as prior art and inflate the
	// drop rate for a reason that never happens on air.
	fmt.Println("pass 2 of 2: validator ENABLED")
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM said_lines`); err != nil {
		return fmt.Errorf("clearing said_lines: %w", err)
	}
	validator := &dj.Validator{Writer: writer, Said: &dj.SaidLines{Store: s, JockID: persona.ID()}}
	generated, dropped, err := validatedPass(ctx, validator, persona, dossiers, names, sample)
	if err != nil {
		return err
	}

	report := dj.AnalyseGate5Ignoring(raw, rawNames, generated, dropped, factsAsserted, factsResolved)
	fmt.Printf("\n%s", report)
	fmt.Printf("\nvalidator reasons: %v\n", validator.Stats().Reasons)
	if !report.Pass {
		return errors.New("GATE 5 FAILED")
	}
	return nil
}

// rawPass generates with NO validator, so the model's own repetition is visible.
// The fact cooldown is applied here TOO, and that is deliberate rather than a
// way of flattering the number. It is not validation -- nothing is rejected by
// it -- it is part of how the schema is built, and production always builds the
// schema that way. Measuring raw output against a vocabulary the product never
// actually offers would grade a configuration that does not exist.
func rawPass(ctx context.Context, w dj.Writer, p *dj.Persona, dossiers []*enrich.Dossier, names []trackName, n int) ([]string, [][]string, int, int, error) {
	var raw []string
	var rawNames [][]string
	asserted, resolved := 0, 0
	usedAt := map[string]int{}

	for i := 0; i < n; i++ {
		prev, cur, next := pick(dossiers, i)
		prompt, err := buildPrompt(p, prev, cur, next, i)
		if err != nil {
			return nil, nil, 0, 0, err
		}

		var cooling []string
		for id, at := range usedAt {
			if i-at < dj.FactCooldown {
				cooling = append(cooling, id)
			}
		}
		out, err := w.WriteBreak(ctx, prompt, dj.BreakSchemaExcluding(prev, cur, next, cooling))
		if err != nil {
			fmt.Printf("  %2d  model error: %v\n", i+1, err)
			continue
		}
		b, err := dj.ParseBreak(out)
		if err != nil {
			fmt.Printf("  %2d  unparseable: %v\n", i+1, err)
			continue
		}
		// Groundedness is measured here, on RAW output, because the validator
		// would otherwise have rejected exactly the breaks this criterion is
		// about.
		for _, id := range b.AssertedFacts {
			asserted++
			if _, ok := dj.ResolveFactText(id, prev, cur, next); ok {
				resolved++
			}
			usedAt[id] = i
		}
		raw = append(raw, b.Text())
		rawNames = append(rawNames, namesAt(names, i))
		fmt.Printf("  %2d  %s\n", i+1, truncate(b.Text(), 96))
	}
	return raw, rawNames, asserted, resolved, nil
}

// validatedPass runs the same requests with the validator on and counts drops.
func validatedPass(ctx context.Context, v *dj.Validator, p *dj.Persona, dossiers []*enrich.Dossier, names []trackName, n int) (int, int, error) {
	generated, dropped := 0, 0
	for i := 0; i < n; i++ {
		prev, cur, next := pick(dossiers, i)
		prompt, err := buildPrompt(p, prev, cur, next, i)
		if err != nil {
			return 0, 0, err
		}
		generated++
		// The same proper nouns production would excuse. Without this the gate
		// measures a configuration the product never runs, and counts naming
		// the record as repetition.
		v.SetTrackNames(namesAt(names, i)...)
		if _, err := v.Generate(ctx, prompt, prev, cur, next); err != nil {
			dropped++
			fmt.Printf("  %2d  DROPPED  %v\n", i+1, truncate(err.Error(), 88))
			continue
		}
		fmt.Printf("  %2d  aired\n", i+1)
	}
	return generated, dropped, nil
}

func buildPrompt(p *dj.Persona, prev, cur, next *enrich.Dossier, i int) (string, error) {
	return dj.BuildBreakPrompt(dj.PromptInput{
		Persona: p, Previous: prev, Current: cur, Next: next,
		Placement:     mix.PlacementRamp.String(),
		WindowSeconds: 12,
		Schema:        dj.BreakSchema(prev, cur, next),
		IsColdOpen:    i == 0,
	})
}

func pick(d []*enrich.Dossier, i int) (prev, cur, next *enrich.Dossier) {
	return d[i%len(d)], d[(i+1)%len(d)], d[(i+2)%len(d)]
}

func loadPersona(path string) (*dj.Persona, error) {
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
	chosen, _ := dj.SelectPersona(all, nil, nil)
	return chosen, nil
}

// trackName is the artist and title of one track.
//
// Read from the TRACKS table, not from the dossier: a dossier's prose mentions
// the artist but is mostly ordinary words, and excusing all of it would empty
// the collision index rather than narrow it.
type trackName struct{ artist, title string }

func loadDossiers(ctx context.Context, s *store.Store) ([]*enrich.Dossier, []trackName, error) {
	rows, err := s.DB().QueryContext(ctx,
		`SELECT d.track_id, coalesce(t.artist,''), coalesce(t.title,'')
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
		if err := rows.Scan(&id, &n.artist, &n.title); err != nil {
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

// namesAt returns the artist and title of the three tracks in play at break i,
// which are exactly the names production would excuse at that boundary.
func namesAt(names []trackName, i int) []string {
	if len(names) == 0 {
		return nil
	}
	var out []string
	for _, k := range []int{i, i + 1, i + 2} {
		n := names[k%len(names)]
		out = append(out, n.artist, n.title)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
