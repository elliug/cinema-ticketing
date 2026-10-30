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
