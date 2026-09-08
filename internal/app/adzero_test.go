// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"testing"

	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/station"
)

// TestAdEveryNZeroReachesTheStation: applyTuning skipped the assignment when
// the value was 0 -- the guard existed to avoid a divide by zero in adDue and
// the flag help was never updated to match. The guard now lives where the
// writer is built, so 0 can travel all the way here meaning what it says.
func TestAdEveryNZeroReachesTheStation(t *testing.T) {
	was := station.AdEveryNBreaks
	t.Cleanup(func() { station.AdEveryNBreaks = was })

	applyTuning(&config.Config{AdEveryNBreaks: 0}, quietLogger())
	if station.AdEveryNBreaks != 0 {
		t.Errorf("station.AdEveryNBreaks = %d after -ad-every-n-breaks 0; the flag says it disables adverts",
			station.AdEveryNBreaks)
	}

	applyTuning(&config.Config{AdEveryNBreaks: 6}, quietLogger())
	if station.AdEveryNBreaks != 6 {
		t.Errorf("station.AdEveryNBreaks = %d, want 6", station.AdEveryNBreaks)
	}
}
