# Manual licence checklist

`scripts/check-licences.sh` inspects Go modules only. The five components below
are invisible to it: four are separate processes and one is a browser asset.
None of them is linked into the Jockora binary, and that distinction is the whole
reason the commercial track survives.

This file exists so that a commercial licensee's lawyer finds a documented
finding rather than an accident.

## Why this matters at all

Plain AGPL-3.0 is perfectly happy with GPL dependencies. Selling a **commercial
exception** is not: to sublicense Jockora under other terms, Jockora must own or
be able to sublicense everything it ships. One GPL library *linked into the
binary* ends that quietly.

Allowed for anything linked: **MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause,
ISC**. Not LGPL. Not MPL. Both impose obligations a commercial exception cannot
absorb.

## The five components go-licenses cannot see

| Component | Licence | Relationship | Verdict |
|---|---|---|---|
| **ffmpeg / ffprobe** | GPL-2.0+ or LGPL-2.1+ depending on build flags | Separate process, invoked via `exec`, operator-supplied | **Safe.** Never vendored, never linked. Jockora exchanges bytes with it over pipes and command-line arguments, which is not a derivative work. |
| **hls.js** v1.5.17 | Apache-2.0 | Browser asset, served to the client, vendored at `web/vendor/` | **Safe and permissive anyway.** Its licence ships beside it at `web/vendor/hls.js.LICENSE`, as Apache-2.0 requires. |
| **Kokoro TTS** | Model weights **Apache-2.0** (hexgrad/Kokoro-82M); the `kokoro-onnx` runtime wrapper **MIT**; onnxruntime **MIT** | Python sidecar, separate process, spoken to over local HTTP; model files and the venv are operator-supplied, never vendored | **Safe, and permissive even if it were linked.** Recorded 2026-09-06 with 96e.1. Nothing here enters the Go binary: there is no cgo ONNX binding, deliberately, because one would end the static cross-compile as well as pull a second licence surface into the executable. |
| **llama.cpp / llama-server** | MIT | Separate service the operator runs and points Jockora at | **Safe.** MIT regardless, and out-of-process. |
| **librosa** and its stack (numpy, scipy, numba, llvmlite) | librosa **ISC**; numpy, scipy, soundfile **BSD-3-Clause**; numba, llvmlite **BSD-2-Clause** | Imported by the Python sidecar for vocal-onset analysis; separate process, operator-installed into its own venv | **Safe, and permissive anyway.** Recorded 2026-09-06 with 96e.9. Every one is on the allowed list even for linked code, and none of it is linked: the sidecar is reached over local HTTP and the audio it analyses is decoded by ffmpeg, not by this stack. |

## Container images are aggregation, not linking

A GHCR image that ships GPL ffmpeg alongside the AGPL-3.0-only Jockora binary is
**mere aggregation** under GPL §5: two independent works on one distribution
medium, not a combined work. The binary does not link ffmpeg, does not include
its headers, and runs correctly against any ffmpeg the operator supplies.

This is recorded deliberately. It is a documented finding, not something to be
discovered later by a licensee.

## Manual verifications the tooling could not make

- **modernc.org/mathutil** — `go-licenses` cannot classify its licence file at
  the default confidence threshold and reports it as unknown. Inspected by hand:
  it is **BSD-3-Clause**, carrying all three clauses including *"Neither the
  names of the authors nor the names of the contributors may be used to endorse
  or promote products derived from this software"*. The gate runs at
  `--confidence_threshold=0.8`, at which it is classified correctly.

- **github.com/andrewloable/jockora** — this repository's own licence is
  AGPL-3.0, which any GPL check correctly objects to. It is `--ignore`d: we are
  the copyright holder, and the gate is about what we link, not what we are.

- **github.com/hashicorp/golang-lru/v2 (MPL-2.0)** — appears in the *module
  graph* via the SQLite driver but is **not linked**: `go list -deps` reports
  zero hashicorp packages. This is why the gate uses `go-licenses check`, which
  walks the import graph, rather than anything based on `go list -m all`. Gating
  on the module graph would fail the build over a package that never ships.

## Adding a dependency

Run `bash scripts/check-licences.sh` **before** committing it. The gate is not
advisory.
