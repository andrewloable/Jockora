// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

// The advert rotation, read from the database on every pick.
//
// It used to be loaded once per station start, and a station runs while it has
// a listener -- so a busy one runs for days. An operator who deleted an advert
// and kept hearing it all afternoon reported it as broken, and they were right.

// AdSource is the pool, fresh. Wired into dj.AdRotation.Source.
//
// A FUNCTION OVER THE STORE rather than a method on App, because the rotation
// is built beside the pipeline -- before the App exists -- and threading a
// half-built App through would be worse than passing the one thing it needs.
func AdSource(s *store.Store) func(context.Context) ([]dj.Ad, error) {
	return func(ctx context.Context) ([]dj.Ad, error) { return readAds(ctx, s) }
}

func readAds(ctx context.Context, s *store.Store) ([]dj.Ad, error) {
	if s == nil {
		return nil, fmt.Errorf("app: no library to read adverts from")
	}
	rows, err := s.ListAds(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]dj.Ad, 0, len(rows))
	for _, r := range rows {
		out = append(out, dj.Ad{
			// THE ROW ID, AS A STRING. dj.Ad.ID is a string and the row id is
			// an integer, and a silent mismatch here means the cooldown never
			// matches a row -- every advert looks eligible for ever, which
			// reads as a rotation ignoring its own cooldown.
			ID:          strconv.FormatInt(r.ID, 10),
			Brand:       r.Brand,
			Script:      r.Script,
			LastAiredAt: r.LastAiredAt,
		})
	}
	return out, nil
}

// AdAired records that one went out. Wired into dj.AdRotation.Aired.
func AdAired(s *store.Store) func(context.Context, string, time.Time) error {
	return func(ctx context.Context, id string, at time.Time) error {
		if s == nil {
			return fmt.Errorf("app: no library to record an advert in")
		}
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return fmt.Errorf("app: advert id %q is not a row id: %w", id, err)
		}
		return s.MarkAdAired(ctx, n, at)
	}
}

// ImportPackAds puts a JockPack's adverts in the table, once.
//
// A PACK TRAVELS WITH ITS ADVERTS -- a jock who arrives with nothing to sell is
// not the character -- and this keeps that while making them editable and
// deletable like everything else the operator wrote.
//
// AND THEY BECOME GLOBAL, which is a real change in what a pack advert is. A
// pack advert used to belong to its jock: only that station's jock ever read
// it. Adverts are one shared pool now, so an imported one is read by EVERY
// jock -- and a pack advert written in a strong voice will come out of the
// wrong mouth. There is no heuristic for that and there should not be one: the
// operator reads them in the Ads list and deletes what does not fit, which
// takes ten seconds and which a detector would get wrong for ever. The caller
// says so in the log line, once, at import.
//
// MATCHED ON BRAND PLUS SCRIPT, because a pack advert has no database identity
// of its own: its pack-local id is "ad-01" and collides with every other pack's.
// Re-importing the same pack therefore inserts nothing.
func ImportPackAds(ctx context.Context, s *store.Store, ads []dj.Ad) (int, error) {
	if len(ads) == 0 {
		return 0, nil
	}
	existing, err := s.ListAds(ctx)
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(existing))
	for _, e := range existing {
		seen[e.Brand+"\x00"+e.Script] = true
	}

	var added int
	for _, ad := range ads {
		key := ad.Brand + "\x00" + ad.Script
		if seen[key] {
			continue
		}
		if _, err := s.CreateAd(ctx, store.Ad{Brand: ad.Brand, Script: ad.Script}); err != nil {
			return added, err
		}
		seen[key] = true
		added++
	}
	return added, nil
}

// Writing one, through whatever model the console is currently pointed at.
//
// The operator types what the product is, presses a button, and reads the blurb
// before anything is saved. Through the Switchable everything else already
// holds, so a provider changed on the model page takes effect here immediately
// and there is no second thing to reconfigure.

// WriteAd turns the operator's brief into the blurb a DJ speaks.
//
// A missing model returns enrich.ErrNoModel UNWRAPPED, so the caller can tell
// it from a model that failed with errors.Is. Those are different problems: one
// sends the operator to the model page and the other sends them to their
// provider, and reporting them alike sends them to configure something that is
// already configured.
//
// NOTHING IS SAVED HERE. Writing is a preview -- the operator reads it and
// decides -- and the real-brand check is not run here either: it travels on the
// Ad and the API layer is what reports it, because a warning nobody sees is a
// warning that did not happen.
func (a *App) WriteAd(ctx context.Context, in dj.AdBrief) (dj.Ad, error) {
	if a.opts.LLM == nil || !a.opts.LLM.Configured() {
		return dj.Ad{}, enrich.ErrNoModel
	}

	// ZERO TOKENS, so the writer takes the default break budget -- which is 200
	// against a schema that caps the script at 360 characters, comfortably more
	// than the sampler can legally produce. The schema is what bounds an
	// advert's length; a token cap here would only ever truncate.
	//
	// NewWriter, not NewWriterAt, because the writing temperature is the right
	// one: the operator presses the button again when they do not like the
	// blurb, so a deterministic writer would hand them the same sentence back
	// and look broken -- but the rule this prompt exists to enforce is INVENT
	// NO FACTS ABOUT THE PRODUCT, and a hotter sampler is exactly how a price
	// nobody quoted gets into somebody's real advert.
	w := dj.NewWriter(a.opts.LLM, 0)
	ad, err := dj.WriteAdFromBrief(ctx, w, in)
	if err != nil {
		a.log.Warn("advert could not be written", "brand", in.Brand, "err", err)
		return dj.Ad{}, err
	}

	// LOGGED WITH THE WARNING, because an advert that went out naming somebody
	// else's trademark is the kind of thing that gets asked about days later,
	// and the log is the only place the answer will be.
	brand, flagged := ad.RealBrandWarning()
	a.log.Info("advert written", "brand", ad.Brand,
		"real_brand_warning", flagged, "matched", brand)
	return ad, nil
}

// consoleAds is what the Ads section talks to.
//
// The CRUD is the store's, unchanged; only the drafting is the App's. Rather
// than five pass-through methods on App that exist to be forwarded, the two
// halves are joined here, where the join is one line and visible.
type consoleAds struct {
	*store.Store
	app *App
}

func (c consoleAds) WriteAd(ctx context.Context, in dj.AdBrief) (dj.Ad, error) {
	return c.app.WriteAd(ctx, in)
}
