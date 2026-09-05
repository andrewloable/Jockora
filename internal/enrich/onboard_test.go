// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/store"
)

// blockingLLM answers only when released, so a test can hold enrichment
// mid-flight and inspect the world while work is still pending.
type blockingLLM struct {
	release chan struct{}
	calls   chan struct{}
}

func newBlockingLLM() *blockingLLM {
	return &blockingLLM{release: make(chan struct{}), calls: make(chan struct{}, 1000)}
}

func (b *blockingLLM) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	select {
	case b.calls <- struct{}{}:
	default:
	}
	select {
	case <-b.release:
		return ok(goodJSON()), nil
	case <-ctx.Done():
		return Completion{}, ctx.Err()
	}
}

// TestServeStartsBeforeEnrichmentCompletes: the whole point of stream-first
// onboarding. A library with no dossiers must still be playable, with the DJ
// running personality-only, rather than silent for the hours a full pass takes.
func TestServeStartsBeforeEnrichmentCompletes(t *testing.T) {
	s := queueStore(t, 100)

	// Tracks are selectable for playback the moment they are scanned.
	pool, err := PlayableTrackIDs(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 100 {
		t.Fatalf("%d playable tracks before any enrichment, want 100", len(pool))
	}

	llm := newBlockingLLM()
	q := newQueue(s, llm)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- q.Run(ctx) }()

	// Wait for enrichment to be genuinely in flight and blocked.
	select {
	case <-llm.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("enrichment never started")
	}

	// While it is stuck, playback selection still works and breaks still build.
	if _, total := q.Progress(); total != 100 {
		t.Errorf("Progress total = %d, want 100", total)
	}
	if n := dossierCount(t, s); n != 0 {
		t.Fatalf("%d dossiers exist; the test needs enrichment still pending", n)
	}
	facts, err := AssertableFacts(context.Background(), s, pool[0])
	if err != nil {
		t.Fatalf("a track with no dossier must still be airable: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("facts = %v for an unenriched track, want none", facts)
	}

	close(llm.release)
	cancel()
	<-done
}

// TestPersonalityOnlyWhenNoDossier: an empty dossier is a designed outcome, not
// an error. The DJ talks from personality and asserts nothing.
func TestPersonalityOnlyWhenNoDossier(t *testing.T) {
	s := queueStore(t, 1)

	facts, err := AssertableFacts(context.Background(), s, 1)
	if err != nil {
		t.Fatalf("no dossier should not be an error: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("facts = %v, want none", facts)
	}

	// And a confidence-none dossier asserts nothing either.
	if err := StoreDossier(context.Background(), s, 1, emptyDossier()); err != nil {
		t.Fatal(err)
	}
	facts, err = AssertableFacts(context.Background(), s, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 0 {
		t.Errorf("facts = %v from a confidence-none dossier, want none", facts)
	}
}

// TestDossierPickedUpMidSession: the lookup is a live read, so a track enriched
// while the stream is running is used on its next airing with no restart.
func TestDossierPickedUpMidSession(t *testing.T) {
	s := queueStore(t, 1)

	before, err := AssertableFacts(context.Background(), s, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("facts before enrichment = %v", before)
	}

	// Enrichment lands while the "session" is still going.
	d := Dossier{
		ArtistFacts: []string{"The band formed in 1980."},
		Sources:     []string{SourceMusicBrainz},
		Confidence:  ConfidenceHigh,
	}
	if err := StoreDossier(context.Background(), s, 1, d); err != nil {
		t.Fatal(err)
	}

	after, err := AssertableFacts(context.Background(), s, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0] != "The band formed in 1980." {
		t.Errorf("facts after enrichment = %v, want the stored fact with no restart", after)
	}
}

// TestAssertableFactsRespectsConfidence: a low-confidence dossier must not be
// spoken as fact. The DJ may only assert what the dossier actually supports.
func TestAssertableFactsRespectsConfidence(t *testing.T) {
	s := queueStore(t, 1)

	for _, c := range []struct {
		confidence string
		wantFacts  int
	}{
		{ConfidenceHigh, 1},
		{ConfidenceLow, 0},
		{ConfidenceNone, 0},
	} {
		if err := StoreDossier(context.Background(), s, 1, Dossier{
			ArtistFacts: []string{"A fact."},
			Sources:     []string{SourceMusicBrainz},
			Confidence:  c.confidence,
		}); err != nil {
			t.Fatal(err)
		}
		got, err := AssertableFacts(context.Background(), s, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != c.wantFacts {
			t.Errorf("confidence %q yielded %d facts, want %d", c.confidence, len(got), c.wantFacts)
		}
	}
}

// TestScanBlocksButEnrichDoesNot states the startup contract: the tag scan is
// fast and gates serving; enrichment is slow and must not.
func TestScanBlocksButEnrichDoesNot(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Before any scan there is nothing to play, so serving would be pointless.
	pool, err := PlayableTrackIDs(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 0 {
		t.Fatalf("%d playable tracks in an unscanned library", len(pool))
	}

	// A scan makes tracks playable immediately, with no dossier and no loudness.
	for i := 1; i <= 3; i++ {
		if _, err := s.DB().Exec(`INSERT INTO tracks (id, path) VALUES (?, ?)`,
			i, filepath.Join("/music", "t.mp3")+string(rune('0'+i))); err != nil {
			t.Fatal(err)
		}
	}
	pool, err = PlayableTrackIDs(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 3 {
		t.Errorf("%d playable tracks straight after a scan, want 3: serving must not wait for enrichment", len(pool))
	}

	// And none of them has a dossier or a loudness measurement yet.
	var enriched, measured int
	if err := s.DB().QueryRow(`SELECT count(*) FROM dossiers`).Scan(&enriched); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM tracks WHERE loudness_lufs IS NOT NULL`).Scan(&measured); err != nil {
		t.Fatal(err)
	}
	if enriched != 0 || measured != 0 {
		t.Errorf("enriched=%d measured=%d, want both zero", enriched, measured)
	}
}

func TestUnmeasuredTrackPlaysAtUnityGain(t *testing.T) {
	s := queueStore(t, 1)

	// Until a track is measured, loudness_lufs is NULL, which arrives in Go as
	// 0, which mix.GainFor treats as unmeasured and returns unity for. A track
	// that has not been measured yet plays at its own level rather than being
	// slammed 16 dB down.
	lufs, err := TrackLoudness(context.Background(), s, 1)
	if err != nil {
		t.Fatal(err)
	}
	if lufs != 0 {
		t.Errorf("loudness = %v for an unmeasured track, want 0 (unmeasured)", lufs)
	}
}
