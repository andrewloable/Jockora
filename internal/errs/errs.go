// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

// Package errs holds the failure classes that cross package boundaries.
//
// Every one of these is CERTAIN over a month of continuous running, and each is
// a place where the "music never stops" invariant could quietly break. They are
// named rather than handled by a catch-all on purpose: an operator reading a log
// at three in the morning needs to know which of these happened, because the
// fixes are completely different and only one of them is theirs to make.
package errs

import (
	"errors"
	"strings"
	"syscall"
)

var (
	// ErrUnsupportedFormat means a file is not audio this build can decode.
	//
	// Detected at SCAN time, never at play time. A library scan is allowed to
	// take an hour; a stream is not allowed to discover mid-transition that the
	// next track was a video file somebody dropped in the folder.
	ErrUnsupportedFormat = errors.New("unsupported audio format")

	// ErrToolMissing means an operator-supplied binary is not installed.
	//
	// Fails FAST, at startup, naming the binary. The alternative is a stream
	// that starts, plays nothing, and reports no reason -- and ffmpeg missing is
	// the single most likely first-run problem this program has.
	ErrToolMissing = errors.New("required external tool is missing")

	// ErrSegmentWrite means a segment could not be written to disk.
	//
	// Almost always a full disk, and it MUST NOT crash the server. A disk that
	// fills and is then cleared -- by logrotate, by a cleanup job, by a person
	// noticing -- should find the stream still running and resume, rather than
	// find a dead process and a listener who left an hour ago.
	ErrSegmentWrite = errors.New("segment write failed")
)

// diskFullText matches the messages that carry ENOSPC across a process
// boundary, where the errno itself does not survive.
//
// ffmpeg writes the segments, not this program, so a full disk usually arrives
// as a line of ffmpeg's stderr rather than as a syscall error. Matching the text
// is unavoidable; matching ONLY the text would miss the local case.
var diskFullText = []string{
	"no space left on device",
	"disk quota exceeded",
	"enospc",
}

// IsDiskFull reports whether an error is a full disk, however it arrived.
func IsDiskFull(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, t := range diskFullText {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}
