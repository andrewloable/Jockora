# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only
#
# GATE 2 harness image. Built FROM the shipping image, so the ffmpeg under test
# is the one the product actually runs, then given the shell tooling the harness
# needs and the runtime image deliberately does not carry.
#
# Running the gate in a container is not merely convenient. The harness kills
# processes by name, and on the host a containerised Jockora is visible in the
# same PID namespace -- so an isolated namespace makes it STRUCTURALLY unable to
# touch a live deployment, instead of relying on a carefully worded pkill.
ARG JOCKORA_IMAGE=jockora:latest
FROM ${JOCKORA_IMAGE}
USER root
RUN apk add --no-cache bash curl python3 iproute2 procps
ENTRYPOINT ["/bin/bash"]
