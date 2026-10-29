# cinema-web — M0 foundation, M1 layout administration, M2 seat selection

Next.js (App Router) + TypeScript web application for the cinema ticketing
platform. It exposes a deterministic health contract for local and CI checks, a
local administrative layout editor from M1, and the guest seat selection surface
from M2.

**Scope:** `/admin/*` is development-only administration. `/events/{eventID}/seats`
is the customer facing seat selection surface: it consumes the public
reservation API, needs no account, and no checkout or ticket wallet exists yet
(those are M3).

## M1 layout administration

`/admin/venues` creates and lists cinemas, rooms, and layout versions.
`/admin/schedule` creates films, schedules events, and shows the materialized
event seats. `/admin/layouts` renders one layout version from the API and lets an operator
edit rows with different seat counts, save, publish, and clone a published
version into a new draft. The browser never holds the bearer token: every read
and write runs server-side through `src/lib/admin-api.ts`.

| Variable | Purpose |
| --- | --- |
| `API_BASE_URL` | API base URL, default `http://127.0.0.1:8080` |
| `API_LOCAL_ADMIN_TOKEN` | Development bearer token; must match the API's `API_LOCAL_ADMIN_TOKEN` |

Both are server-only. The API must be started with local administration enabled
(see the root `README.md`); otherwise `/api/v1/admin` answers 404 by design.

## M2 guest seat selection

`/events/{eventID}/seats` is the customer facing seat selection surface. It is a
guest surface: it sends no credential, creates no account, and reaches only the
public `/api/v1` routes, which are mounted outside the administrative guard.

The page reads `GET /api/v1/events/{eventID}/seats` on the server and posts
`POST /api/v1/reservations/holds` through a server action, because the Go API is
same-origin only for the web server. `src/lib/public-api.ts` owns both calls and
never throws: every failure arrives as an RFC 9457 problem detail typed by code.

What the surface does, and where each rule is pinned:

| Behavior | Rule | Tested in |
| --- | --- | --- |
| Rows render with their own seat counts; no rectangular grid, no padding cell | `buildSeatMap` | `seat-selection.test.ts` |
| Each seat shows its label and kind | `buildSeatMap` | `seat-selection.test.ts` |
| Multi-seat selection with a running total | `toggleSeat`, `selectionSummary` | `seat-selection.test.ts` |
| A hold attempt always mints a fresh `Idempotency-Key` | `beginHoldAttempt`, `newIdempotencyKey` | `seat-selection.test.ts`, `public-api.test.ts` |
| The countdown is the server's `expiresAt` minus the clock, ending in an explicit expired state | `describeCountdown`, `remainingMilliseconds`, `formatCountdown` | `seat-selection.test.ts` |
| `409 seat_unavailable` refetches availability, drops the lost seats and names them | `describeHoldFailure`, `dropUnavailableSeats` | `seat-selection.test.ts` |
| `400 hold_window_expired` is an expired window, never a taken seat | `describeHoldFailure` | `seat-selection.test.ts` |
| Any `503` (or unreachable API) is a temporary, retryable failure | `describeHoldFailure` | `seat-selection.test.ts`, `public-api.test.ts` |

### The served layout projection

The public availability read now serves the layout with the occupancy: every seat
carries `rowLabel`, `seatNumber` and `kind` next to `id`, `layoutSeatId`, `state`
and `held`, and the rows come in row-position order with each row's own seat
count. `layoutFromAvailability` uses them directly, so the page renders the real
irregular map with each row's own seat count and no padding cell.

The projection is optional on the wire: a response that carries no row label is
treated as an older server, and only then does the page fall back to a single
ordered group with an `unknown` kind, `geometryKnown: false` and a notice. A
partially labelled response also falls back entirely, because inventing the rows
that are missing would be worse than saying so.

Money is absent: M2 has no price bands, so the running total is a seat count,
never a fabricated amount. Pricing arrives with M3.

### Manual browser check

The pure rules and the HTTP contract are covered by `pnpm test`, but the rendered
behavior needs a browser and a live API:

1. `docker compose up` (or `make verify-m2`) and enable local administration.
2. Create a cinema, a room, an irregular layout (rows with different seat counts),
   publish it, then create an event with `/admin/schedule`.
3. Open `/events/<eventID>/seats`, select seats in different rows, and confirm the
   running total and that the countdown starts from the server's `expiresAt`.
4. Hold the same seats from a second browser profile to trigger `409
   seat_unavailable`, and confirm the page refetches, drops and names the lost
   seats, and keeps the calm message instead of an error screen.
5. Stop the API and retry a hold to confirm the temporary, retryable message.

## Local run

Uses [pnpm](https://pnpm.io).

```sh
cd apps/web
pnpm install
pnpm dev
```

Open http://localhost:3000.

## Scripts

| Script            | What it does                                               |
| ---------------- | --------------------------------------------------------- |
| `pnpm dev`       | Start the Next.js development server.                     |
| `pnpm build`     | Production build.                                         |
| `pnpm start`     | Serve the production build.                               |
| `pnpm lint`      | Lint `src/**` with ESLint (Next core web vitals config).  |
| `pnpm typecheck` | Type-check with `tsc --noEmit`.                          |
| `pnpm test`      | Run the deterministic health, admin, public-API and seat tests (Node native). |

## Health contract

`GET /api/health` returns a fixed, self-contained JSON payload with HTTP 200:

```json
{
  "status": "ok",
  "service": "cinema-web",
  "milestone": "M0",
  "checks": {
    "runtime": "ok"
  }
}
```

The exact serialization is pinned by `src/lib/health.test.ts` via
`node --test`, with no framework or install dependency.

> The Go API exposes its own `GET /health/live` and `GET /health/ready`
> endpoints. This web health report is an independent front-end readiness
> marker, not a proxy of the Go API.

## Why no remote fonts or images

The M0 foundation must build and run fully offline. Typography uses the browser
system font stack (see `src/app/globals.css`), so no network fetch occurs at
build or runtime.
