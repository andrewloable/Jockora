# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only
"""Vocal-onset detection.

Answers one question about a track: when does the singing start, and when does
it stop? Everything between those two points is off limits to the DJ; the intro
before and the tail after are where a break may sit.

WHY THIS EXISTS. The cheap source for these numbers is synced lyrics from
LRCLIB, and where they exist they are better than anything here -- a person
listened. Measured on the real library, they exist for 41.5% of tracks. This
covers the rest, including every track with no artist or title tag, which LRCLIB
can never answer for at all.

IT IS A HEURISTIC AND IT IS LABELLED AS ONE. Results are stored with
ramp_confidence "analysis", never "lrc", so that when a break lands on top of a
vocal the first question -- which source produced this number -- has an answer.

TUNING. The two thresholds are the knobs. Real music does not match any fixed
number, and a sung intro over a string pad reads differently from a rap over a
drum loop, so both are settable without editing this file.
"""

import os
import subprocess

import numpy as np

# Analysis rate. Vocals live well below 11 kHz, and decoding at 22050 rather
# than 48000 halves the work for every track in the library.
SR = 22050

# The band a voice occupies. Below 200 Hz is bass and kick; above 4000 Hz is
# mostly cymbals and air, plus consonants that are not sustained anyway.
VOCAL_LOW_HZ = 200.0
VOCAL_HIGH_HZ = 4000.0

# Fraction of the track's own loud-band energy that counts as "voice present".
# Relative, not absolute, so it survives a quiet master and a loud one.
#
# 0.30 and a 4-second minimum run were MEASURED, not guessed. Forty tracks that
# carry LRC data were used as ground truth -- a human typed those timestamps --
# and the pair was swept against them:
#
#   threshold   min run   usable ramps   talks over a vocal   worst overshoot
#      0.15       4.0        13/40              0/13                 --
#      0.25       3.0        19/40              2/19  (11%)         7.7s
#      0.30       4.0        24/40              2/24  ( 8%)         7.7s
#      0.40       3.0        33/40              5/33  (15%)         7.7s
#
# Lower is safer and useless: it reports the vocal starting almost immediately,
# the ramp clamps away, and the track falls back to "between" placement, which
# is the outcome this whole module exists to avoid. 0.30/4.0 roughly doubles
# ramp availability -- 41.5% from LRC alone to about 76% -- at 8% wrong.
#
# The overshoot is bounded by the CAP on the Go side, not by these numbers.
# Sweeping showed the cap is what limits the damage: at a 20s cap the worst case
# was 17.7s of talking over singing, at 10s it is 7.7s, and that held at every
# threshold. Retune during the live-with-it month by listening.
THRESHOLD = float(os.environ.get("JOCKORA_ONSET_THRESHOLD", "0.30"))

# How long a region must hold before it is a vocal rather than a stab, a horn
# line or a sampled shout.
MIN_RUN_S = float(os.environ.get("JOCKORA_ONSET_MIN_RUN", "4.0"))

HOP = 512


def decode(path):
    """Decode any input to mono float32 at SR, through ffmpeg.

    Not librosa.load. ffmpeg is already a hard dependency, already handles every
    container in a real library, and keeping one audio-IO stack means a file
    that plays in Jockora is a file this can analyse. librosa's loader would add
    soundfile and audioread with their own format gaps.
    """
    out = subprocess.run(
        ["ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
         "-i", path, "-map", "0:a:0", "-ac", "1", "-ar", str(SR),
         "-f", "f32le", "pipe:1"],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True)
    return np.frombuffer(out.stdout, dtype=np.float32)


def envelope(y):
    """A vocal-activity envelope, normalised to the track's own loud passages."""
    import librosa

    # Harmonic component only. Percussion carries almost no pitch and would
    # otherwise put a drum fill in the middle of the "vocal" region. margin > 1
    # separates harder, at some cost in recall, which is the right trade when a
    # false positive means the DJ talks over singing.
    harmonic = librosa.effects.harmonic(y, margin=3.0)

    spec = np.abs(librosa.stft(harmonic, hop_length=HOP))
    freqs = librosa.fft_frequencies(sr=SR)
    band = (freqs >= VOCAL_LOW_HZ) & (freqs <= VOCAL_HIGH_HZ)
    env = spec[band].mean(axis=0)

    # Median-smooth over about a quarter of a second so a single loud frame
    # cannot start or end a region.
    width = max(3, int(0.25 * SR / HOP) | 1)
    env = np.convolve(env, np.ones(width) / width, mode="same")

    # Normalise by a high percentile rather than the maximum: one clipped frame
    # would otherwise scale the whole track towards zero.
    loud = np.percentile(env, 95)
    if loud <= 0:
        return np.zeros_like(env)
    return env / loud


def sustained_bounds(env, frames_per_second):
    """First and last sample of a run at least MIN_RUN_S long, or (None, None)."""
    active = env > THRESHOLD
    min_frames = max(1, int(MIN_RUN_S * frames_per_second))

    runs = []
    start = None
    for i, on in enumerate(active):
        if on and start is None:
            start = i
        elif not on and start is not None:
            if i - start >= min_frames:
                runs.append((start, i))
            start = None
    if start is not None and len(active) - start >= min_frames:
        runs.append((start, len(active)))

    if not runs:
        return None, None
    return runs[0][0] / frames_per_second, runs[-1][1] / frames_per_second


def analyse(path, duration=0.0):
    """Return {"vocal_start": float|None, "vocal_end": float|None, "duration": float}.

    The duration is reported because the caller may not know it. The Go side
    validates against it, and a caller that passed zero would otherwise have a
    perfectly good detection thrown away as "no usable ramp".
    """
    y = decode(path)
    if y.size == 0:
        return {"vocal_start": None, "vocal_end": None, "duration": 0.0}

    env = envelope(y)
    start, end = sustained_bounds(env, SR / HOP)

    # Clamp to the track. The Go side validates again -- deliberately, since
    # these numbers feed placement maths -- but returning something outside the
    # file would be a bug worth catching here too.
    total = duration if duration > 0 else y.size / SR
    if start is not None:
        start = max(0.0, min(start, total))
        end = max(0.0, min(end, total))
        if end <= start:
            return {"vocal_start": None, "vocal_end": None, "duration": total}
    return {"vocal_start": start, "vocal_end": end, "duration": total}
