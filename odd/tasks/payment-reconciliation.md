# Reconcile stale payment attempts (issue #87)

The hole #82's guard opened: an attempt left `pending` for ever strands its hold and its
seats. This closes it by asking the provider again, never by inventing a verdict.

Issue: [#87](https://github.com/elliug/cinema-ticketing/issues/87). Branch: `feat/reconcile-stale-payments`.

## Specs

- **S1 — A stale attempt must not strand its hold.** "A hold whose attempt stays `pending`
  past a bounded age is eventually expired and its seats released, **without** reopening #82:
  while an attempt is genuinely in flight (younger than the bound), the sweep must still leave
  the hold alone."
- **S2 — The bound comes from the database clock.** "The age bound and the sweep deadline must
  come from the same clock (the database's, never a process clock — see `DatabaseClock`)."
- **S3 — Gated evidence on real PostgreSQL**, with the constraint this package actually
  enforces: `M3_CHECKOUT_DATABASE_URL`, a database whose name begins with `cinema_m3`, one per
  package, run with `-p 1` (see Log L3).
- **S4 — `make verify-m2`, `make verify-m3` and `make verify-m41` stay green.**
- **S5 — The mechanism was stated before the code.** "Whichever mechanism is chosen, say in
  this issue **before writing code** what a person sees" — done in issue comment
  [`6550428362`](https://github.com/elliug/cinema-ticketing/issues/87#issuecomment-6550428362).
- **S6 — Never assign a verdict the provider did not give.** The reconciler writes only
  `captured` or `declined`, from the provider's own answer, with the attempt's stored amount and
  its deterministic reference. It never writes `'failed'`, which keeps the latent #79 window
  latent, and it never releases a hold whose money question is unanswered.

## Tasks

- **T1 — S1,S2,S6** — store: select stale `pending` attempts with their reservation projection;
  service: `ReconcileStaleAttempts`, reusing `recordFrom` + `RecordOutcome`. Unit tests with
  fakes. — **done** (RED observed as a build failure, then GREEN; 6 unit tests)
- **T2 — S1,S2** — the `Reconciler` worker (bounded batch, per-attempt timeout, logs, defers
  explicitly when the provider does not answer) and its wiring in `cmd/api`, reusing
  `API_EXPIRY_INTERVAL` and `API_HOLD_TTL` so no new knob is invented. — **done**
- **T3 — S3,S4** — gated PostgreSQL test (`cinema_m3_*`, `-p 1`): approved → reservation paid,
  seats sold, tickets issued; declined → attempt decided, then the #82-guarded sweep releases
  the hold; younger than the bound → untouched. Then the three gates. — **in progress**: the test
  passes and proves the composition with #82; it also exposed that no gate ever set
  `M3_CHECKOUT_DATABASE_URL` (see L5), so it is wired into `make verify-m3` with a no-skip
  assertion.
- **T4 — S5** — record the behaviour where a reader looks for it (`GUIDE.md` honest limits and
  `ARCHITECTURE.md` if the surface changed). — commit: pending

- **T5 — S3,S6** — the independent verifier's probes adopted as durable evidence, and its two
  advisories closed: the sub-microsecond age bound refused, and the two honest limits written
  in `GUIDE.md`. — **in progress** on `test/reconciler-probes`

## Slices

Two work-unit commits, because each has to be reviewable and independently green:

1. **The mechanism** — `feat(checkout): settle stale payments by asking the provider again`:
   the store selection, the service method, the worker, its wiring, the unit tests and the
   guide line. Deliberately above the ~400-line target: the `Store` interface method, its
   PostgreSQL implementation, the test fake, the worker and the wiring cannot be split
   further without leaving the package uncompilable, and a commit that does not compile is
   not a reviewable slice.
2. **The proof and the gate** — `test(checkout): prove reconciliation on PostgreSQL`:
   the gated test, the `verify_m3.sh` step that runs the gated suite and refuses a skip, and
   this document. Kept apart on purpose: this is the slice where a reviewer checks that the
   evidence actually runs, which is exactly what was broken before it.
3. **The verifier's evidence, adopted** — `test(checkout): adopt the independent probes as
   durable evidence`: the two probe files, 844 lines. Above the ~400-line target and
   deliberately so: it is one artefact (a suite of probes that compile together and share one
   setup helper), and splitting it would produce commits that neither compile nor run alone.
   It is reviewable as one thing — only tests, no production code.
4. **The guard and the limits** — `fix(checkout): refuse a reconciliation age the query cannot
   express`: `minReconcileAge`, its test case, and the two honest limits in `GUIDE.md`.

## Log

- **L1** — the user's request, verbatim: «hagamos el issue 87». (The issue itself is
  [#87](https://github.com/elliug/cinema-ticketing/issues/87), opened after the native review of
  #82 surfaced `R1-abandoned-pending-attempt-stalls-expiry`.)
- **L2** — decision, chosen by the user from three options: **reconcile by retrying the
  provider**, never mark a stale attempt `failed` and release. The reason it is safe:
  `providerReference(attempt.ID)` is deterministic and `MemoryLedger.record` replays by
  reference, so a re-call cannot double charge and does answer what really happened. Marking
  `failed` was rejected because, if the provider had captured, the buyer is charged, the seat is
  resold and no ticket exists — the exact harm #82 closed.
- **L3** — correction to S3 as written in the issue: the reconciler lives in the `checkout`
  package, whose gated tests read `M3_CHECKOUT_DATABASE_URL` and refuse any database not named
  `cinema_m3*` (`checkout/postgres_test.go`). The issue's "`cinema_m2_*` database of its
  package" describes the reservation package's harness, not this one's. Same rule — isolated
  database per package, `-p 1` — different prefix.
- **L4** — the age bound reuses `API_HOLD_TTL` and the cadence reuses `API_EXPIRY_INTERVAL`
  rather than adding configuration: a `pending` attempt older than its own hold window is
  certainly abandoned, and a new knob would need a default nobody can justify yet.
- **L5** — the gated test exposed a second instrument defect of the same family as #82's:
  **no gate and no CI job ever set `M3_CHECKOUT_DATABASE_URL`**, so every gated test in the
  `checkout` package skipped itself and `make verify-m3` reported green while roughly a
  thousand lines of capture guards never ran. With the variable pointed at the gate's own
  database the whole package passes (21 tests, 0 skips), so `verify_m3.sh` now runs it with
  `-p 1`, asserts my reconciliation test passed by name, and **fails if any gated test
  skipped** — because a skip is not a pass and it is how this suite stayed dead.
- **L6** — the independent verifier (Task Size item 4 escalated) returned **8/8 PASS, nothing
  merge-blocking**, having probed on real PostgreSQL rather than reading: no release with an
  unanswered call, one sale under `Charge` racing three reconciliations, zero deadlocks in a
  three-second contention, the gate fail-closed by negative simulation, and 48 → 60 tests with
  every baseline test still passing. Its nine probes are adopted here as permanent evidence in
  `reconcile_probes_test.go` and `reconcile_limits_test.go`, with the two exploratory `t.Logf`
  advisories turned into assertions.
- **L7** — two advisories closed as code, one as documentation. **Code**: `Duration.Microseconds`
  truncates, so a bound below one microsecond reached the query as zero and meant `now()` — the
  probe showed it returning **two candidates**, one of them a live attempt. `minReconcileAge`
  now refuses it in both the service and the store. **Documentation**: `GUIDE.md` says the worker
  charges the attempt's stored amount rather than today's price, and that "a charge cannot happen
  twice" is the simulated provider's in-process ledger — a deployment's protection is its real
  provider's contract, not a guarantee of this build.
