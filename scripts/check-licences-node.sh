#!/usr/bin/env bash
# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only
#
# The npm half of the dependency gate. scripts/check-licences.sh is go-licenses;
# this is its opposite number for the browser app.
#
# WHY THE ANGULAR BUILD IS INSIDE THE GATE: the bundle is embedded in the binary
# and served to every listener, so a package that reaches it ships exactly as
# surely as a linked Go module does. Plain AGPL would not care; selling a
# commercial exception does, and one GPL package would kill that track silently.
set -euo pipefail

APP="${JOCKORA_WEB_APP:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/web/app}"

# The same five as the Go gate, plus three that only appear in the npm world and
# are all strictly more permissive than MIT: 0BSD, CC0-1.0 and Unlicense are
# public-domain-equivalent grants. Not LGPL, not MPL: both impose obligations a
# commercial exception cannot absorb.
ALLOWED='MIT;Apache-2.0;BSD-2-Clause;BSD-3-Clause;ISC;0BSD;CC0-1.0;Unlicense'

if [ ! -d "$APP/node_modules" ]; then
    echo "node licence gate: SKIP ($APP/node_modules is absent; run npm ci first)"
    exit 0
fi

echo "checking licences of everything npm puts in the bundle"
echo "allowed: $ALLOWED"
echo

# --production: devDependencies do not ship. The Angular CLI, Vitest and the
#   TypeScript compiler are build tools, exactly as go-licenses walks the import
#   graph rather than the module graph.
# A package with NO licence field is reported as UNKNOWN, which is not in the
#   allowed list, so --onlyAllow rejects it: a package whose terms nobody has
#   read is the same risk as one with bad terms. (--failOn cannot be combined
#   with --onlyAllow -- the tool refuses outright -- and is not needed here; the
#   deliberate-failure tests in test/licence_node_test.go prove both cases.)
# The checker itself is MIT; it is fetched by npx and does not ship. See
# LICENCES-MANUAL.md.
# --summary keeps a passing run to three lines. A rejection still names the
#   offending package, which is the only output anybody needs to act on -- the
#   deliberate-failure tests assert exactly that.
# --excludePrivatePackages skips THIS package, which is AGPL-3.0-only because
#   it is ours -- the same exemption the Go gate spells as
#   `--ignore github.com/andrewloable/jockora`. The gate is about what we link,
#   not what we are. Nothing published to npm can be private, so this cannot
#   hide a third-party dependency.
if ! (cd "$APP" && npx --yes license-checker-rseidelsohn@4 \
        --production \
        --excludePrivatePackages \
        --summary \
        --onlyAllow "$ALLOWED"); then
    echo
    echo "NODE LICENCE GATE FAILED."
    echo "Something npm puts in the browser bundle is not MIT / Apache-2.0 / BSD /"
    echo "ISC / 0BSD / CC0 / Unlicense, or carries no licence at all. Jockora is"
    echo "dual licensed, so it must be able to sublicense everything it ships --"
    echo "and the bundle is embedded in the binary. Remove the dependency, or"
    echo "load it at runtime from outside the build. See LICENCES-MANUAL.md."
    exit 1
fi

echo
echo "node licence gate: PASS"
