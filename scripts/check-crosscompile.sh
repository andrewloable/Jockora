#!/usr/bin/env bash
# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only
#
# The distribution plan claims one machine can build for every target. A single
# cgo dependency breaks that silently, so it is proven on every build instead of
# assumed.
set -euo pipefail

failed=0
for target in "linux/amd64" "linux/arm64" "darwin/arm64"; do
    GOOS="${target%/*}"
    GOARCH="${target#*/}"
    printf "  CGO_ENABLED=0 %-14s " "$target"
    if CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -o /dev/null ./... 2>/tmp/cc.err; then
        echo "ok"
    else
        echo "FAILED"
        sed 's/^/      /' /tmp/cc.err
        failed=$((failed + 1))
    fi
done

if [ "$failed" -gt 0 ]; then
    echo
    echo "CROSS-COMPILE GATE FAILED for $failed target(s)."
    echo "Something now requires cgo. The SQLite driver is modernc.org/sqlite"
    echo "precisely to avoid this; check what was added."
    exit 1
fi

echo "cross-compile gate: PASS (all three targets build with CGO_ENABLED=0)"
