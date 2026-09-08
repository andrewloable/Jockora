// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andrewloable/jockora/internal/obs"
	"github.com/andrewloable/jockora/internal/store"
)

// The live log level.
//
// It was fixed at build time -- main.go built the handler with
// slog.HandlerOptions{Level: slog.LevelInfo} -- so getting more detail meant
// changing a flag and redeploying. A redeploy restarts the station, cuts off
// whoever is listening, and may destroy the very state that was being
// investigated, which makes it the worst possible tool for reproducing an
// intermittent fault.

// LogLevelSetting is where the operator's choice is stored.
const LogLevelSetting = "log_level"

// DefaultLogLevel is what a fresh install records, before an operator has
// chosen anything.
//
// WARN, because info on a scanning library is more than twenty consecutive
// "scanning elapsed=Ns files=N" lines plus break scheduled, break slot offered
// and station refilled every few minutes -- and the one line that matters,
// SERVING WITHOUT AUTHENTICATION ON A NON-LOOPBACK ADDRESS, was one row in
// forty of it. It was never a decision anybody made: slog.LevelVar's zero value
// is Info, so this was the default of the type.
//
// A DEFAULT IS NOT A RESTRICTION. The stored setting still wins, every level is
// still offered, and an operator chasing something drops to info or debug from
// the console with no restart. The cost is that the informational lines are not
// there to read AFTERWARDS -- so anybody about to reproduce an intermittent
// fault should turn it down first.
const DefaultLogLevel = slog.LevelWarn

// LogLevel is the level in force right now.
func (a *App) LogLevel() slog.Level {
	if a.level == nil {
		return DefaultLogLevel
	}
	return a.level.Level()
}

// SetLogLevel changes the live level and remembers it.
//
// DEBUG IS NOT FREE: on a busy station it is a great many records, and the
// ring wraps that much faster, so the window the operator can look back over
// shrinks exactly when they turn on the thing meant to help them look. The
// console says so where an operator will read it; this is the note for whoever
// changes this function.
//
// The live change happens FIRST and is reported even when persisting fails. An
// operator asked for debug; refusing to give it to them because it could not be
// written down is the least useful reading of that failure.
func (a *App) SetLogLevel(ctx context.Context, l slog.Level) error {
	if a.level == nil {
		return fmt.Errorf("app: no log level to change; this process logs at a fixed level")
	}
	a.level.Set(l)
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return fmt.Errorf("app: log level set to %s but not saved: no library to save it in", l)
	}
	if err := a.opts.Library.Store.SetSetting(ctx, LogLevelSetting, strings.ToLower(l.String())); err != nil {
		return fmt.Errorf("app: log level set to %s but not saved: %w", l, err)
	}
	a.log.Info("log level changed", "level", strings.ToLower(l.String()))
	return nil
}

// SetLogLevelNamed takes the operator's word for it.
func (a *App) SetLogLevelNamed(ctx context.Context, name string) error {
	l, err := ParseLogLevel(name)
	if err != nil {
		return err
	}
	return a.SetLogLevel(ctx, l)
}

// ParseLogLevel reads one of the four names, in any case.
//
// Delegates to obs.ParseLevel so the console, the API and this all refuse the
// same words with the same sentence -- three spellings of "not a level" is how
// an operator ends up believing the error rather than the form.
func ParseLogLevel(name string) (slog.Level, error) { return obs.ParseLevel(name) }

// RestoreLogLevel puts back what the operator last chose.
//
// STARTUP ORDERING IS THE TRAP HERE. The logger is built in main before the
// store is open, so the stored level cannot be read when the LevelVar is
// created -- moving the read earlier finds a nil store. It is restored here
// instead, after the store opens, and the restore is logged at info so the
// level actually in force is always visible in the log itself.
//
// A level that will not parse is not an error worth refusing to start over: the
// station comes up at the compiled default and says what it ignored.
func (a *App) RestoreLogLevel(ctx context.Context) {
	if a.level == nil || a.opts.Library == nil || a.opts.Library.Store == nil {
		return
	}
	raw, ok, err := a.opts.Library.Store.Setting(ctx, LogLevelSetting)
	if err != nil || !ok || raw == "" {
		return
	}
	l, err := ParseLogLevel(raw)
	if err != nil {
		a.log.Warn("stored log level ignored", "stored", raw, "using", a.LogLevel())
		return
	}
	a.level.Set(l)
	a.log.Info("log level restored", "level", strings.ToLower(l.String()))
}

// PersistQueue is how many warnings may wait for the disk.
//
// 256. Deep enough that a burst of drops from one bad minute all land, shallow
// enough that a wedged disk costs a bounded number of records rather than
// growing without limit -- and a full queue drops and counts, exactly as a slow
// live subscriber does. The audio path logs; it must never wait on SQLite.
const PersistQueue = 256

// logSink adapts the store to the shape package obs asks for.
//
// An adapter rather than a shared type, so obs never imports the store -- it is
// the logging package and everything imports IT -- and the store never learns
// about slog.
type logSink struct{ store *store.Store }

func (l logSink) AppendLogs(ctx context.Context, recs []obs.Record) error {
	out := make([]store.LogRecord, 0, len(recs))
	for _, r := range recs {
		var attrs string
		if len(r.Attrs) > 0 {
			// Already REDACTED by the sink, so this cannot carry a secret --
			// which is why the redaction lives there and not here.
			b, err := json.Marshal(r.Attrs)
			if err == nil {
				attrs = string(b)
			}
		}
		out = append(out, store.LogRecord{
			At: r.Time, Level: r.Level.String(), Message: r.Message, Attrs: attrs,
		})
	}
	return l.store.AppendLogs(ctx, out)
}

// persistLogs starts the durable half, if there is a store and a sink.
//
// Both are nil on the spike path, which has no database to write to and logs to
// stderr like any other command.
func (a *App) persistLogs(ctx context.Context) {
	if a.opts.LogSink == nil || a.opts.Library == nil || a.opts.Library.Store == nil {
		return
	}
	a.opts.LogSink.Persist(ctx, logSink{a.opts.Library.Store}, PersistQueue)
}

// The Logs interface the console's API talks to.
//
// The RING is the live view and the TABLE is what survives a restart, and an
// operator does not care which is which -- so RecentLogs merges them and the
// distinction stays an implementation detail.

// RecentLogs is the ring merged with the persisted warnings, newest first.
func (a *App) RecentLogs(ctx context.Context, minLevel string, limit int) ([]obs.Record, error) {
	level, err := obs.ParseLevel(minLevel)
	if err != nil {
		return nil, err
	}
	var out []obs.Record
	if a.opts.LogSink != nil {
		out = a.opts.LogSink.Recent(level, limit)
	}
	// THE DURABLE HALF, and only when the ring did not already fill the ask.
	// A healthy station's ring covers hours; reading the table as well every
	// time would be a query per poll for rows already on screen.
	if len(out) < limit && a.opts.Library != nil && a.opts.Library.Store != nil {
		stored, err := a.opts.Library.Store.RecentLogs(ctx, minLevel, limit)
		if err != nil {
			return nil, err
		}
		out = mergeLogs(out, stored, limit)
	}
	return out, nil
}

// mergeLogs folds the persisted rows into the ring, newest first, without
// repeating a record that is in both -- which every warning is, for as long as
// the ring still holds it.
func mergeLogs(ring []obs.Record, stored []store.LogRecord, limit int) []obs.Record {
	// KEYED ON WHOLE SECONDS, because the two halves keep time differently and
	// the key has to survive the crossing. The ring holds slog's timestamp with
	// nanoseconds; AppendLogs writes At.Unix() and RecentLogs reads it back
	// with the nanoseconds gone. Keying on the formatted time compared two
	// strings that could never be equal, so the dedupe never fired once and
	// every persisted warning still in the ring was shown twice -- which reads
	// as the station having done the thing twice.
	key := func(at time.Time, msg string) string {
		return strconv.FormatInt(at.Unix(), 10) + "\x00" + msg
	}
	seen := make(map[string]bool, len(ring))
	for _, r := range ring {
		seen[key(r.Time, r.Message)] = true
	}
	for _, s := range stored {
		if len(ring) >= limit {
			break
		}
		// THE RING'S COPY WINS, and it is the one kept: it carries the full
		// timestamp and the attributes the sink captured, where the row has
		// been through a truncation and a JSON round trip.
		if seen[key(s.At, s.Message)] {
			continue
		}
		level, err := obs.ParseLevel(s.Level)
		if err != nil {
			continue
		}
		rec := obs.Record{Time: s.At, Level: level, Message: s.Message}
		if s.Attrs != "" {
			// A row this build cannot parse still shows its message: half a
			// record is more use than none when the console is what somebody
			// is using to find out what broke.
			_ = json.Unmarshal([]byte(s.Attrs), &rec.Attrs)
		}
		ring = append(ring, rec)
	}
	// SORTED AFTER THE MERGE, not before: the ring and the table are each
	// ordered, and interleaving two ordered lists by hand is a bug waiting for
	// the first record that arrives out of order.
	//
	// No trim here -- the loop above stops at the limit, so it is the cap. A
	// second one would be a line no input can reach.
	sort.SliceStable(ring, func(i, j int) bool { return ring[i].Time.After(ring[j].Time) })
	return ring
}

// SubscribeLogs is the live feed the SSE endpoint reads.
func (a *App) SubscribeLogs(buffer int) (<-chan obs.Record, func(), error) {
	if a.opts.LogSink == nil {
		return nil, nil, fmt.Errorf("app: this process keeps no log ring to stream")
	}
	return a.opts.LogSink.Subscribe(buffer)
}

// ClearLogs empties BOTH halves. Clearing one and leaving the other is a
// console that says the log is empty and a database that disagrees.
func (a *App) ClearLogs(ctx context.Context) error {
	if a.opts.LogSink != nil {
		a.opts.LogSink.Clear()
	}
	if a.opts.Library == nil || a.opts.Library.Store == nil {
		return nil
	}
	return a.opts.Library.Store.ClearLogs(ctx)
}

// DroppedLogs is how many records a slow reader or a slow disk has cost.
func (a *App) DroppedLogs() int64 {
	if a.opts.LogSink == nil {
		return 0
	}
	return a.opts.LogSink.Dropped()
}
