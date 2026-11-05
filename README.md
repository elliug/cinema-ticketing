# Cinema ticketing foundation

M0 provides a runnable monorepo baseline for the cinema ticketing system: a Go modular monolith, a Next.js web application, PostgreSQL, Redis, reversible migrations, and CI checks. On top of it, four milestones are delivered: **M1** venues, rooms and versioned layouts; **M2** durable seat holds with per-seat exclusivity and expiry; **M3** the purchase path — a guest holds seats, pays through a simulated provider, and receives a ticket with a signed code and a PDF stored in S3; **M3.5** the product web — catalogue, keyboard-operable seat map, checkout, tickets, and an administration surface that can price and publish a screening.

Identity arrived after M3.5 and is **optional**: an account keeps one list, the tickets bought with a proved address, and no route stops being public because of it. Reporting and audit do not exist yet. `## What this build does not do` lists the gaps, with what each one currently costs.

## Quick path

Prerequisites: Go 1.27+, Node.js 22+, pnpm 12+, Docker Engine, Docker Compose, and `curl`.

To run the product and click through a purchase, follow [`## Run locally, from zero`](#run-locally-from-zero): six numbered steps, two of them long-running. The only optional one starts the mail server; skipping it changes nothing except that no email is delivered.

To prove the build instead, without reading anything else:

```sh
pnpm install
make deps-up
make migrate
make check
```

Expected result: Go tests and vet pass, and the web tests, lint, typecheck and build pass. The milestones also have their own end-to-end gates — `make verify-m1`, `make verify-m2`, `make verify-m3`, `make verify-m35` and `make verify-m41` — which start their own stack and prove the behaviour rather than the compilation.

## Run locally, from zero

Prerequisites: Go 1.27+, Node.js 22+, pnpm 12+, Docker Engine with Compose v2, and `curl`. Nothing else is installed by hand: PostgreSQL, Redis, the local S3 and the mail server are containers this repository defines, and the schema, the demo data and the ticket bucket are created by the commands below.

Six steps. Steps 1–3 finish and give the terminal back; steps 4 and 5 stay running, each in its own terminal. Every step is re-runnable: running one twice changes nothing.

### Step 1 — the JavaScript dependencies (once per clone)

```sh
pnpm install
```

### Step 2 — the containers

```sh
make up
```

`make deps-up` creates the volumes and starts the three services the application cannot work without: PostgreSQL (`postgres:17.6-alpine`, port 5432), Redis (`redis:7.4.6-alpine`, port 6379) and Floci (`floci/floci`, pinned by digest because CI starts it on every run, port 4566), the local S3 that stores the M3 ticket PDFs and answers path-style requests at `http://127.0.0.1:4566` with the dummy `floci`/`floci` credentials. `make up` then waits for the services themselves within a bounded timeout: PostgreSQL with `pg_isready` inside its container, Redis with `PING`, and Floci with `GET http://127.0.0.1:4566/_floci/health`. A service that does not answer fails the target and is named (`postgres is not ready after 1m0s: …`); the wait only reads and never touches the database, and its budget can be changed with `make up WAIT_TIMEOUT=120s`. Redis runs with `--maxmemory-policy noeviction`, because an eviction policy could drop a hold key before its TTL and let a second buyer hold the same seat. Floci's image carries its own healthcheck (a shell probe of `/_floci/init`), so `make deps-up` waits for all three containers; because the image has neither `curl` nor `wget`, scripts that need an explicit check poll `http://127.0.0.1:4566/_floci/health` from the host. The API's object-storage settings therefore default to that service (`API_S3_ENDPOINT=http://127.0.0.1:4566`, `API_S3_BUCKET=cinema-m3-tickets`, `API_S3_REGION=us-east-1`, `API_S3_PRESIGN_TTL=5m`), and a non-local endpoint is refused while the development credentials are in place.

The mail server is the one container `make up` deliberately leaves out. Start it only if you want to see the ticket email:

```sh
make mail-up
```

That brings up `axllent/mailpit` (pinned tag) behind its own compose profile, with SMTP on `127.0.0.1:1025` and a web inbox on `http://127.0.0.1:8025`, and waits for the container's own healthcheck. It stays out of `make up` because a mail server must never become a prerequisite of a gate: a sale never depends on the mail, and every gate runs with no provider at all. Skip this step and the application behaves identically — you simply receive nothing.

If it says it is recreating the container, that is a leftover: Mailpit is the only container behind a profile, so it is the only one a `make deps-down` followed by `make up` can leave holding a network that no longer exists, and the target repairs that instead of failing. The inbox it drops is disposable.

### Step 3 — the schema, the demo data and the bucket

```sh
API_ENV=development make bootstrap
```

It runs three things in order: `migrate-all` reads `platform.schema_migrations`, compares it with `db/migrations/*.up.sql` and applies only the missing ones (on a fresh database, all of them); `seed-dev` loads `db/seeds/dev_seed.sql` — one cinema, one room with a published irregular layout, two films and two screenings; `ensure-bucket` creates the ticket bucket when it is missing. It refuses to run without `API_ENV=development`, because it writes demo data.

### Step 4 — the API, first terminal (stays running)

One command, and every variable must be in **this same invocation**: the API reads its environment once, at startup, and decides there whether it mounts the checkout, the administration and the email consumer. There is no second pass and no "add the mail variables later".

```sh
API_ENV=development \
API_DATABASE_URL="postgres://cinema:cinema_local_only@127.0.0.1:5432/cinema?sslmode=disable" \
API_DEPENDENCIES=postgresql,redis \
API_LOCAL_ADMIN=true \
API_LOCAL_ADMIN_TOKEN=change_me_in_local \
API_SMTP_ADDR=127.0.0.1:1025 \
API_EMAIL_FROM=tickets@cinema.local \
API_PUBLIC_WEB_URL=http://localhost:3000 \
   go run ./apps/api/cmd/api
```

What each one is doing there:

| Variable | Why it is in the command |
|---|---|
| `API_ENV=development` | The only value that enables the documented development defaults (the ticket keys) and the local administrative surface. |
| `API_DATABASE_URL` | Without it **no customer surface is mounted at all**: holds, payments and tickets answer `404` while the process still reports healthy. |
| `API_DEPENDENCIES=postgresql,redis` | Names the TCP probes `GET /health/ready` reports on. |
| `API_LOCAL_ADMIN` and its token | Mounts `/api/v1/admin/`. The web must be started with **the same token**. |
| `API_SMTP_ADDR` and `API_EMAIL_FROM` | Start the ticket email consumer. Omit **both** and the sale, the PDF and every gate are unaffected; omit only one and the process refuses to start, because a half-configured provider would fail every send. |
| `API_PUBLIC_WEB_URL` | The origin the mailed link points at, as `<origin>/t/<token>`. Absolute `http`/`https`. |

Nothing has to be exported or repeated if you do not want to: `set -a; …; set +a` around these lines, or exporting them once in that terminal, works the same. What does **not** work is expecting a `.env` file to do it — `.env` is read by Docker Compose for the container ports, not by the API process.

Two lines in the startup log say whether the mail half is live:

- `ticket email worker started` with `interval=5s`, `batch=20`, `timeout=30s`, `from=tickets@cinema.local` — it will deliver;
- `ticket email disabled: no mail provider is configured` — it will not, and nothing else changes.

The email consumer, the PDF worker, the expiry sweep and the payment reconciler are goroutines of **this one process**. Starting `go run ./apps/api/cmd/api` a second time does not add a worker: it fails on the listen address (`:8080` by default, `API_ADDR` decides), and it would be wrong even on another port, because a second sweep and a second consumer on one database is not a second worker but two writers.

### Step 5 — the web, second terminal (stays running)

```sh
API_LOCAL_ADMIN_TOKEN=change_me_in_local pnpm web:dev
```

The token must match the API's. `change_me_in_local` is a placeholder you choose: any value works locally, as long as **both** processes are started with the same one. When they disagree, the administration answers `404` rather than `401`, so nobody can tell a missing surface from a wrong token.

### Step 6 — use it

| Where | What |
|---|---|
| `http://localhost:3000` | Buy: catalogue, seat map, hold, pay, ticket. |
| `http://localhost:3000/admin/venues` | Administer: cinemas, rooms, layouts, films and screenings. |
| `http://localhost:3000/tickets` | The tickets this browser kept. |
| `http://127.0.0.1:8025` | The mail inbox, when `make mail-up` is running: one message per paid order that carried an address. |

To see an email end to end: pay an order leaving an address in the checkout field, then wait one worker tick (five seconds) and open the inbox; the message carries a signed link that opens the tickets and expires seven days later. The same link can be mailed to another address from the ticket page, at most three times per order per hour.

### Stopping, and starting again

`Ctrl-C` in each of the two terminals. `make deps-down` stops every container — Mailpit included, because `compose down` takes the whole project — and keeps the volumes, so `make up` comes back with the same data; `make migrate-down` is the explicit way to roll the schema back, and `make deps-down` alone never deletes a database.

### The browser gate

The browser gate drives Chromium and Firefox, so install both once:

```sh
pnpm --dir apps/web exec playwright install --with-deps chromium firefox
```

`make verify-m35` owns the stack and needs nothing else. `make verify-e2e` runs the same suite against a server you already have, and it needs `WEB_BASE_URL` pointed at a **non-loopback** address: `e2e/insecure-origin.spec.ts` fails on loopback by design, because loopback is a secure context.

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
| `design/` | the design system (`system.md`, `system.html`) and the artboard directions compared before the redesign |
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
make verify-m41         # M4.1 gate over HTTP: the door, its second presentation, a forgery, the race
make migrate-down       # explicitly roll back the development schema
make deps-down          # stop local containers without deleting volumes
```

`Makefile` automatically uses either `docker compose` or `docker-compose`.

## What `make bootstrap` does

Step 3 of [the local walkthrough](#run-locally-from-zero) runs it once; this is the detail behind each of its three parts. It is re-runnable: a second run applies nothing, seeds nothing and creates nothing.

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
that applying it with `psql` directly cannot seed a database by accident. That
first guard is a string the caller supplies, so the file adds a second one: it
reads the address the server answered the connection on, and refuses when that
address is not loopback, which a shared database's own address never is. What it
does not cover is someone seeding a database local to them on purpose. Never run
it against a shared or production database.

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
Two gates used to share Redis logical database 9 and refuse to run concurrently. Each now
takes its own index by default — `verify-m2` index 9, `verify-m3` index 10, with
`M2_REDIS_DB` and `M3_REDIS_DB` to change either — so neither blocks the other. Each still
refuses to start on an index that already holds keys rather than flushing work that is not
its own.

## M3.5 the product web

M3 shipped the purchase path over HTTP; M3.5 put a product on top of it, and administration on top of that.

**The interface is Castilian Spanish, in one dark scheme.** Screen copy, the sentences the rules
produce, and the answers the door gives are all Spanish; there is no `prefers-color-scheme` branch
and no light theme, because the redesign chose the dark room everywhere. Poster art is drawn in
the application from the film title — no image is fetched — and every amount is formatted by one
formatter, `lib/receipt.ts`, from the cents the API serves. The directions this replaced are kept
in `design/cinema-redesign/`, next to the design system they were drawn against.

| Surface | What it does |
|---|---|
| `/events` | the catalogue: screenings on sale, the venue's own hour, price per seat, seats available — one page at a time, with the cursor in the address so the next page is a link a buyer can bookmark, share and reload |
| `/events/{id}/seats` | the seat map: the real layout, operable with the keyboard alone, states legible without colour |
| `/reservations/{id}` | the checkout: pays with a key it keeps per reservation, and answers thirteen outcomes as thirteen states |
| `/tickets/{id}` · `/tickets` | the ticket, and the list: the account's, read from the box office, when this browser holds a session; this tab's own note of what it bought when it does not |
| `/admin/venues` · `/admin/layouts` · `/admin/schedule` | cinemas, layouts and screenings — including the price and the status that put a screening on sale, which travel with the scheduling form so one submit schedules a screening the catalogue already sells |
| `/admin/door` | the door: one field and one action for the code a buyer shows, and one answer a cashier cannot misread — admitted with its seat and screening, already used with the hour it was first used, not a ticket of ours in the API's own words, or not admissible because the ticket was voided or its screening was cancelled. The code is posted, answered and forgotten: nothing is kept in the browser |

Two things this build is honest about:

- **Signing in is optional, and it protects the list rather than the tickets.** An account exists once an address is proved by a link mailed to it — a link that expires in minutes and works once. It adds exactly one thing: `/tickets` becomes the account's list, read from the box office, so it follows you to another browser, another device and a cleared site. Nobody needs an account to buy a ticket or to open one, and a ticket is opened by its own link, which admits whoever holds it — so login never protected a link already handed out.
- **Without a session, the ticket list is what this tab kept**, and it says so. Unavailable storage says it cannot be read rather than showing an empty list, and an account is offered as the way to have a list the box office answers.

`make verify-m35` proves all of it in a real browser: administration creates a screening already priced and on sale in one request, then edits it through the price-and-status form; a buyer picks seats with the keyboard, pays, gets a ticket whose code the server verifies and whose PDF downloads; the list survives a reload and distinguishes empty from unreadable. It runs in CI as its own job, one stack, in Chromium and Firefox.

The gate drives an origin the browser does **not** treat as secure: it serves the web on the machine's primary non-loopback IPv4 address and refuses to fall back to loopback, because loopback is a secure context and a gate pointed at one cannot see anything the browser decides by origin — which is how `crypto.randomUUID` and an absolute ticket link both escaped a green build. A spec asserts `window.isSecureContext === false` and `typeof crypto.randomUUID === "undefined"` inside the browser, so pointing the gate at a secure origin fails with a message naming the reason instead of passing quietly.

Its run scope is enforced rather than assumed. `M35_REDIS_DB=0` (the development keyspace) and an `M35_S3_ENDPOINT` that is not loopback are both refused by name, with the value that makes them safe, because the gate flushes the Redis index it uses and creates and deletes a bucket in the store it is pointed at. CI uses the defaults.

## What this build does not do

The milestones delivered so far, and what they leave out, are part of the delivery. Each line names what is missing and what that costs today, so nobody has to infer it from a green build.

| Not there | What it costs today |
|---|---|
| **Identity and authorization** | Accounts exist, and they authorize one thing: the list of tickets bought with the address a mailed link proved. They are optional — buying a ticket and opening one need no account — and they never protect a link already handed out: a ticket and a reservation are still opened by their identifier, so anyone holding the link sees them. There is no address change and no account deletion, so a person has no way to ask for their data to be removed; a session ends when it expires, and signing out removes this browser's cookie without revoking the row at the box office yet. |
| **Cashier, reporting and audit** | No takings and no record of who changed what: the door screen admits a ticket once, but nothing counts the evening's takings or says who held the till. The administrative surface is local-only and unauthenticated beyond a shared token. |
| **Catalogue filters beyond time** | `GET /events` pages with a keyset cursor and bounds each page with `from`/`to`, and nothing else: there is no film or cinema filter until a deployment has more than one cinema. |
| **Email** | A purchase may carry one address, asked for at the checkout and never verified. Where the deployment configures a mail provider, a worker mails a signed link that expires; with no provider configured nothing is mailed at all. |
| **PDFs that follow a void** | The PDF is rendered when the payment is captured and stored immutably: cancelling a screening or voiding a ticket does not change a PDF already issued. It is a record of the sale and never proof of validity — validity is `POST /tickets/verify`. |
| **Real payment and storage** | The provider is simulated and storage is Floci. A real decline needs `NewSimulatedWithDeclineRule`; the wired simulator always approves. |
| **Accessibility beyond the keyboard path** | The seat map is operable without a mouse, with a visible focus ring, one `h1` per screen and `aria-current` on the navigation. That is not an accessibility audit. |
| **Browser coverage beyond Chromium and Firefox** | `make verify-m35` drives Chromium and Firefox, both over one origin family: a private LAN address on plain HTTP, never WebKit, never a public host and never TLS. So what WebKit decides differently, and what a secure context gates *on*, stay outside it. |

The tagged acceptance test for the double-booking contract still runs as its own proof:

```sh
go test -tags=acceptance_m2 ./tests/concurrency
```

See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the full design and the milestone table, [`GUIDE.md`](GUIDE.md) for what a customer and an administrator can do, and the records in [`odd/tasks/`](odd/tasks): `m0-foundation.md`, `m1-venue-layout.md`, `m2-reservation-core.md`, `m3-checkout-tickets.md`, `m35-product-web.md` and `m41-door-and-hardening.md`.
