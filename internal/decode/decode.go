// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package decode turns audio files on disk into canonical-bus frames by
// shelling out to ffmpeg.
//
// ffmpeg is container-aware, so MP3 LAME/Xing gapless trim and MP4 edit lists
// come for free. That is the whole reason this program never parses audio
// frames itself: a Go decoding library gets gapless wrong, and gapless is
// audible on every album that was mastered as one continuous piece.
package decode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/andrewloable/jockora/internal/mix"
)

// ErrDecodeFailed wraps every failure to turn a file into audio.
var ErrDecodeFailed = errors.New("decode failed")

// ErrDecodeTimeout means ffmpeg stopped producing audio and never exited.
//
// A separate error from ErrDecodeFailed so a log can distinguish "this file is
// broken" from "this file hangs ffmpeg". Both end the same way for the stream
// -- skip the track, keep the music -- but they are different problems with
// different fixes.
var ErrDecodeTimeout = errors.New("decoder timed out")

// blockFrames is how many frames are read from ffmpeg before a block is handed
// to the consumer. 4096 frames is ~85 ms on the bus: small enough to stay
// responsive, large enough that the channel is not the bottleneck.
const blockFrames = 4096

// ffmpegPath is the decoder binary. A variable so tests can substitute a
// stand-in that hangs on purpose; nothing else should ever change it.
var ffmpegPath = "ffmpeg"

// StallTimeout is how long the decoder waits on ffmpeg producing NOTHING before
// giving up on the track.
//
// It measures time spent waiting on ffmpeg, NOT time spent in Decode. The
// decoder blocks sending to the mixer for long stretches by design -- that is
// the pacing -- so a timeout around the whole loop would kill every healthy
// decode of a long track. This one only runs while a read is outstanding.
//
// Thirty seconds because ffmpeg has to probe a container before it emits a
// sample, and a network-mounted library on a cold spindle is slow.
var StallTimeout = 30 * time.Second

// Decode reads path through ffmpeg and sends canonical-bus frames on out.
//
// The channel belongs to the caller: Decode never closes it. Decode returns nil
// only when ffmpeg exited cleanly having produced at least one frame.
func Decode(ctx context.Context, path string, out chan<- []mix.Frame) error {
	// Cancellable independently of the caller, so the watchdog can end a decode
	// the caller has not given up on.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", path,
		"-f", "f32le",
		"-ar", strconv.Itoa(mix.SampleRate),
		"-ac", strconv.Itoa(mix.Channels),
		"-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%w: %s: stdout pipe: %v", ErrDecodeFailed, path, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("%w: %s: stderr pipe: %v", ErrDecodeFailed, path, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: %s: starting ffmpeg: %v", ErrDecodeFailed, path, err)
	}

	// Drain stderr concurrently and unconditionally. ffmpeg blocks forever once
	// the stderr pipe fills, and a file that produces many warnings will fill it.
	var diag bytes.Buffer
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		io.Copy(&diag, stderr) //nolint:errcheck // a failed drain is reported by Wait
	}()

	// The watchdog. A truncated container makes ffmpeg block on INPUT and never
	// exit, and nothing else here would ever notice: the read simply does not
	// return. waitingSince is the nanosecond a read began, or zero while the
	// decoder is blocked on the CONSUMER, which is not a stall and must not be
	// counted as one.
	var waitingSince atomic.Int64
	var timedOut atomic.Bool
	// The watchdog has its OWN stop channel rather than watching ctx. Stopping
	// it must not cancel the context, because cancelling before cmd.Wait makes
	// Wait report "context canceled" instead of the process's real exit status
	// -- which turned every clean decode into a reported failure.
	watchdogStop := make(chan struct{})
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		tick := time.NewTicker(StallTimeout / 4)
		defer tick.Stop()
		for {
			select {
			case <-watchdogStop:
				return
			case <-tick.C:
				since := waitingSince.Load()
				if since != 0 && time.Since(time.Unix(0, since)) > StallTimeout {
					timedOut.Store(true)
					cancel() // CommandContext kills the process
					return
				}
			}
		}
	}()

	total := 0
	var loopErr error
	for loopErr == nil {
		buf := make([]mix.Frame, blockFrames)
		waitingSince.Store(time.Now().UnixNano())
		n, readErr := mix.ReadF32LE(stdout, buf)
		waitingSince.Store(0)
		if n > 0 {
			total += n
			select {
			case out <- buf[:n]:
			case <-ctx.Done():
				loopErr = ctx.Err()
			}
		}
		if readErr != nil {
			// EOF either way: a clean end, or an end mid-frame because the
			// process was killed. A killed process shows up in Wait below.
			if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
				loopErr = readErr
			}
			break
		}
	}

	// Never leave ffmpeg blocked writing to a pipe nobody is reading; Wait would
	// then never return.
	io.Copy(io.Discard, stdout) //nolint:errcheck // best-effort drain before Wait
	<-drained
	close(watchdogStop)
	<-watchdogDone

	// Wait on every path, including cancellation. An unreaped process leaks, and
	// ten thousand tracks over a month exhausts the process table.
	//
	// NOT preceded by cancel(). exec.CommandContext makes Wait return the
	// context's error rather than the process's exit status when the context is
	// already cancelled, so cancelling first reports every successful decode as
	// a failure. The deferred cancel at the top of this function runs after
	// this, which is exactly right.
	waitErr := cmd.Wait()

	// The timeout is reported as itself, not as a generic decode failure, so an
	// operator reading a log can tell "this file is broken" from "this file
	// hangs ffmpeg" -- different problems with different fixes. Both end the
	// same way for the stream: skip the track, keep the music.
	if timedOut.Load() {
		return fmt.Errorf("%w: %s: produced nothing for %s", ErrDecodeTimeout, path, StallTimeout)
	}
	if loopErr != nil {
		return fmt.Errorf("%w: %s: %w", ErrDecodeFailed, path, loopErr)
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(diag.String()); msg != "" {
			return fmt.Errorf("%w: %s: %v: %s", ErrDecodeFailed, path, waitErr, msg)
		}
		return fmt.Errorf("%w: %s: %v", ErrDecodeFailed, path, waitErr)
	}
	if total == 0 {
		// Exit 0 and not one sample. The file is structurally valid and
		// musically empty, which is a corrupt file wearing a good header.
		return fmt.Errorf("%w: %s: ffmpeg exited 0 but produced no audio", ErrDecodeFailed, path)
	}
	return nil
}
