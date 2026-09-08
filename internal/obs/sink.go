// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package obs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The operator's window onto what the server is doing.
//
// Logs went to stderr and nowhere else, so finding out why a station went quiet
// meant ssh and then docker logs. Three live defects on the deployment were
// found exactly that way and could not have been found any other way -- which
// is both the argument for this and the proof that the information already
// exists and is merely out of reach.
//
// THIS WRAPS THE REAL HANDLER, it does not replace it. Stderr and Seq stay the
// record of truth and keep receiving every record unchanged; the ring is a
// second, lossy, redacted copy for a browser.

// RingCapacity is how many records the live view keeps.
//
// 2000. A record is a message, a few attributes and a timestamp -- call it 300
// bytes with its map -- so the ring costs well under a megabyte and never
// grows. It is a constant rather than a setting because there is no answer an
// operator could give that is better than this one, and a number that can be
// set to a million is a memory leak with a form field.
const RingCapacity = 2000

// MaxSubscribers caps live viewers.
//
// Eight is plenty for one operator with a few tabs. Refusing beyond it is
// better than growing goroutines without limit for a page somebody left open.
const MaxSubscribers = 8

// ErrTooManySubscribers is returned when the cap is reached.
var ErrTooManySubscribers = errors.New("obs: too many live log subscribers")

// Record is one log line, redacted, in the shape the console renders.
type Record struct {
	Time    time.Time         `json:"time"`
	Level   slog.Level        `json:"level"`
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// ring is the state every derived handler shares.
//
// SHARED, because slog hands a caller a NEW handler from WithAttrs and
// serve.go:179 does exactly that -- log.With("station", stationID) is the
// per-station logger, and if its records landed in a different ring the console
// would show everything except the lines that say which station broke.
type ring struct {
	mu      sync.Mutex
	records []Record // fixed size, allocated once, overwritten in place
	n       int      // total ever written; records[n%len] is the next slot
	subs    map[int]chan Record
	nextID  int
	dropped atomic.Int64

	// durable is the queue feeding the writer goroutine, nil when nothing is
	// persisting -- which is every path that is not the server.
	durable chan Record
}

// Sink is an slog.Handler that keeps the recent past and fans it out live.
type Sink struct {
	r    *ring
	next slog.Handler

	// Attributes added through WithAttrs, kept because slog does not replay
	// them into Handle's record -- the wrapped handler gets them, and without
	// this the ring would not.
	//
	// THE KEYS ARE ALREADY PREFIXED with whatever group was open when they were
	// added. slog says an attribute added BEFORE a group is not inside it, and
	// prefixing these at Handle time instead put them all in the group that
	// happened to be open last: log.With("station", 1).WithGroup("llm") wrote
	// llm.station to the ring while the wrapped handler wrote station to
	// stderr. Two records that disagree is the exact failure a console for
	// reading logs exists to avoid.
	attrs []slog.Attr
	// Key prefix from WithGroup. Nothing in this repository uses groups today;
	// this is here because a partial slog.Handler is a trap for whoever does.
	group string
}

// NewSink wraps next, keeping the last capacity records.
func NewSink(next slog.Handler, capacity int) *Sink {
	if capacity <= 0 {
		capacity = RingCapacity
	}
	return &Sink{
		r: &ring{
			records: make([]Record, capacity),
			subs:    map[int]chan Record{},
		},
		next: next,
	}
}

// Enabled defers to the wrapped handler, so a level change made anywhere
// reaches both destinations at once.
func (s *Sink) Enabled(ctx context.Context, l slog.Level) bool {
	return s.next.Enabled(ctx, l)
}

// WithAttrs returns a handler carrying attrs, writing into the SAME ring.
func (s *Sink) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := *s
	out.next = s.next.WithAttrs(attrs)
	// PREFIXED NOW, with the group open at this moment, because that is the
	// only time it is known. See the field comment.
	kept := append([]slog.Attr{}, s.attrs...)
	for _, a := range attrs {
		kept = append(kept, slog.Attr{Key: s.group + a.Key, Value: a.Value})
	}
	out.attrs = kept
	return &out
}

// WithGroup nests subsequent attribute names under name.
func (s *Sink) WithGroup(name string) slog.Handler {
	if name == "" {
		return s
	}
	out := *s
	out.next = s.next.WithGroup(name)
	out.group = s.group + name + "."
	return &out
}

// Handle records, fans out, and passes through. IT MUST NEVER BLOCK.
//
// The mixer, the encoder and the pipeline all log. A browser tab that stopped
// reading its SSE connection must not be able to stall the audio path, so a
// subscriber whose buffer is full loses the record and the loss is counted.
// Dropping is correct here and stalling is not; the count is shown in the
// console so a gap is visible rather than mysterious.
func (s *Sink) Handle(ctx context.Context, r slog.Record) error {
	rec := s.record(r)

	s.r.mu.Lock()
	s.r.records[s.r.n%len(s.r.records)] = rec
	s.r.n++
	for _, ch := range s.r.subs {
		select {
		case ch <- rec:
		default:
			s.r.dropped.Add(1)
		}
	}
	// The durable copy takes the same non-blocking send for the same reason: a
	// slow disk must cost records rather than the audio path.
	if s.r.durable != nil && rec.Level >= PersistLevel {
		select {
		case s.r.durable <- rec:
		default:
			s.r.dropped.Add(1)
		}
	}
	s.r.mu.Unlock()

	// OUTSIDE THE LOCK. The wrapped handler writes to stderr, and holding a
	// mutex across a write to a pipe nobody is draining is a deadlock waiting
	// for a full pipe buffer.
	return s.next.Handle(ctx, r)
}

// record turns an slog.Record into the redacted shape the console sees.
func (s *Sink) record(r slog.Record) Record {
	rec := Record{Time: r.Time, Level: r.Level, Message: r.Message}
	if n := r.NumAttrs() + len(s.attrs); n > 0 {
		rec.Attrs = make(map[string]string, n)
		// NO PREFIX: these carry the group they were added under already.
		for _, a := range s.attrs {
			rec.put("", a)
		}
		r.Attrs(func(a slog.Attr) bool {
			rec.put(s.group, a)
			return true
		})
	}
	return rec
}

// put stores one attribute, redacted, under the handler's group prefix.
func (rec Record) put(group string, a slog.Attr) {
	rec.Attrs[group+a.Key] = redact(a.Key, a.Value.String())
}

// redact applies the same policy scrub applies to the typed event methods.
//
// IT RUNS OVER EVERY RECORD, which is the point: scrub only ever covered the
// typed methods, so the hundreds of plain slog calls in the rest of the
// repository were unscrubbed. That was tolerable while logs reached stderr on a
// box only the developer could see. A viewer in a browser is a new exposure.
func redact(key, value string) string {
	if IsForbiddenKey(key) {
		return "[redacted]"
	}
	return stripURLCredentials(value)
}

// stripURLCredentials removes userinfo from anything that parses as a URL.
//
// serve.go logs the language model's base URL and an operator is free to paste
// https://user:pass@host into that field on the console's own model page.
func stripURLCredentials(v string) string {
	if !strings.Contains(v, "@") || !strings.Contains(v, "://") {
		return v
	}
	u, err := url.Parse(v)
	if err != nil || u.User == nil {
		return v
	}
	u.User = nil
	return u.String()
}

// Recent returns up to limit records at or above min, newest first.
//
// Newest first because an operator opening the page is looking at what just
// happened, not at what happened first.
func (s *Sink) Recent(min slog.Level, limit int) []Record {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()

	out := make([]Record, 0, limit)
	held := s.r.n
	if held > len(s.r.records) {
		held = len(s.r.records)
	}
	for i := 1; i <= held && len(out) < limit; i++ {
		rec := s.r.records[(s.r.n-i)%len(s.r.records)]
		if rec.Level >= min {
			out = append(out, rec)
		}
	}
	return out
}

// Subscribe returns a channel of records and a function that stops it.
//
// The buffer is the caller's, and it is what stands between a slow reader and
// the audio path: a full one loses records rather than blocking Handle.
func (s *Sink) Subscribe(buffer int) (<-chan Record, func(), error) {
	if buffer < 1 {
		buffer = 1
	}
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	if len(s.r.subs) >= MaxSubscribers {
		return nil, nil, ErrTooManySubscribers
	}
	id := s.r.nextID
	s.r.nextID++
	ch := make(chan Record, buffer)
	s.r.subs[id] = ch

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.r.mu.Lock()
			delete(s.r.subs, id)
			s.r.mu.Unlock()
			close(ch)
		})
	}, nil
}

// Clear empties the ring. Subscribers keep their connections, because clearing
// is for reading what happens NEXT and dropping the live view is the opposite
// of that.
func (s *Sink) Clear() {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	for i := range s.r.records {
		s.r.records[i] = Record{}
	}
	s.r.n = 0
}

// Dropped is how many records a slow subscriber has lost, ever.
func (s *Sink) Dropped() int64 { return s.r.dropped.Load() }

// The durable half.
//
// The ring above is the live view and empties on a restart -- which is exactly
// what a crash loop does, taking with it the evidence somebody wanted. WARN and
// above therefore also go to a store, on a goroutine the logging call never
// waits for.

// PersistLevel is the floor for the durable copy.
//
// INFO is chatter and a station produces a great deal of it; persisting all of
// it would turn a music library into a log database. The live ring still keeps
// every level -- this is about what survives a restart, not about what the
// console shows.
const PersistLevel = slog.LevelWarn

// LogStore is the durable destination. An interface so package obs does not
// import the store, and so a test can make the write block or fail.
type LogStore interface {
	AppendLogs(ctx context.Context, recs []Record) error
}

// Persist starts the writer goroutine. Calling it twice is a no-op: two writers
// draining one queue would interleave batches for no benefit.
//
// The buffer is what stands between a slow disk and the audio path. A full
// queue DROPS and counts, exactly as a slow live subscriber does.
func (s *Sink) Persist(ctx context.Context, store LogStore, buffer int) {
	if store == nil {
		return
	}
	if buffer < 1 {
		buffer = 1
	}
	s.r.mu.Lock()
	if s.r.durable != nil {
		s.r.mu.Unlock()
		return
	}
	queue := make(chan Record, buffer)
	s.r.durable = queue
	// UNDER THE SAME LOCK THE FAN-OUT USES, which is what makes this exact
	// rather than approximate: a record already in the ring is in this
	// snapshot, a record logged after this unlock goes to the queue, and
	// nothing can be both.
	backlog := s.r.backlogLocked(PersistLevel)
	s.r.mu.Unlock()

	go s.drain(ctx, store, queue, backlog)
}

// backlogLocked returns every held record at or above min, OLDEST FIRST.
//
// Oldest first because this is the durable log, which is read forwards -- the
// opposite of Recent, which answers "what just happened" for a browser.
//
// The caller must hold r.mu.
func (r *ring) backlogLocked(min slog.Level) []Record {
	held := r.n
	if held > len(r.records) {
		held = len(r.records)
	}
	var out []Record
	for i := held; i >= 1; i-- {
		if rec := r.records[(r.n-i)%len(r.records)]; rec.Level >= min {
			out = append(out, rec)
		}
	}
	return out
}

// persisting reports whether the writer is still running. Test-facing, and the
// reason it exists is that a goroutine outliving its context is invisible.
func (s *Sink) persisting() bool {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	return s.r.durable != nil
}

// drain batches whatever is waiting and writes it.
//
// BATCHED because SQLite must not be asked for a write per line and a station
// under load produces them in bursts: it takes one record, then sweeps up
// everything already queued behind it before writing.
func (s *Sink) drain(ctx context.Context, store LogStore, queue chan Record, backlog []Record) {
	defer func() {
		s.r.mu.Lock()
		s.r.durable = nil
		s.r.mu.Unlock()
	}()
	// Whatever was logged before the store existed goes first, so the table
	// reads in the order things happened. Skipped when empty rather than
	// written as a batch of nothing.
	if len(backlog) > 0 {
		s.write(ctx, store, backlog)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case rec := <-queue:
			batch := []Record{rec}
			for more := true; more; {
				select {
				case r := <-queue:
					batch = append(batch, r)
				default:
					more = false
				}
			}
			s.write(ctx, store, batch)
		}
	}
}

// write persists one batch and reports a failure to stderr only.
func (s *Sink) write(ctx context.Context, store LogStore, batch []Record) {
	if err := store.AppendLogs(ctx, batch); err != nil && ctx.Err() == nil {
		// THE WRAPPED HANDLER ONLY, never back through the Sink. A failed write
		// reported through here would become a record, which would be queued,
		// which would fail -- and the console would show the log complaining
		// about the log.
		s.next.Handle(ctx, failureRecord(err)) //nolint:errcheck // nothing left to tell
	}
}

// failureRecord is how a write failure reaches stderr without touching the ring.
func failureRecord(err error) slog.Record {
	r := slog.NewRecord(time.Now(), slog.LevelError, "log records could not be persisted", 0)
	r.AddAttrs(slog.String("err", err.Error()))
	return r
}

// ParseLevel reads one of the four level names, in any case.
//
// HERE rather than in package app, because the server needs it too and the
// server must not import the app. Refused BY NAME, listing what is valid: an
// operator who typed "verbose" should read a sentence rather than have their
// instruction silently ignored and then wonder why the log looks the same.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf(
			"obs: %q is not a log level; use debug, info, warn or error", name)
	}
}
