// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/store"
)

// BreakWriter turns the current point in the broadcast into a spoken line.
//
// It is the join between three things that were built separately and had no
// way to reach each other: the persona and validator in internal/dj, the
// dossiers in internal/enrich, and the length ladder in this package.
type BreakWriter struct {
	// Persona is read through persona() rather than directly, because a
	// listener can change the jock while the station is running.
	Persona   *dj.Persona
	Validator *dj.Validator
	Session   *dj.Session
	Store     *store.Store

	mu                    sync.Mutex
	prevID, curID, nextID int64
	prevArtist, prevTitle string
}

// SetPersona changes the jock mid-session.
//
// THREE THINGS MOVE TOGETHER OR NONE DO: the persona the prompt is built from,
// the said-lines index the validator checks against, and the voice the break is
// spoken in. A jock swapped in only one of those places speaks as one character
// in the voice of another, or inherits somebody else's phrase history and gets
// its own writing rejected as repetition.
//
// The break already being generated is not recalled. It was written by the
// previous jock and will air in that jock's voice, which is correct: yanking a
// half-rendered break to honour a click is the stutter this design avoids.
func (w *BreakWriter) SetPersona(p *dj.Persona) {
	if p == nil {
		return
	}
	w.mu.Lock()
	w.Persona = p
	w.mu.Unlock()

	if w.Validator != nil && w.Validator.Said != nil {
		w.Validator.Said.JockID = p.ID()
	}
}

// CurrentPersona is whoever is writing breaks right now.
func (w *BreakWriter) CurrentPersona() *dj.Persona { return w.persona() }

// persona reads the current jock.
func (w *BreakWriter) persona() *dj.Persona {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Persona
}

// Voice is the TTS voice of the jock currently on air.
func (w *BreakWriter) Voice() string {
	if p := w.persona(); p != nil {
		return p.VoiceID()
	}
	return ""
}

// SetContext tells the writer which tracks surround the coming break.
//
// Called by the scheduler as the buffer advances. Track ids rather than
// dossiers, because a dossier read is cheap and holding one across a boundary
// is how stale facts get said about the wrong song.
func (w *BreakWriter) SetContext(prevID, curID, nextID int64, prevArtist, prevTitle string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prevID, w.curID, w.nextID = prevID, curID, nextID
	w.prevArtist, w.prevTitle = prevArtist, prevTitle
}

// SetTrackNames tells the writer which artists and titles this break may
// repeat.
//
// Consecutive breaks share tracks by design -- one break's NEXT is the
// following break's CURRENT -- so a DJ that says what is coming and then says
// what just played repeats the name every time. Naming the record is the job;
// the collision index is for reused PHRASING.
func (w *BreakWriter) SetTrackNames(names ...string) {
	if w.Validator != nil {
		w.Validator.SetTrackNames(names...)
	}
}

// Write produces one break, or an error if none could be written.
//
// An error here is NOT a fault. LengthCheck turns it into a dropped break and
// the music continues; that is the designed outcome for a repetitive line, a
// refusal, or a model that is not running at all.
func (w *BreakWriter) Write(ctx context.Context, wordTarget int, placement mix.Placement) (string, error) {
	in, err := w.promptInput(ctx, wordTarget, placement)
	if err != nil {
		return "", err
	}
	prompt, err := dj.BuildBreakPrompt(in)
	if err != nil {
		return "", fmt.Errorf("building the break prompt: %w", err)
	}

	b, err := w.Validator.Generate(ctx, prompt, wordTarget, in.Previous, in.Current, in.Next)
	if err != nil {
		return "", err
	}
	return b.Text(), nil
}

// promptInput gathers everything the prompt is built from.
//
// SPLIT OUT SO THE GATHERING IS TESTABLE ON ITS OWN. Reaching it through Write
// costs a language model, and the half of this that goes wrong is exactly the
// half in here: a query widened and nothing passing the new columns on reads
// precisely like the bug it was meant to fix.
func (w *BreakWriter) promptInput(ctx context.Context, wordTarget int,
	placement mix.Placement) (dj.PromptInput, error) {
	w.mu.Lock()
	prevID, curID, nextID := w.prevID, w.curID, w.nextID
	prevArtist, prevTitle := w.prevArtist, w.prevTitle
	w.mu.Unlock()

	prev, err := w.dossier(ctx, prevID)
	if err != nil {
		return dj.PromptInput{}, err
	}
	cur, err := w.dossier(ctx, curID)
	if err != nil {
		return dj.PromptInput{}, err
	}
	next, err := w.dossier(ctx, nextID)
	if err != nil {
		return dj.PromptInput{}, err
	}

	// The names of the records either side of the break, read from the same
	// place the dossiers come from. Without them the writer has to invent a
	// title for whatever it is introducing, and it does.
	// ALBUM AND YEAR TRAVEL WITH THEM. The scanner writes both and the prompt
	// prints them only where there is no dossier, so an unenriched record has
	// more than a bare title to be interesting about.
	curArtist, curTitle, curAlbum, curYear := w.names(ctx, curID)
	nextArtist, nextTitle, nextAlbum, nextYear := w.names(ctx, nextID)

	return dj.PromptInput{
		Persona:        w.persona(),
		Previous:       prev,
		Current:        cur,
		Next:           next,
		PreviousArtist: prevArtist,
		PreviousTitle:  prevTitle,
		CurrentArtist:  curArtist,
		CurrentTitle:   curTitle,
		NextArtist:     nextArtist,
		NextTitle:      nextTitle,
		CurrentAlbum:   curAlbum,
		CurrentYear:    curYear,
		NextAlbum:      nextAlbum,
		NextYear:       nextYear,
		Placement:      placement.String(),
		// The window the writer aims at, derived from the target it was given
		// rather than passed separately, so the two can never disagree.
		WindowSeconds: float64(wordTarget) / dj.WordsPerSecond,
		Schema:        dj.BreakSchema(prev, cur, next),
		IsColdOpen:    w.Session.IsColdOpen(),
	}, nil
}

// names reads one track's artist and title.
//
// A missing name is not an error and not worth a log line: the scanner falls
// back to the filename, so an empty result here means the row is genuinely
// nameless, and the prompt simply omits it.
func (w *BreakWriter) names(ctx context.Context, id int64) (artist, title, album string, year int) {
	if id == 0 || w.Store == nil {
		return "", "", "", 0
	}
	_ = w.Store.DB().QueryRowContext(ctx, `
		SELECT COALESCE(artist,''), COALESCE(title,''), COALESCE(album,''), COALESCE(year,0)
		  FROM tracks WHERE id = ?`, id).Scan(&artist, &title, &album, &year)
	return artist, title, album, year
}

// dossier reads one track's dossier. A track with none is not an error: it
// means personality-only talk, which is the whole anti-hallucination design.
func (w *BreakWriter) dossier(ctx context.Context, id int64) (*enrich.Dossier, error) {
	if id == 0 || w.Store == nil {
		return nil, nil
	}
	d, ok, err := enrich.LoadDossier(ctx, w.Store, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &d, nil
}

// Aired records that a break went out, which clears the cold open.
func (w *BreakWriter) Aired() { w.Session.BreakAired() }

// Stats is how the break writer has been doing, for the console.
//
// The validator has counted breaks, drops and the REASON for each drop since it
// was written, and nothing outside a gate tool ever read them -- so an operator
// asking why the DJ is quiet had the answer sitting in memory with no way to
// see it.
func (w *BreakWriter) Stats() (dj.Stats, bool) {
	if w == nil || w.Validator == nil {
		return dj.Stats{}, false
	}
	return w.Validator.Stats(), true
}
