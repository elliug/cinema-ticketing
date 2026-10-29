# M1 — Venue & Layout

Implementation tracking for the approved M1 plan.
M1 builds on the integrated M0 foundation.

## Specs

1. S1 — "Cada fila puede tener una cantidad diferente de asientos."
2. S2 — "Un layout publicado no puede modificarse."
3. S3 — "La edición posterior requiere una nueva versión en borrador."
4. S4 — "Se creará exactamente un asiento de evento por cada asiento habilitado."
5. S5 — "La creación del evento y de sus asientos se hará en una única transacción: si falla, no quedará un evento parcial."
6. S6 — "Un cambio posterior del layout activo no alterará los eventos existentes."
7. S7 — "Las rutas administrativas no están disponibles por defecto."
8. S8 — "No pueden habilitarse en una configuración identificada como producción."
9. S9 — "La protección deberá probar que" "Las restricciones se aplican en el servidor, no solo en el navegador."
10. S10 — "Crear y consultar cines y salas." "Configurar filas con cantidades de asientos diferentes." "Visualizar el layout sin asumir una cuadrícula rectangular." "Publicar un layout y consultar su versión." "Crear un evento y visualizar sus asientos materializados."
11. S11 — "Tipos de asiento: `standard`, `accessible` y `companion`." "Película: título, duración y clasificación." "Evento: sala, película, fecha/hora, layout publicado y estado."
12. S12 — "Precio: posponer su implementación hasta definir las reglas comerciales, salvo que quieras fijar ya un precio básico en M1." "Versión nueva: copiar inicialmente las filas y asientos de una versión existente para editarlos como borrador."

## Tasks

- T1 — S1–S6, S11–S12 — reversible schema and transactional domain behavior — parent inline + independent verifier — complete; `0002_venue_layout` up/down/up passed, independent verifier reported no blockers, RED observed for direct published insertion, missing clone function, and draft-source copy — commit: `ce745df`
- T2 — S7–S9 — protected local-only administrative access — gentle-ai-worker + independent verifier — complete; guard denies by default and in production, ignores forwarding headers, constant-time token compare; verifier passed S7–S9 — commit: `6771832`
- T3 — S1–S9, S11–S12 — administrative HTTP API and runtime wiring — parent inline + gentle-ai-worker — complete; worker found and fixed a real mount defect (missing StripPrefix); the end-to-end script then exposed a missing JSON field contract, now pinned by a test; read endpoints added so administration can query what it creates — commits: `437f907`, `971ed75`, `812879a`
- T4 — S10 — administrative frontend and tests — gentle-ai-worker — complete; `/admin/venues`, `/admin/layouts`, and `/admin/schedule` with a materialized seat view; the first verifier found S10 partially unmet, and the follow-up work closed it, confirmed by a bounded recheck — commit: `8718083`
- T5 — S1–S12 — documentation and full functional verification — parent inline — complete; `scripts/verify_m1.sh` passed end to end covering the guard, irregular layouts, enabled-only materialization and the read endpoints; `make verify-m0` stayed green — commit: `812879a`

## Log

- L1 — "estoy conforme"
- L2 — "por favor, implementa todas las tareas"
- L3 — "continuamos con todo pero de forma secuenciar. comienza con la continuación de la implementación del esquema directamente"
- L4 — "reanudemos la revision independiente del esquema y continuemos"
- L5 — Independent schema verifier (bounded relaunch) reported no blockers for S1–S6, S11–S12; its three suggested regressions were added to `tests/migrations/m1_schema.sql` and pass.
- L6 — First bounded verifier launch was refused because the candidate declared no high-risk item; relaunched with the genuine declaration (item 1: new durable schema whose down migration destroys M1 data).
- L7 — A worker timeout earlier produced no schema files; the parent implemented the schema directly and the feature continues.
- L8 — `scripts/verify_m1.sh` first failed because orphaned `api` processes held the test ports: killing the parent of `go run` leaves the child server alive, so a stale server with administration disabled answered 404. Both verification scripts now build a binary and run it directly, and fail loudly on a bind error.
- L9 — The same script then exposed a real contract defect: layout responses serialized Go field names, so `id` was missing. JSON tags were added and `TestLayoutJSONContract` pins the wire contract.
- L10 — A worker reported that `pnpm --filter web` matches nothing in this workspace because the package is named `cinema-web`; the root `pnpm check` scripts use `--dir` and were confirmed green instead.
- L11 — The final independent verifier reported S1–S9, S11 and S12 met, one proof gap in S4, and S10 partially unmet (no film, event or seat-view surface). The S4 proof gap was closed by adding a disabled seat to the verification layout, so the enabled-only filter is now discriminating.
- L12 — S10 was closed by adding film/event scheduling, a materialized seat view and cinema/room/layout creation plus read endpoints. A bounded recheck confirmed S10 MET with 89 passing web tests and a green end-to-end script, and reproduced no regression.
- L13 — Gate before closing: `go test ./apps/api/...`, `go vet`, `pnpm check`, isolated up/down/up migrations and `verify_m1.sh` all passed. The tagged M2 concurrency contract remains intentionally RED because M2 is not implemented.
