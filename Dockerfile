# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only

# --- build ------------------------------------------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src

# Dependencies first, so a source change does not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO_ENABLED=0 is not incidental: the SQLite driver is modernc.org/sqlite
# precisely so this binary is static and cross-compilable, and the CI
# cross-compile gate exists to keep it that way.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /jockora ./cmd/jockora

# --- runtime ----------------------------------------------------------------
#
# DEBIAN, NOT ALPINE, and the reason is onnxruntime.
#
# The speech sidecar needs kokoro-onnx, which depends on onnxruntime, and
# onnxruntime publishes manylinux wheels only. On Alpine's musl there is no
# wheel to install and pip falls back to building from source, which needs a
# full C++ toolchain and CMake in the image and takes tens of minutes. The
# binary is static (CGO_ENABLED=0) so it does not care which libc it lands on.
FROM debian:trixie-slim

# ffmpeg and ffprobe are OPERATOR-SUPPLIED DEPENDENCIES run OUT OF PROCESS.
# They are installed here, never vendored and never linked. An image shipping
# GPL ffmpeg alongside the AGPL-3.0-only binary is mere aggregation under GPL
# section 5, not a combined work; see LICENCES-MANUAL.md, where this is recorded
# as a deliberate finding rather than left to be discovered by a licensee.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ffmpeg ca-certificates tzdata python3 python3-pip python3-venv \
 && rm -rf /var/lib/apt/lists/* \
 && ffmpeg -hide_banner -filters | grep -q ' loudnorm ' \
 && ffmpeg -hide_banner -filters | grep -q ' aresample '

# The speech sidecar, in its own virtualenv.
#
# A SEPARATE PROCESS, supervised by the Go binary, exactly as it is on a
# workstation. It is in the image rather than operator-supplied because unlike
# ffmpeg it is not a general-purpose tool anyone already has, and unlike the
# MODELS it is small. kokoro-onnx is MIT and onnxruntime is MIT, so neither
# touches the dependency gate that keeps the commercial track alive.
RUN python3 -m venv /opt/tts \
 && /opt/tts/bin/pip install --no-cache-dir kokoro-onnx \
 && /opt/tts/bin/python -c "import kokoro_onnx"

# Runs unprivileged. The music library is mounted read-only anyway, but Jockora
# must never be able to write to it even by accident: that is an invariant, not
# a preference.
RUN useradd --uid 10001 --create-home --shell /usr/sbin/nologin jockora
USER jockora

COPY --from=build /jockora /usr/local/bin/jockora

# The sidecar script and the jocks travel WITH the binary. A persona card is
# part of the product, not operator configuration: shipping an image whose DJ
# directory is empty produces a station that plays music and never speaks, which
# is exactly the failure this deployment already had once.
COPY sidecar/ /app/sidecar/
COPY personas/ /app/personas/
WORKDIR /app

# Segments are transient and rewritten constantly; keeping them out of any
# bind mount avoids pointless disk churn.
# The MODELS are not here on purpose. Kokoro's weights are 337MB and are
# operator-supplied, mounted read-only, for the same reason ffmpeg is not
# vendored: shipping someone else's artefacts inside this image is a licensing
# question nobody needs to answer to run a radio station.
ENV JOCKORA_SEGMENT_DIR=/tmp/segments \
    JOCKORA_LISTEN_ADDR=0.0.0.0:8080 \
    JOCKORA_DB_PATH=/config/jockora.db \
    JOCKORA_TTS_PYTHON=/opt/tts/bin/python \
    JOCKORA_TTS_SCRIPT=/app/sidecar/kokoro_server.py \
    JOCKORA_KOKORO_MODEL=/models/kokoro-v1.0.onnx \
    JOCKORA_KOKORO_VOICES=/models/voices-v1.0.bin \
    JOCKORA_PERSONA=/app/personas

EXPOSE 8080

# doctor runs automatically at startup and refuses to serve on a failed hard
# check, so a broken deployment says why instead of going quiet.
ENTRYPOINT ["/usr/local/bin/jockora"]
