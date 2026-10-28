# cinema-web — M0 foundation

M0-only Next.js (App Router) + TypeScript foundation for the cinema ticketing
platform. It proves the web application runs locally and exposes a
deterministic health contract for local and CI checks.

**Scope:** M0 is operational only. There is no venue, layout, reservation,
payment, ticketing, or identity UI. Those arrive in later milestones.

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
| `pnpm test`      | Run the deterministic health-contract test (Node native). |

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
