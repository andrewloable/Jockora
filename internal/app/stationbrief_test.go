// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/station"
)

// Jockora-g1t.4. The operator types a description and presses a button; this is
// what answers, using whatever model the console is currently pointed at.

type briefCompleter struct {
	reply string
	err   error
	calls int
}

func (b *briefCompleter) Complete(context.Context, enrich.CompletionRequest) (enrich.Completion, error) {
	b.calls++
	if b.err != nil {
		return enrich.Completion{}, b.err
	}
	return enrich.Completion{Content: b.reply, StopType: "eos"}, nil
}

// TestResolveStationBriefNoModel: A LIBRARY WITH NO MODEL IS A WORKING SHUFFLE,
// not a fault -- so the caller has to be able to tell "no model" from "the model
// failed", and only errors.Is can do that.
func TestResolveStationBriefNoModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    *App
	}{
		{"no switchable at all", &App{log: quietLogger()}},
		{"a switchable holding nothing", &App{log: quietLogger(),
			opts: Options{LLM: enrich.NewSwitchable(nil)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.a.ResolveStationBrief(context.Background(), "late-night rock")
			if !errors.Is(err, enrich.ErrNoModel) {
				t.Errorf("err = %v, want it to satisfy errors.Is with ErrNoModel", err)
			}
		})
	}
}

func TestResolveStationBriefHappy(t *testing.T) {
	c := &briefCompleter{reply: `{"name":"Night Rock","genres":["rock"],
		"moods":["nocturnal"],"year_min":1975,"year_max":2005}`}
	a := &App{log: quietLogger(), opts: Options{LLM: enrich.NewSwitchable(c)}}

	got, err := a.ResolveStationBrief(context.Background(), "late-night rock for driving")
	if err != nil {
		t.Fatalf("ResolveStationBrief: %v", err)
	}
	if got.Name != "Night Rock" || len(got.Genres) != 1 || got.Genres[0] != "rock" {
		t.Errorf("params = %+v", got)
	}
	if got.YearMin != 1975 || got.YearMax != 2005 {
		t.Errorf("years = %d..%d", got.YearMin, got.YearMax)
	}
	// THROUGH THE SWITCHABLE, which is what everything else holds. A second
	// path to the model would be a second thing to reconfigure.
	if c.calls != 1 {
		t.Errorf("the model was called %d times", c.calls)
	}
}

// TestResolveStationBriefPropagatesError: a model that failed is a different
// problem from no model, and reporting them alike sends an operator to
// configure something that is already configured.
func TestResolveStationBriefPropagatesError(t *testing.T) {
	c := &briefCompleter{err: errors.New("rejected the API key")}
	a := &App{log: quietLogger(), opts: Options{LLM: enrich.NewSwitchable(c)}}

	_, err := a.ResolveStationBrief(context.Background(), "late-night rock")
	if err == nil {
		t.Fatal("a model failure was swallowed")
	}
	if errors.Is(err, enrich.ErrNoModel) {
		t.Error("a configured model that refused was reported as no model at all")
	}
	if !strings.Contains(err.Error(), "rejected the API key") {
		t.Errorf("err = %v, want the provider's own reason", err)
	}
}

func TestResolveStationBriefCounts(t *testing.T) {
	a, st := llmApp(t)
	ctx := context.Background()

	for i, tags := range [][]string{{"rock"}, {"rock"}, {"jazz"}} {
		id := int64(i + 1)
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO tracks (id, path, playable) VALUES (?, ?, 1)`,
			id, fmt.Sprintf("/m/%d.mp3", id)); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"station_tags": tags, "mood": []string{}})
		if _, err := st.DB().ExecContext(ctx,
			`INSERT INTO dossiers (track_id, json, confidence) VALUES (?, ?, ?)`,
			id, string(raw), enrich.ConfidenceHigh); err != nil {
			t.Fatal(err)
		}
	}

	n, err := a.CountMatching(ctx, station.Filter{Genres: []string{"rock"}})
	if err != nil {
		t.Fatalf("CountMatching: %v", err)
	}
	if n != 2 {
		t.Errorf("counted %d, want 2", n)
	}

	// ZERO IS A REAL ANSWER and it means an empty station, which is exactly
	// what the operator needs to see before they save it.
	n, err = a.CountMatching(ctx, station.Filter{Genres: []string{"metal"}})
	if err != nil {
		t.Fatalf("CountMatching on an empty result: %v", err)
	}
	if n != 0 {
		t.Errorf("counted %d for a genre nothing carries, want 0", n)
	}

	// A filter nothing could satisfy is refused rather than counted as zero:
	// an operator seeing 0 would go looking for missing music.
	if _, err := a.CountMatching(ctx, station.Filter{YearMin: 1990, YearMax: 1980}); err == nil {
		t.Error("an impossible filter was counted rather than refused")
	}
}

// TestResolveStationBriefCountNoStore: an error, NOT 0. Zero means an empty
// station, and answering it for a missing library would tell the operator their
// music is gone.
func TestResolveStationBriefCountNoStore(t *testing.T) {
	a := &App{log: quietLogger()}
	if _, err := a.CountMatching(context.Background(), station.Filter{}); err == nil {
		t.Error("CountMatching answered without a library to count")
	}
}

// TestResolveStationBriefLogsWhatItChose: the live box is where these calls are
// diagnosed, and a derive that quietly picked the wrong four tags leaves no
// other trace. A brief can be a paragraph; a log line is read at a glance.
func TestResolveStationBriefLogsWhatItChose(t *testing.T) {
	long := strings.Repeat("é", 200)
	for _, tc := range []struct{ in, want string }{
		{"late-night rock", "late-night rock"},
		{"  spread   over\n lines  ", "spread over lines"},
		{long, strings.Repeat("é", briefLogLimit) + "…"},
	} {
		if got := shortBrief(tc.in); got != tc.want {
			t.Errorf("shortBrief(%.20q) = %.90q, want %.90q", tc.in, got, tc.want)
		}
	}
	// CUT ON A RUNE BOUNDARY: a log line ending in a replacement character is
	// the sort of thing somebody spends an hour on at three in the morning.
	if got := shortBrief(long); len([]rune(got)) != briefLogLimit+1 {
		t.Errorf("a 200-rune brief became %d runes", len([]rune(got)))
	}
}
