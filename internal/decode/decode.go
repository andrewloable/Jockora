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

	"github.com/andrewloable/jockora/internal/mix"
)

// ErrDecodeFailed wraps every failure to turn a file into audio.
var ErrDecodeFailed = errors.New("decode failed")

// blockFrames is how many frames are read from ffmpeg before a block is handed
// to the consumer. 4096 frames is ~85 ms on the bus: small enough to stay
// responsive, large enough that the channel is not the bottleneck.
const blockFrames = 4096

// Decode reads path through ffmpeg and sends canonical-bus frames on out.
//
// The channel belongs to the caller: Decode never closes it. Decode returns nil
// only when ffmpeg exited cleanly having produced at least one frame.
func Decode(ctx context.Context, path string, out chan<- []mix.Frame) error {
	cmd := exec.CommandContext(ctx, "ffmpeg",
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

	total := 0
	var loopErr error
	for loopErr == nil {
		buf := make([]mix.Frame, blockFrames)
		n, readErr := mix.ReadF32LE(stdout, buf)
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

	// Wait on every path, including cancellation. An unreaped process leaks, and
	// ten thousand tracks over a month exhausts the process table.
	waitErr := cmd.Wait()

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
