// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package dj

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/enrich"
)

// TestLiveAdPool writes the station's advert rotation against a real model and
// saves it for a person to read.
//
// v0.1 REQUIRES A HUMAN TO READ EVERY ADVERT BEFORE IT AIRS. The brand denylist
// in Validate is a backstop that catches obvious slips; it cannot tell that an
// invented name is someone's actual company in a market nobody here has heard
// of. That judgement is a person's, and it is why the pool is ten.
//
//	JOCKORA_LIVE_LLM=http://127.0.0.1:8123 \
//	JOCKORA_AD_OUT=/path/to/ads.json \
//	go test ./internal/dj/ -run TestLiveAdPool -v -timeout 30m
func TestLiveAdPool(t *testing.T) {
	llmURL := os.Getenv("JOCKORA_LIVE_LLM")
	if llmURL == "" {
		t.Skip("set JOCKORA_LIVE_LLM to run")
	}

	persona, err := LoadPersona("../../personas/midnight_vale.toml")
	if err != nil {
		t.Fatalf("loading persona: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	ads, err := GenerateAdPool(ctx, NewWriterAt(enrich.NewLlamaCPP(llmURL, nil), 400, enrich.InventionTemperature), persona, AdPoolSize)
	if err != nil {
		t.Fatalf("generated %d of %d adverts: %v", len(ads), AdPoolSize, err)
	}

	t.Logf("ADVERT ROTATION for %s (%d adverts) -- EVERY ONE NEEDS READING BEFORE IT AIRS",
		persona.Name(), len(ads))
	for _, ad := range ads {
		t.Logf("  %s  %s", ad.ID, ad.Brand)
		t.Logf("      %s", ad.Script)
		if err := ad.Validate(); err != nil {
			t.Errorf("  %s failed validation after generation: %v", ad.ID, err)
		}
	}

	if len(ads) != AdPoolSize {
		t.Errorf("pool has %d adverts, want %d", len(ads), AdPoolSize)
	}
	// A rotation of one brand under ten names is not a rotation.
	brands := make(map[string]bool, len(ads))
	for _, ad := range ads {
		brands[ad.Brand] = true
	}
	if len(brands) != len(ads) {
		t.Errorf("%d adverts share %d distinct brands", len(ads), len(brands))
	}

	if out := os.Getenv("JOCKORA_AD_OUT"); out != "" {
		raw, err := json.MarshalIndent(ads, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("written to %s", out)
	}
}
