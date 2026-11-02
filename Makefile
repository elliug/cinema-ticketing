SHELL := /bin/bash

COMPOSE := $(shell if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version >/dev/null 2>&1; then echo "docker-compose"; fi)

.PHONY: install check api-test web-check deps-up deps-down mail-up up migrate migrate-down migrate-all seed-dev ensure-bucket bootstrap verify-migrations smoke verify-m0 verify-m1 verify-m2 verify-m3 verify-m35 verify-m41 verify-email verify-identity verify-e2e

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

# The local mail server the ticket email is delivered to (Mailpit: SMTP on 1025, inbox on
# http://127.0.0.1:8025). It is opt-in and deliberately absent from deps-up: the ticket
# email never blocks a sale (S5), so no verification target may require a mail server.
# Point the API at it with API_SMTP_ADDR=127.0.0.1:1025 and API_EMAIL_FROM=<any mailbox>.
#
# Mailpit is the only container behind a compose profile, and that makes it the only one a
# `deps-down` followed by `up` can leave behind holding a network that no longer exists:
# compose then tries to *start* the old container instead of recreating it and the daemon
# answers "failed to set up container networking: network … not found". The repair is to
# recreate it, and the inbox that costs is disposable — so the target does that itself
# rather than making the reader learn a Docker detail to see their mail.
mail-up:
	@test -n "$(COMPOSE)" || (echo "Docker Compose is required" >&2; exit 1)
	$(COMPOSE) --profile mail up -d --wait --wait-timeout 90 mailpit || \
	  { echo "mail-up: the container could not join the project network; recreating it" >&2; \
	    $(COMPOSE) rm -sf mailpit >/dev/null 2>&1 || true; \
	    $(COMPOSE) --profile mail up -d --wait --wait-timeout 90 mailpit; }

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

# The M4.1 exit evidence: the door, over HTTP and never through a screen. One fresh stack
# at the door's own migration: the whole fixture created through the administrative API
# (a screening on sale at a price in one request), a real sale, the first admission, the
# second presentation answering the first instant instead of an error, a forged code
# refused exactly as verification refuses it and writing no row, the sale and its PDF
# unchanged, and two doors racing one ticket with exactly one winner. It creates and
# removes its own database, Redis index (11 by default), API port and bucket, and it
# needs Docker Compose, Go, jq, openssl and curl, each with a named refusal.
verify-m41:
	./scripts/verify_m41.sh

# The ticket email exit evidence: the other half of what the gates above can prove. They all
# run with **no mail provider**, which shows only that a sale does not need one (S5); this one
# starts Mailpit and shows the direction nobody had proven — a sale paid with an address is
# mailed exactly one message carrying a signed /t/<token> link, that token opens the order's
# own tickets, two ticks later the mailbox still holds one message while the `ticket.issued`
# outbox row is already published by the PDF worker (the failure S4's own delivery record
# prevents), and the resend route mails a second address without storing it until the fourth
# send inside the hour is refused. It creates and removes its own database, Redis index (12 by
# default), API port and bucket; it starts Mailpit on purpose and stops it again only when
# this run is what started it, so a Mailpit already running is left alone. It needs Docker
# Compose, Go, jq and curl, each checked with a named refusal, and it needs no mail provider
# to be configured for any other target.
verify-email:
	./scripts/verify_email.sh

# The identity exit evidence: the login, proven end to end through the public surface on its own
# stack. A sale is paid with an address and belongs to no account; asking for a sign-in link
# answers 202 and exactly one message to that address carries `<origin>/in/<token>`; that token
# redeems **once** into a session whose row holds its SHA-256 — the property S1 exists for — and
# claims the order the address was used for (D5); GET /me/tickets with the token answers exactly
# that order's tickets, while no token, an unissued token, a spent link and a revoked session all
# answer the login route's one opaque 401; three links per address are allowed and the next four
# answer the same 202 while the mailbox does not grow (S6). There is no sign-out route yet, so
# revocation is proven as the predicate the session row decides and not as a flow a person can
# perform — the gate says so in its own summary. It creates and removes its own database, Redis
# index (13 by default), API port and bucket; it starts Mailpit on purpose, because the login is
# mail, and stops it again only when this run is what started it. It needs Docker Compose, Go, jq
# and curl, each checked with a named refusal.
verify-identity:
	./scripts/verify_identity.sh

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
