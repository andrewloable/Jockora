// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package station

// Ready reports whether a station is worth tuning to yet, and why not.
//
// A STATION GROWS WHILE ENRICHMENT RUNS. Its tracks are selected by dossier, so
// a genre station made on a fresh library starts with whatever happened to be
// classified already -- a dozen tracks -- and fills over hours as the enricher
// works through the library. Letting a listener tune to it at twelve tracks
// means a handful of songs on a loop, which is a worse first impression than
// the station simply not being there yet.
//
// THE CATCH-ALL IS ALWAYS READY. It is defined as everything enrichment could
// not place, including tracks with no dossier at all, so on a fresh library it
// holds nearly the whole library and is the one station that works on day one.
// Gating it would leave a new install with nothing to play, which is the
// opposite of "breaks are optional, music is not".
//
// Once enrichment is DONE the gate lifts entirely: a small station is then
// small because that is all the music there is, not because the answer has not
// arrived, and no amount of waiting will add to it.
func Ready(genre string, tracks int, enrichmentDone bool) (bool, string) {
	// isCatchAll, not a genre comparison: there are TWO catch-all tags. The
	// seeded dial station uses "unsorted" and the dossier vocabulary's fallback
	// is "other", and a rule that knew only one of them would gate the very
	// station that has to work on day one.
	if isCatchAll(genre) || enrichmentDone || tracks >= WarnStationTracks {
		return true, ""
	}
	return false, "still filling"
}
