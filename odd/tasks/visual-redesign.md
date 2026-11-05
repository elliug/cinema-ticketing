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
- T2 — S3 — direction A (Marquee) chosen by the user; open, non-blocking: poster images, phone layouts, admin personality — decision recorded in L6
- T3 — S1, S2 — tokens and shell: one dark scheme, amber accent, contrast-checked pairs, spaced-capital brand and marquee strip — done in the T4 slice
- T4 — S1, S2 — the catalogue as a listings board, with the primitives it needed (`PosterArt`, `TimeChip`, `ScreeningRow`, `describeAvailability`) — done, slice commit below
- T5 — S1, S2 — seats and checkout on the same vocabulary (seat plan, hold countdown, receipt summary) — done, slice commits below
- T6 — S1, S2 — tickets: the list, the ticket itself, the emailed link, with a drawn code square — pending
- T7 — S1, S2 — the box office and the venue/room/layout screens — pending
- T8 — S1, S2 — the shell's remaining pages (home, sign-in, the identity link, the footer copy, which is still English) — pending
- T9 — S1 — update the browser gate (`apps/web/e2e/*.spec.ts`): its assertions still pin English copy ("2 seats: Row A seat 1…", "25.00 was taken for this order", "Download the ticket PDF"), so `make verify-m35` fails against the Spanish screens — pending

## Log

- L1 — "Ahora quiero trabajar en un diseño para la web que hemos realizado, la versión actual es una básica para comprobar funcionalidades pero ahora quiero que me diseñes varias ejemplos para hacer todas las pantallas que tenemos. Quiero que sea unas pantallas originales y como un cine real. Hazme 3 versiones y las pensamos antes de implementarlas."
- L2 — Design system synced to `design/system.md` and `design/system.html` from `apps/web/src/app/globals.css` before the first artboard, as the gentle-design gate requires.
- L4 — "quiero que me salga en castellano y no en chino" — the artboards' interface copy moves to Castilian Spanish, so the implementation of the chosen direction includes translating the current English strings of the web app (`cartelera`, `butacas`, `taquilla`, `acceso`, amounts as `12,50 €`). Design-system documentation stays in English; the user-facing boards are Spanish.
- L3 — Directions written to `design/cinema-redesign/`: `01-marquee.html` (dark listings), `02-paper-programme.html` (light print), `03-poster-grid.html` (modern app). Each covers now showing, seat selection, checkout, ticket and door.
- L5 — Design work paused by the user to resume later. The two design commits are the delivery slice boundary: 2,603 authored lines of artboards in `design/`, no application code. Implementation of the chosen direction starts from T2 and splits into tokens (T3), public screens (T4) and administration (T5), each its own reviewable slice.
- L6 — "Vamos a implementar la A, marquesina. Quiero que esté bien construida creando los componentes necesarios." Direction A is the implemented design: dark ink canvas, amber accent, listing-board catalogue, seat plan, receipt-style checkout, punched ticket stub and box-office bar. The work is component-first: tokens in `globals.css`, then the primitives the boards revealed, then the screens. Copy stays Castilian Spanish (L4).
- L7 — Open decisions not yet answered by the user and not blocking: poster art as real images, phone layouts, and whether the box office keeps the same personality. Implementation proceeds with drawn poster art, desktop plus responsive wrapping, and the box office on the same dark shell.
- L8 — Implementation started on branch `design/cinema-redesign`. Slice 1 (T3 + T4): one dark scheme in `globals.css` with contrast-checked token pairs, the spaced-capital brand and marquee strip, and the catalogue rebuilt as a listings board with `PosterArt`, `TimeChip` and `ScreeningRow`. The availability sentence moved into `lib/screening-display.ts` so it can be tested as a rule; two copy-pinning tests were updated to the Castilian labels the redesign chose.
- L9 — Slice 2 done (T5). Verification was a real render, not only unit tests: the local stack came up (`API_ENV=development make bootstrap`, the Go API, `next dev`), the catalogue and the seat screen were driven with Playwright, and three defects only a browser could show were fixed — the chip printed the whole ISO instant instead of `19:00` (`localHour`, with tests), both seeded films drew the same poster (FNV-1a with an avalanche, plus tilt), and the price came out as the API's `12.50` while the receipt printed `12,50 €` (the screens now format `priceCents` with the app's one money formatter).
- L10 — Not verified yet: the browser gate (T9) still expects English strings, so `make verify-m35` is red until it is updated; the footer is still English (T8); and nothing has been checked on a phone-width viewport.
