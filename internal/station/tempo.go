// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

// moodTempo is the tempo a mood implies, in BPM.
//
// FIVE OF SIXTEEN. Most moods have no characteristic tempo -- a romantic or
// cold or lonely song can be any speed -- and giving all sixteen a range would
// quietly halve those stations for no reason anybody could see.
//
// ponytail: these five ranges are a first guess. Tune them against a real
// library before adding a sixth mood; a wrong number here shows up as a station
// that is thinner than it should be, which nothing on screen explains.
var moodTempo = map[string][2]float64{
	"calm":       {50, 95},
	"hypnotic":   {100, 130},
	"euphoric":   {118, 150},
	"propulsive": {110, 160},
	"aggressive": {120, 200},
}

// TempoRange is the tempo a set of moods admits, and whether they constrain it
// at all.
//
// THE UNION, WIDEST WINS, because Filter.Moods is already an OR: a station that
// plays calm or aggressive plays both tempos rather than the gap between them.
//
// ONE UNRANGED MOOD MEANS NO CONSTRAINT. A station playing calm or romantic
// admits every romantic track, and a romantic track may be any tempo -- so
// narrowing would drop music the operator explicitly asked for.
func TempoRange(moods []string) (lo, hi float64, ok bool) {
	for _, m := range moods {
		r, ranged := moodTempo[m]
		if !ranged {
			return 0, 0, false
		}
		if !ok || r[0] < lo {
			lo = r[0]
		}
		if !ok || r[1] > hi {
			hi = r[1]
		}
		ok = true
	}
	return lo, hi, ok
}

// anyTempo is the range used when nothing constrains it.
//
// A range rather than a second query shape: the clause is then always present
// with parameters that cannot exclude anything, and there is one query to read
// instead of two that have to be kept in step. The bounds are far outside what
// the analyser will store -- the sidecar refuses anything outside 30-250.
const anyTempoLo, anyTempoHi = 0, 100000

// tempoBounds is what the filter passes to SQL: the mood's range, or bounds
// nothing can fall outside.
// REPLACEMENT, NOT INTERSECTION, and this is the whole decision.
//
// Five of sixteen moods imply a tempo. Intersecting an explicit 120-180 with
// calm's 50-95 gives an EMPTY station, and an operator who wrote "calm but
// driving" would get silence with nothing on screen to explain it. The explicit
// range is the operator's own words and it wins; with none set, the mood's
// implication is exactly today's behaviour.
func tempoBounds(f Filter) (lo, hi float64) {
	if f.TempoMin != 0 || f.TempoMax != 0 {
		return bounds(f.TempoMin, f.TempoMax, anyTempoLo, anyTempoHi)
	}
	if lo, hi, ok := TempoRange(f.Moods); ok {
		return lo, hi
	}
	return anyTempoLo, anyTempoHi
}
