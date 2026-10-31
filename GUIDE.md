# User guide

What this build does, from the two chairs you can sit in: the person buying a ticket, and the
person running the venue. It is written against the code in this repository, not against a plan:
if a screen or a route is not described here, it does not exist.

For how to install and start it, [`README.md`](README.md) is the source of truth — four commands
and two long-running terminals. For why it is built this way, [`ARCHITECTURE.md`](ARCHITECTURE.md).

## Before you start

```sh
pnpm install
make deps-up                      # PostgreSQL, Redis and Floci (the local S3)
API_ENV=development make bootstrap   # migrations, demo data, ticket bucket
```

Then two terminals:

```sh
# API — listens on :8080 by default
API_ENV=development \
API_DATABASE_URL="postgres://cinema:cinema_local_only@127.0.0.1:5432/cinema?sslmode=disable" \
API_DEPENDENCIES=postgresql,redis \
API_LOCAL_ADMIN=true \
API_LOCAL_ADMIN_TOKEN=change_me_in_local \
   go run ./apps/api/cmd/api

# Web — :3000
pnpm web:dev
```

The web server talks to the API through `API_BASE_URL` and never exposes that address to the
browser. `make bootstrap` matters for one reason beyond data: the door needs migration `0008`,
and a database that stops short of it will fail to admit anyone.

## What a customer can do

1. **Browse the catalogue.** `GET /events` answers one bounded page: `limit` (1–100, 20 when
   unnamed, refused out of range rather than clamped) and optional `from`/`to` on the screening
   start, with a keyset cursor that says how to ask for the next page (`null` on the last one).
   Films are browsed through screenings; there is no separate film list.
2. **Open a screening.** `GET /events/{eventId}` gives the film, the venue and room, the hour in
   **the venue's own timezone**, the price and how many seats are available.
3. **Choose seats.** The seat map is operable without a mouse, with visible focus, and says which
   seats are held. `GET /events/{eventId}/seats` is the snapshot behind it — availability changes
   only on the next read, because there is no live stream.
4. **Hold the seats.** `POST /reservations/holds` claims the whole selection at once or none of it,
   and needs an `Idempotency-Key`: the same key replays the same hold instead of taking a second
   one. The hold carries a deadline; a seat taken by someone else is named in the refusal.
5. **Pay.** `POST /reservations/{reservationId}/payment` charges the hold through a **simulated**
   provider, also idempotently. It always approves in this build; a decline needs
   `NewSimulatedWithDeclineRule`, which the wired simulator does not use. Both `approved` and
   `declined` are answered `200` — the payment status is in the body, not in the HTTP code.
   A payment whose request dies mid-call is **settled later, not lost**: a worker asks the
   provider again with the same reference, so the charge becomes either a ticket or a released
   hold — never money taken without a ticket. While a payment is in flight its hold is never
   released, which is why a seat can look stuck for the length of one hold window.
6. **Get the tickets.** One ticket per seat. `GET /tickets/{ticketId}` is the presentation data,
   `/qr.svg` is the signed code as an image, and `/pdf` answers `302` to a short-lived pre-signed
   link rather than the document itself.
7. **Keep them.** The ticket list is what this browser tab kept from its own purchases. It is not
   an account: there is no sign-in anywhere in the product.

**Anyone holding a ticket link can open that ticket.** That is by design in this build and it is
recorded, not accidental: there is no identity, so the link is the credential. The same applies to
a reservation.

## What the door does

The staff surface is `/admin/door`. Type or scan a ticket code and it answers one of:

| Verdict | Meaning |
|---|---|
| admitted | first presentation; the answer carries the seat and the instant, in the venue's own hour |
| already admitted | the ticket was presented before; the answer repeats the **first** instant, byte for byte |
| invalid code | the code does not verify; refused exactly as verification refuses it, and nothing is written |
| voided / cancelled screening | the ticket or its screening is not admissible |

It calls `POST /tickets/redeem` from the browser, and gains its verdict server-side: the screen
holds no authority of its own. What it deliberately does not prove: who the staff member was
(there are no accounts; `redeemedBy` is an opaque label), whether the clock says the screening is
running, and anything about a PDF already issued.

## What administration can do

The order matters; each step is a prerequisite of the next.

1. **`/admin/venues`** — create a **cinema** with its timezone (this is what makes every later hour
   local), a **room**, and a **layout version**.
2. **`/admin/layouts`** — open the version, add rows and seats, and **publish** it. A screening may
   only use a published version; a draft is refused as a conflict, and the form says so before you
   try.
3. **`/admin/schedule`** — create a **film**, then **schedule a screening**: film, published layout
   version, start time **in UTC**, price per seat, status. Price and status travel in the create
   request, so a screening can go on sale in one step; the form defaults to `draft` at no price if
   you leave them empty.
4. **`/admin/schedule`** — the **Price and status** section changes those two fields on a screening
   that already exists.
5. **`/admin/schedule`** — **Materialized seats** shows the seats a screening actually has, which is
   the layout's enabled seats at the moment it was scheduled.
6. **`/admin/door`** — admission, described above.

Two traps worth knowing: the start time is typed in **UTC** and read back in the venue's hour, and
removing or editing a layout version after it is published is refused by design — layouts are
immutable once they exist.

## The administrative API, and how it is guarded

`/api/v1/admin/...` serves cinemas, rooms, layouts (`POST /layouts`, `PUT /layouts/{id}/rows`,
`POST /layouts/{id}/publish`, `POST /layouts/{id}/clone`, `GET /layouts/{id}`), films, and events
(`POST`, `GET`, `GET /{id}`, `PATCH /{id}`, `GET /{id}/seats`).

The guard is deliberately narrow and fail-closed. Three facts must hold at once — the local admin
flag is on, the environment is `development`, and a non-empty token is configured — and the request
must arrive **from loopback** with `Authorization: Bearer <token>` compared in constant time.
Anything else gets `404`, indistinguishable from a route that does not exist, on purpose.

There are **no roles** in this build. No `admin`, `cashier` or `analyst` permission is checked
anywhere; the administrative surface is local-only and bears a single shared token.

## What this build does not do

| Not there | What it means in practice |
|---|---|
| Identity and accounts | a ticket and a reservation are opened by their link alone |
| Cashier, reporting, audit | nobody counts the evening's takings, and no record says who did what |
| Email | the PDF is downloaded from the web; nothing is mailed |
| Refunds | a paid order cannot be cancelled; a cancel of a paid one is refused `409 already_paid` |
| Real payments and storage | the provider is simulated and storage is Floci |
| Catalogue filters beyond time | no film or cinema filter, until a deployment has more than one |
| PDFs that follow a void | the PDF is a record of the sale, never proof of validity — validity is `POST /tickets/verify` |
| Browser coverage beyond Chromium and Firefox | no WebKit, and one origin family: a private LAN address on plain HTTP |
| Accessibility beyond the keyboard path | operable, focus-visible, one `h1` per screen — not an audit |

## When something is off

- **The door refuses everything, or answers `503`.** The database is behind: run
  `API_ENV=development make bootstrap`. Migration `0008` is what creates `redeemed_at`.
- **`pnpm check` kills the dev server with `Cannot find module './NNN.js'`.** `next build` and
  `next dev` share `.next`. Stop the dev server, `rm -rf apps/web/.next`, start it again.
- **The browser gate fails on a missing browser.** Install the engines it drives:
  `pnpm --dir apps/web exec playwright install chromium firefox` (no `--with-deps` on macOS).
- **A Go test skips instead of running.** The PostgreSQL-gated suites need
  `M3_CHECKOUT_DATABASE_URL` / `M2_RECOVERY_DATABASE_URL` pointing at migrated databases whose
  names begin with `cinema_m3` / `cinema_m2`, one database per package.

## Proving it rather than clicking it

Each milestone ships a gate that runs the whole promise against real services, and each runs in CI:

```sh
make verify-m0    # foundation
make verify-m1    # venues, layouts, events
make verify-m2    # holds: one winner in sixteen, expiry, worker recovery
make verify-m3    # purchase end to end, including the PDF in the bucket
make verify-m35   # the same, plus administration, in Chromium and Firefox
make verify-m41   # the door: admit once, refuse a forgery, two doors racing one ticket
```

`make verify-m41` is the shortest read of what this build promises: eleven assertions, then the
cleanup lines that say what it removed.
