# Cinema ticketing foundation

M0 provides a runnable monorepo baseline for the cinema ticketing system: a Go modular monolith, a Next.js web application, PostgreSQL, Redis, reversible migrations, and CI checks. Product behavior starts in M1; reservations and concurrency guarantees in M2; a guest buys a ticket through a simulated payment and receives it as a PDF in M3.

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

Start dependencies and apply the migrations:

```sh
make up
make migrate-all
```

`make deps-up` starts the three local services: PostgreSQL (`postgres:17.6-alpine`, port 5432), Redis (`redis:7.4.6-alpine`, port 6379) and Floci (`floci/floci`, pinned by digest because CI starts it on every run, port 4566), the local S3 that stores the M3 ticket PDFs and answers path-style requests at `http://127.0.0.1:4566` with the dummy `floci`/`floci` credentials. `make up` then waits for the services themselves within a bounded timeout: PostgreSQL with `pg_isready` inside its container, Redis with `PING`, and Floci with `GET http://127.0.0.1:4566/_floci/health`. A service that does not answer fails the target and is named (`postgres is not ready after 1m0s: …`); the wait only reads and never touches the database, and its budget can be changed with `make up WAIT_TIMEOUT=120s`. Redis runs with `--maxmemory-policy noeviction`, because an eviction policy could drop a hold key before its TTL and let a second buyer hold the same seat. Floci's image carries its own healthcheck (a shell probe of `/_floci/init`), so `make deps-up` waits for all three containers; because the image has neither `curl` nor `wget`, scripts that need an explicit check poll `http://127.0.0.1:4566/_floci/health` from the host. The API's object-storage settings therefore default to that service (`API_S3_ENDPOINT=http://127.0.0.1:4566`, `API_S3_BUCKET=cinema-m3-tickets`, `API_S3_REGION=us-east-1`, `API_S3_PRESIGN_TTL=5m`), and a non-local endpoint is refused while the development credentials are in place.

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
| Floci | `GET http://localhost:4566/_floci/health` | Local S3 readiness, probed from the host |
| Web | `GET http://localhost:3000/api/health` | Web runtime health contract |

## Configuring an environment that is not development

Nothing below is needed locally: `API_ENV=development` supplies documented defaults,
including the ticket QR signing key. A deployment other people can reach must set them
explicitly, because the defaults either assume the local services or are refused once
`API_ENV` is anything else.

| Variable | Effect outside development |
|---|---|
| `API_ENV` | The only value that enables the development defaults and the local administrative surface. Leave it unset and the process behaves like a real environment, without the defaults. |
| `API_DATABASE_URL` | Without it no customer surface is mounted at all: no holds, no payment, no tickets. |
| `API_TICKET_QR_KEY` | **Required.** At least 32 bytes of entropy-bearing text. The documented development key is refused, and with the ticket schema present the process **exits with an error** instead of serving tickets it cannot sign. |
| `API_S3_ENDPOINT`, `API_S3_BUCKET`, `API_S3_REGION`, `API_S3_ACCESS_KEY_ID`, `API_S3_SECRET_ACCESS_KEY` | Where the ticket PDFs live. The dummy `floci` credentials are refused for a non-local endpoint, and without storage the download route is not mounted. |
| `API_S3_PRESIGN_TTL` | Download-link lifetime (`5m` by default, capped at `168h`): a longer link is a longer-lived credential. |
| `API_REDIS_URL`, `API_HOLD_TTL`, `API_EXPIRY_INTERVAL`, `API_EXPIRY_BATCH` | Coordination and the expiry sweep; the defaults point at the local services. |
| `API_LOCAL_ADMIN`, `API_LOCAL_ADMIN_TOKEN` | Leave administration off in a deployment; when it is enabled, the development token is refused. |

Generate the signing key with real entropy and keep it out of the repository (`.env`
is ignored; the value belongs in the deployment's secret store):

```sh
openssl rand -base64 48
```

Three properties worth knowing before the first deploy:

- **A key that cannot sign stops the process.** With the ticket schema present, a
  missing or short key is a startup failure naming the variable, not a degraded run with
  half of the ticket surface missing.
- **Rotating the key invalidates every code already printed.** The QR token carries no
  key identifier, so there is no overlap period: a ticket issued before the change stops
  verifying until its detail is served again, which mints a new code. Treat rotation as
  a planned act with a decision about the tickets already sold.
- **The stored PDF is an immutable artefact.** It is written once when the payment is
  confirmed and never re-rendered, so it keeps describing the sale even if the screening
  is later cancelled or the ticket voided. Validity is answered by the ticket page and by
  the door — the server-side verification of the QR — never by the PDF.

## Repository map

| Path | Responsibility |
|---|---|
| `apps/api` | Go entrypoint, platform HTTP/config/health code, and module boundaries |
| `apps/web` | Next.js App Router application and web health contract |
| `db/migrations` | Reversible PostgreSQL migrations |
| `db/seeds` | Development-only seed data |
| `apps/api/cmd/bootstrap` | Readiness wait, development seed and ticket bucket creation |
| `scripts` | Migration and M0 smoke verification |
| `odd/tasks` | ODD specifications, task state, and evidence |
| `ARCHITECTURE.md` | Approved system architecture and milestone scope |

## Common commands

```sh
make check              # Go and web checks
make verify-migrations  # isolated PostgreSQL up/down/up proof
make smoke              # API + web health smoke test (dependencies must be up)
make migrate-all        # apply every migration that is not recorded yet
make seed-dev           # development seed rows; requires API_ENV=development
make ensure-bucket      # create the configured ticket bucket when it is missing
make bootstrap          # migrate-all, then seed-dev, then ensure-bucket
make verify-m3          # M3 gate end to end: hold, pay, ticket, code, PDF from the bucket
make migrate-down       # explicitly roll back the development schema
make deps-down          # stop local containers without deleting volumes
```

`Makefile` automatically uses either `docker compose` or `docker-compose`.

## Bootstrap

The whole local environment is two commands, and both are re-runnable:

```sh
make up
export API_ENV=development
make bootstrap
```

`make bootstrap` runs `migrate-all`, then `seed-dev`, then `ensure-bucket`:

- `migrate-all` reads `platform.schema_migrations`, compares it with
  `db/migrations/*.up.sql` in version order and applies only the missing ones, so
  a second run applies nothing. It works on a database with no schema yet, where
  `0001_init` creates the table.
- `seed-dev` applies `db/seeds/dev_seed.sql`: one cinema (`Cine Prueba`), one room
  (`Sala 1`) with one published irregular layout (row A with three seats, row B
  with two and one of them disabled), two films and two sessions, which
  materialize the four enabled seats as event seats. Every row has a fixed
  identifier and `ON CONFLICT DO NOTHING`, so a second run changes no row count.
  The layout is inserted as a draft, filled and then published, because the M1
  guard refuses a layout inserted as published and freezes the geometry of a
  published one. There is no price in the seed: the per-event price arrives with
  the checkout schema.
- `ensure-bucket` loads the validated configuration, creates the bucket when it is
  missing and prints its name. It never prints the credentials, and it exits
  non-zero when the object storage is not configured instead of pretending to
  have a bucket.

The seed is **development-only**. `make seed-dev` refuses to run unless
`API_ENV=development` is explicitly set, and the seed file repeats the guard so
that applying it with `psql` directly cannot seed a database by accident. Never
run it against a shared or production database.

The idempotence of both the migration runner and the seed is proven against a
throwaway database that each script creates and drops:

```sh
./tests/bootstrap/migrate_all.sh
./tests/bootstrap/seed_idempotence.sh
```

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

## M3 checkout and ticket delivery

A guest, with no account, turns a hold into a ticket they can show and print.

The buyer holds seats (`POST /api/v1/reservations/holds`), pays
(`POST /api/v1/reservations/{id}/payment`, idempotent by an `Idempotency-Key` header),
and gets one ticket per seat — `GET /api/v1/tickets/{id}` for the detail,
`GET /api/v1/tickets/{id}/qr.svg` for the code, `GET /api/v1/tickets/{id}/pdf` for a
redirect to a short-lived pre-signed URL of the stored PDF. The web app walks the same
path: `/reservations/{id}` to pay, `/tickets/{id}` to show the ticket.

Three things are worth knowing before relying on it:

- **The payment key belongs to the reservation.** A reservation with an open payment
  attempt can only be settled by the request holding its key, so the browser keeps it and
  a page reload resumes rather than starting over. A retry with a different key is answered
  as a conflict, which is the honest answer: a payment is already under way.
- **The PDF is a record of the sale, not a certificate.** It is written once, when the
  payment is captured, and never re-rendered, so a later cancellation or a voided ticket
  changes what the page and the door say — not the document.
- **The code is signed, not encrypted.** It carries the ticket, event and seat identifiers
  and nothing personal; anyone holding it can read those and fetch the detail. What it
  proves is provenance: a forged, tampered or unknown code is refused server side, and
  single use is the next milestone's work.

`make verify-m3` proves the milestone against the running stack and runs in CI as its own
job: it holds seats, pays, fetches the ticket, verifies the code, downloads the PDF through
its redirect, and shows the artefact survives a Floci restart and a container recreation.
Two gates share Redis logical database 9 by default (`verify-m2`, `verify-m3`) and refuse
to run concurrently rather than flushing each other.

## M0 boundaries

M0 intentionally contained no cinema, room, layout, event, seat, reservation, payment, ticket, identity, or reporting behavior. The M1 migration added the `cinema` schema; the M2 migrations added durable holds with per-seat exclusivity and expiry; the M3 migrations added the price per event, the payment attempt, the sold ticket, the outbox and the guard that allows one open payment attempt per reservation. Identity and reporting remain absent, and so does single-use redemption: a ticket is issued and can be verified, but nothing yet marks it as used at a door. The tagged acceptance test for the double-booking contract still runs as its own proof:

```sh
go test -tags=acceptance_m2 ./tests/concurrency
```

See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the full design and the milestone table, and the records in [`odd/tasks/`](odd/tasks): `m0-foundation.md`, `m1-venue-layout.md`, `m2-reservation-core.md` and `m3-checkout-tickets.md`.
