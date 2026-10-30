# M3.5 — Product web: cartelera, butacas, compra y administración

The backend already walks the whole path (hold, payment, ticket, QR, PDF). This milestone
builds the product on top of it: a web a stranger can use, and an administration surface
that can put a price on a screening without a SQL prompt.

Decisions are quoted from the human in `## Log`; every task names the specs it must satisfy.

## Specs

- **S1 — Build on the existing Next.js app, not a rewrite.** The web stays `apps/web`
  (Next.js App Router, React), and the three M3 pages are kept and improved rather than
  discarded. Client components are used where interaction needs them (seat map, forms);
  data comes from server components.
- **S2 — Identity is a demonstration session, and says so.** A browser-only session picks
  one of two demo people (Ana, Luis) and scopes what the interface shows. It grants **no**
  access control: tickets remain addressable by their identifier, exactly as in M3. The
  interface must label it as a demo wherever it is visible, and no document may describe
  it as authentication. Real accounts are a separate milestone and a backend slice.
- **S3 — This version is public purchase plus administration.** Public: catalogue of
  screenings, seat selection, checkout, ticket with QR and PDF. Administration: cinemas,
  rooms, layouts, screenings and prices.
- **S4 — The price of a screening is settable from the administration.** `events.price_cents`
  stops being a column only SQL can write. A screening created or edited in the interface
  can carry its price, and the public catalogue shows it.
- **S5 — The screens the interface needs exist in the API.** A public catalogue endpoint
  (`GET /events`) so nobody needs to know an event id in advance, and a read of one
  reservation (`GET /reservations/{id}`) so reloading the confirmation keeps its state.
- **S6 — The seat map draws the real layout and is usable without a mouse.** Rows and seat
  labels come from the layout, states (available, held, sold, disabled/accessible) are
  visible without relying on colour alone, the legend matches, and keyboard navigation and
  screen-reader roles are present.
- **S7 — Browser verification comes last.** Playwright drives a real browser through the
  purchase and lands in its own gate (`make verify-m35`) with a CI job, added in the final
  coding slice rather than throughout.
- **S8 — Nothing on screen may claim something the system does not do.** The PDF is a record
  of the sale, not proof of validity; validity is answered by the server; the demo session
  is never presented as authentication; a seat is only shown as selectable if the API says
  it is available.

## Tasks

- T1 — S5 — API: public `GET /events` catalogue (film, cinema, room, start, price, seats
  available) and `GET /reservations/{id}` read — commit
- T2 — S4 — API admin: set a screening's price and expose it in the administrative reads
  — commit
- T3 — S1, S2, S8 — Web shell: design tokens, layout, navigation, and the demo-session
  banner — commit
- T3b — S1, S2, S8 — Adopt Tailwind + shadcn/ui as the component base, superseding the
  styling half of T3 — commit
- T4 — S3, S5 — Public catalogue screen over the new endpoint — commit
- T5 — S3, S6 — Accessible seat map component with the real layout — commit
- T6 — S3, S6 — Checkout flow: selection → hold → payment states → result — commit
- T7 — S3 — Tickets: list for the demo session, detail with QR and PDF download — commit
- T8 — S3, S4 — Administration: venues, rooms, layouts and screenings with price — commit
- T9 — S7 — Playwright gate `make verify-m35` and its CI job — commit
- T10 — S8 — Closing documentation, milestone status and evidence — commit

## Log

- L1 — "Bueno, ahora antes de seguir quiero realizar una web completa que utilice todo el
  backend que hemos implementado. Quiro hacerlo con React con componente. Quiero una buena
  UX. con login, que se vea bien el dibujo de las salas. Quiero una primera versión end to
  end. lo vemos? podría ser M3.5"
- L2 — Decisions taken before implementation: **framework** — keep Next.js and build on it
  (S1); **login** — browser-only demonstration session with no security, labelled as such
  (S2); **scope** — public purchase plus administration (S3); **browser verification** —
  Playwright at the end, in its own gate (S7).
- L3 — Consequence recorded before any code: the demonstration session protects nothing.
  Tickets stay addressable by their identifier, so anyone holding a link sees them. It is
  labelled in the interface and stated here rather than implied to be authentication. Real
  accounts (users, password hashing, sessions, `tickets.user_id`, ownership checks) are a
  backend milestone, not a UI task.
- L4 — T1 extends L3's consequence to the new read and writes it down where the code
  lives: the demonstration session authenticates nobody (S2), so `GET /reservations/{id}`
  is **not** protected either — the reservation identifier is the capability, exactly like
  a ticket id. The projection is therefore limited to what the confirmation screen shows
  (status, deadline, the money actually taken, its seats and its tickets): there is no
  buyer identity to leak, and no payment-attempt internals (attempt, provider, reference,
  idempotency key, fingerprint, outcome) are exposed. Recorded in
  `apps/api/internal/catalog/doc.go`, not merely implied. The catalogue's availability is
  defined there too, as the seat map's definition — neither sold nor held by a live hold,
  decided by the database clock — so the catalogue and the seat page cannot disagree.
- L5 — T3 built the shell the rest of the milestone sits in. **Absence is navigable, not a
  dead link**: the catalogue and "my tickets" entries are plain text with a "not built yet"
  badge, so no entry of the primary navigation points at a route this build does not serve
  (S8); T4 and T7 change each entry's `href` and nothing else. **The demonstration session
  is one module and one sentence**: `DEMO_SESSION_LABEL` — "Demonstration session: it grants
  no access, and nothing here checks who you are." — rendered in the header, the footer, the
  home page and the document metadata from that same constant, with two fixed demo people
  (Ana, Luis) stored per browser under `cinema.demo-session`; the module authenticates
  nothing and no page sends it to the API (extends L3). **No M3 page file was edited**: the
  seat, confirmation and ticket pages inherit header and footer through the root layout, and
  the only change they see is the shared container width. Evidence: 13 tests for the session
  module (default, switching, clearing, per-browser isolation, a throwing storage, and the
  label's content with the words "sign in", "log in", "account", "secure", "protect" and
  "authenticat" refused), 7 structural guards, and `pnpm check` green with 175 tests.
- L6 — T3b adopted Tailwind and shadcn/ui at the human's request, as its own slice, and the
  cost was stated before starting: the ten hand-written components of T3 are superseded.
  What survives is everything that was not cosmetic — the root layout, the demonstration
  session and its single sentence, the navigation with `aria-current`, the honest home page
  and the accessibility decisions. Three workers failed on this task without writing a
  single file (the first two likely on shadcn's interactive CLI, which needs a TTY; the third
  on the same step), so it was finished inline, and only the dependency installation from the
  second attempt was reused. **The shadcn vocabulary is aliased onto the existing tokens**
  with `@theme inline`, so one palette still drives the application and dark mode reaches
  every new component without a second copy. **Preflight is deliberately not imported**: it
  resets margins, headings and form controls, and the seat, confirmation and ticket screens
  still rely on the hand-written rules; importing it now would have changed their appearance
  — a behaviour change disguised as modernisation. It lands, together with the deletion of
  the legacy rules, when the last of those screens migrates to utilities in T5–T7. Twelve
  primitives live in `components/ui/` and are imported by path, the shadcn convention, so the
  semantic barrel cannot shadow them; three new structural guards pin that `ui/` never
  imports from `components/`, that no primitive carries a literal colour, and which
  primitives declare a client boundary and which must stay server components.
- L7 — T2 gave administration the price it could not set: `GET /admin/events`,
  `GET /admin/events/{id}` and `PATCH /admin/events/{id}`, all behind the existing loopback
  plus bearer guard. **A missing field is not a free screening**: the body is decoded as a
  field map, so `{}` is refused with `priceCents: must be provided` while an explicit `0` is
  accepted and stored — a semantics distinction that a struct with a zero value would have
  erased. The write is one guarded statement, `UPDATE … WHERE id = $1 … RETURNING`, so an
  unknown event updates no row and answers `404` instead of claiming a success the database
  never produced, and `SET` names only `price_cents`, leaving the room, film, layout, start
  and status untouched. Documented where it matters: the price is per seat, an order's amount
  is price × seats, and a stored attempt and an issued ticket keep the money actually taken.
  Evidence: handler rules over fakes for every refusal (missing, negative, int4 overflow,
  non-integer, malformed identifier, unknown event, malformed and oversized body), a gated
  SQL suite proving the write, the explicit zero, the refusal without side effects and the
  untouched columns, and a real API run whose `PATCH` answered the stored event with
  `priceCents: 1250` and `priceDisplay: "12.50"`. Still missing for the administration
  screen: `POST /events` cannot set a price in the same request, the list returns identifiers
  without film or room names, and there is no administrative `GET /films`.
- L8 — T4 built the catalogue screen over T1's `GET /events`, the first page whose data a
  stranger can read without an identifier. **The parser is the contract**: `listEvents()` refuses
  what it cannot print — an entry without a film, cinema or room name, without `startsAtLocal`,
  without `priceDisplay`, or without its `seatCount`/`availableSeats` pair fails the whole read as
  `unexpected_response`, naming the field. One unreadable entry refuses the catalogue rather than
  being dropped or shown as "0 of 0 seats available", the same choice the API makes for a venue
  zone it cannot present. **Nothing on the screen is recomputed**: the venue's hour is printed
  verbatim in a `<time>` whose machine value is the UTC instant, the price is `priceDisplay` with
  no currency invented, and the array is rendered in the order served, unsorted. Four honest
  states, none of them invented data: a slow catalogue streams a real region (an `h2`, a
  `role="status"` sentence and decorative skeletons); an unreadable one prints the code, the HTTP
  status and the API's detail and says that an empty list would mean something different; an
  empty one says no screening is on sale and shows no placeholder row; the fourth is the list.
  Availability is words as well as digits — "12 of 40 seats available", "0 of 24 seats available —
  sold out" — with a `Sold out` badge whose word is the state and a link whose text changes with
  it ("See the seat map for X" rather than "Choose seats for X"), so greyscale loses nothing.
  Navigation follows L5's rule at last: "Now showing" gained its `href` and lost the "not built
  yet" badge. **One finding was not T4's to fix and was fixed anyway in the same commit**: the
  hand-written rules of `globals.css` were unlayered while Tailwind's utilities live in
  `@layer utilities`, and an unlayered rule always wins — so `text-muted-foreground` on a
  paragraph and a smaller `CardTitle` were silently inert, and a primary-button link would have
  painted accent on accent. The legacy block now sits in `@layer base`, which is where it belongs
  until each screen migrates and it is deleted. Evidence: 5 tests for the read, `pnpm check` green
  with 183 tests, and a real render of the populated catalogue against a stub API and of the
  failing one against the live API.
- L9 — T5a closed the gap T5 would otherwise have hit: the seat page a buyer reaches from the
  catalogue knew nothing about the screening it was selling. `GET /api/v1/events/{eventID}`
  answers one screening from the `catalog` read model, mounted inside the same wiring decision as
  `GET /events`, so a deployment without the read schema advertises neither. **The entry shape is
  the list's own, not a second one**: one `screeningQuery` projection wrapped by a filter for the
  list and by an identifier predicate for the single read, one `scanScreening`, one
  `screeningDocument` — so availability, the venue's local hour and the price text are produced
  once, and the checkout finally has an amount to multiply (price × seats). **`catalog.Screening`
  gained two fields and the list answers both**, the one contract change of this slice: the pinned
  key set grew from ten to twelve by `status` (`cinema.events.status` verbatim) and `onSale` (the
  list's membership predicate, written once as a column of the projection: `status = 'on_sale' AND
  starts_at > now()`, the database clock the hold window already uses, so no client compares a
  clock of its own to know whether a seat can still be bought). **A screening that exists but is
  not offered answers**: a stale link is a `200` with its status and `onSale: false` — cancelled,
  still a draft, or simply already started — never a `404` that would claim it never existed; only
  an identifier that names nothing is `404`, and an unreadable store or an unusable venue zone is
  `503`, never a partially filled screening. Read-only: the existing availability read was not
  touched. Evidence: handler rules over fakes, a gated SQL suite over a throwaway database
  (migrated 0001–0006 and dropped) where the single read equals the list entry field for field,
  and a real API run whose three responses behave as described.
- L10 — T5 gave the seat screen the screening it belongs to and a keyboard. The page now reads two
  things — `GET /api/v1/events/{eventID}`, which answers `status` and `onSale` from the same
  projection as the catalogue, and the seat map — and renders six states that stay apart: loading;
  the screening does not exist (404, no retry offered); the screening could not be read (503,
  network, 5xx: retryable, never an empty page); not on sale (the context is kept and the reason is
  derived from `status` — cancelled, still a draft, already started, or a status this page does not
  interpret and prints verbatim); the map could not be read (the same retry state, because on sale
  with an unreadable map is not an empty room); and the map. A screening that exists but is not on
  sale is neither a 404 nor a dead seat map, and `availableSeats` decides nothing: it is a count
  from a different moment, the seat read is the map, and when the two disagree the page prints the
  map's numbers and says which count it was read with. **Selection is only offered for what the API
  reports as available**: `sold`, `unavailable` and any state this milestone does not interpret are
  inert, a live `held` flag beats a state that says available, and a snapshot with no state at all
  keeps the older flag-only rule. **The map is usable without a mouse**: every seat is a button
  named after itself ("Row A seat 3, available"), arrows walk rows and seats, Home and End stay
  inside a row, Space and Enter toggle, one roving `tabIndex` makes the map a single tab stop, and
  one polite live region announces the count, the running amount and the reason a seat refused a
  toggle. Each state is words as well as tone — the cell prints its own state word, the border style
  differs per state, and the legend walks one exported list — so the map survives greyscale. A
  200-seat room stays navigable through fixed 44px cells in one horizontally scrolling region that
  is focusable and named, so rows scroll together and no seat shrinks below a touch target at
  360px. The amount is `priceCents × seats` at the screening's own price and says it is a summary:
  the order the API creates is what gets paid, and that is T6's. The keyboard, sale-state, failure
  and money rules are pure functions in `lib/seat-selection.ts` with 39 new tests beside the 40 that
  were already there, and the single read is a parser rule with 5 client tests: a missing `onSale`
  or `status` is refused rather than defaulted, so no page ever compares a clock of its own to
  decide whether a seat can be bought. **The screen left the legacy stylesheet and `globals.css`
  lost nothing** — every rule it stopped using is still the only styling of the ticket,
  confirmation or administration screens, the inventory now sits in the file itself, and the
  preflight stays deferred exactly as L6 recorded. Evidence: `pnpm check` green with 223 tests,
  clean lint and typecheck, and a build whose seat route is 8.03 kB.
- L11 — T6 built the checkout on the order read, and the confirmation page stopped being the one
  purchase screen that could not survive a reload. **A reload loses nothing and charges nothing**:
  the page reads `GET /api/v1/reservations/{id}`, whose projection carries the screening (film,
  cinema, room, the venue's own `startsAtLocal`), the seats, the server deadline, the money actually
  captured and the ticket ids — so a paid order reloads straight into its tickets, and the
  `?eventId=` parameter the seat map still sends is no longer how the page knows where to go back
  to. **The amount is never the page's own**: it prints the payment answer's number after a charge,
  the read's pair on a reload, and *nothing* before a charge, because the read answers `null` while
  unpaid and the only other number available would be `priceCents × seats` — a summary of a price
  that may have moved, which T5 already refused to call a charge. Thirteen outcomes stay thirteen
  states: `approved` carries the tickets and says a ticket is a record of a sale whose validity is
  the scanner's answer; `declined` keeps the seats held and discards its key so the retry mints a
  new one; `payment_in_flight` keeps the key and says only the request that opened the attempt can
  settle it; `already_paid` is not an error but a read; `hold_expired` offers the seat map and
  re-holds nothing behind the buyer; the rest each get their own words and next step, with 404 kept
  distinct from 503. **The window ends out loud**: the countdown comes from the server deadline and,
  at the deadline, replaces the Pay control with an explicit expired state; a paid order keeps its
  old deadline and is not expired, and an unreadable deadline fails closed. Every key disposition is
  *obtained* from the M3 `paymentKeyDisposition` rather than restated, so the new states cannot
  drift from the rule they answer to, and the panel has exactly one polite live region with the
  ticking clock deliberately outside it. **The three files left the legacy stylesheet and
  `globals.css` again lost nothing** — every rule they stopped using is still the only styling of
  the ticket, administration or home screens, and the inventory now names who is left with each.
  Evidence: 27 new tests beside the untouched M3 key tests, `pnpm check` green with 250 tests, and a
  real render of a held, a paid, an already-expired, a missing and an unreachable order against a
  stub API with no error or hydration warning. Not covered: the click path in a browser, which is
  T9's gate.
