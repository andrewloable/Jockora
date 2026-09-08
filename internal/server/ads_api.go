// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

// The operator sells airtime.
//
// They describe a product, read the blurb the model wrote, edit it if they
// like, and save it. Everything here turns on the difference between WRITING
// and SAVING: /write calls the model and stores nothing, and the CRUD routes
// store what they are given and never call the model. An advert re-written on
// save would air words the operator never saw, which is the one thing they
// pressed the button to prevent.

// Ads is advert management, plus the model that drafts one.
type Ads interface {
	ListAds(ctx context.Context) ([]store.Ad, error)
	GetAd(ctx context.Context, id int64) (store.Ad, error)
	CreateAd(ctx context.Context, a store.Ad) (int64, error)
	UpdateAd(ctx context.Context, a store.Ad) error
	DeleteAd(ctx context.Context, id int64) error
	WriteAd(ctx context.Context, in dj.AdBrief) (dj.Ad, error)
}

// MaxAdAboutRunes and MaxAdDeliveryRunes cap the free text on the write form.
//
// s.decode wraps every admin body in a 4KB MaxBytesReader, which is good news
// twice over: an operator's free text cannot become an unbounded prompt, and
// nobody had to invent a limit. The bad news is the FAILURE. Over 4KB the
// decode fails and they get a flat 400 saying the body is too large -- not
// attached to the box they were typing in, and with no hint of how much is too
// much. Somebody pasting a product description hits an opaque wall.
//
// These sit well clear of the 4KB body, so the operator always meets the
// friendly message on the right box and never the flat one. RUNES, not bytes:
// a paragraph of accented characters is under the limit in characters and over
// it in bytes, and refusing it would be Jockora-9jo one field over.
const (
	MaxAdAboutRunes    = 1500
	MaxAdDeliveryRunes = 200
)

// SetAds wires advert management. Without it every /admin/ads route reports
// 503, the way an unwired station list already does.
func (s *Server) SetAds(a Ads) { s.ads = a }

// adView is an advert as the console sees it.
type adView struct {
	ID          int64  `json:"id"`
	Brand       string `json:"brand"`
	Brief       string `json:"brief"`
	Delivery    string `json:"delivery"`
	Script      string `json:"script"`
	LastAiredAt string `json:"last_aired_at,omitempty"`
}

// serveAds handles every /admin/ads route.
func (s *Server) serveAds(w http.ResponseWriter, r *http.Request, path string) {
	if s.ads == nil {
		s.writeJSON(w, http.StatusServiceUnavailable,
			map[string]any{"error": "this server cannot manage adverts"})
		return
	}
	rest := strings.TrimPrefix(path, "/admin/ads")

	if rest == "" || rest == "/" {
		switch r.Method {
		case http.MethodGet:
			s.listAds(w, r)
		case http.MethodPost:
			s.createAd(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// "write" BEFORE the id, or ParseInt eats it and the route 404s -- the
	// same trap /admin/stations/derive has.
	if rest == "/write" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.writeAdBlurb(w, r)
		return
	}

	id, action, ok := userTarget(rest)
	if !ok || action != "" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.updateAd(w, r, id)
	case http.MethodDelete:
		s.deleteAd(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listAds(w http.ResponseWriter, r *http.Request) {
	rows, err := s.ads.ListAds(r.Context())
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]adView, 0, len(rows))
	for _, a := range rows {
		v := adView{ID: a.ID, Brand: a.Brand, Brief: a.Brief,
			Delivery: a.Delivery, Script: a.Script}
		// NEVER AIRED IS AN EMPTY STRING, not 1970. The console shows this
		// beside an advert and "aired 1 January 1970" is a bug report.
		if !a.LastAiredAt.IsZero() {
			v.LastAiredAt = a.LastAiredAt.UTC().Format(time.RFC3339)
		}
		out = append(out, v)
	}
	s.writeJSON(w, http.StatusOK, out)
}

// adBody is what the console posts to save one. NO SCRIPT REGENERATION: these
// are the words the operator read.
type adBody struct {
	Brand    string `json:"brand"`
	Brief    string `json:"brief"`
	Delivery string `json:"delivery"`
	Script   string `json:"script"`
}

func (s *Server) createAd(w http.ResponseWriter, r *http.Request) {
	var body adBody
	if !s.decode(w, r, &body) {
		return
	}
	if field, msg := validateAdSave(body); field != "" {
		s.writeFieldError(w, field, msg)
		return
	}
	id, err := s.ads.CreateAd(r.Context(), store.Ad{
		Brand: strings.TrimSpace(body.Brand), Brief: strings.TrimSpace(body.Brief),
		Delivery: strings.TrimSpace(body.Delivery), Script: strings.TrimSpace(body.Script)})
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) updateAd(w http.ResponseWriter, r *http.Request, id int64) {
	var body adBody
	if !s.decode(w, r, &body) {
		return
	}
	if field, msg := validateAdSave(body); field != "" {
		s.writeFieldError(w, field, msg)
		return
	}
	err := s.ads.UpdateAd(r.Context(), store.Ad{ID: id,
		Brand: strings.TrimSpace(body.Brand), Brief: strings.TrimSpace(body.Brief),
		Delivery: strings.TrimSpace(body.Delivery), Script: strings.TrimSpace(body.Script)})
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such advert", http.StatusNotFound)
		return
	}
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteAd(w http.ResponseWriter, r *http.Request, id int64) {
	err := s.ads.DeleteAd(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no such advert", http.StatusNotFound)
		return
	}
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateAdSave holds an operator's own words to the same rules the model's
// are held to, and names the box to highlight.
//
// THE CAP IS WHAT MAKES AN ADVERT FIT ITS SLOT. A 900-character advert is not a
// style choice, it is a break that gets dropped -- so the length, the minimum
// words, the degenerate run, the prompt echo and "the script says the brand"
// all apply to text they typed themselves.
//
// The REAL-BRAND MATCH IS NOT HERE. It is reported by /write and never refused:
// the denylist was written when a MODEL invented the brands, and an operator
// naming their own advertiser is the opposite situation.
func validateAdSave(body adBody) (field, msg string) {
	ad := dj.Ad{Brand: strings.TrimSpace(body.Brand), Script: strings.TrimSpace(body.Script)}
	if ad.Brand == "" {
		return "brand", "an advert needs the name of what it is selling"
	}
	if ad.Script == "" {
		return "script", "write the advert, or let the model draft one"
	}
	if err := ad.Validate(); err != nil {
		// THE VALIDATOR'S OWN SENTENCE, on the script box. It already says
		// which rule and by how much -- "advert is 412 characters, over the
		// 360-character slot" -- and paraphrasing it here would drift.
		return "script", strings.TrimPrefix(err.Error(), "dj: ")
	}
	if n := utf8.RuneCountInString(body.Brief); n > MaxAdAboutRunes {
		return "brief", fmt.Sprintf("that is %d characters; keep it under %d", n, MaxAdAboutRunes)
	}
	if n := utf8.RuneCountInString(body.Delivery); n > MaxAdDeliveryRunes {
		return "delivery", fmt.Sprintf("that is %d characters; keep it under %d", n, MaxAdDeliveryRunes)
	}
	return "", ""
}

// writeAdBlurb drafts one from the operator's brief. IT SAVES NOTHING.
func (s *Server) writeAdBlurb(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Brand    string `json:"brand"`
		About    string `json:"about"`
		Delivery string `json:"delivery"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	in := dj.AdBrief{Brand: strings.TrimSpace(body.Brand),
		About: strings.TrimSpace(body.About), Delivery: strings.TrimSpace(body.Delivery)}
	switch {
	case in.Brand == "":
		s.writeFieldError(w, "brand", "name what the advert is selling")
		return
	case in.About == "":
		s.writeFieldError(w, "about", "say what the product is, in your own words")
		return
	case utf8.RuneCountInString(in.About) > MaxAdAboutRunes:
		s.writeFieldError(w, "about", fmt.Sprintf("that is %d characters; keep it under %d",
			utf8.RuneCountInString(in.About), MaxAdAboutRunes))
		return
	case utf8.RuneCountInString(in.Delivery) > MaxAdDeliveryRunes:
		s.writeFieldError(w, "delivery", fmt.Sprintf("that is %d characters; keep it under %d",
			utf8.RuneCountInString(in.Delivery), MaxAdDeliveryRunes))
		return
	}

	deadline := s.deriveDeadline
	if deadline <= 0 {
		deadline = DeriveDeadline
	}
	ctx, cancel := context.WithTimeout(r.Context(), deadline)
	defer cancel()

	ad, err := s.ads.WriteAd(ctx, in)
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		// THE SAME SENTENCE THE STATION BRIEF GIVES, because it is the same
		// cause: the enrichment queue is serial and shares the model, so a
		// draft fired mid-enrichment queues behind a dossier pass.
		s.writeJSON(w, http.StatusGatewayTimeout, map[string]any{
			"error": "the language model did not answer in time. Enrichment may be running " +
				"and using it; you can pause that on the Overview. You can also write the " +
				"advert yourself.",
		})
		return
	case errors.Is(err, enrich.ErrNoModel):
		// BOTH WAYS OUT, named. Drafting is a convenience; the form still
		// works, and a bare 503 sends the operator to the logs for something
		// the console can simply say.
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "no language model is configured, so a blurb cannot be drafted. " +
				"Choose one on the Model page, or write the advert yourself.",
		})
		return
	case err != nil:
		// 502, not 503: the model IS configured and answered badly, which is a
		// different thing for the operator to go and fix.
		s.writeJSON(w, http.StatusBadGateway,
			map[string]any{"error": "the language model could not write it: " + err.Error()})
		return
	}

	// THE WARNING TRAVELS WITH A 200. An operator naming their own advertiser
	// is not a policy breach, and refusing would make the feature useless to
	// anybody whose client is a real company. They read it and decide.
	matched, flagged := ad.RealBrandWarning()
	out := map[string]any{"brand": ad.Brand, "script": ad.Script}
	if flagged {
		out["matched"] = matched
		out["warning"] = "this advert names " + matched +
			", which somebody else owns. You can use it anyway; make sure you have the right to."
	}
	s.writeJSON(w, http.StatusOK, out)
}
