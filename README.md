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

## M1 local administration

M1 adds cinemas, rooms, immutable versioned layouts, films, and events with
materialized event seats. Its administrative API is **development-only and
disabled by default**. Enable it explicitly:

```sh
API_ENV=development \
API_LOCAL_ADMIN=true \
API_LOCAL_ADMIN_TOKEN=local-dev-token \
API_DATABASE_URL='postgres://cinema:cinema_local_only@127.0.0.1:5432/cinema?sslmode=disable' \
go run ./apps/api/cmd/api
```

Requests must then come from a loopback peer and carry
`Authorization: Bearer local-dev-token`. Loading fails if local administration
is enabled outside `API_ENV=development` or without a token and a database URL,
so it cannot be switched on by accident in production. This is a development
guard, not production authentication.

The administrative contract lives under `/api/v1/admin`:

| Method | Route | Purpose |
|---|---|---|
| `POST` | `/cinemas` | Create a cinema |
| `GET` | `/cinemas` | List cinemas |
| `POST` | `/rooms` | Create a room for a cinema |
| `GET` | `/cinemas/{cinemaId}/rooms` | List a cinema's rooms |
| `GET` | `/rooms/{roomId}/layouts` | List a room's layout versions |
| `POST` | `/layouts` | Create the next draft layout version for a room |
| `PUT` | `/layouts/{layoutId}/rows` | Replace a draft layout geometry; rows may hold different seat counts |
| `POST` | `/layouts/{layoutId}/publish` | Freeze a draft layout, making it immutable |
| `POST` | `/layouts/{layoutId}/clone` | Copy a published layout into a new draft version |
| `GET` | `/layouts/{layoutId}` | Read a layout version with its rows and seats |
| `POST` | `/films` | Create a catalog entry |
| `POST` | `/events` | Schedule an event and materialize its event seats |
| `GET` | `/events/{eventId}/seats` | Read the materialized event seats |

Errors use RFC 9457 problem details with a stable `code` (`invalid_input`, `invalid_request`, `not_found`, `conflict`, `internal_error`) and a human-safe `detail`.

Apply and verify the M1 migration:

```sh
MIGRATION_VERSION=0002_venue_layout docker compose --profile tools run --rm migrate
make verify-migrations
./scripts/verify_m1.sh
```

`scripts/verify_m1.sh` runs the built API against a temporary database and proves
the guard, irregular layout publication, and event seat materialization through
HTTP only.

## M0 boundaries

M0 intentionally contains no cinema, room, layout, event, seat, reservation, payment, ticket, identity, or reporting behavior. The M1 migration adds the `cinema` schema; reservation, payment, ticket, identity, and reporting behavior remain absent. The tagged test in `tests/concurrency` records M2's double-booking acceptance contract and remains RED until the reservation API exists:

```sh
go test -tags=acceptance_m2 ./tests/concurrency
```

See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the full design and [`odd/tasks/m0-foundation.md`](odd/tasks/m0-foundation.md) for M0 evidence and [`odd/tasks/m1-venue-layout.md`](odd/tasks/m1-venue-layout.md) for M1 evidence.
