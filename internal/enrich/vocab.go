// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package enrich

// The closed vocabularies a dossier may use.
//
// These are the expensive fields to get wrong. A dossier is written once and
// reused for the life of the library; re-enriching to fix a field costs hours of
// LLM time and a full MusicBrainz throttle cycle, so free text written badly now
// is paid for twice later.
//
// Left unconstrained, a model emits "sad", "melancholic", "wistful", "blue" and
// "heartbroken" for five tracks that belong together, and any grouping built on
// the field produces near-duplicate buckets. Probe evidence from the real model:
// asked for genres without constraint it returned "retrowave", "night_drive",
// "80s_vibe", "neon" and "darksynth" -- exactly the drift that would produce
// forty near-duplicate stations.
//
// These lists are CODE. They change deliberately, and changing them does NOT
// retroactively fix dossiers already stored.
var (
	// StationTags is the genre vocabulary the dial is built from.
	StationTags = []string{
		"rock", "alternative", "metal", "punk", "pop",
		"electronic", "synthwave", "house", "techno", "ambient",
		"jazz", "blues", "classical", "hiphop", "rnb",
		"soul", "funk", "folk", "country", "reggae",
		"latin", "world", "opm", "soundtrack", "spokenword", "other",
	}

	// Moods is the feeling vocabulary stations may filter on.
	Moods = []string{
		"melancholic", "euphoric", "aggressive", "calm", "nocturnal",
		"propulsive", "wistful", "playful", "tense", "warm",
		"cold", "triumphant", "romantic", "lonely", "hypnotic", "raw",
	}
)

// FallbackStationTag is used when nothing in the vocabulary fits. It is stored
// in place of the raw value, never alongside it.
const FallbackStationTag = "other"

// Confidence values a dossier may carry.
const (
	ConfidenceHigh = "high"
	ConfidenceLow  = "low"
	// ConfidenceNone is reused from the ramp vocabulary: it means the same
	// thing, that nothing here may be asserted on air.
)

var (
	stationTagSet = toSet(StationTags)
	moodSet       = toSet(Moods)
)

func toSet(vals []string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

// filterVocab keeps only in-vocabulary values, deduplicated, preserving order.
//
// Out-of-vocabulary values are DROPPED, never stored "just in case": storing
// them is exactly the free-text drift the vocabularies exist to prevent.
// Deduplication is required even under a schema enum, because GBNF enforces set
// membership but not uniqueness -- the probe returned ["synthwave","synthwave"].
func filterVocab(in []string, allowed map[string]bool, limit int) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range in {
		if !allowed[v] || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}
