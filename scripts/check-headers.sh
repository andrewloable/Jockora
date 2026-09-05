#!/usr/bin/env bash
# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only
#
# Every Go source file must carry the two-line AGPL-3.0-only header.
#
# -only, not -or-later, so the terms the commercial exception is written against
# stay fixed. This is enforceable at file 3 and unenforceable at file 40, which
# is why it is automated now rather than later.
set -euo pipefail

WANT="SPDX-License-Identifier: AGPL-3.0-only"
missing=0

while IFS= read -r f; do
    if ! head -2 "$f" | grep -qF "$WANT"; then
        echo "MISSING HEADER: $f"
        missing=$((missing + 1))
    fi
done < <(find . -name '*.go' -not -path './vendor/*' -not -path './.git/*' | sort)

if [ "$missing" -gt 0 ]; then
    echo
    echo "HEADER GATE FAILED: $missing file(s) missing the two-line header:"
    echo "  // Copyright (C) 2026 Andrew Loable"
    echo "  // $WANT"
    exit 1
fi

echo "header gate: PASS (every .go file carries the AGPL-3.0-only header)"
