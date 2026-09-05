#!/usr/bin/env bash
# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only
#
# Fail the build if anything linked into the binary carries a licence Jockora
# cannot sublicense.
#
# This gate exists SOLELY for the commercial track. Plain AGPL is perfectly happy
# with GPL dependencies; selling a commercial exception is not. One GPL library
# linked in kills that track silently, and it would be discovered by a licensee
# rather than by us.
set -euo pipefail

# Exactly these. Not LGPL, not MPL: both impose obligations a commercial
# exception cannot absorb.
ALLOWED="MIT,Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC"

command -v go-licenses >/dev/null 2>&1 || {
    echo "installing go-licenses..."
    go install github.com/google/go-licenses@latest
    export PATH="$PATH:$(go env GOPATH)/bin"
}

echo "checking licences of everything linked into ./..."
echo "allowed: $ALLOWED"
echo

# `go-licenses check` walks the IMPORT graph, not the module graph. That
# distinction is load-bearing: `go list -m all` reports modules that are merely
# in go.mod and never reach the binary. Measured on this repo,
# github.com/hashicorp/golang-lru (MPL-2.0) appears in the module graph via the
# SQLite driver and is NOT linked. Gating on the module graph would fail the
# build over a package that does not ship.
# --ignore: this repository's OWN licence is AGPL-3.0, which every GPL check
#   correctly objects to. We are the copyright holder; the gate is about what we
#   link, not what we are.
#
# --confidence_threshold=0.8: at the 0.9 default the classifier fails to
#   recognise modernc.org/mathutil's licence at all and reports it as unknown.
#   That file was inspected by hand and is unambiguously BSD-3-Clause -- three
#   clauses including "Neither the names ... may be used to endorse" -- and it is
#   recorded in LICENCES-MANUAL.md. 0.8 still demands a strong match, and GPL
#   text bears no resemblance to BSD text, which the deliberate-failure test in
#   this task confirms.
if ! go-licenses check ./... \
        --allowed_licenses="$ALLOWED" \
        --ignore github.com/andrewloable/jockora \
        --confidence_threshold=0.8; then
    echo
    echo "LICENCE GATE FAILED."
    echo "Something linked into the binary is not MIT / Apache-2.0 / BSD / ISC."
    echo "Jockora is dual licensed, so it must be able to sublicense everything it"
    echo "ships. Remove the dependency, or run the tool out-of-process the way"
    echo "ffmpeg is run. See LICENCES-MANUAL.md."
    exit 1
fi

echo
echo "licence gate: PASS"
