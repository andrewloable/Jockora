// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

package errs_test

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"testing"

	"github.com/andrewloable/jockora/internal/decode"
	"github.com/andrewloable/jockora/internal/enrich"
	"github.com/andrewloable/jockora/internal/errs"
)

// TestErrAllSixAreDistinct walks the six failure classes the hardening pass
// named. They live in the packages that raise them rather than being moved
// here, but nothing is worth anything unless errors.Is can tell them apart --
// a catch-all is how a full disk gets debugged as a decoder bug.
func TestErrAllSixAreDistinct(t *testing.T) {
	all := map[string]error{
		"decode-failed":      decode.ErrDecodeFailed,
		"unsupported-format": errs.ErrUnsupportedFormat,
		"tool-missing":       errs.ErrToolMissing,
		"segment-write":      errs.ErrSegmentWrite,
		"llm-empty":          enrich.ErrLLMEmpty,
		"llm-refusal":        enrich.ErrLLMRefusal,
	}
	if len(all) != 6 {
		t.Fatalf("%d classes, want 6", len(all))
	}

	for name, err := range all {
		wrapped := fmt.Errorf("while doing something: %w", err)
		if !errors.Is(wrapped, err) {
			t.Errorf("%s does not survive wrapping", name)
		}
		for otherName, other := range all {
			if otherName == name {
				continue
			}
			if errors.Is(wrapped, other) {
				t.Errorf("%s is indistinguishable from %s", name, otherName)
			}
		}
	}
}

// TestErrRefusalIsNotBadJSON. Collapsing a refusal into a parse failure sends
// you debugging the parser for a week when the real answer is that the persona
// prompt trips the model's guardrails on every single track.
func TestErrRefusalIsNotBadJSON(t *testing.T) {
	err := fmt.Errorf("track 12: %w", enrich.ErrLLMRefusal)
	if !errors.Is(err, enrich.ErrLLMRefusal) {
		t.Fatal("a refusal is not recognisable as one")
	}
	if errors.Is(err, enrich.ErrLLMBadJSON) {
		t.Error("a refusal is being reported as unparseable JSON")
	}
	if errors.Is(err, enrich.ErrLLMEmpty) {
		t.Error("a refusal is being reported as an empty response")
	}
}

// TestErrDiskFullIsRecognisedHoweverItArrives. ffmpeg writes the segments, so a
// full disk usually reaches this program as a line of another process's stderr,
// where the errno did not survive.
func TestErrDiskFullIsRecognisedHoweverItArrives(t *testing.T) {
	full := []error{
		syscall.ENOSPC,
		fmt.Errorf("writing segment: %w", syscall.ENOSPC),
		&fs.PathError{Op: "write", Path: "/tmp/segments/seg1.ts", Err: syscall.ENOSPC},
		errors.New("av_interleaved_write_frame(): No space left on device"),
		errors.New("Error writing trailer: Disk quota exceeded"),
		fmt.Errorf("%w: ENOSPC", errs.ErrSegmentWrite),
	}
	for _, err := range full {
		if !errs.IsDiskFull(err) {
			t.Errorf("IsDiskFull(%v) = false", err)
		}
	}

	notFull := []error{
		nil,
		errors.New("connection refused"),
		syscall.EACCES,
		decode.ErrDecodeFailed,
		errors.New("no such file or directory"),
	}
	for _, err := range notFull {
		if errs.IsDiskFull(err) {
			t.Errorf("IsDiskFull(%v) = true", err)
		}
	}
}
