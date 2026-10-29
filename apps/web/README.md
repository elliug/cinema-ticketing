# cinema-web — M0 foundation and M1 layout administration

Next.js (App Router) + TypeScript web application for the cinema ticketing
platform. It exposes a deterministic health contract for local and CI checks and,
from M1, a local administrative layout editor.

**Scope:** the layout editor is development-only administration. There is no
reservation, payment, ticketing, or identity UI; those arrive in later milestones.

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
| `pnpm test`      | Run the deterministic health-contract and admin tests (Node native). |

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
