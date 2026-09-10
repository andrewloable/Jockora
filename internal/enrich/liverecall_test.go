// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestLiveRecall probes model recall against a real llama-server.
//
// THIS IS THE TEST THAT MATTERS FOR THIS FEATURE. Three dossier defects in this
// package were found only live and none by a unit test: field echoing, a worked
// example copied verbatim into two unrelated tracks, and supplied lyrics quoted
// into a field that IS stored. Recall asks a small model to answer from its own
// memory and then trusts it, so the only question worth asking is what it
// actually does -- especially on a track it cannot possibly know.
//
//	llama-server -m <model.gguf> --port 8123 -c 8192 -ngl 99
//	JOCKORA_LIVE_LLM=http://127.0.0.1:8123 go test ./internal/enrich/ -run TestLiveRecall -v
func TestLiveRecall(t *testing.T) {
	base := os.Getenv("JOCKORA_LIVE_LLM")
	if base == "" {
		t.Skip("set JOCKORA_LIVE_LLM to run")
	}
	llm := NewLlamaCPP(base, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	if err := llm.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}

	cases := []struct {
		name       string
		in         TrackInput
		mustRecall bool // true = a song any model knows; false = one that cannot exist
	}{
		{
			name: "famous song, no lyrics supplied",
			in: TrackInput{
				Artist: "Queen", Title: "Bohemian Rhapsody",
				Album: "A Night at the Opera", Year: 1975, DurationS: 355,
				AllowRecall: true,
			},
			mustRecall: true,
		},
		{
			// THE ONE THAT DECIDES WHETHER THIS FEATURE IS SAFE. No such
			// record exists, so anything in subject_summary is invention --
			// and invention is what the DJ would read out as fact.
			name: "a track that does not exist",
			in: TrackInput{
				Artist: "Grelvin Marsh & The Ochre Tessellation",
				Title:  "Pemberton Vane Reversal (Kelp Mix)",
				Album:  "Dorsal Cartography", Year: 2029, DurationS: 214,
				AllowRecall: true,
			},
			mustRecall: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			d, err := GenerateDossier(ctx, llm, tc.in)
			if err != nil {
				t.Fatalf("GenerateDossier: %v", err)
			}
			raw, _ := json.MarshalIndent(d, "  ", "  ")
			t.Logf("%s in %s:\n  %s", tc.in.Title, time.Since(start).Round(time.Millisecond), raw)

			// THE BOUNDS HOLD WHATEVER THE MODEL SAID. These are not about
			// whether recall worked; they are about what recall is allowed to
			// touch, and they must hold on every track either way.
			if len(d.ArtistFacts) > 0 {
				t.Errorf("artist facts appeared with no lookup: %v", d.ArtistFacts)
			}
			if d.NotableLine != "" {
				t.Errorf("a line was quoted with no lyrics supplied: %q", d.NotableLine)
			}

			knows := d.SubjectSummary != "" || len(d.Themes) > 0
			switch {
			case tc.mustRecall && !knows:
				t.Errorf("the model did not recall a song it certainly knows; recall is inert")
			case tc.mustRecall && knows:
				if !recalled(d) {
					t.Errorf("recalled meaning is not labelled: sources=%v", d.Sources)
				}
				// Only "high" clears the DJ's 0.6 gate. Anything less and the
				// summary is written, stored and never said.
				if d.Confidence != ConfidenceHigh {
					t.Errorf("confidence %q: the DJ can never use this", d.Confidence)
				}
			case !tc.mustRecall && knows:
				// NOT A HARD FAILURE, because it is a property of the model
				// rather than of this code -- but it is the number that decides
				// whether the feature should be on, so it is reported loudly.
				t.Errorf("INVENTED: the model described a track that does not exist: %q / %v",
					d.SubjectSummary, d.Themes)
			}
		})
	}
}
