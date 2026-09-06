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
FROM alpine:3.21

# ffmpeg and ffprobe are OPERATOR-SUPPLIED DEPENDENCIES run OUT OF PROCESS.
# They are installed here, never vendored and never linked. An image shipping
# GPL ffmpeg alongside the AGPL-3.0-only binary is mere aggregation under GPL
# section 5, not a combined work; see LICENCES-MANUAL.md, where this is recorded
# as a deliberate finding rather than left to be discovered by a licensee.
RUN apk add --no-cache ffmpeg ca-certificates tzdata \
 && ffmpeg -hide_banner -filters | grep -q ' loudnorm ' \
 && ffmpeg -hide_banner -filters | grep -q ' aresample '

# Runs unprivileged. The music library is mounted read-only anyway, but Jockora
# must never be able to write to it even by accident: that is an invariant, not
# a preference.
RUN adduser -D -u 10001 jockora
USER jockora

COPY --from=build /jockora /usr/local/bin/jockora

# Segments are transient and rewritten constantly; keeping them out of any
# bind mount avoids pointless disk churn.
ENV JOCKORA_SEGMENT_DIR=/tmp/segments \
    JOCKORA_LISTEN_ADDR=0.0.0.0:8080 \
    JOCKORA_DB_PATH=/config/jockora.db

EXPOSE 8080

# doctor runs automatically at startup and refuses to serve on a failed hard
# check, so a broken deployment says why instead of going quiet.
ENTRYPOINT ["/usr/local/bin/jockora"]
