# Cinema ticketing foundation

M0 provides a runnable monorepo baseline for the cinema ticketing system: a Go modular monolith, a Next.js web application, PostgreSQL, Redis, reversible migrations, and CI checks. On top of it, four milestones are delivered: **M1** venues, rooms and versioned layouts; **M2** durable seat holds with per-seat exclusivity and expiry; **M3** the purchase path — a guest holds seats, pays through a simulated provider, and receives a ticket with a signed code and a PDF stored in S3; **M3.5** the product web — catalogue, keyboard-operable seat map, checkout, tickets, and an administration surface that can price and publish a screening.

Identity, single-use redemption, reporting and audit do not exist yet. `## What this build does not do` lists them, with what each one currently costs.

## Quick path

Prerequisites: Go 1.27+, Node.js 22+, pnpm 12+, Docker Engine, Docker Compose, and `curl`.

To run the product and click through a purchase, follow [`## Run locally`](#run-locally): four commands, two of them long-running.

To prove the build instead, without reading anything else:

```sh
pnpm install
make deps-up
make migrate
make check
```

Expected result: Go tests and vet pass, and the web tests, lint, typecheck and build pass. The four milestones also have their own end-to-end gates — `make verify-m1`, `make verify-m2`, `make verify-m3` and `make verify-m35` — which start their own stack and prove the behaviour rather than the compilation.

## Run locally

Four commands. The first two finish; the last two stay running, each in its own terminal.

```sh
make up
API_ENV=development make bootstrap
```

`make up` creates the containers and their volumes and waits until all three are healthy. `make bootstrap` applies the six migrations, seeds the development data and creates the ticket bucket. Both are re-runnable: a second run changes nothing.

`make deps-up` starts the three local services: PostgreSQL (`postgres:17.6-alpine`, port 5432), Redis (`redis:7.4.6-alpine`, port 6379) and Floci (`floci/floci`, pinned by digest because CI starts it on every run, port 4566), the local S3 that stores the M3 ticket PDFs and answers path-style requests at `http://127.0.0.1:4566` with the dummy `floci`/`floci` credentials. `make up` then waits for the services themselves within a bounded timeout: PostgreSQL with `pg_isready` inside its container, Redis with `PING`, and Floci with `GET http://127.0.0.1:4566/_floci/health`. A service that does not answer fails the target and is named (`postgres is not ready after 1m0s: …`); the wait only reads and never touches the database, and its budget can be changed with `make up WAIT_TIMEOUT=120s`. Redis runs with `--maxmemory-policy noeviction`, because an eviction policy could drop a hold key before its TTL and let a second buyer hold the same seat. Floci's image carries its own healthcheck (a shell probe of `/_floci/init`), so `make deps-up` waits for all three containers; because the image has neither `curl` nor `wget`, scripts that need an explicit check poll `http://127.0.0.1:4566/_floci/health` from the host. The API's object-storage settings therefore default to that service (`API_S3_ENDPOINT=http://127.0.0.1:4566`, `API_S3_BUCKET=cinema-m3-tickets`, `API_S3_REGION=us-east-1`, `API_S3_PRESIGN_TTL=5m`), and a non-local endpoint is refused while the development credentials are in place.

Run the API in the first terminal. Every variable below matters, and the next section says what breaks without each one:

```sh
API_ENV=development \
API_DATABASE_URL="postgres://cinema:cinema_local_only@127.0.0.1:5432/cinema?sslmode=disable" \
API_DEPENDENCIES=postgresql,redis \
API_LOCAL_ADMIN=true \
API_LOCAL_ADMIN_TOKEN=change_me_in_local \
   go run ./apps/api/cmd/api
```

Run the web application in the second terminal, with **the same token**:

```sh
API_LOCAL_ADMIN_TOKEN=change_me_in_local pnpm web:dev
```

Open `http://localhost:3000` to buy, and `http://localhost:3000/admin/venues` to administer. `change_me_in_local` is a placeholder you choose: any value works locally, as long as **both** processes are started with the same one.

Health endpoints:

| Service | Endpoint | Purpose |
|---|---|---|
| API | `GET http://localhost:8080/health/live` | Process liveness |
| API | `GET http://localhost:8080/health/ready` | PostgreSQL and Redis TCP readiness |
| Floci | `GET http://localhost:4566/_floci/health` | Local S3 readiness, probed from the host |
| Web | `GET http://localhost:3000/api/health` | Web runtime health contract |

### Settings that fail quietly

Two settings fail at startup, and both failures look like a product that answers nothing rather than a process that was started wrong.

- **`API_DATABASE_URL` is not optional.** Without it the API mounts no customer surface at all: `/api/v1/events`, holds, payments and tickets all answer `404`, and the process still reports healthy. If the catalogue is empty and every route 404s, read the API's startup log for the mounted surfaces before suspecting the code.
- **The token must match on both sides.** `API_LOCAL_ADMIN=true` mounts the administrative surface on the API; the web reaches it with `API_LOCAL_ADMIN_TOKEN`, which must equal the API's. When it does not, the administrative guard answers `404` — not `401` — so a caller cannot tell a missing surface from a wrong token. The administration screens name both causes when they see that `404`; the fix is to restart whichever process has the other value.

A third one fails later, when the first cinema is created: a cinema's timezone must be an **IANA name** such as `Europe/Madrid`, and the API refuses anything else instead of storing a name it could never render. See [`## M1 local administration`](#m1-local-administration) for the rule and its exact message.

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
make verify-m35         # M3.5 gate in a real browser: administration, purchase, tickets
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
  published one. Both sessions carry a price, because the catalogue a stranger
  reads prints one: `12.50` and `11.50`. Change either with
  `PATCH /api/v1/admin/events/{eventId}` or on the administration screen.
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

A cinema's `timezone` must be an **IANA time zone name** (`Europe/Madrid`,
`America/New_York`, `UTC`), not a city, a country or an abbreviation. It is the
zone the venue's own clock is rendered from — the ticket detail prints the
screening's local start through it — so `POST /cinemas` resolves the name with
`time.LoadLocation` and refuses one that does not load with
`timezone: must be an IANA timezone name such as Europe/Madrid`, storing
nothing. The API binary embeds the zone database (`time/tzdata`), so a
deployment without system zoneinfo still validates and renders these names.

A wrong `API_LOCAL_ADMIN_TOKEN`, or an API started without `API_LOCAL_ADMIN=true`,
is answered **404** rather than 401: the guard makes an absent administrative
surface indistinguishable from a denied one. The admin web reports that as
`the API has no administration at that address: either it runs without
API_LOCAL_ADMIN=true, or API_LOCAL_ADMIN_TOKEN does not match the token it was
started with`, so a 404 there is a settings problem, not a problem with the
form that was submitted.

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

The web app does **not** link those two binary API routes directly. It serves them itself
at `/api/tickets/{id}/qr.svg` and `/api/tickets/{id}/pdf`, forwarding to the API from the
server, because the API's absolute address only means something on the machine running it:
opened from a phone or a second laptop, a link at `http://127.0.0.1:8080/...` would point
each device at itself — a blank code and a "page not found" download, both silent. The
forwarded PDF keeps its declared `Content-Disposition`, so the download is still named
after its ticket.

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

## M3.5 the product web

M3 shipped the purchase path over HTTP; M3.5 put a product on top of it, and administration on top of that.

| Surface | What it does |
|---|---|
| `/events` | the catalogue: screenings on sale, the venue's own hour, price per seat, seats available |
| `/events/{id}/seats` | the seat map: the real layout, operable with the keyboard alone, states legible without colour |
| `/reservations/{id}` | the checkout: pays with a key it keeps per reservation, and answers thirteen outcomes as thirteen states |
| `/tickets/{id}` · `/tickets` | the ticket and the list of what this browser tab kept |
| `/admin/venues` · `/admin/layouts` · `/admin/schedule` | cinemas, layouts and screenings — including the price and the status that put a screening on sale |

Two things this build is honest about:

- **The session in the header is a demonstration**, not authentication: it grants no access, no page sends it to the API, and the wording is one exported constant so the interface and these lines cannot drift.
- **"My tickets" is what the tab kept**, because without accounts no route can list a buyer's tickets. Unavailable storage says so rather than showing an empty list.

`make verify-m35` proves all of it in a real browser: administration creates, prices and publishes a screening; a buyer picks seats with the keyboard, pays, gets a ticket whose code the server verifies and whose PDF downloads; the list survives a reload and distinguishes empty from unreadable. It runs in CI as its own job, one stack, Chromium only.

## What this build does not do

Four milestones are delivered, and what they leave out is part of the delivery. Each line names what is missing and what that costs today, so nobody has to infer it from a green build.

| Not there | What it costs today |
|---|---|
| **Identity and authorization** | There are no accounts. A ticket and a reservation are opened by their identifier, so anyone holding the link sees them — by design for M3.5, and recorded in its milestone log. The session in the header is a demonstration that grants nothing. |
| **Single-use redemption** | A ticket is issued and its code can be verified server-side, but nothing marks it as used. The same QR can be presented at a door indefinitely. This is the largest gap: the ticket is worth one admission and currently proves an infinite number. |
| **Cashier, reporting and audit** | No door screen, no takings, no record of who changed what. The administrative surface is local-only and unauthenticated beyond a shared token. |
| **Cancelling a hold** | `POST /reservations/{id}/cancel` is specified and not implemented — `ARCHITECTURE.md` now says so instead of listing it as if it existed. A buyer who changes their mind waits out the hold's TTL. |
| **Catalogue filtering and pagination** | `GET /events` returns every screening on sale. Fine for a seed, wrong for a month of programming. |
| **Email** | The PDF is downloaded from the web. Nothing is mailed. |
| **PDFs that follow a void** | The PDF is rendered when the payment is captured and stored immutably: cancelling a screening or voiding a ticket does not change a PDF already issued. It is a record of the sale and never proof of validity — validity is `POST /tickets/verify`. |
| **Real payment and storage** | The provider is simulated and storage is Floci. A real decline needs `NewSimulatedWithDeclineRule`; the wired simulator always approves. |
| **Accessibility beyond the keyboard path** | The seat map is operable without a mouse, with a visible focus ring, one `h1` per screen and `aria-current` on the navigation. That is not an accessibility audit. |
| **Browser coverage beyond Chromium** | `make verify-m35` drives Chromium only, and only by `localhost` — an origin the browser treats as secure. Anything the browser decides by origin stays invisible to it. |

The tagged acceptance test for the double-booking contract still runs as its own proof:

```sh
go test -tags=acceptance_m2 ./tests/concurrency
```

See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the full design and the milestone table, and the records in [`odd/tasks/`](odd/tasks): `m0-foundation.md`, `m1-venue-layout.md`, `m2-reservation-core.md`, `m3-checkout-tickets.md` and `m35-product-web.md`.
