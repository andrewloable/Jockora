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

# The four gates CI enforces, runnable by hand.
#
# check-licences-node.sh SKIPS when web/app/node_modules is absent, so this
# target still works on a machine with no Node -- the same reason `web` is not a
# dependency of anything. CI installs Node, so there it always runs.
gates:
	bash scripts/check-headers.sh
	bash scripts/check-licences.sh
	bash scripts/check-licences-node.sh
	bash scripts/check-crosscompile.sh

all: test vet gates

# web builds the browser app into web/dist, which the Go binary embeds.
#
# NOT a dependency of anything else. The Go build has to work on a machine with
# no Node installed -- that is what web/fallback.html is for -- so this is run
# deliberately, by whoever is changing the app.
.PHONY: web web-test
web:
	cd web/app && npm ci && npx ng build --configuration production
	# `ng build` CLEANS its output directory, which takes .gitkeep with it --
	# and .gitkeep is the only thing that makes `//go:embed all:dist` legal on a
	# tree that has never built the app. Without this, `make web` followed by a
	# clean of dist leaves a package that will not compile.
	touch web/dist/.gitkeep

# web-test runs the app's own tests with the 100% coverage thresholds that
# match the Go side's.
web-test:
	cd web/app && npx ng test --watch=false
