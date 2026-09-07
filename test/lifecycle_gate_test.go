// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

//go:build gates

package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrewloable/jockora/internal/app"
	"github.com/andrewloable/jockora/internal/auth"
	"github.com/andrewloable/jockora/internal/config"
	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/station"
	"github.com/andrewloable/jockora/internal/store"
)

// listener is one HLS client: its own session cookie, its own polling loop.
type listener struct {
	station int64
	client  *http.Client
	base    string
}

const (
	gateUser     = "gate-listener"
	gatePassword = "correct horse battery"
)

// newListener signs in, because since 13a a stream is served to accounts and
// not to strangers.
func newListener(t *testing.T, base string, id int64) *listener {
	t.Helper()
	l := &listener{station: id, base: base,
		client: &http.Client{Jar: &cookieJar{}, Timeout: 5 * time.Second}}

	body := strings.NewReader(`{"name":"` + gateUser + `","password":"` + gatePassword + `"}`)
	resp, err := l.client.Post(base+"/login", "application/json", body)
	if err != nil {
		t.Fatalf("signing in: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("signing in = %d", resp.StatusCode)
	}
	return l
}

// poll fetches the playlist once, which is what counts as a heartbeat.
func (l *listener) poll(t *testing.T) (string, int) {
	t.Helper()
	resp, err := l.client.Get(fmt.Sprintf("%s/hls/%d/stream.m3u8", l.base, l.station))
	if err != nil {
		return "", 0
	}
	defer resp.Body.Close()
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(resp.Body)
	return body.String(), resp.StatusCode
}

// keepAlive polls until the returned stop function is called.
func (l *listener) keepAlive(t *testing.T) func() {
	t.Helper()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				l.poll(t)
			}
		}
	}()
	return func() { close(done); <-stopped }
}

// cookieJar is the smallest jar that keeps one session per client, which is
// what makes two listeners two listeners.
type cookieJar struct{ cookies []*http.Cookie }

func (j *cookieJar) SetCookies(_ *url.URL, cs []*http.Cookie) { j.cookies = cs }
func (j *cookieJar) Cookies(_ *url.URL) []*http.Cookie        { return j.cookies }

// lifecycleApp builds a two-station library on real audio and runs the server.
func lifecycleApp(t *testing.T, tracksPerStation int) (base, segRoot string, s *store.Store, stop func()) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skipf("ffmpeg is not installed: %v", err)
	}

	root := t.TempDir()
	music := filepath.Join(root, "music")
	if err := os.MkdirAll(music, 0o755); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(context.Background(), filepath.Join(root, "jockora.db"))
	if err != nil {
		t.Fatal(err)
	}

	// Two stations, built directly: this gate is about the lifecycle, and the
	// dial's proposal has its own gate in row 10.
	ctx := context.Background()
	var pools [2][]int64
	id := int64(0)
	for st := 0; st < 2; st++ {
		for i := 0; i < tracksPerStation; i++ {
			id++
			name := fmt.Sprintf("s%d-t%d.mp3", st+1, i)
			// A different tone per station, so "the two streams differ" is a
			// statement about the pipelines and not about the fixture.
			path := makeTone(t, music, name, fmt.Sprintf("Artist %d", st+1),
				fmt.Sprintf("Station%d Track%d", st+1, i), 220+220*(st+1))
			if _, err := s.DB().Exec(
				`INSERT INTO tracks (id, path, artist, title, playable) VALUES (?, ?, ?, ?, 1)`,
				id, path, fmt.Sprintf("Artist %d", st+1),
				fmt.Sprintf("Station%d Track%d", st+1, i)); err != nil {
				t.Fatal(err)
			}
			pools[st] = append(pools[st], id)
		}
	}
	for st := 0; st < 2; st++ {
		sid, err := s.CreateStation(ctx, store.Station{
			Name: fmt.Sprintf("STATION %d", st+1), Genre: "rock", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceStationTracks(ctx, sid, pools[st]); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		ListenAddr:     "127.0.0.1:0",
		SegmentDir:     filepath.Join(root, "segments"),
		SampleRate:     mix.SampleRate,
		Channels:       mix.Channels,
		SegmentSeconds: 2,
		ListSize:       10,
	}
	all := append(append([]int64{}, pools[0]...), pools[1]...)
	// A listener account. Since 13a the stream follows the account: an
	// unidentified client cannot be counted, and a station nobody is counted
	// on would stop underneath them.
	hash, err := auth.HashPassword(gatePassword, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, gateUser, hash, auth.RoleListener); err != nil {
		t.Fatal(err)
	}

	logs := &syncBuf{}
	a, err := app.New(cfg, app.Options{
		Library: &app.Library{Store: s, Selector: station.NewSelector(all, 1)},
		Log:     slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server log:\n%s", logs.String())
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()

	// Wait for the listener to be up.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && a.Addr() == "" {
		time.Sleep(10 * time.Millisecond)
	}
	return "http://" + a.Addr(), cfg.SegmentDir, s, func() {
		cancel()
		<-done
		s.Close()
	}
}

func segmentsIn(t *testing.T, root string, station int) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(root, fmt.Sprintf("%d", station), "seg*.ts"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(m)
	return m
}

// ffmpegFor counts the encoder processes writing into one station's directory.
// A stopped station whose ffmpeg is still running is a core burning for nobody.
func ffmpegFor(t *testing.T, root string, station int) int {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "command").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, fmt.Sprintf("%d", station))
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, want) && strings.Contains(line, "ffmpeg") {
			n++
		}
	}
	return n
}

func waitForSegments(t *testing.T, root string, station int, want int, within time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if segs := segmentsIn(t, root, station); len(segs) >= want {
			return segs
		}
		time.Sleep(100 * time.Millisecond)
	}
	return segmentsIn(t, root, station)
}

// TestLifecycleGateTwoStations is the architectural claim of the whole row: two
// listeners on two stations get two different, continuous streams. It is the
// one thing a singleton pipeline could not do.
func TestLifecycleGateTwoStations(t *testing.T) {
	base, root, _, stop := lifecycleApp(t, 4)
	defer stop()

	one := newListener(t, base, 1)
	two := newListener(t, base, 2)
	stop1 := one.keepAlive(t)
	stop2 := two.keepAlive(t)
	defer stop1()
	defer stop2()

	const window = 30 * time.Second
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if len(segmentsIn(t, root, 1)) >= 4 && len(segmentsIn(t, root, 2)) >= 4 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	for _, st := range []int{1, 2} {
		segs := segmentsIn(t, root, st)
		if len(segs) < 4 {
			t.Fatalf("station %d produced %d segments in %v, want at least 4", st, len(segs), window)
		}
		playlist, code := newListener(t, base, int64(st)).poll(t)
		if code != http.StatusOK {
			t.Fatalf("station %d playlist = %d", st, code)
		}
		// A discontinuity is the encoder having restarted, which is the seam a
		// client hears as a glitch.
		if strings.Contains(playlist, "EXT-X-DISCONTINUITY") {
			t.Errorf("station %d playlist carries a discontinuity:\n%s", st, playlist)
		}

		// The concatenation must decode as ONE continuous audio stream.
		joined := filepath.Join(t.TempDir(), fmt.Sprintf("station%d.ts", st))
		var all []byte
		for _, sg := range segs {
			b, err := os.ReadFile(sg)
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, b...)
		}
		if err := os.WriteFile(joined, all, 0o600); err != nil {
			t.Fatal(err)
		}
		// nb_streams rather than counting codec_type lines: ffprobe prints a
		// TS stream once per program AND once for itself, so the line count is
		// two for a single-stream file.
		probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
			"format=nb_streams,duration", "-of", "csv=p=0", joined).CombinedOutput()
		if err != nil {
			t.Fatalf("station %d concatenation did not decode: %v: %s", st, err, probe)
		}
		fields := strings.Split(strings.TrimSpace(string(probe)), ",")
		if len(fields) != 2 || fields[0] != "1" {
			t.Errorf("station %d reports %q, want exactly 1 stream", st, probe)
		}
		// CONTINUOUS: the concatenation must be as long as the segments it is
		// made of. A gap, a lost segment or a restart shows up as short.
		secs, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			t.Fatalf("station %d duration %q: %v", st, fields[1], err)
		}
		want := float64(len(segs)) * 2.0
		if secs < want*0.8 {
			t.Errorf("station %d decoded %.1fs from %d segments, want about %.1fs",
				st, secs, len(segs), want)
		}
	}

	// TWO DIFFERENT STREAMS. Identical bytes would mean one pipeline serving
	// both, which is the failure this row exists to prevent.
	a, err := os.ReadFile(segmentsIn(t, root, 1)[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(segmentsIn(t, root, 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Error("both stations produced identical audio; they are sharing a pipeline")
	}
}

// TestLifecycleGateIdleStops: a station with nobody on it must let go of its
// encoder, or a box that can run four stations runs out after four visitors.
func TestLifecycleGateIdleStops(t *testing.T) {
	base, root, _, stop := lifecycleApp(t, 4)
	defer stop()

	two := newListener(t, base, 2)
	stopPolling := two.keepAlive(t)
	if segs := waitForSegments(t, root, 2, 2, 30*time.Second); len(segs) < 2 {
		stopPolling()
		t.Fatalf("station 2 never started: %d segments", len(segs))
	}
	if n := ffmpegFor(t, root, 2); n == 0 {
		stopPolling()
		t.Fatal("station 2 is producing segments with no encoder of its own")
	}

	stopPolling()
	before := len(segmentsIn(t, root, 2))

	// Grace plus a reconcile tick plus room for the encoder to be reaped.
	deadline := time.Now().Add(app.ListenerGrace + 15*time.Second)
	for time.Now().Before(deadline) {
		if ffmpegFor(t, root, 2) == 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if n := ffmpegFor(t, root, 2); n != 0 {
		t.Errorf("%d encoders still running for a station nobody is listening to", n)
	}

	// And it has stopped growing.
	settled := len(segmentsIn(t, root, 2))
	time.Sleep(3 * time.Second)
	if now := len(segmentsIn(t, root, 2)); now != settled {
		t.Errorf("the segment count went %d -> %d -> %d after everyone left", before, settled, now)
	}
}

// TestLifecycleGateRejoinLatency: a station starts from cold when its first
// listener arrives, and they should not be staring at a spinner.
func TestLifecycleGateRejoinLatency(t *testing.T) {
	base, root, _, stop := lifecycleApp(t, 4)
	defer stop()

	var took []time.Duration
	for join := 0; join < 3; join++ {
		// By NAME, not by count: the encoder prunes as it writes, so the
		// number of files on disk is capped and a growing stream never makes
		// it grow.
		before := map[string]bool{}
		for _, sg := range segmentsIn(t, root, 1) {
			before[sg] = true
		}
		fresh := func() bool {
			for _, sg := range segmentsIn(t, root, 1) {
				if !before[sg] {
					return true
				}
			}
			return false
		}

		l := newListener(t, base, 1)
		stopPolling := l.keepAlive(t)

		start := time.Now()
		deadline := start.Add(20 * time.Second)
		for time.Now().Before(deadline) && !fresh() {
			time.Sleep(50 * time.Millisecond)
		}
		took = append(took, time.Since(start))
		stopPolling()

		// Let it go idle again so the next join is genuinely cold.
		if join < 2 {
			idle := time.Now().Add(app.ListenerGrace + 15*time.Second)
			for time.Now().Before(idle) && ffmpegFor(t, root, 1) != 0 {
				time.Sleep(250 * time.Millisecond)
			}
		}
	}

	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	median := took[len(took)/2]
	if median > 5*time.Second {
		t.Errorf("median time to first audio = %v over %v, want 5s or less", median, took)
	}
}

// TestLifecycleGateReloadNoRestart: a page reload is a new session on the same
// station. Tearing the station down and building it again would drop the audio
// the listener was already hearing.
func TestLifecycleGateReloadNoRestart(t *testing.T) {
	base, root, _, stop := lifecycleApp(t, 4)
	defer stop()

	l := newListener(t, base, 1)
	stopPolling := l.keepAlive(t)
	if segs := waitForSegments(t, root, 1, 2, 30*time.Second); len(segs) < 2 {
		stopPolling()
		t.Fatalf("station 1 never started: %d segments", len(segs))
	}
	before := ffmpegPID(t, root, 1)
	if before == "" {
		stopPolling()
		t.Fatal("no encoder found for a running station")
	}

	// Gone for ten seconds, well inside the thirty second grace.
	stopPolling()
	time.Sleep(10 * time.Second)

	fresh := newListener(t, base, 1) // a new session: the reloaded page
	resume := fresh.keepAlive(t)
	defer resume()
	time.Sleep(3 * time.Second)

	if after := ffmpegPID(t, root, 1); after != before {
		t.Errorf("the encoder was replaced across a reload: pid %s -> %s", before, after)
	}
}

func ffmpegPID(t *testing.T, root string, station int) string {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, fmt.Sprintf("%d", station))
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, want) && strings.Contains(line, "ffmpeg") {
			return strings.Fields(strings.TrimSpace(line))[0]
		}
	}
	return ""
}

// TestLifecycleGateResumeNoRepeat: a station that stopped mid-shuffle comes
// back where it was. Restarting the order every time the last listener left
// would open every session with the same track.
func TestLifecycleGateResumeNoRepeat(t *testing.T) {
	base, root, s, stop := lifecycleApp(t, 8)
	defer stop()

	l := newListener(t, base, 1)
	stopPolling := l.keepAlive(t)

	seen := []string{}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && len(seen) < 3 {
		if title := nowTitle(t, base); title != "" {
			if len(seen) == 0 || seen[len(seen)-1] != title {
				seen = append(seen, title)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	stopPolling()
	if len(seen) < 2 {
		t.Fatalf("only heard %v before stopping; the station never got going", seen)
	}
	last := seen[len(seen)-1]

	// Let it go idle, which is what persists the cursor.
	idle := time.Now().Add(app.ListenerGrace + 15*time.Second)
	for time.Now().Before(idle) && ffmpegFor(t, root, 1) != 0 {
		time.Sleep(250 * time.Millisecond)
	}
	if _, ok, err := s.GetSelectorState(context.Background(), 1); err != nil || !ok {
		t.Fatalf("the station stopped without saving its position: ok = %v, err = %v", ok, err)
	}

	back := newListener(t, base, 1)
	resume := back.keepAlive(t)
	defer resume()

	var first string
	deadline = time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if title := nowTitle(t, base); title != "" && title != last {
			first = title
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if first == "" {
		t.Fatalf("the station never resumed with a different track; last heard %q", last)
	}
	if first == last {
		t.Errorf("the station resumed on %q, the track it stopped on", first)
	}
}

func nowTitle(t *testing.T, base string) string {
	t.Helper()
	resp, err := http.Get(base + "/now.json")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var body struct {
		Now *struct {
			Title string `json:"title"`
		} `json:"now"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Now == nil {
		return ""
	}
	return body.Now.Title
}

// makeTone is makeAudio with a chosen frequency, so two stations can be told
// apart by ear as well as by byte.
func makeTone(t *testing.T, dir, name, artist, title string, hz int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:sample_rate=44100:duration=6", hz),
		"-ac", "2", "-metadata", "artist="+artist, "-metadata", "title="+title,
		"-metadata", "album=Gate", path).CombinedOutput()
	if err != nil {
		t.Fatalf("making %s: %v: %s", name, err, out)
	}
	return path
}

// syncBuf is a bytes.Buffer a test can read while the server writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
