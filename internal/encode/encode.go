// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package encode runs the single long-lived ffmpeg that turns the mixer's f32le
// stream into AAC and writes HLS segments plus a playlist.
//
// Encoding once, from an already-mixed stream, is what makes the splice
// seamless: the segmenter never sees a seam because there is not one. Speech
// lives inside the music timeline rather than being stitched next to it.
package encode

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
)

// Config describes the output. Zero fields take the bus defaults.
type Config struct {
	SegmentDir     string
	StartNumber    int    // first segment/media-sequence number; 0 for a fresh stream
	DiscontStart   bool   // mark the start of this run as a discontinuity (restarts only)
	SegmentSeconds int    // default 4
	ListSize       int    // default 10
	SampleRate     int    // default 48000
	Channels       int    // default 2
	Bitrate        string // default "128k"
	LogLevel       string // default "warning"
	FFmpegPath     string // default "ffmpeg"
}

func (c *Config) applyDefaults() {
	if c.SegmentSeconds == 0 {
		c.SegmentSeconds = 4
	}
	if c.ListSize == 0 {
		c.ListSize = 10
	}
	if c.SampleRate == 0 {
		c.SampleRate = 48000
	}
	if c.Channels == 0 {
		c.Channels = 2
	}
	if c.Bitrate == "" {
		c.Bitrate = "128k"
	}
	if c.LogLevel == "" {
		c.LogLevel = "warning"
	}
	if c.FFmpegPath == "" {
		c.FFmpegPath = "ffmpeg"
	}
}

// PlaylistName is the file clients fetch.
const PlaylistName = "stream.m3u8"

// Encoder is one running ffmpeg. Write mixed f32le to Writer(); call Stop() to
// finish the stream cleanly.
type Encoder struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser

	drained chan struct{}
	mu      sync.Mutex
	diag    bytes.Buffer

	stopOnce sync.Once
	stopErr  error
}

// Start spawns the encoder.
//
// A cold start clears stale segments first. ffmpeg's append_list over a stale
// directory produces a playlist naming segments that no longer exist, and every
// client errors on its first fetch. A warm restart keeps them, so the supervisor
// can resume a stream without a visible break.
func Start(ctx context.Context, cfg Config, cold bool) (*Encoder, error) {
	cfg.applyDefaults()

	if err := os.MkdirAll(cfg.SegmentDir, 0o755); err != nil {
		return nil, fmt.Errorf("encode: segment dir: %w", err)
	}
	if cold {
		if err := clearSegments(cfg.SegmentDir); err != nil {
			return nil, err
		}
	}

	playlist := filepath.Join(cfg.SegmentDir, PlaylistName)

	// No append_list. It was measured against the alternative and lost twice:
	// on an empty directory it emits a spurious EXT-X-DISCONTINUITY at the top
	// of a brand-new stream, and on a restart it keeps the old entries while
	// ffmpeg overwrites EXT-X-MEDIA-SEQUENCE with the new start number. The
	// result is a playlist whose first entry is seg0.ts announced at sequence 2,
	// so a client re-fetches audio it has already played.
	//
	// discont_start instead writes a fresh window: the media sequence then
	// correctly identifies the first segment listed, exactly one discontinuity
	// marks the join, and the pre-restart files stay on disk for requests
	// already in flight.
	// No delete_segments either. Retention is owned by the server's sweeper,
	// which deliberately keeps segments on disk LONGER than the playlist
	// advertises so a briefly-suspended client still finds what it last saw.
	// ffmpeg pruning at the window edge would make that impossible, and it is
	// also why old segments used to survive an encoder restart uncollected.
	flags := "independent_segments+temp_file"
	if cfg.DiscontStart {
		flags += "+discont_start"
	}

	args := []string{
		"-nostdin", "-hide_banner", "-loglevel", cfg.LogLevel,
		// No -re here. Pacing is the mixer's job; ffmpeg must consume as fast as
		// it is fed or the mixer's clock and the stream's would be two clocks.
		"-f", "f32le", "-ar", strconv.Itoa(cfg.SampleRate), "-ac", strconv.Itoa(cfg.Channels), "-i", "pipe:0",
		"-c:a", "aac", "-b:a", cfg.Bitrate, "-ar", strconv.Itoa(cfg.SampleRate), "-ac", strconv.Itoa(cfg.Channels),
		"-f", "hls",
		"-hls_time", strconv.Itoa(cfg.SegmentSeconds),
		"-hls_list_size", strconv.Itoa(cfg.ListSize),
		// temp_file matters: without it a client can fetch a half-written
		// segment and hear a click or drop the stream.
		"-hls_flags", flags,
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(cfg.SegmentDir, "seg%d.ts"),
	}
	if cfg.StartNumber > 0 {
		args = append(args, "-start_number", strconv.Itoa(cfg.StartNumber))
	}
	args = append(args, playlist)

	cmd := exec.CommandContext(ctx, cfg.FFmpegPath, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("encode: stdin pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("encode: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("encode: starting %s: %w", cfg.FFmpegPath, err)
	}

	e := &Encoder{cmd: cmd, stdin: stdin, drained: make(chan struct{})}

	// Drain stderr for the life of the process. ffmpeg BLOCKS once its stderr
	// buffer fills, and the whole stream then freezes with no error message
	// anywhere: no log line, no exit code, just silence.
	go func() {
		defer close(e.drained)
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				e.mu.Lock()
				e.diag.Write(buf[:n])
				e.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	return e, nil
}

// Writer is where the mixer sends f32le. Only the mixer goroutine may write here.
func (e *Encoder) Writer() io.Writer { return e.stdin }

// Stderr returns everything ffmpeg has said so far.
func (e *Encoder) Stderr() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.diag.String()
}

// Stop closes the input and waits for ffmpeg to finish writing the last segment.
// It is safe to call more than once; shutdown paths do.
func (e *Encoder) Stop() error {
	e.stopOnce.Do(func() {
		// Closing stdin is what tells ffmpeg the stream ended, so it flushes the
		// final segment and updates the playlist before exiting.
		closeErr := e.stdin.Close()
		<-e.drained

		if err := e.cmd.Wait(); err != nil {
			e.stopErr = fmt.Errorf("encode: ffmpeg exited: %w: %s", err, e.Stderr())
			return
		}
		if closeErr != nil {
			e.stopErr = fmt.Errorf("encode: closing input: %w", closeErr)
		}
	})
	return e.stopErr
}

// clearSegments removes only the files this program writes.
//
// It deliberately does not empty the directory: SegmentDir is operator-supplied
// and someone will eventually point it somewhere shared. Deleting seg*.ts and
// the playlist achieves the cold-start goal without ever touching a file that
// is not ours.
func clearSegments(dir string) error {
	patterns := []string{"seg*.ts", PlaylistName, PlaylistName + ".tmp"}
	for _, p := range patterns {
		matches, err := filepath.Glob(filepath.Join(dir, p))
		if err != nil {
			return fmt.Errorf("encode: scanning segment dir: %w", err)
		}
		for _, m := range matches {
			if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("encode: clearing stale segment %s: %w", m, err)
			}
		}
	}
	return nil
}

// process exposes the running process so the supervisor can watch it and tests
// can kill it.
func (e *Encoder) process() *os.Process {
	if e.cmd == nil {
		return nil
	}
	return e.cmd.Process
}
