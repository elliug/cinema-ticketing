# Visual redesign of every web screen

This feature tracks the visual redesign of the product surfaces after the functional M3.5/M4.1
build: three design directions first, one of them chosen by the user, then implementation.

## Specs

1. S1 — "la versión actual es una básica para comprobar funcionalidades pero ahora quiero que me diseñes varias ejemplos para hacer todas las pantallas que tenemos".
2. S2 — "Quiero que sea unas pantallas originales y como un cine real".
3. S3 — "Hazme 3 versiones y las pensamos antes de implementarlas".

Scope implied by S1 and S3: the three directions cover every screen the web application has
today — catalogue, seat selection, checkout, ticket and the local administration surfaces — and
no implementation starts before the user chooses one.

## Tasks

- T1 — S1–S3 — three design directions under `design/cinema-redesign/` — every screen represented in each direction, manifest written, canvas opened, copy in Castilian Spanish (L4) — commits: `2c98d07` (system sync) and the direction commit on branch `design/cinema-redesign`
- T2 — S3 — user picks one direction and the open decisions are answered — pending human decision (open: poster images, phone layouts, admin personality)
- T3 — S1, S2 — merge the chosen direction's tokens into `apps/web/src/app/globals.css` — pending T2
- T4 — S1, S2 — restyle the public screens (catalogue, seats, checkout, ticket) — pending T3
- T5 — S1 — restyle the administration screens (schedule, venues, layouts, door) — pending T3

## Log

- L1 — "Ahora quiero trabajar en un diseño para la web que hemos realizado, la versión actual es una básica para comprobar funcionalidades pero ahora quiero que me diseñes varias ejemplos para hacer todas las pantallas que tenemos. Quiero que sea unas pantallas originales y como un cine real. Hazme 3 versiones y las pensamos antes de implementarlas."
- L2 — Design system synced to `design/system.md` and `design/system.html` from `apps/web/src/app/globals.css` before the first artboard, as the gentle-design gate requires.
- L4 — "quiero que me salga en castellano y no en chino" — the artboards' interface copy moves to Castilian Spanish, so the implementation of the chosen direction includes translating the current English strings of the web app (`cartelera`, `butacas`, `taquilla`, `acceso`, amounts as `12,50 €`). Design-system documentation stays in English; the user-facing boards are Spanish.
- L3 — Directions written to `design/cinema-redesign/`: `01-marquee.html` (dark listings), `02-paper-programme.html` (light print), `03-poster-grid.html` (modern app). Each covers now showing, seat selection, checkout, ticket and door.
- L5 — Design work paused by the user to resume later. The two design commits are the delivery slice boundary: 2,603 authored lines of artboards in `design/`, no application code. Implementation of the chosen direction starts from T2 and splits into tokens (T3), public screens (T4) and administration (T5), each its own reviewable slice.
