# M0 Foundation

This feature document tracks implementation of the approved M0 foundation milestone.
M0 establishes a runnable monorepo baseline without implementing venue, layout, reservation, payment, or ticketing behavior.

## Specs

1. S1 — "continúaamos con la  implementaCIÓN DE M0"
2. S2 — "M0 — Foundation | Repository, local dependencies, migrations, module boundaries, CI baseline | Service and web app run locally; health checks and migration rollback verified."
3. S3 — "The first implementation milestone must begin with a deterministic red integration test that launches concurrent hold requests for overlapping seats and proves exactly one succeeds."
4. S4 — "Go API (modular monolith)"
5. S5 — "Next.js + TypeScript PWA"
6. S6 — "PostgreSQL as the durable source of truth, and Redis as the short-lived coordination layer for seat holds."

## Tasks

- T1 — S1–S6 — foundation specification and repository baseline — parent inline — complete — commit: `docs(architecture): establish cinema ticketing baseline`
- T2 — S2, S4 — Go API skeleton, module boundaries, configuration, health endpoints, and tests — gentle-ai-worker + parent verification — complete; `go test ./apps/api/...` and `go vet ./apps/api/...` passed — commit: `feat(api): add M0 service foundation`
- T3 — S2, S5 — Next.js web skeleton, health UI, and checks — gentle-ai-worker + parent verification — complete; test, lint, typecheck, and production build passed — commit: `feat(web): add M0 application foundation`
- T4 — S2, S6 — PostgreSQL/Redis local dependencies, reversible migrations, and verification scripts — gentle-ai-worker + parent correction/verification — complete; live service health and migration up/down/up passed — commit: `feat(foundation): complete M0 operations baseline`
- T5 — S2 — CI baseline, developer commands, documentation, and end-to-end M0 verification — parent inline — complete; `make verify-m0` passed — commit: `feat(foundation): complete M0 operations baseline`
- T6 — S3 — deterministic concurrency acceptance-test contract for M2 — parent inline — complete; deterministic RED observed and contract documented — commit: `feat(api): add M0 service foundation`

## Log

- L1 — "continúaamos con la  implementaCIÓN DE M0"
- L2 — "CONTINUAR"
- L3 — "continue"
- L4 — "continuar"
- L5 — "continua"
- L6 — M2 concurrency acceptance contract observed RED: `go test -tags=acceptance_m2 ./tests/concurrency` failed because `M2_API_URL` is absent.
- L7 — Go API foundation verified GREEN with `go test ./apps/api/...` and `go vet ./apps/api/...`.
- L8 — Next.js foundation verified GREEN with `pnpm test`, `pnpm lint`, `pnpm typecheck`, and `pnpm build` in `apps/web`.
- L9 — PostgreSQL and Redis became healthy under Docker Compose; isolated migration verification passed up/down/up; Redis returned `PONG`; PostgreSQL accepted connections.
- L10 — Runtime readiness now uses real TCP probes for configured PostgreSQL and Redis addresses; focused RED/GREEN tests passed.
- L11 — Final `make verify-m0` passed backend tests/vet, web test/lint/typecheck/build, live migration up/down/up, and exact API/web health smoke checks.
- L12 — M2 concurrency contract remains intentionally RED until the reservation endpoint and fixture exist.
