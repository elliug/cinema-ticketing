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
| T2 | S8 | the five cheap defects | 2 · `3c26259` (#60) |
| T3 | S9 | `POST /events` with price and status; local hour in admin reads | 3 · `8560cf1` (#61) |
| T3b | S9 | the schedule form creates on sale in one request | 3b · `9bb7647` (#62) |
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

### L5 — T2 merged, and T3 split in two after overshooting

T2 merged as #60 (`3c26259`), reviewed and approved (`review-d638ef08e07d96ed`, seven
informational findings, one real defect found and deliberately left as T12 rather than widened
in scope).

T3 was planned as one slice and did not fit. The API half — `POST /admin/events` accepting
`priceCents` and `status`, and the administrative reads carrying `startsAtLocal` — came to **670
authored lines in one commit (`501bbb6`), well past the ~400 line boundary this milestone set
for itself**. The cause is worth writing down: the two halves are conceptually separate but
share three files (`venue.go`, `store_postgres.go`, `events.go`), so splitting by file would not
have separated them, and splitting by hunk was not worth the review value it would have bought.
The PR is therefore structured by half so a reviewer can take it in two passes, and the
boundary is recorded here instead of being quietly absorbed.

The web half is **T3b**, deliberately not in T3: switching the schedule form to a single request
changes what the browser gate asserts today ("created as a draft", then "saved"), and disturbing
the gate twice for one feature is exactly the cost T1 was built to stop paying.

Verified by the parent, beyond the writer's word: the PostgreSQL-gated test the writer could not
run (no database URL) was run against a throwaway database migrated by the project's own runner,
proving that the INSERT writes the price and that the read's join renders the venue hour. The
throwaway database was dropped afterwards. The rest: 16 Go packages ok, `gofmt -l` empty,
`go vet` clean, and the differential create-versus-update refusal test passing on eight values.

### L6 — T3 merged, and the M1 gate caught what the suite could not

Merged as #61 (`8560cf1`), in two commits after a defect that only one gate could see.

With the API half in, CI's `foundation` job went red: `POST /events` answered **500** where the M1
gate expected **409**. The cause:

```
ERROR: column "price_cents" of relation "events" does not exist (SQLSTATE 42703)
```

`price_cents` arrives with `0005_checkout`, and `verify-m1` runs the API against a database
migrated only to M1. Writing the column unconditionally therefore broke a schema the
administrative surface is supposed to keep working on. Sixteen Go packages and 336 web tests
were green throughout, because none of them runs the API against a partially migrated database.

The fix: the create picks its INSERT from the caller's **intent** — no price named, no column;
a price (including an explicit zero, the free screening) writes it. The service passes the
caller's pointer rather than its own default, because "named nothing" and "named zero" are now
different statements. `./scripts/verify_m1.sh` passes, which is the only proof that mattered.

**A verification lesson, recorded because it is mine:** earlier the same day I claimed to have
closed the gated-test gap by running "the full gated suite". I had run a `-run` subset, against a
database whose name the tests' own guard rejected — four of them "failed" without testing
anything. When I finally ran the full suite with a valid name, the store passed; the defect was
one layer above it, in the schema. Shortcut verification produced a stronger claim than the
evidence supported, twice in one day.

### L7 — T3b merged, and the gate proved the thing it could not promise

#62 merged (`9bb7647`), and with it the one claim the PR could not make: the browser gate runs
with the two scoped form selectors and passes in both engines. The scheduling form now puts a
screening on sale in one request; the price-and-status form keeps its three refusals by
exercising them on the screening the create just stored, and gains a real edit so the test
still proves a write, not only two refusals.

One coverage trade, recorded: the draft create is no longer driven by the browser gate, because
the same scenario would need a second screening. It stays pinned by unit tests, by the API's
tests, and by `verify-m1`, which creates a draft over HTTP against an M1-only schema.

### L8 — T4 delivered, and the slice that got away from me

`POST /reservations/{id}/cancel` exists and works: a guarded transition, seats released, the
read's own answer shape, paid refused `409 already_paid`, twice not an error, lapsed holds
answered as expired, unknown and malformed ids refused. `ARCHITECTURE.md`'s row stopped claiming
a route nobody could call.

**It came to 1447 authored lines**, the largest slice of the milestone and roughly three and a
half times the boundary this document set. Nothing here is padding — five new files, the
PostgreSQL-gated tests among them, and the commentary this codebase asks for — but a number that
size is a review problem whatever its cause. The mitigation is the PR body: it is written in
layers (store transition → service → route and wiring → docs) so a reviewer can stop between
them. The honest conclusion for the next slices: **splitting the service from the route into two
sequential PRs is the right shape**, and this one is the counter-example that proves it.

Verified by the parent, not taken on the writer's word: the five PostgreSQL-gated tests had not
been run at all (no database URL), so I created a throwaway `cinema_m3…` database, brought it to
`0006` with the project's own runner, and ran them — the release and re-claim, the paid refusal,
the lapsed hold, the unknown id, and **the two racing cancels three times**. All green, database
dropped afterwards. That is the second time in this milestone that a writer's unrun gated tests
were the only untested part of a slice, and the second time they passed once actually run.

**Found, deliberately not fixed:** a cancel can now take a live hold out from under an in-flight
capture, leaving the capture to fail with the provider having already taken the money. Expiry
carries the same window today; this change neither widens nor closes it. Recorded as T13 rather
than absorbed into a slice about cancelling.

### L9 — the CI stopped lying, and T5 is split before it starts

#64 merged (`3cb6f35`): one CI run per ref, so a superseded push cannot finish red and leave its
failure on a pull request that is green. Three false alarms in one session were three too many,
and the eight-line slice cost nine minutes end to end — the cheapest evidence so far that the
milestone's real problem was never effort but **slice size**.

T5 is split for that reason, before writing any of it: **T5a** is the API (`GET /events` pages and
filters, and says how to ask for the next page), **T5b** is the catalogue screen that consumes it.
They are different questions with different failure modes — a cursor that skips a screening versus
a button that never appears — and a reviewer holding both cannot tell which one is wrong when a
gate goes red.

### L10 — T5a delivered, and the size problem is mine, not the writers'

The catalogue pages with a keyset cursor over `(starts_at, id)`, bounds its pages with optional
`from`/`to`, refuses a limit or a cursor it cannot mean by naming the field, and adds a sibling
`page` object the web client can ignore until T5b consumes it. Proven against a real database:
the gated tests the writer could not run — the bounded page, the full walk at four limits, the
**insertion mid-walk that neither repeats nor skips**, the inclusive bounds, the refusal of an
impossible request — all pass once run.

**And it came to 1506 authored lines**, the third slice in a row between 700 and 1500. That is no
longer bad luck or a verbose writer; it is a decomposition problem in this milestone. Two slices
this size would have been caught earlier if I had set the rule before writing them rather than
after: **one behaviour per PR, and a slice whose diff crosses roughly 600 authored lines is
re-scoped before it is reviewed**, not explained afterwards in a body.

What the pattern has cost, concretely: three PRs whose review load was carried by CI rather than
by a reader, and native review declining all three on `under_budget`. A change that is never read
closely is a change whose defects are found by gates or by users, in that order.

Concretely for what remains: T5b is the catalogue screen only. T6 is one migration. T7 is the
redeem schema **or** the endpoint, not both. T8 is one screen. The remaining work is small enough
to be sliced small; the only thing standing in the way was this document not saying so until now.

### L11 — T5a merged; the screen now shows less than the API serves

#65 merged (`7e0e629`). The catalogue API pages with a keyset cursor and refuses what it cannot
mean; the screen ignores the `page` sibling until T5b, so it renders at most 20 screenings with no
way to ask for more. That gap is the slice, and it is one screen wide: T5b is not allowed to
re-open the API.

### L12 — T5 complete: the catalogue is paged end to end

#66 (`26f23ea`) taught the client to read one page; #67 (`77c8459`) is the screen that asks for
it, with the cursor as the address so the next page is a link a person can bookmark, share and
reload. Both were split out of one 619-line slice, and both came in around 300 lines — the first
time the milestone's own size rule produced two PRs that read in one sitting each.

Two sentences on that screen had become false the moment it started paging ("Every screening on
sale right now", "N screenings are on sale") and were corrected in the same PR rather than left
for someone to notice. The last page says nothing about a next one, because the API does not say
how many pages exist and inventing a count is the same class of claim the milestone forbids.

One flake, recorded and not filtered: a pull-request run failed in Firefox with an RSC
`Error in input stream` while the push run of the same commit passed. Re-running was green. If it
returns, it is a real intermittent defect and becomes a task; it is not an allowlist entry.
