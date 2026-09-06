// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

//go:build soak

// Package soak runs the pipeline for hours and watches what only breaks slowly.
//
// Build-tagged so it can never run in ordinary CI: `go test ./...` does not
// compile this file, let alone execute it.
package soak

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/app"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/dj"
	"github.com/andrewloable/jockora/internal/store"
)

// sample is one observation of everything that degrades slowly.
type sample struct {
	At           time.Time
	Elapsed      time.Duration
	Goroutines   int
	OpenFDs      int
	RSSKB        int
	WALBytes     int64
	SaidLines    int
	CollisionP95 time.Duration
	DriftMs      float64
	Underruns    uint64
	Restarts     int
}

// TestSoak runs the full pipeline and asserts on TRENDS, not on any one reading.
//
// Everything here is invisible at thirty minutes and fatal over a month: a
// descriptor leaked per track, a goroutine leaked per break, the said-lines
// table growing until its collision query dominates break generation, WAL growth
// with no checkpoint, resampler rounding accumulating across thousands of
// transitions.
//
//	JOCKORA_SOAK_LIBRARY=/media/data1/music \
//	JOCKORA_SOAK_DURATION=24h \
//	go test -tags soak ./test/soak/ -run TestSoak -v -timeout 30h
func TestSoak(t *testing.T) {
	library := os.Getenv("JOCKORA_SOAK_LIBRARY")
	if library == "" {
		t.Skip("set JOCKORA_SOAK_LIBRARY to a music directory")
	}

	duration := 24 * time.Hour
	if v := os.Getenv("JOCKORA_SOAK_DURATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("JOCKORA_SOAK_DURATION=%q: %v", v, err)
		}
		duration = d
	}
	interval := 5 * time.Minute
	if v := os.Getenv("JOCKORA_SOAK_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("JOCKORA_SOAK_INTERVAL=%q: %v", v, err)
		}
		interval = d
	}

	if runtime.GOOS == "darwin" {
		// Not a portability limitation -- a measurement one. Every number here
		// is timing and resource sensitive, and a laptop that sleeps, throttles
		// and runs a browser produces a trend that means nothing.
		t.Log("WARNING: running on macOS. The real run belongs on the Linux target box.")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "soak.db")

	cfg, err := config.LoadArgs([]string{
		"-db-path", dbPath,
		"-segment-dir", filepath.Join(dir, "segments"),
		"-listen-addr", "127.0.0.1:0",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	tracks, err := audioUnder(library)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) < 2 {
		t.Fatalf("only %d playable files under %s", len(tracks), library)
	}
	t.Logf("soaking for %s over %d tracks, sampling every %s", duration, len(tracks), interval)

	logFile, err := os.Create(filepath.Join(dir, "soak.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	a, err := app.New(cfg, app.Options{
		Tracks: tracks,
		Log:    slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(ctx) }()

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var samples []sample
	start := time.Now()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// One sample before anything has warmed up, so the baseline is honest.
	samples = append(samples, take(t, s, a, start))

collect:
	for {
		select {
		case err := <-runDone:
			if err != nil && ctx.Err() == nil {
				t.Fatalf("the pipeline stopped after %s: %v", time.Since(start), err)
			}
			break collect
		case <-ticker.C:
			cur := take(t, s, a, start)
			samples = append(samples, cur)
			t.Logf("t+%-9s goroutines=%-4d fds=%-4d rss=%-8dKB wal=%-9d said=%-6d p95=%-8s drift=%.1fms underruns=%d restarts=%d",
				cur.Elapsed.Round(time.Second), cur.Goroutines, cur.OpenFDs, cur.RSSKB,
				cur.WALBytes, cur.SaidLines, cur.CollisionP95.Round(time.Microsecond),
				cur.DriftMs, cur.Underruns, cur.Restarts)
		}
	}

	report(t, samples)
}

// report asserts on the shape of the run, not on any single reading. A single
// encoder restart is a fact of life; a restart every hour is a trend.
func report(t *testing.T, samples []sample) {
	t.Helper()
	if len(samples) < 2 {
		t.Fatalf("only %d samples; the run was too short to show a trend", len(samples))
	}
	first, last := samples[0], samples[len(samples)-1]

	t.Log("")
	t.Logf("SOAK RESULT over %s, %d samples", last.Elapsed.Round(time.Minute), len(samples))
	t.Logf("  goroutines     %d -> %d", first.Goroutines, last.Goroutines)
	if first.OpenFDs == 0 {
		t.Logf("  open fds       NOT MEASURED on %s", runtime.GOOS)
	} else {
		t.Logf("  open fds       %d -> %d", first.OpenFDs, last.OpenFDs)
	}
	t.Logf("  rss            %d KB -> %d KB", first.RSSKB, last.RSSKB)
	t.Logf("  wal            %d -> %d bytes", first.WALBytes, last.WALBytes)
	t.Logf("  said_lines     %d -> %d rows", first.SaidLines, last.SaidLines)
	t.Logf("  collision p95  %s -> %s", first.CollisionP95.Round(time.Microsecond), last.CollisionP95.Round(time.Microsecond))
	t.Logf("  drift          %.1f ms", last.DriftMs)
	t.Logf("  underruns      %d", last.Underruns)
	t.Logf("  encoder restarts %d", last.Restarts)

	// A leak shows as a rise that never comes back down, which is why the
	// comparison is first-to-last rather than against a fixed ceiling.
	if last.Goroutines > first.Goroutines+10 {
		t.Errorf("goroutines grew %d -> %d: something is leaked per track or per break",
			first.Goroutines, last.Goroutines)
	}
	switch {
	case first.OpenFDs == 0:
		// Said out loud rather than skipped quietly. A metric that reads zero
		// and passes is the same shape as a gate that cannot fail, and the FD
		// leak is one of the specific things this run exists to catch.
		t.Errorf("OPEN DESCRIPTORS WERE NOT MEASURED (neither /proc/self/fd nor /dev/fd was readable). "+
			"That check did not run, and it is one of the six this soak exists for. Run on Linux. os=%s", runtime.GOOS)
	case last.OpenFDs > first.OpenFDs+10:
		t.Errorf("open descriptors grew %d -> %d", first.OpenFDs, last.OpenFDs)
	}
	if last.CollisionP95 > 50*time.Millisecond {
		t.Errorf("said-lines collision query p95 is %s at %d rows; break generation is now dominated by it",
			last.CollisionP95, last.SaidLines)
	}
	if abs(last.DriftMs) > 500 {
		t.Errorf("cumulative drift %.1f ms: resampler rounding is accumulating across transitions", last.DriftMs)
	}
	if last.Restarts > 0 {
		// Not a failure by itself. The task is explicit: a single restart is
		// not a fault, an unexplained TREND is. Rate is what says which.
		perHour := float64(last.Restarts) / last.Elapsed.Hours()
		t.Logf("  encoder restart rate %.2f/hour", perHour)
		if perHour > 1 {
			t.Errorf("encoder restarted %.2f times an hour; that is a trend, not an incident", perHour)
		}
	}
}

func take(t *testing.T, s *store.Store, a *app.App, start time.Time) sample {
	t.Helper()
	st := a.Status()
	cur := sample{
		At:         time.Now(),
		Elapsed:    time.Since(start),
		Goroutines: runtime.NumGoroutine(),
		OpenFDs:    openFDs(),
		RSSKB:      rssKB(),
		WALBytes:   fileSize(walPath(s)),
		DriftMs:    st.Metrics.DriftMs,
		Underruns:  st.Metrics.Underruns,
		Restarts:   st.Metrics.EncoderRestarts,
	}
	cur.SaidLines = countRows(s.DB(), "said_lines")
	cur.CollisionP95 = collisionP95(t, s)
	return cur
}

// collisionP95 times the query that gets slower as the DJ says more things.
//
// It is a FULL TABLE SCAN by design elsewhere in this program, so its cost
// grows with every break ever aired. Over a month that is the most likely
// source of a break arriving late.
func collisionP95(t *testing.T, s *store.Store) time.Duration {
	t.Helper()
	said := &dj.SaidLines{Store: s, JockID: "soak"}
	const probes = 20
	times := make([]time.Duration, 0, probes)
	for i := 0; i < probes; i++ {
		line := fmt.Sprintf("a probe line number %d about nothing in particular tonight", i)
		began := time.Now()
		if _, _, err := said.CheckCollision(context.Background(), line); err != nil {
			t.Fatalf("collision probe: %v", err)
		}
		times = append(times, time.Since(began))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[int(0.95*float64(len(times)))]
}

func countRows(db *sql.DB, table string) int {
	var n int
	//nolint:gosec // table is a literal at every call site
	_ = db.QueryRow("SELECT count(*) FROM " + table).Scan(&n)
	return n
}

func walPath(s *store.Store) string {
	var path string
	_ = s.DB().QueryRow("PRAGMA database_list").Scan(new(int), new(string), &path)
	if path == "" {
		return ""
	}
	return path + "-wal"
}

func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func openFDs() int {
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		if entries, err := os.ReadDir(dir); err == nil {
			return len(entries)
		}
	}
	return 0
}

func rssKB() int {
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				if kb, err := strconv.Atoi(strings.Fields(line)[1]); err == nil {
					return kb
				}
			}
		}
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	kb, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return kb
}

func audioUnder(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subdirectory must not end the walk
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".mp3", ".flac", ".m4a", ".ogg", ".opus", ".wav":
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
