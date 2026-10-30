# M4.1 the door, and M5 hardening, plus three loose gaps

Issue: [#58](https://github.com/elliug/prueba-gentle/issues/58). One milestone, three groups,
because none of them adds a surface a buyer sees and all three close holes a green build hides.

## Specs

- **S1 — A ticket admits exactly one person, once.** Redeeming a valid, unredeemed ticket marks
  it redeemed and answers with the ticket and the moment it was used. The same ticket presented
  again does **not** admit: the answer names the first redemption's hour, so a cashier can say
  "this was used at 20:14" instead of "invalid".
- **S2 — Two doors racing the same code is the normal case.** Exactly one redemption succeeds.
  The loser is told it was already redeemed; neither both, nor neither. The guarantee is one
  atomic write with a database constraint behind it, not a read-then-write in application code.
- **S3 — A forged code redeems nothing.** Verification and redemption are one server answer, so
  a code the server cannot verify never reaches the "already redeemed" state and never changes a
  stored row.
- **S4 — The door screen uses the existing local administrative surface.** `/admin/door` takes
  the code, offers one action, and prints an answer a cashier cannot misread. It authenticates
  nobody new: it uses the same local administration and token as the rest of `/admin`. Who the
  staff member is belongs to the identity milestone.
- **S5 — The PDF stays a record of the sale.** It is rendered when the payment is captured and
  never changes. The door trusts `POST /tickets/redeem`, never the printed page.
- **S6 — The M3.5 gate must exercise an origin the browser does not consider secure.** Today it
  navigates to `127.0.0.1`, which **is** a secure context, so a whole class of defect — anything
  the browser gates on origin — is invisible to it. That is how `crypto.randomUUID` and the
  absolute ticket links both escaped. After this slice the gate fails if it is pointed at a
  secure origin, so the insecure case cannot silently disappear.
- **S7 — The M3.5 gate exercises a second browser engine**, so "works in the browser" stops
  meaning "works in Chromium".
- **S8 — The five cheap defects close**: the API path prefix mirrored in `lib/ticket-media.ts`
  (R3-001), the M3.5 gate's run scope (R1-run-scope), two unformatted Go files, the shared Redis
  index between `verify-m2` and `verify-m3`, and the development seed's string-comparison guard.
- **S9 — `POST /events` carries `price` and `status`**, so putting a screening on sale is one
  request; and the administrative reads carry the venue's local hour beside `startsAt`, never
  instead of it.
- **S10 — `POST /reservations/{reservationId}/cancel` exists and works.** It releases the seats
  and answers with the reservation's new state. Cancelling a reservation that is already paid is
  refused explicitly, and cancelling twice is not an error the second time.
- **S11 — The catalogue paginates and filters.** `GET /events` stops returning everything on
  sale, and the response says how to ask for the next page.
- **S12 — Cinemas stored with a timezone that does not resolve are repaired** by a migration,
  so tickets issued before #55 can render again. The migration reports what it changed.
- **S13 — Every route in `ARCHITECTURE.md` exists in the code.** A table that names a surface
  nobody can call costs more than an empty cell; #57 marked the one known case, this slice keeps
  it true.
- **S14 — Email is decided before it is built.** If the answer is "simulated like payments", it
  reuses the existing outbox and says so on screen; if it is a real provider, it needs credentials
  and a delivery story this milestone does not assume.
- **S15 — The Redis `{hold}` hash tag stops concentrating every hold key in one cluster slot**,
  with a compatibility note for holds written by the previous scheme.

## Tasks

| ID | Specs | Route | Slice |
|---|---|---|---|
| T1 | S6, S7 | gate: insecure origin + second engine | 1 · `8b2afc0` (#59) |
| T2 | S8 | the five cheap defects | 2 |
| T3 | S9 | `POST /events` with price and status; local hour in admin reads | 3 |
| T4 | S10 | `POST /reservations/{id}/cancel` end to end | 4 |
| T5 | S11 | catalogue pagination and filters | 5 |
| T6 | S12 | timezone backfill migration | 6 |
| T7 | S1, S2, S3 | redemption schema and `POST /tickets/redeem` | 7 |
| T8 | S4, S5 | the door screen | 8 |
| T9 | S1–S4 | `make verify-m41` and its CI job | 9 |
| T10 | S15 | Redis slot distribution | 10 |
| T11 | S13, S14 | route-table truth; email decision then implementation | 11 |

## Log

### L1 — the request, verbatim

> vamos a comenzar con M4.1 y M5 junto con los 3 huecos sueltos. juntalo todo en una issue.
> comienza solo cuando termines y mergeemos lo que está en vuelo

### L2 — the issue, and the decisions recorded in it

Issue #58 groups three things: **M4.1 the door** (single-use redemption), **M5 hardening** (eleven
defects, each measured — a review finding, a gate failure, or a defect found by using the
product), and **the three loose gaps** (`POST /reservations/{id}/cancel`, catalogue pagination,
email).

Decisions taken before implementation, with their consequences stated up front:

- **Redemption needs no buyer identity.** The door uses the existing local administration and
  token. Who the staff member is belongs to the identity milestone, and the record says so.
- **Validity is always the server's answer.** The PDF is a record of the sale and cannot change
  after issue; the door trusts the server, never the printed page.
- **Two doors racing is the normal case.** One atomic write and a constraint, not a read-then-write.
- **Hardening ships inside this milestone, gate first.** A redemption proven only by `localhost`
  would repeat the exact class of defect that produced the list.
- **Email is decided before it is built.** Simulated or real is the human's call; the slice
  begins with that answer rather than with code.

### L3 — why the hardenening list exists

Four defects reached a delivered milestone within one hour of real use, and none was caught by
315 unit tests, the browser gate, or native review:

| Defect | What was assumed | Fixed in |
|---|---|---|
| `crypto.randomUUID is not a function` | that every origin is a secure context | #53 |
| the ticket's QR blank, the PDF "page not found" | that `127.0.0.1` means the same to the server and the buyer | #54 |
| a timezone the API could never render | that an invented `America/Madrid` did not matter | #55 |
| a forward with no deadline, a header that could carry a CRLF | that the URL path only ever holds a UUID | #56 |

The common thread is in the spec that opens this milestone: each was true only where it was
written. S6 exists so the gate stops being blind to that class, and it is T1 for that reason.

### L4 — T1 delivered: the gate now fails for the right reasons

Merged as #59 (`8b2afc0`). The gate drives the machine's own non-loopback IPv4 instead of
loopback, refuses to fall back to a secure origin, runs Chromium and Firefox against one
served stack, and carries `e2e/insecure-origin.spec.ts`, which asserts from inside the browser
that `isSecureContext` is false and `crypto.randomUUID` is undefined. Two receipts approved it:
`review-ec69f0632e46a78c` for the slice (high risk, process boundary in the workflow) with five
informational findings, and `review-e8a2f6cd7a12eaa2` for the delta — two files, 59 lines,
medium, no findings at all.

**The gate found three defects while being built, and only one was its own.** In order:

1. A constant film title in the fixture. Both engines share one stack, so the second engine
   found two catalogue entries and `toHaveCount(1)` failed, correctly. The fixture assumed a
   virgin database; the suffix that fixed it also makes the test honest on a reused stack.
2. A console error only Firefox reports: `Failed to fetch RSC payload … Falling back to
   browser navigation`. It looked like noise to filter. It was not.
3. The cause of (2), in the product: the schedule screen renders one link per screening, all to
   *the same route with a different query*, and Next prefetched every one — a full render per
   screening for a navigation the operator may never take. `prefetch={false}` removed a real
   waste and the symptom with it. **Filtering the console would have hidden the next error that
   looked the same**, which is why the rule stayed as it was.

Three CI round trips, roughly five minutes each, and a `visit()` that waits a bounded half
second for a route's prefetches so the test stops aborting them.

**Recorded honestly:** the first review's `R1-run-scope` finding was never written down anywhere
this session can read, so T2 does not pretend to fix it. What T2 does instead is re-derive the
isolation question from the code itself — the gate's `M35_*` overrides are advisory, and a
Redis index of `0` would point the gate at the development database's neighbour. If the
original finding said something else, this log is where that becomes visible.
