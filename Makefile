# Copyright (C) 2026 Andrew Loable
# SPDX-License-Identifier: AGPL-3.0-only

.PHONY: test test-race vet gates all

# The ordinary suite. Fast enough to run on every save.
test:
	go test ./...

# The race detector, which is the only tool that can see what fake-clock tests
# cannot: this program runs a dozen concurrent goroutines -- the mixer, one
# decoder per track plus the next, the break generator, the scheduler, the LLM
# client, the TTS supervisor, two stderr drainers, the encoder supervisor, and
# net/http's goroutine per connection -- and a fake clock proves timing, not the
# absence of a data race between any two of them.
#
# NEVER in production. It is a test-time tool and it costs several times the CPU.
test-race:
	go test -race ./...

vet:
	go vet ./...

# The three gates CI enforces, runnable by hand.
gates:
	bash scripts/check-headers.sh
	bash scripts/check-licences.sh
	bash scripts/check-crosscompile.sh

all: test vet gates
