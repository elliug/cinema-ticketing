SHELL := /bin/bash

COMPOSE := $(shell if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif docker-compose version >/dev/null 2>&1; then echo "docker-compose"; fi)

.PHONY: install check api-test web-check deps-up deps-down migrate migrate-down verify-migrations smoke verify-m0 verify-m1

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
	$(COMPOSE) up -d postgres redis

deps-down:
	@test -n "$(COMPOSE)" || (echo "Docker Compose is required" >&2; exit 1)
	$(COMPOSE) down

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
