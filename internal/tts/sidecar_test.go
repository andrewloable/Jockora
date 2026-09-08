// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package tts

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

// The fake sidecar is this same test binary re-executed with JOCKORA_TTS_ADDR
// set, which is how the manager addresses any child. That gives the tests a
// REAL operating-system process to kill -- a httptest.Server would test the
// HTTP client and nothing about process supervision, which is the whole point
// of this file. Real Kokoro is never started here.
func TestMain(m *testing.M) {
	if addr := os.Getenv("JOCKORA_TTS_ADDR"); addr != "" {
		runFakeSidecar(addr)
		return
	}
	os.Exit(m.Run())
}

func runFakeSidecar(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/die", func(w http.ResponseWriter, r *http.Request) { os.Exit(1) })
	// WHAT IT WAS ASKED TO SAY. The fake is a separate operating-system
	// process, so a test cannot read a variable out of it -- and the one thing
	// Jockora-hm2 has to prove is that the text reaching the sidecar has been
	// normalised. It reports the last line back over the same HTTP it speaks.
	var lastText atomic.Value
	mux.HandleFunc("/last", func(w http.ResponseWriter, r *http.Request) {
		text, _ := lastText.Load().(string)
		_, _ = io.WriteString(w, text)
	})
	mux.HandleFunc("/synth", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Text, Voice string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		lastText.Store(req.Text)
		switch req.Text {
		case "DIE":
			os.Exit(1) // die mid-request, before writing a response
		case "SLEEP":
			time.Sleep(5 * time.Second)
		}
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(fakeSpeechWAV(5.0))
	})
	_ = http.ListenAndServe(addr, mux) //nolint:gosec // fake, test-only
	os.Exit(0)
}

// fakeSpeechWAV is 24 kHz mono 16-bit, the shape Kokoro actually returns, and
// it is deliberately NOT silence: it is quiet syllable-shaped bursts about
// 30 dB below full scale. Silence would let a broken resample and a missing
// loudnorm both pass unnoticed, because silence resampled is still silence and
// silence normalised is still silence.
func fakeSpeechWAV(seconds float64) []byte {
	const rate = 24000
	n := int(seconds * rate)
	data := make([]byte, n*2)
	le := binary.LittleEndian
	for i := 0; i < n; i++ {
		t := float64(i) / rate
		// ~3.3 syllables a second, each with a soft attack and decay.
		env := math.Max(0, math.Sin(2*math.Pi*3.3*t))
		v := 0.03 * env * math.Sin(2*math.Pi*220*t)
		le.PutUint16(data[i*2:], uint16(int16(v*32767)))
	}

	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	le.PutUint32(hdr[4:], uint32(36+len(data)))
	copy(hdr[8:], "WAVEfmt ")
	le.PutUint32(hdr[16:], 16)
	le.PutUint16(hdr[20:], 1)
	le.PutUint16(hdr[22:], 1)
	le.PutUint32(hdr[24:], rate)
	le.PutUint32(hdr[28:], rate*2)
	le.PutUint16(hdr[32:], 2)
	le.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	le.PutUint32(hdr[40:], uint32(len(data)))
	return append(hdr, data...)
}

func startFake(t *testing.T) *Sidecar {
	t.Helper()
	s, err := Start(context.Background(), Config{
		Command:        []string{os.Args[0]},
		HealthInterval: 25 * time.Millisecond,
		Backoff:        10 * time.Millisecond,
		StartTimeout:   10 * time.Second,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", d, what)
}

func TestStartsAndReportsHealthy(t *testing.T) {
	s := startFake(t)
	waitFor(t, "healthy", 10*time.Second, s.Healthy)
	if s.RespawnCount() != 0 {
		t.Fatalf("RespawnCount = %d on a clean start, want 0", s.RespawnCount())
	}
}

func TestRespawnsAfterDeath(t *testing.T) {
	s := startFake(t)
	waitFor(t, "healthy", 10*time.Second, s.Healthy)

	resp, err := http.Post("http://"+s.Addr()+"/die", "", nil)
	if err == nil {
		_ = resp.Body.Close()
	}

	waitFor(t, "respawn", 10*time.Second, func() bool { return s.RespawnCount() >= 1 && s.Healthy() })

	// And it must actually work again, not merely report healthy.
	if _, err := s.Synthesize(context.Background(), "after respawn", ""); err != nil {
		t.Fatalf("Synthesize after respawn: %v", err)
	}
}

func TestDeathDropsInFlightRequest(t *testing.T) {
	s := startFake(t)
	waitFor(t, "healthy", 10*time.Second, s.Healthy)

	done := make(chan error, 1)
	go func() {
		_, err := s.Synthesize(context.Background(), "DIE", "")
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrTTSUnavailable) {
			t.Fatalf("Synthesize into a dying sidecar = %v, want ErrTTSUnavailable", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Synthesize blocked when the sidecar died mid-request")
	}

	waitFor(t, "respawn after mid-request death", 10*time.Second, func() bool { return s.Healthy() })
}

func TestNeverBlocksTheCaller(t *testing.T) {
	s := startFake(t)
	waitFor(t, "healthy", 10*time.Second, s.Healthy)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Synthesize(ctx, "SLEEP", "")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Synthesize returned nil past its context deadline")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Synthesize took %s to honour a 200ms deadline", elapsed)
	}
}

func TestDeadSidecarReturnsImmediately(t *testing.T) {
	s := startFake(t)
	waitFor(t, "healthy", 10*time.Second, s.Healthy)
	_ = s.Close()

	start := time.Now()
	_, err := s.Synthesize(context.Background(), "hello", "")
	if !errors.Is(err, ErrTTSUnavailable) {
		t.Fatalf("Synthesize on a closed sidecar = %v, want ErrTTSUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a closed sidecar blocked the caller for %s", elapsed)
	}
}

// TestFiftyConsecutiveSyntheses is the step 6 endurance gate in miniature. A
// respawn here is a FAILURE, not a recovery: the supervisor exists to survive a
// crash, and if it fires during steady-state use it is masking a leak.
func TestFiftyConsecutiveSyntheses(t *testing.T) {
	s := startFake(t)
	waitFor(t, "healthy", 10*time.Second, s.Healthy)

	var latencies []time.Duration
	for i := 0; i < 50; i++ {
		start := time.Now()
		wav, err := s.Synthesize(context.Background(), "line", "")
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		if len(wav) == 0 {
			t.Fatalf("request %d returned no audio", i+1)
		}
		latencies = append(latencies, time.Since(start))
	}

	if n := s.RespawnCount(); n != 0 {
		t.Fatalf("RespawnCount = %d across 50 requests, want 0: a respawn here hides a leak", n)
	}

	first := append([]time.Duration(nil), latencies[:10]...)
	sort.Slice(first, func(i, j int) bool { return first[i] < first[j] })
	median := first[5]
	if median < time.Millisecond {
		median = time.Millisecond // a sub-ms median makes 3x meaninglessly tight
	}
	for i, d := range latencies {
		if d > 3*median {
			t.Fatalf("request %d took %s, over 3x the opening median %s: degradation, not a clean crash", i+1, d, median)
		}
	}
}

// TestVoiceIDStripsTheEngineNamespace guards a bug that cost every break in a
// live run and that no other test in this package could see: the fake sidecar
// ignores the voice field, so only real Kokoro rejects "kokoro:am_michael".
func TestVoiceIDStripsTheEngineNamespace(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"kokoro:am_michael", "am_michael", false},
		{"am_michael", "am_michael", false},
		{"", "", false},
		{"orpheus:tara", "", true},
	}
	for _, tc := range cases {
		got, err := voiceFor(tc.in)
		if tc.wantErr {
			if !errors.Is(err, ErrWrongEngine) {
				t.Errorf("voiceFor(%q) error = %v, want ErrWrongEngine: an Orpheus voice sent to Kokoro must fail loudly, not silently drop every break", tc.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("voiceFor(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("voiceFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
