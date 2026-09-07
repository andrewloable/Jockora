// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A SERVICE THAT IS DOWN IS NOT A TRACK THAT HAS NOTHING TO SAY.
//
// enrichOne stored an empty dossier on ANY generation error, so that the track
// "is not attempted forever". That is right for a track the model looked at and
// could say nothing about. It is destructive for a rate limit: a 429 comes back
// INSTANTLY, with no model latency, so the queue would spin through every
// remaining track in minutes, write an empty dossier for each, and never retry
// one of them. The console would then report every track enriched, and the DJ
// would be permanently factless -- which is the exact quiet degradation the
// dossier design exists to prevent.
//
// Live at the time of writing: a hosted free tier at 1,000 requests a day with
// roughly 900 already spent, and 6,341 tracks left to enrich.
//
// Every test here is TestUnavailable*, which is the -run pattern for this fix.

// unavailableLLM fails with ErrLLMUnavailable for the first n calls.
type unavailableLLM struct {
	failFirst int
	calls     int
}

func (u *unavailableLLM) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	u.calls++
	if u.calls <= u.failFirst {
		return Completion{}, ErrLLMUnavailable
	}
	return ok(goodJSON()), nil
}

func TestUnavailableLeavesTheTrackInTheQueue(t *testing.T) {
	s := queueStore(t, 3)
	q := newQueue(s, &unavailableLLM{failFirst: 1000})
	q.Backoff = time.Millisecond

	err := q.Run(context.Background())
	if !errors.Is(err, ErrLLMUnavailable) {
		t.Fatalf("Run returned %v, want ErrLLMUnavailable", err)
	}
	// THE POINT: no track was marked done. Every one of them is still work.
	if got := dossierCount(t, s); got != 0 {
		t.Errorf("%d dossiers written while the model was unavailable, want 0", got)
	}
}

func TestUnavailableStopsRatherThanBurningTheQueue(t *testing.T) {
	// 200 tracks, a model that never answers: the run must give up after a
	// bounded number of attempts rather than walking the whole library.
	s := queueStore(t, 200)
	llm := &unavailableLLM{failFirst: 1000}
	q := newQueue(s, llm)
	q.Backoff = time.Millisecond

	if err := q.Run(context.Background()); !errors.Is(err, ErrLLMUnavailable) {
		t.Fatalf("Run returned %v, want ErrLLMUnavailable", err)
	}
	if llm.calls > MaxConsecutiveUnavailable {
		t.Errorf("made %d calls against a dead service, want at most %d",
			llm.calls, MaxConsecutiveUnavailable)
	}
}

func TestUnavailableRecoversWhenTheServiceReturns(t *testing.T) {
	// A rate limit is temporary by definition. The track that was refused must
	// be the next one tried, and it must end up with a real dossier.
	s := queueStore(t, 2)
	q := newQueue(s, &unavailableLLM{failFirst: 2})
	q.Backoff = time.Millisecond

	if err := q.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := dossierCount(t, s); got != 2 {
		t.Errorf("%d dossiers, want both tracks enriched once the service came back", got)
	}
	// ON CONTENT, not on the confidence column. Confidence is "none" whenever
	// no facts or lyrics source was consulted, which is true of every dossier
	// this fixture writes -- so counting confidence would have called a
	// perfectly good dossier empty. What distinguishes a refusal from a real
	// answer is whether anything is IN it.
	var empty int
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM dossiers WHERE json NOT LIKE '%synthwave%'`).Scan(&empty); err != nil {
		t.Fatal(err)
	}
	if empty != 0 {
		t.Errorf("%d empty dossiers, want none: the refusal was the service, not the track", empty)
	}
}

func TestUnavailableStillGivesUpOnATrackTheModelCannotDo(t *testing.T) {
	// The original behaviour, preserved: a model that ANSWERS with something
	// unusable has looked at the track, and the track must not be retried for
	// ever. Only "the service is not there" is treated as temporary.
	s := queueStore(t, 1)
	if err := newQueue(s, &badJSONLLM{}).Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := dossierCount(t, s); got != 1 {
		t.Errorf("%d dossiers, want 1 empty one so the track is not attempted forever", got)
	}
}

// badJSONLLM answers, badly. That is a track-level failure, not a service one.
type badJSONLLM struct{}

func (badJSONLLM) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	return ok("not json at all"), nil
}

func TestUnavailableBackoffDefaultsToAMinute(t *testing.T) {
	// Tests set it small; production must not inherit that by accident.
	if got := (&Queue{}).backoff(); got != DefaultUnavailableBackoff {
		t.Errorf("backoff = %v, want %v", got, DefaultUnavailableBackoff)
	}
	if got := (&Queue{Backoff: time.Second}).backoff(); got != time.Second {
		t.Errorf("backoff = %v, want the configured second", got)
	}
}
