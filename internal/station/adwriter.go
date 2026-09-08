// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"sync"
	"time"

	"github.com/andrewloable/jockora/internal/clock"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/mix"
)

// AdEveryNBreaks is how often a break slot is given to an advert instead.
//
// One in four, which is roughly a commercial radio hour without being one. This
// is the most TASTE-SENSITIVE number in the station and the first thing to
// change if adverts start to grate.
var AdEveryNBreaks = 4

// MinAdInterval is the floor between adverts regardless of the ratio above.
//
// A short session that happens to hit several break slots quickly must not turn
// into an advert every few minutes. The ratio governs the long run; this
// governs the short one.
var MinAdInterval = 90 * time.Minute

// AdWriter airs adverts in some of the DJ's break slots.
//
// A WRAPPER AROUND THE WRITER, not a second path through the pipeline. An
// advert is rendered, length-checked, relocated and enqueued by exactly the
// machinery a break is, so everything already proven about airing breaks --
// the retry ladder, the between-gap fallback, the never-truncate rule --
// applies unchanged. The only decision made here is WHOSE WORDS to hand back.
//
// Before this existed, GenerateAdPool, AdRotation and the whole ads table had
// no callers at all: adverts could be written and could never be heard.
type AdWriter struct {
	Writer   Writer
	Rotation *dj.AdRotation
	Clock    clock.Clock

	mu       sync.Mutex
	breaks   int
	lastAd   time.Time
	everyN   int
	interval time.Duration
}

// NewAdWriter wraps w so that one break slot in AdEveryNBreaks becomes an
// advert. A nil rotation returns w unchanged, so a station with no ad pool
// behaves exactly as it did before.
func NewAdWriter(w Writer, rotation *dj.AdRotation, clk clock.Clock) Writer {
	if rotation == nil {
		return w
	}
	// ZERO MEANS NO ADVERTS, which is what the flag help has always said and
	// what the code did not do.
	//
	// The guard belongs HERE rather than inside adDue, so a zero divisor cannot
	// exist in a live writer at all: adDue does breaks % everyN, and a zero
	// that got that far would take the station off air on the first break slot
	// rather than merely being ignored. Not wrapping is also honest -- a
	// station with no adverts should not carry an advert writer that always
	// declines. Negative is the same state; nobody means one advert every minus
	// two breaks. Jockora-bpr.
	if AdEveryNBreaks <= 0 {
		return w
	}
	return &AdWriter{Writer: w, Rotation: rotation, Clock: clk,
		everyN: AdEveryNBreaks, interval: MinAdInterval}
}

func (a *AdWriter) now() time.Time {
	if a.Clock != nil {
		return a.Clock.Now()
	}
	return clock.Real{}.Now()
}

// Write returns either an advert or the DJ's own break.
func (a *AdWriter) Write(ctx context.Context, wordTarget int, placement mix.Placement) (string, error) {
	if !a.adDue() {
		return a.Writer.Write(ctx, wordTarget, placement)
	}

	ad, err := a.Rotation.Next(ctx, a.now())
	if err != nil {
		// An empty or fully-cooled-down pool is not a failure. The slot goes
		// back to the DJ, which is the whole point of adverts being optional.
		return a.Writer.Write(ctx, wordTarget, placement)
	}

	a.mu.Lock()
	a.lastAd = a.now()
	a.mu.Unlock()
	// Recorded through the rotation, which writes to the row when there
	// is one behind it -- the cooldown is global now.
	_ = a.Rotation.MarkAired(ctx, ad.ID, a.now()) //nolint:errcheck // a lost cooldown is not a reason to drop the advert

	return ad.Script, nil
}

// adDue counts the slot and reports whether this one belongs to an advert.
//
// Counted even when the answer is no, because the ratio is over BREAK SLOTS
// rather than over wall-clock time: a quiet hour with two boundaries should not
// bank four adverts for the next busy one.
func (a *AdWriter) adDue() bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.breaks++
	if a.breaks%a.everyN != 0 {
		return false
	}
	// The first advert of a session is allowed immediately; only the gap
	// between adverts is floored.
	if !a.lastAd.IsZero() && a.now().Sub(a.lastAd) < a.interval {
		return false
	}
	return true
}
