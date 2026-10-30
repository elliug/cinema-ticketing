SHELL := /bin/bash

COMPOSE := $(shell if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version >/dev/null 2>&1; then echo "docker-compose"; fi)

.PHONY: install check api-test web-check deps-up deps-down up migrate migrate-down migrate-all seed-dev ensure-bucket bootstrap verify-migrations smoke verify-m0 verify-m1 verify-m2 verify-m3 verify-m35 verify-e2e

# The bootstrap command that owns the readiness wait, the development seed and the
# ticket bucket. It uses the same Docker Compose CLI detection as COMPOSE below.
BOOTSTRAP := go run ./apps/api/cmd/bootstrap

install:
	pnpm install --frozen-lockfile

api-test:
	go test ./apps/api/...
	go vet ./apps/api/...

web-check:
	pnpm check

check: api-test web-check

deps-up:
	@test -n "$(COMPOSE)" || (echo "Docker Compose is required" >&2; exit 1)
	$(COMPOSE) up -d --wait --wait-timeout 90 postgres redis floci

deps-down:
	@test -n "$(COMPOSE)" || (echo "Docker Compose is required" >&2; exit 1)
	$(COMPOSE) down

# deps-up waits for the container healthchecks; this waits for the services
# themselves and names the one that is not ready, so a slow dependency cannot
# leave the next target running against a half-started stack.
# Override the bounded wait with `make up WAIT_TIMEOUT=120s`.
up: deps-up
	$(BOOTSTRAP) wait-deps --timeout $${WAIT_TIMEOUT:-60s}

# Apply every migration that is not recorded yet and skip the rest, so it is safe
# on a fresh database and on one that is already up to date.
migrate-all: up
	@test -n "$(COMPOSE)" || (echo "Docker Compose is required" >&2; exit 1)
	$(COMPOSE) --profile tools run --rm migrate-all

# Development-only: the command refuses unless API_ENV=development is explicitly
# set, and the seed itself repeats the guard.
seed-dev: up
	$(BOOTSTRAP) seed-dev

ensure-bucket: up
	$(BOOTSTRAP) ensure-bucket

# The whole local environment in one re-runnable target.
# Checks the seed's precondition before migrating anything, so a forgotten
# API_ENV fails with one clear line instead of a database that is already
# migrated and a make error halfway through.
bootstrap:
	@test "$${API_ENV:-}" = "development" || (echo "make bootstrap needs API_ENV=development: it migrates the database and loads demo data" >&2; exit 1)
	$(MAKE) migrate-all
	$(MAKE) seed-dev
	$(MAKE) ensure-bucket

migrate: deps-up
	$(COMPOSE) --profile tools run --rm migrate

migrate-down: deps-up
	MIGRATION_DIRECTION=down $(COMPOSE) --profile tools run --rm migrate

verify-migrations: deps-up
	$(COMPOSE) --profile tools run --rm migration-verify

smoke:
	./scripts/verify_m0.sh

verify-m0: check verify-migrations smoke

verify-m1:
	./scripts/verify_m1.sh

verify-m2:
	./scripts/verify_m2.sh

# The milestone's exit evidence: against the real compose services, a hold is paid,
# the tickets answer their own detail, the signed code verifies, the PDF is stored in
# Floci and served by pre-signed link, the payment guards hold, and the artefact
# survives both a Floci restart and a Floci container recreation. It creates and
# removes its own database, Redis index, API port and bucket. It needs Docker Compose
# v2, Go, jq and openssl (checked with a named refusal, never a stack trace) and
# refuses to run when the Redis index it would flush already holds keys.
verify-m3:
	./scripts/verify_m3.sh

# The M3.5 exit evidence, in a browser: one Chromium drives the administration (create a
# film, schedule a screening against a published layout version, set its price and its
# status) and then the purchase (catalogue, seat map with the keyboard, hold, payment,
# ticket with its QR image and its PDF download), plus the ticket list and the negatives.
# It builds the web application once and serves that build — never `next dev` — and it
# creates and removes its own database, Redis index, ports and bucket, refusing by name
# to run against the development `cinema` database. It needs Docker Compose, Go, Node and
# Playwright's Chromium (`pnpm --dir apps/web exec playwright install --with-deps
# chromium`), all checked with a named refusal rather than a stack trace.
verify-m35:
	./scripts/verify_m35.sh

# The Playwright suite alone, against a web server and an API that are already running
# (`API_BASE_URL` defaults to http://127.0.0.1:8080). Useful while iterating on a scenario.
#
# `WEB_BASE_URL` must name a **non-loopback** address: `e2e/insecure-origin.spec.ts` fails on
# loopback by design, because a secure origin is precisely the blindness this gate exists to
# remove. Serve the web broadly and point it at the machine's own address:
#   WEB_BASE_URL=http://192.168.1.71:3000 make verify-e2e
#
# No `--project`: both configured engines are the gate's engines now, and naming one here is
# how the gate went blind to the other. The milestone's evidence remains `make verify-m35`,
# which owns the whole stack, the throwaway data and both browsers.
verify-e2e:
	pnpm --dir apps/web exec playwright test
