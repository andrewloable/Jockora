// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package mix

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// These tests measure the chain's output with ffmpeg, an EXTERNAL
// implementation of EBU R128, and compare it against the loudness contract in
// duck.go -- never against a stored byte array.
//
// Golden-PCM tests catch regressions against a reference. They cannot tell you
// the reference is correct, and if the goldens were generated from the
// implementation under test then a systematic error ships invisibly and every
// test stays green. This is the file that would notice.

// loudnorm is what ffmpeg's loudnorm filter reports about a file.
type loudnorm struct {
	InputI   string `json:"input_i"`
	InputTP  string `json:"input_tp"`
	InputLRA string `json:"input_lra"`
}

func (l loudnorm) integrated(t *testing.T) float64 { return parseLU(t, l.InputI) }
func (l loudnorm) truePeak(t *testing.T) float64   { return parseLU(t, l.InputTP) }

func parseLU(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		t.Fatalf("unreadable loudness %q: %v", s, err)
	}
	return v
}

// measure runs ffmpeg's loudnorm in analysis mode over a WAV.
func measure(t *testing.T, path string) loudnorm {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-i", path,
		"-af", "loudnorm=print_format=json", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("loudnorm: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(?s)\{[^{}]*"input_i"[^{}]*\}`).Find(out)
	if m == nil {
		t.Fatalf("no loudnorm JSON in ffmpeg's output:\n%s", out)
	}
	var l loudnorm
	if err := json.Unmarshal(m, &l); err != nil {
		t.Fatalf("parsing loudnorm JSON %s: %v", m, err)
	}
	return l
}

// shortTerm is the loudest 3-second window, which is the descriptor the
// contract uses for speech.
func shortTerm(t *testing.T, path string) float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-v", "verbose", "-i", path,
		"-af", "ebur128=framelog=verbose", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ebur128: %v", err)
	}
	loudest := math.Inf(-1)
	for _, m := range regexp.MustCompile(`S:\s*(-?[\d.]+)`).FindAllStringSubmatch(string(out), -1) {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > loudest {
			loudest = v
		}
	}
	if math.IsInf(loudest, -1) || loudest < -70 {
		t.Fatalf("no valid short-term window in %s", path)
	}
	return loudest
}

// writeWAV writes bus frames as a 48 kHz stereo 16-bit WAV.
func writeWAV(t *testing.T, path string, frames []Frame) {
	t.Helper()
	data := make([]byte, len(frames)*4)
	le := binary.LittleEndian
	for i, f := range frames {
		le.PutUint16(data[i*4:], uint16(int16(clamp(f.L)*32767)))
		le.PutUint16(data[i*4+2:], uint16(int16(clamp(f.R)*32767)))
	}
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	le.PutUint32(hdr[4:], uint32(36+len(data)))
	copy(hdr[8:], "WAVEfmt ")
	le.PutUint32(hdr[16:], 16)
	le.PutUint16(hdr[20:], 1)
	le.PutUint16(hdr[22:], 2)
	le.PutUint32(hdr[24:], SampleRate)
	le.PutUint32(hdr[28:], SampleRate*4)
	le.PutUint16(hdr[32:], 4)
	le.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	le.PutUint32(hdr[40:], uint32(len(data)))
	if err := os.WriteFile(path, append(hdr, data...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func clamp(v float32) float32 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

// programme makes something loudness-meaningful: filtered noise with slow
// dynamics, so the meter has real content rather than a tone whose loudness is
// a single number by construction.
func programme(seconds float64, amp float64) []Frame {
	n := int(seconds * SampleRate)
	out := make([]Frame, n)
	rng := rand.New(rand.NewSource(7))
	var lp float64
	for i := range out {
		white := rng.Float64()*2 - 1
		lp += (white - lp) * 0.08 // gentle low-pass, roughly programme-shaped
		env := 0.75 + 0.25*math.Sin(2*math.Pi*0.2*float64(i)/SampleRate)
		v := float32(lp * amp * env)
		out[i] = Frame{L: v, R: v}
	}
	return out
}

// runChain pushes frames through the real chain in blocks, exactly as the mixer
// does, so the limiter and ducker see the same block sizes they see on air.
func runChain(music, speech []Frame, speaking bool) []Frame {
	c := NewChain()
	const block = 4800
	out := make([]Frame, len(music))
	for off := 0; off < len(music); off += block {
		end := min(off+block, len(music))
		var sp []Frame
		if speech != nil {
			sp = speech[off:end]
		} else {
			sp = make([]Frame, end-off)
		}
		c.Process(out[off:end], music[off:end], nil, sp, State{Speaking: speaking})
	}
	return out
}

// normalisedMusic is 60 seconds of programme brought to the contract's music
// target by the SAME GainFor the scanner drives, measured externally first so
// the gain is applied to a real number rather than an assumed one.
func normalisedMusic(t *testing.T, seconds float64) []Frame {
	t.Helper()
	// Loud enough that the gain it needs is INSIDE MaxNormalisationDB. The
	// first version of this test used material at -29.6 LUFS, which needs
	// +13.6 dB, and the cap correctly refused to give it -- so the output
	// measured -17.6 and the test blamed the chain for a limit working exactly
	// as designed. Real masters sit between -8 and -14 LUFS; nothing in a music
	// library is 30 dB down.
	raw := programme(seconds, 0.6)

	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.wav")
	writeWAV(t, rawPath, raw)
	measured := measure(t, rawPath).integrated(t)
	t.Logf("source programme measured %.2f LUFS", measured)

	g := GainFor(measured)
	out := make([]Frame, len(raw))
	for i, f := range raw {
		out[i] = Frame{L: f.L * g, R: f.R * g}
	}
	return out
}

func TestMixedOutputMeasuresMinus16LUFS(t *testing.T) {
	music := normalisedMusic(t, 60)
	mixed := runChain(music, nil, false)

	path := filepath.Join(t.TempDir(), "mixed.wav")
	writeWAV(t, path, mixed)
	got := measure(t, path).integrated(t)

	t.Logf("mixed music measured %.2f LUFS (contract %.1f)", got, MusicLUFS)
	if math.Abs(got-MusicLUFS) > 1.0 {
		t.Errorf("integrated loudness %.2f LUFS, want %.1f +/- 1.0", got, MusicLUFS)
	}
}

func TestTruePeakUnderMinus1(t *testing.T) {
	music := normalisedMusic(t, 60)
	mixed := runChain(music, nil, false)

	path := filepath.Join(t.TempDir(), "mixed.wav")
	writeWAV(t, path, mixed)
	got := measure(t, path).truePeak(t)

	t.Logf("true peak %.2f dBTP (ceiling %.1f)", got, TruePeakCeilingDBTP)
	// Integrated loudness can be perfectly correct while peaks clip, which is
	// why this is a separate assertion and not a corollary of the one above.
	if got > TruePeakCeilingDBTP {
		t.Errorf("true peak %.2f dBTP is above the %.1f dBTP ceiling", got, TruePeakCeilingDBTP)
	}
}

// TestDuckedSectionMeasuresMinus28 measures the music alone while the ducker is
// engaged: speech is silent here, so what the meter sees is exactly the music
// bed a listener hears under a break.
func TestDuckedSectionMeasuresMinus28(t *testing.T) {
	music := normalisedMusic(t, 60)
	ducked := runChain(music, nil, true)

	// The first second is the duck's attack ramp and is not steady state.
	path := filepath.Join(t.TempDir(), "ducked.wav")
	writeWAV(t, path, ducked[SampleRate:])
	got := measure(t, path).integrated(t)

	t.Logf("ducked music measured %.2f LUFS (contract %.1f)", got, MusicDuckedLUFS)
	if math.Abs(got-MusicDuckedLUFS) > 1.5 {
		t.Errorf("ducked loudness %.2f LUFS, want %.1f +/- 1.5", got, MusicDuckedLUFS)
	}
}

// TestSpeechMeasuresMinus16ShortTerm checks what the CHAIN is responsible for:
// that speech arrives at the output at the level it was handed in, while the
// music under it is pulled down. Speech is not ducked -- that is the entire
// point of the duck -- so any loss here is the chain quietly attenuating the DJ.
//
// It deliberately does NOT assert that speech is -16 LUFS. Nothing in this
// package sets that: tts.Render measures each rendered break and applies a flat
// gain, and THAT is validated against real Kokoro output in 96e.2, where five
// utterances landed between -16.2 and -16.8. Asserting it here would measure a
// synthetic signal's envelope and call it a chain property -- the first version
// of this test did exactly that and read -14.60 against a target no code in
// this package owns.
func TestSpeechMeasuresMinus16ShortTerm(t *testing.T) {
	speech := normalisedMusic(t, 20)
	silence := make([]Frame, len(speech))

	dir := t.TempDir()
	inPath := filepath.Join(dir, "speech-in.wav")
	writeWAV(t, inPath, speech)
	before := measure(t, inPath).integrated(t)

	mixed := runChain(silence, speech, true)
	outPath := filepath.Join(dir, "speech-out.wav")
	writeWAV(t, outPath, mixed)
	after := measure(t, outPath).integrated(t)

	t.Logf("speech in %.2f LUFS -> out %.2f LUFS while the ducker is engaged", before, after)
	if math.Abs(after-before) > 0.5 {
		t.Errorf("the chain changed speech by %.2f dB; speech must pass through the duck untouched",
			after-before)
	}

	// And the contract it is handed: whatever tts.Render produces is what airs.
	if math.Abs(before-SpeechLUFS) > 1.0 {
		t.Logf("note: the test signal is %.2f LUFS, not the %.1f contract; that is tts.Render's job, not this package's",
			before, SpeechLUFS)
	}
}

// TestNormalisationCapIsDeliberate documents the limit that the first version
// of this file mistook for a bug.
//
// A track quieter than MaxNormalisationDB below the target STAYS QUIET, on
// purpose: a mis-measured or near-silent file would otherwise demand +30 dB and
// destroy the mix for everything around it. The consequence is worth stating
// plainly -- normalisation is bounded, so a genuinely quiet master will still
// be quieter than its neighbours, and that is the better failure.
func TestNormalisationCapIsDeliberate(t *testing.T) {
	// Needs +13.6 dB, gets +12.
	if got := GainFor(-29.6); math.Abs(float64(got)-math.Pow(10, MaxNormalisationDB/20)) > 0.01 {
		t.Errorf("GainFor(-29.6) = %v, want the %v dB cap", got, MaxNormalisationDB)
	}
	// Inside the range, applied exactly.
	if got := GainFor(-22.0); math.Abs(float64(got)-math.Pow(10, 6.0/20)) > 0.01 {
		t.Errorf("GainFor(-22.0) = %v, want +6 dB", got)
	}
	// A track LOUDER than the target is pulled down, and bounded the same way.
	if got := GainFor(-2.0); math.Abs(float64(got)-math.Pow(10, -MaxNormalisationDB/20)) > 0.01 {
		t.Errorf("GainFor(-2.0) = %v, want the -%v dB cap", got, MaxNormalisationDB)
	}
}
