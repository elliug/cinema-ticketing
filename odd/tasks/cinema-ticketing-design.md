# Cinema ticketing architecture

This feature document tracks the approved architecture-design work before application implementation.
It is the entry point for future implementation tasks and reviews.

## Specs

1. S1 — "Quiero desarrollar una aplicacion que sea la ventas de entradas para cine con distintas salas y distintas organizaciones de sala, distintas filas y cada fila puede tener distinto numero de asientos."
2. S2 — "backend y frontend".
3. S3 — "dos usuarios no pueden reservar el mismo asiento simultáneamente".
4. S4 — Reservation uses a hold window with TTL-based automatic release before payment.
5. S5 — Go backend; web frontend for Chrome using Next.js and TypeScript.
6. S6 — Support authenticated users and guest checkout with phone or email.
7. S7 — Include an admin panel, cashier panel, and reports/metrics.
8. S8 — Start with simulated payments behind a replaceable integration boundary.
9. S9 — Defer the virtual waiting room and extreme-peak traffic handling beyond v1.

## Tasks

- T1 — S1–S9 — `ARCHITECTURE.md` — authoritative architecture and scope baseline completed; structural check passed — commit: not created (documentation-only design baseline)
- T2 — S1, S4, S5 — backend/data model implementation — pending definition — commit: pending
- T3 — S2, S6, S7 — frontend and management surfaces — pending definition — commit: pending

## Log

- L1 — "Sí, cierra el diseño en un documento de arquitectura"
- L2 — "reintentar"
