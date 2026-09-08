// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/station"
)

// Turning an operator's description into a station, before anything is saved.
//
// The operator types "late-night rock for driving, nothing after 2005", presses
// a button, and sees what it would become AND how much music it would hold.
// Both halves matter: four tags and no number does not answer the question they
// actually have, which is whether the station has anything in it.

// briefLogLimit is how much of a brief reaches the log.
//
// Enough to recognise which one it was, not the whole thing: a brief can be a
// paragraph and a log line is read at a glance.
const briefLogLimit = 80

// ResolveStationBrief turns a brief into station parameters using WHATEVER
// MODEL THE CONSOLE IS CURRENTLY POINTED AT.
//
// Through the Switchable everything else already holds, so a provider changed
// on the model page takes effect here immediately and there is no second thing
// to reconfigure.
//
// A missing model returns enrich.ErrNoModel unwrapped, so the caller can tell
// it from a model that failed with errors.Is. Those are different problems: one
// sends the operator to the model page and the other sends them to their
// provider, and reporting them alike sends them to configure something that is
// already configured.
func (a *App) ResolveStationBrief(ctx context.Context, brief string) (enrich.StationParams, error) {
	if a.opts.LLM == nil || !a.opts.LLM.Configured() {
		return enrich.StationParams{}, enrich.ErrNoModel
	}

	p, err := enrich.DeriveStationParams(ctx, a.opts.LLM, brief)
	if err != nil {
		a.log.Warn("station brief could not be resolved", "brief", shortBrief(brief), "err", err)
		return enrich.StationParams{}, err
	}
	// LOGGED, because the live box is where these are diagnosed and a derive
	// that quietly picked the wrong four tags leaves no other trace at all.
	a.log.Info("station brief resolved",
		"brief", shortBrief(brief), "name", p.Name,
		"genres", strings.Join(p.Genres, ","), "moods", strings.Join(p.Moods, ","))
	return p, nil
}

// shortBrief is a brief a log line can carry.
func shortBrief(brief string) string {
	brief = strings.Join(strings.Fields(brief), " ")
	if r := []rune(brief); len(r) > briefLogLimit {
		return string(r[:briefLogLimit]) + "…"
	}
	return brief
}

// CountMatching is how much music a filter would select.
//
// ZERO IS A REAL ANSWER and it means an empty station -- which is precisely
// what the operator needs to see before they save one. No store is therefore an
// ERROR rather than a zero: answering 0 for a missing library would tell them
// their music is gone.
func (a *App) CountMatching(ctx context.Context, f station.Filter) (int, error) {
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return 0, fmt.Errorf("app: no library to count against")
	}
	ids, err := f.TrackIDs(ctx, a.opts.Library.Store)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}
