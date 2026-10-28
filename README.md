# Cinema ticketing foundation

M0 provides a runnable monorepo baseline for the cinema ticketing system: a Go modular monolith, a Next.js web application, PostgreSQL, Redis, reversible migrations, and CI checks. Product behavior starts in M1; reservations and concurrency guarantees start in M2.

## Quick path

Prerequisites: Go 1.27+, Node.js 22+, pnpm 12+, Docker Engine, Docker Compose, and `curl`.

```sh
pnpm install
make deps-up
make migrate
make verify-m0
```

Expected result: Go tests and vet pass, the web tests/lint/typecheck/build pass, migrations pass an isolated up/down/up cycle, and both health endpoints return their exact contracts.

## Run locally

Start dependencies and apply the migration:

```sh
make deps-up
make migrate
```

Run the API:

```sh
API_DEPENDENCIES=postgresql,redis go run ./apps/api/cmd/api
```

Run the web application in another terminal:

```sh
pnpm web:dev
```

Open `http://localhost:3000`. Health endpoints:

| Service | Endpoint | Purpose |
|---|---|---|
| API | `GET http://localhost:8080/health/live` | Process liveness |
| API | `GET http://localhost:8080/health/ready` | PostgreSQL and Redis TCP readiness |
| Web | `GET http://localhost:3000/api/health` | Web runtime health contract |

## Repository map

| Path | Responsibility |
|---|---|
| `apps/api` | Go entrypoint, platform HTTP/config/health code, and module boundaries |
| `apps/web` | Next.js App Router application and web health contract |
| `db/migrations` | Reversible PostgreSQL migrations |
| `scripts` | Migration and M0 smoke verification |
| `odd/tasks` | ODD specifications, task state, and evidence |
| `ARCHITECTURE.md` | Approved system architecture and milestone scope |

## Common commands

```sh
make check              # Go and web checks
make verify-migrations  # isolated PostgreSQL up/down/up proof
make smoke              # API + web health smoke test (dependencies must be up)
make migrate-down       # explicitly roll back the development schema
make deps-down          # stop local containers without deleting volumes
```

`Makefile` automatically uses either `docker compose` or `docker-compose`.

## M0 boundaries

M0 intentionally contains no cinema, room, layout, event, seat, reservation, payment, ticket, identity, or reporting behavior. The tagged test in `tests/concurrency` records M2's double-booking acceptance contract and remains RED until the reservation API exists:

```sh
go test -tags=acceptance_m2 ./tests/concurrency
```

See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the full design and [`odd/tasks/m0-foundation.md`](odd/tasks/m0-foundation.md) for implementation evidence.
