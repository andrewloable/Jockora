// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package sched

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// FakeSilenceBreak writes a WAV of pure silence and returns its path.
//
// It exists so the splice spike can exercise the scheduler-to-mixer coupling
// with no LLM and no TTS anywhere in the loop. A spike that never enqueues
// anything proves the transport works and proves nothing about coordination.
func FakeSilenceBreak(seconds float64, dir string) (string, error) {
	if !(seconds > 0) {
		return "", fmt.Errorf("sched: silence break duration must be positive, got %v", seconds)
	}

	const (
		rate     = 48000
		channels = 2
		bits     = 16
	)
	blockAlign := channels * bits / 8
	frames := int(seconds * rate)
	dataBytes := frames * blockAlign

	buf := make([]byte, 44+dataBytes) // the payload stays zeroed: that is the silence
	le := binary.LittleEndian

	copy(buf[0:], "RIFF")
	le.PutUint32(buf[4:], uint32(36+dataBytes))
	copy(buf[8:], "WAVE")

	copy(buf[12:], "fmt ")
	le.PutUint32(buf[16:], 16) // PCM fmt chunk size
	le.PutUint16(buf[20:], 1)  // PCM
	le.PutUint16(buf[22:], channels)
	le.PutUint32(buf[24:], rate)
	le.PutUint32(buf[28:], rate*uint32(blockAlign))
	le.PutUint16(buf[32:], uint16(blockAlign))
	le.PutUint16(buf[34:], bits)

	copy(buf[36:], "data")
	le.PutUint32(buf[40:], uint32(dataBytes))

	path := filepath.Join(dir, fmt.Sprintf("silence-%dms.wav", int(seconds*1000)))
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", fmt.Errorf("sched: writing silence break: %w", err)
	}
	return path, nil
}
