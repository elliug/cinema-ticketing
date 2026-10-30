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
