// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/store"
)

// UnsortedTag is the catch-all bucket.
//
// It is NOT a genre. It holds tracks that have no dossier yet, which on a large
// library is most of them for the first few days. They must appear somewhere:
// a dial that silently omits four thousand tracks looks like a broken scan, and
// the operator has no way to tell the difference.
const UnsortedTag = "unsorted"

// MinStationTracks is the smallest bucket worth showing as its own station.
//
// TEN, not eight: at ten tracks of average length a listener hears the same
// record again inside forty minutes, which is the point where a station stops
// sounding like a station. Everything smaller is folded into the catch-all,
// which is honest -- those tracks are still there, they just do not deserve a
// dial position.
const MinStationTracks = 10

// WarnStationTracks is where a station stops feeling thin.
//
// Not a refusal: fifty is a judgement, and an operator who wants a station of
// twelve deep cuts is entitled to one. It is the number the console warns at so
// they choose it rather than discover it.
const WarnStationTracks = 50

// CheckThreshold reports whether a station may run, and whether to warn.
//
// Two answers rather than one, because "too small to work" and "smaller than
// most people want" are different things and a console says different words
// about them.
func CheckThreshold(n int) (ok, warn bool) {
	return n >= MinStationTracks, n >= MinStationTracks && n < WarnStationTracks
}

// Station is one position on the dial.
type Station struct {
	Tag    string `json:"tag"`
	Tracks int    `json:"tracks"`
	// Jock is the persona id that best fits this station's tags and moods,
	// empty when no persona directory was supplied.
	Jock     string `json:"jock,omitempty"`
	JockName string `json:"jock_name,omitempty"`
	// Moods are the feelings that actually occur in this bucket, most common
	// first, so the dial can show what a station sounds like rather than only
	// what it is called.
	Moods []string `json:"moods,omitempty"`
}

// Dial is the whole set of stations, in the order they should be shown.
type Dial struct {
	Stations []Station `json:"stations"`
	// Enriched and Total say how much of the library the dial is actually
	// based on. A dial built from 3% of a library is a guess, and the client
	// should be able to say so rather than presenting it as settled.
	Enriched int `json:"enriched"`
	Total    int `json:"total"`
}

// ProposeDial builds the dial from the library's own dossiers.
//
// PROPOSE, not configure. §22A's whole point is that first run shows
//
//	ROCK 412 · SYNTHWAVE 208 · OPM 173 · AMBIENT 96 · UNSORTED 240
//
// rather than a blank form. This is only possible because station_tags is a
// CLOSED vocabulary: grouping free-text tags would produce forty near-duplicate
// stations and no usable dial at all.
func ProposeDial(ctx context.Context, s *store.Store, personas []*dj.Persona) (Dial, error) {
	var d Dial

	if err := s.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM tracks WHERE playable = 1`).Scan(&d.Total); err != nil {
		return d, fmt.Errorf("station: counting tracks: %w", err)
	}

	rows, err := s.DB().QueryContext(ctx, `
		SELECT d.json FROM dossiers d
		  JOIN tracks t ON t.id = d.track_id
		 WHERE t.playable = 1`)
	if err != nil {
		return d, fmt.Errorf("station: reading dossiers: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only

	counts := map[string]int{}
	moods := map[string]map[string]int{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return d, err
		}
		var doc enrich.Dossier
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			continue // one unreadable row must not cost the whole dial
		}
		d.Enriched++

		// A track with two tags belongs to BOTH stations. Stations are views of
		// one library, not a partition of it, so the counts deliberately sum to
		// more than the library.
		for _, tag := range doc.StationTags {
			tag = strings.ToLower(strings.TrimSpace(tag))
			if tag == "" {
				continue
			}
			counts[tag]++
			if moods[tag] == nil {
				moods[tag] = map[string]int{}
			}
			for _, m := range doc.Mood {
				moods[tag][strings.ToLower(strings.TrimSpace(m))]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return d, err
	}

	// Unenriched tracks, plus every bucket too small to stand on its own.
	unsorted := d.Total - d.Enriched
	if unsorted < 0 {
		unsorted = 0
	}

	for tag, n := range counts {
		if n < MinStationTracks {
			unsorted += n
			continue
		}
		d.Stations = append(d.Stations, Station{Tag: tag, Tracks: n, Moods: topMoods(moods[tag], 3)})
	}

	// Biggest first: the dial should open on the station the library is
	// actually made of.
	sort.Slice(d.Stations, func(i, j int) bool {
		if d.Stations[i].Tracks != d.Stations[j].Tracks {
			return d.Stations[i].Tracks > d.Stations[j].Tracks
		}
		return d.Stations[i].Tag < d.Stations[j].Tag
	})

	// Both catch-alls sink to the end, whatever their size. They are holding
	// pens, not stations, and a fresh library whose dial opened on them would
	// look like it had failed.
	sort.SliceStable(d.Stations, func(i, j int) bool {
		return !isCatchAll(d.Stations[i].Tag) && isCatchAll(d.Stations[j].Tag)
	})

	if unsorted > 0 {
		// Always last, whatever its size. It is a holding pen, not a station,
		// and putting it first on a fresh library would make the dial look like
		// it had failed.
		d.Stations = append(d.Stations, Station{Tag: UnsortedTag, Tracks: unsorted})
	}

	assignJocks(d.Stations, personas)
	return d, nil
}

// isCatchAll reports whether a tag is a holding pen rather than a sound.
func isCatchAll(tag string) bool {
	return tag == UnsortedTag || tag == enrich.FallbackStationTag
}

// assignJocks matches a persona to each station on its declared genres AND
// moods, which is what makes the dial feel authored rather than generated.
func assignJocks(stations []Station, personas []*dj.Persona) {
	if len(personas) == 0 {
		return
	}
	for i := range stations {
		// NEITHER CATCH-ALL GETS A JOCK. "unsorted" is not enriched yet and
		// "other" is enriched but fits no genre; neither describes a SOUND, so
		// there is no character to match against. Matching anyway is not
		// harmless: measured on a real library, the "other" bucket drew a jock
		// purely from the literal word "other" and the dial then presented that
		// accident as a deliberate pairing.
		if isCatchAll(stations[i].Tag) {
			continue
		}
		chosen, _ := dj.SelectPersona(personas, []string{stations[i].Tag}, stations[i].Moods)
		if chosen != nil {
			stations[i].Jock, stations[i].JockName = chosen.ID(), chosen.Name()
		}
	}
}

// topMoods returns the n most common moods in a bucket.
func topMoods(counts map[string]int, n int) []string {
	type kv struct {
		mood string
		n    int
	}
	var all []kv
	for m, c := range counts {
		if m != "" {
			all = append(all, kv{m, c})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].mood < all[j].mood
	})
	if len(all) > n {
		all = all[:n]
	}
	out := make([]string, 0, len(all))
	for _, kv := range all {
		out = append(out, kv.mood)
	}
	return out
}
