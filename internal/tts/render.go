// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package tts

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"

	"github.com/andrewloable/jockora/internal/mix"
	"github.com/andrewloable/jockora/internal/say"
)

// Render turns a break script into a WAV the mixer can read directly.
//
// Kokoro speaks 24 kHz mono. The bus is 48 kHz stereo. Handing 24 kHz to a
// 48 kHz bus does not merely sound wrong, it plays at half speed an octave
// down, so the resample is not optional -- and neither is doing it here: the
// mixer must never meet a format it did not expect.
//
// Loudness is not optional either. The duck is a FIXED 12 dB, which only means
// something if speech always arrives at the same level; unnormalised speech
// under a fixed duck is either inaudible or a shout depending on the take.
//
// ffmpeg does all of it, out of process, as everywhere else in Jockora: it is
// GPL and must never be linked in.
func Render(ctx context.Context, s *Sidecar, text, voice, outPath string) (durationSeconds float64, err error) {
	// SPOKEN FORM, and only here. The DJ said "nineteen hundred and ninety
	// three" for 1993 because kokoro_server.py does no number normalisation at
	// all and the model's own spelling is no better.
	//
	// NOT EARLIER, and this is the part that matters: the said-lines index
	// records the break text and the length check counts its words, so
	// normalising before either would make the stored text differ from what the
	// validator saw. This is the last point before the audio, and it covers DJ
	// breaks, adverts and the voice preview because all three arrive through
	// here. Jockora-hm2.
	raw, err := s.Synthesize(ctx, say.SpeakableText(text), voice)
	if err != nil {
		return 0, err
	}

	// A failed render must leave nothing behind. A half-written WAV in the
	// break directory is a file the scheduler would happily air.
	defer func() {
		if err != nil {
			_ = os.Remove(outPath)
		}
	}()

	measured, err := measureLUFS(ctx, raw)
	if err != nil {
		return 0, err
	}
	if err := renderTo(ctx, raw, mix.SpeechLUFS-measured, outPath); err != nil {
		return 0, err
	}

	// The length is read back from the file rather than predicted, and through
	// mix.ReadWAV rather than ffprobe: that is the same strict reader the mixer
	// uses, so anything it will not accept fails here at render time instead of
	// at airtime.
	frames, err := mix.ReadWAV(outPath)
	if err != nil {
		return 0, fmt.Errorf("tts: rendered break is not bus format: %w", err)
	}
	if len(frames) == 0 {
		return 0, fmt.Errorf("tts: rendered break is empty")
	}
	return float64(len(frames)) / mix.SampleRate, nil
}

var integratedRE = regexp.MustCompile(`I:\s+(-?[\d.]+)\s+LUFS`)

// measureLUFS reads the clip's integrated loudness with ebur128.
//
// NOT loudnorm, which is the obvious choice and the wrong one. ffmpeg's
// loudnorm needs about three seconds of audio to work, and real breaks are
// often shorter than that -- "Stay where you are." is 1.3 seconds. Measured on
// real Kokoro output, loudnorm left a 1.34s clip at -17.9 LUFS against a -16
// target in single pass AND in two-pass linear mode, because the clip yields
// too few gating blocks for its loudness range to mean anything.
//
// Measuring and then applying a flat gain has none of that trouble: it is
// exact at any length, and it is linear, so it does not quietly compress the
// dynamics of a voice the persona was written for.
func measureLUFS(ctx context.Context, wav []byte) (float64, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-hide_banner", "-f", "wav", "-i", "pipe:0",
		"-af", "ebur128", "-f", "null", "-")
	cmd.Stdin = bytes.NewReader(wav)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("tts: measuring loudness: %w: %s", err, out.String())
	}
	// The last I: in the log is the summary's integrated figure; the ones
	// before it are per-frame running values.
	m := integratedRE.FindAllStringSubmatch(out.String(), -1)
	if len(m) == 0 {
		return 0, fmt.Errorf("tts: ebur128 reported no integrated loudness:\n%s", out.String())
	}
	v, err := strconv.ParseFloat(m[len(m)-1][1], 64)
	if err != nil {
		return 0, fmt.Errorf("tts: unreadable loudness %q: %w", m[len(m)-1][1], err)
	}
	if v < -70 {
		return 0, fmt.Errorf("tts: synthesised audio measured %.1f LUFS, which is silence", v)
	}
	return v, nil
}

// renderTo applies the gain, holds the ceiling, and resamples to the bus.
//
// The limiter is needed because speech has a high crest factor: bringing a
// voice up to -16 LUFS puts its peaks above full scale, and clipping them
// would be audible on exactly the consonants that carry the words.
func renderTo(ctx context.Context, wav []byte, gainDB float64, outPath string) error {
	ceiling := math.Pow(10, mix.TruePeakCeilingDBTP/20)
	filter := fmt.Sprintf("volume=%.2fdB,alimiter=limit=%.4f:level=false", gainDB, ceiling)
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "wav", "-i", "pipe:0",
		"-af", filter,
		// -ac 2 duplicates mono to both channels. Speech belongs in the middle
		// of the room; anything else puts the DJ in one speaker.
		"-ar", fmt.Sprint(mix.SampleRate), "-ac", fmt.Sprint(mix.Channels),
		"-c:a", "pcm_s16le", "-y", outPath)
	cmd.Stdin = bytes.NewReader(wav)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tts: rendering: %w: %s", err, stderr.String())
	}
	return nil
}
