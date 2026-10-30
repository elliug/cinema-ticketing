// Payment idempotency-key persistence (M3 · T7 · S1, S3).
//
// The API's payment contract decides most of these rules, not this module:
// `0006_one_open_payment_attempt` allows one open attempt per reservation and
// answers any other key with `409 payment_in_flight`, so the only key that can
// settle an attempt is the one that opened it. A browser that invents a fresh
// key on a reload is therefore told, correctly, that a payment is under way —
// which is why the key is minted once per reservation and stored under a name
// that carries the reservation id.
//
// A decline is the one answer that must not reuse the key: the declined attempt
// is recorded, so resending its key replays the refusal instead of charging
// again. Discarding the stored key is what makes a retry a new attempt.
//
// The module is storage-agnostic on purpose: it is given a `getItem`/`setItem`/
// `removeItem` triple, so these rules are testable without a DOM and the browser
// passes `window.sessionStorage` at the one place that touches it.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { PaymentKeyStorage } from "./payment-key.ts";
import {
  discardPaymentKey,
  ensurePaymentKey,
  paymentKeyDisposition,
  paymentKeyStorageName,
  readPaymentKey,
} from "./payment-key.ts";

/** An in-memory stand-in for `window.sessionStorage`. */
function memoryStorage(initial?: Record<string, string>): PaymentKeyStorage & {
  entries: Map<string, string>;
} {
  const entries = new Map<string, string>(Object.entries(initial ?? {}));
  return {
    entries,
    getItem: (key) => entries.get(key) ?? null,
    setItem: (key, value) => {
      entries.set(key, value);
    },
    removeItem: (key) => {
      entries.delete(key);
    },
  };
}

function countingMinter(): { mint: () => string; calls: () => number } {
  let calls = 0;
  return {
    mint: () => {
      calls += 1;
      return `key-${calls}`;
    },
    calls: () => calls,
  };
}

test("the storage name carries the reservation, so two orders never share a key", () => {
  assert.notEqual(
    paymentKeyStorageName("reservation-a"),
    paymentKeyStorageName("reservation-b"),
  );
  assert.match(paymentKeyStorageName("reservation-a"), /reservation-a/);
});

test("the first attempt mints a key and stores it under its reservation", () => {
  const storage = memoryStorage();
  const minter = countingMinter();

  const key = ensurePaymentKey(storage, "reservation-a", minter.mint);

  assert.equal(key, "key-1");
  assert.equal(minter.calls(), 1);
  assert.equal(storage.entries.get(paymentKeyStorageName("reservation-a")), "key-1");
});

test("a page reload reuses the stored key instead of minting a new one", () => {
  const storage = memoryStorage();
  const first = countingMinter();
  const stored = ensurePaymentKey(storage, "reservation-a", first.mint);

  // A reload is a second call against the same storage, with a fresh minter.
  const second = countingMinter();
  const reused = ensurePaymentKey(storage, "reservation-a", second.mint);

  assert.equal(reused, stored);
  assert.equal(second.calls(), 0, "a reload must not invent a key");
});

test("two reservations of one browser keep their own keys", () => {
  const storage = memoryStorage();
  const first = countingMinter();

  const a = ensurePaymentKey(storage, "reservation-a", first.mint);
  const b = ensurePaymentKey(storage, "reservation-b", first.mint);

  assert.notEqual(a, b);
  assert.equal(readPaymentKey(storage, "reservation-a"), a);
  assert.equal(readPaymentKey(storage, "reservation-b"), b);
});

test("a blank stored value is not a key, so a real one is minted", () => {
  const storage = memoryStorage({
    [paymentKeyStorageName("reservation-a")]: "   ",
  });
  const minter = countingMinter();

  const key = ensurePaymentKey(storage, "reservation-a", minter.mint);

  assert.equal(key, "key-1");
  assert.equal(storage.entries.get(paymentKeyStorageName("reservation-a")), "key-1");
});

test("a decline discards the key, so the retry mints a fresh one", () => {
  const storage = memoryStorage();
  const minter = countingMinter();

  const declined = ensurePaymentKey(storage, "reservation-a", minter.mint);
  discardPaymentKey(storage, "reservation-a");

  assert.equal(readPaymentKey(storage, "reservation-a"), null);
  const retry = ensurePaymentKey(storage, "reservation-a", minter.mint);
  assert.equal(retry, "key-2");
  assert.notEqual(retry, declined);
});

test("discarding one reservation leaves the other reservation's key alone", () => {
  const storage = memoryStorage();
  const minter = countingMinter();
  const a = ensurePaymentKey(storage, "reservation-a", minter.mint);
  const b = ensurePaymentKey(storage, "reservation-b", minter.mint);

  discardPaymentKey(storage, "reservation-a");

  assert.equal(readPaymentKey(storage, "reservation-a"), null);
  assert.notEqual(a, b);
  assert.equal(readPaymentKey(storage, "reservation-b"), b);
});

test("an unavailable storage still answers a usable key and never throws", () => {
  const broken: PaymentKeyStorage = {
    getItem: () => {
      throw new Error("storage is not available in this browsing mode");
    },
    setItem: () => {
      throw new Error("storage is not available in this browsing mode");
    },
    removeItem: () => {
      throw new Error("storage is not available in this browsing mode");
    },
  };
  const minter = countingMinter();

  // Paying must not be blocked by a browser that refuses to persist: the attempt
  // is idempotent under the key it sends, and only a reload loses the key.
  const key = ensurePaymentKey(broken, "reservation-a", minter.mint);
  assert.equal(key, "key-1");
  assert.equal(readPaymentKey(broken, "reservation-a"), null);
  assert.doesNotThrow(() => discardPaymentKey(broken, "reservation-a"));
});

test("an approved payment settles the order, so its key is discarded", () => {
  assert.equal(
    paymentKeyDisposition({ paymentStatus: "approved", problemCode: null }),
    "discard",
  );
});

test("a declined payment is a different attempt next time, so its key is discarded", () => {
  assert.equal(
    paymentKeyDisposition({ paymentStatus: "declined", problemCode: null }),
    "discard",
  );
});

test("every request that failed keeps its key, because only that key can settle it", () => {
  const retryable = [
    "payment_in_flight",
    "hold_expired",
    "already_paid",
    "reservation_not_payable",
    "payment_unavailable",
    "service_unavailable",
    "not_found",
    "network_error",
    "unexpected_response",
  ] as const;

  for (const code of retryable) {
    assert.equal(
      paymentKeyDisposition({ paymentStatus: null, problemCode: code }),
      "keep",
      `${code} must keep the key`,
    );
  }
});

// ── The checkout's states (M3.5 · T6 · S3, S8) ──────────────────────────────
//
// The panel that offers the Pay button used to decide these things inline, in JSX,
// where no test could reach them. They are rules, not layout, so they live beside the
// key rules above and are exercised here through their own public functions:
//
//   - `describePaymentOutcome` / `describePaymentFailure` turn one answer into the one
//     state a person can act on, and carry the key disposition that answer implies —
//     computed by `paymentKeyDisposition`, never restated, so the two can never drift;
//   - `describeHoldWindow` turns the read's status and deadline into either a running
//     countdown or an explicit end state, and never invents a deadline of its own;
//   - `describeOrderAmount` decides which number the page may print: the payment
//     answer's after a charge, the order read's before one, and none at all when
//     nothing has been taken. It has no price and no seat count among its inputs, so
//     `price × seats` is not a state it can reach (S8).
import type { PublicErrorCode, PublicPayment, PublicProblem } from "./public-api.ts";
import type { PaymentOutcomeView, PaymentOutcomeKind } from "./payment-key.ts";
import {
  describeHoldWindow,
  describeOrderAmount,
  describePaymentFailure,
  describePaymentOutcome,
} from "./payment-key.ts";

function answeredPayment(overrides: Partial<PublicPayment> = {}): PublicPayment {
  return {
    reservationId: "reservation-a",
    attemptId: "attempt-1",
    paymentStatus: "approved",
    amountCents: 1998,
    ticketIds: ["ticket-1", "ticket-2"],
    ...overrides,
  };
}

function problem(code: PublicErrorCode, status = 409): PublicProblem {
  return {
    type: "https://cinema.local/problems/test",
    title: "Request failed",
    status,
    code,
    detail: `the API answered ${code}`,
  };
}

/** Every state the panel can render, for the cross-cutting rules below. */
function everyOutcome(): { outcome: PaymentOutcomeView; label: string }[] {
  const answers: [string, PublicPayment][] = [
    ["approved", answeredPayment()],
    ["declined", answeredPayment({ paymentStatus: "declined", ticketIds: [] })],
    ["unrecognised status", answeredPayment({ paymentStatus: "refunded" })],
  ];
  const failures: [string, PublicProblem][] = [
    ["payment_in_flight", problem("payment_in_flight")],
    ["already_paid", problem("already_paid")],
    ["hold_expired", problem("hold_expired")],
    ["reservation_not_payable", problem("reservation_not_payable")],
    ["amount_mismatch", problem("amount_mismatch")],
    ["not_sellable", problem("not_sellable", 400)],
    ["not_found", problem("not_found", 404)],
    ["payment_unavailable", problem("payment_unavailable", 503)],
    ["service_unavailable", problem("service_unavailable", 503)],
    ["network_error", problem("network_error", 0)],
    ["idempotency_key_reused", problem("idempotency_key_reused")],
  ];
  return [
    ...answers.map(([label, payment]) => ({
      label,
      outcome: describePaymentOutcome(payment),
    })),
    ...failures.map(([label, failure]) => ({
      label,
      outcome: describePaymentFailure(failure),
    })),
  ];
}

test("an approved answer is a purchase with tickets, and its key is spent", () => {
  const outcome = describePaymentOutcome(answeredPayment());

  assert.equal(outcome.kind, "approved");
  assert.equal(outcome.tone, "success");
  assert.equal(outcome.retryable, false);
  assert.equal(outcome.keyDisposition, "discard");
  // A payment answer reports tickets; nothing here claims one admits its holder.
  assert.match(outcome.message, /ticket/);
  assert.doesNotMatch(`${outcome.message} ${outcome.nextStep}`, /\bvalid\b/i);
});

test("a declined answer leaves the seats held and retries with a new key", () => {
  const outcome = describePaymentOutcome(
    answeredPayment({ paymentStatus: "declined", ticketIds: [] }),
  );

  assert.equal(outcome.kind, "declined");
  assert.equal(outcome.tone, "warning");
  assert.equal(outcome.retryable, true);
  // The refused attempt is recorded, so a retry must be a new attempt.
  assert.equal(outcome.keyDisposition, "discard");
  assert.match(outcome.message, /no money was taken/i);
  assert.match(outcome.nextStep, /still held/i);
  assert.match(outcome.nextStep, /new key/i);
});

test("an unknown payment status is neither an approval nor a failure", () => {
  const outcome = describePaymentOutcome(
    answeredPayment({ paymentStatus: "refunded" }),
  );

  assert.equal(outcome.kind, "unrecognised");
  assert.equal(outcome.retryable, false);
  // The safe direction is the one that cannot abandon an open attempt.
  assert.equal(outcome.keyDisposition, "keep");
  assert.match(outcome.message, /refunded/, "the served status is printed verbatim");
  assert.doesNotMatch(outcome.headline, /approved|declined/i);
});

test("every outcome's key disposition is the one the M3 rule already decides", () => {
  const cases: [PaymentOutcomeView, Parameters<typeof paymentKeyDisposition>[0]][] = [
    ...everyOutcome().map(
      ({ outcome }) =>
        [
          outcome,
          {
            paymentStatus:
              outcome.kind === "approved"
                ? "approved"
                : outcome.kind === "declined"
                  ? "declined"
                  : outcome.kind === "unrecognised"
                    ? "refunded"
                    : null,
            problemCode:
              outcome.kind === "approved" ||
              outcome.kind === "declined" ||
              outcome.kind === "unrecognised"
                ? null
                : "hold_expired",
          },
        ] as [PaymentOutcomeView, Parameters<typeof paymentKeyDisposition>[0]],
    ),
  ];

  for (const [outcome, input] of cases) {
    assert.equal(
      outcome.keyDisposition,
      paymentKeyDisposition(input),
      `${outcome.kind} must keep the key rule in one place`,
    );
  }
});

test("the in-flight conflict asks for the key that is already open", () => {
  const outcome = describePaymentFailure(problem("payment_in_flight"));

  assert.equal(outcome.kind, "payment_in_flight");
  assert.equal(outcome.tone, "warning");
  assert.equal(outcome.retryable, true);
  assert.equal(outcome.keyDisposition, "keep");
  assert.match(outcome.message, /only the request that started it/i);
});

test("already_paid is not an error: it points at the order's own tickets", () => {
  const outcome = describePaymentFailure(problem("already_paid"));

  assert.equal(outcome.kind, "already_paid");
  assert.equal(outcome.tone, "info");
  assert.equal(outcome.retryable, false);
  assert.match(outcome.nextStep, /tickets/i);
  assert.match(outcome.nextStep, /reads/i, "the read is what knows the tickets");
});

test("an expired hold offers the seat map and nothing that could charge", () => {
  const outcome = describePaymentFailure(problem("hold_expired"));

  assert.equal(outcome.kind, "hold_expired");
  assert.equal(outcome.offersSeatMap, true);
  assert.equal(outcome.retryable, false);
  assert.match(outcome.message, /released/i);
  assert.match(outcome.message, /no money was taken/i);
  // The page must never re-hold on its own: only the seat map takes a hold.
  assert.match(outcome.nextStep, /seat map/i);
  assert.match(outcome.nextStep, /never takes a hold/i);
});

test("the refusals that are not each other stay apart", () => {
  const kinds: [PublicProblem, PaymentOutcomeKind][] = [
    [problem("reservation_not_payable"), "not_payable"],
    [problem("amount_mismatch"), "amount_mismatch"],
    [problem("not_sellable", 400), "not_sellable"],
    [problem("not_found", 404), "not_found"],
  ];

  const headlines = new Set<string>();
  for (const [failure, kind] of kinds) {
    const outcome = describePaymentFailure(failure);
    assert.equal(outcome.kind, kind);
    assert.equal(outcome.retryable, false, `${kind} cannot be fixed by asking again`);
    assert.equal(outcome.offersSeatMap, false);
    headlines.add(outcome.headline);
  }
  assert.equal(headlines.size, kinds.length, "each refusal says something of its own");
});

test("a temporary failure is retryable with the same key, whatever its code", () => {
  const cases: [PublicProblem, string][] = [
    [problem("payment_unavailable", 503), "payment_unavailable"],
    [problem("service_unavailable", 503), "service_unavailable"],
    [problem("coordination_unavailable", 503), "coordination_unavailable"],
    [problem("network_error", 0), "network_error"],
    [{ ...problem("unexpected_response"), status: 502 }, "a 502"],
  ];

  for (const [failure, label] of cases) {
    const outcome = describePaymentFailure(failure);
    assert.equal(outcome.kind, "temporary", label);
    assert.equal(outcome.retryable, true, label);
    assert.equal(outcome.keyDisposition, "keep", label);
    assert.match(outcome.nextStep, /same key/i, label);
  }
});

test("no two states share a headline", () => {
  // Codes that intentionally collapse into one state (`payment_unavailable` and
  // `service_unavailable` are both a temporary failure) share its words on purpose; two
  // different *states* reading the same would hide which one happened.
  const seen = new Map<string, string>();
  for (const { label, outcome } of everyOutcome()) {
    const previous = seen.get(outcome.kind);
    if (previous === undefined) {
      seen.set(outcome.kind, outcome.headline);
      continue;
    }
    assert.equal(
      outcome.headline,
      previous,
      `${label} is the ${outcome.kind} state, so it must use that state's one headline`,
    );
  }
  assert.equal(seen.size, new Set(seen.values()).size, "each state says something of its own");
});

// ── The hold window, from the server's deadline ─────────────────────────────

const DEADLINE = "2026-05-04T18:40:00Z";
const BEFORE_DEADLINE = Date.parse("2026-05-04T18:30:00Z");
const AFTER_DEADLINE = Date.parse("2026-05-04T18:41:00Z");

test("a live hold counts down from the deadline the read served", () => {
  const window = describeHoldWindow(
    { status: "held", expiresAt: DEADLINE },
    BEFORE_DEADLINE,
  );

  assert.equal(window.kind, "counting");
  assert.equal(window.remainingMs, 10 * 60 * 1000);
  assert.equal(window.endLabel, null);
  assert.equal(window.notice, null);
  assert.equal(window.payable, true);
});

test("the window ends in an explicit expired state, decided by the server clock", () => {
  const window = describeHoldWindow(
    { status: "held", expiresAt: DEADLINE },
    AFTER_DEADLINE,
  );

  assert.equal(window.kind, "expired");
  assert.equal(window.remainingMs, 0);
  assert.equal(window.endLabel, "Hold expired");
  assert.equal(window.payable, false, "a page past its deadline must not offer a charge");
  assert.match(window.notice ?? "", /expired/i);
  assert.match(window.notice ?? "", /choose them again/i);
});

test("an unreadable deadline fails closed instead of promising a window", () => {
  for (const expiresAt of ["", "not-a-date", "2026-13-45T99:99:99Z"]) {
    const window = describeHoldWindow(
      { status: "held", expiresAt },
      BEFORE_DEADLINE,
    );
    assert.equal(window.kind, "expired", expiresAt);
    assert.equal(window.payable, false, expiresAt);
  }
});

test("a paid order keeps its deadline and is not expired", () => {
  // The API keeps the hold deadline of a paid order; its state is the status, so the
  // page must not read "expired" from a clock that has since passed it.
  const window = describeHoldWindow(
    { status: "paid", expiresAt: DEADLINE },
    AFTER_DEADLINE,
  );

  assert.equal(window.kind, "paid");
  assert.equal(window.payable, false);
  assert.equal(window.endLabel, "Paid");
  assert.equal(window.notice, null);
});

test("a cancelled order says it was cancelled, and a status it does not know is printed", () => {
  const cancelled = describeHoldWindow(
    { status: "cancelled", expiresAt: DEADLINE },
    BEFORE_DEADLINE,
  );
  assert.equal(cancelled.kind, "cancelled");
  assert.equal(cancelled.payable, false);
  assert.match(cancelled.notice ?? "", /cancelled/i);

  const unknown = describeHoldWindow(
    { status: "refund_pending", expiresAt: DEADLINE },
    BEFORE_DEADLINE,
  );
  assert.equal(unknown.kind, "unknown");
  assert.equal(unknown.payable, false);
  assert.match(unknown.notice ?? "", /refund_pending/);
});

test("a payment under way still counts down and can be retried", () => {
  // `payment_pending` means an attempt is open, not that the hold is over: the window
  // is still the server's, and the stored key is what settles the attempt.
  const window = describeHoldWindow(
    { status: "payment_pending", expiresAt: DEADLINE },
    BEFORE_DEADLINE,
  );

  assert.equal(window.kind, "counting");
  assert.equal(window.payable, true);
});

// ── Which number the page may print ─────────────────────────────────────────

test("before paying, the API states no amount and the page prints none", () => {
  const amount = describeOrderAmount({ amountCents: null, amountDisplay: null }, null);

  // An unpaid order has no charge, and the page owns no price: the number it would
  // have to print is `priceCents × seats`, which is exactly what this function cannot
  // compute because neither is one of its inputs (S8).
  assert.equal(amount.source, "none");
  assert.equal(amount.amountCents, null);
  assert.equal(amount.amountLabel, null);
  assert.equal(amount.known, false);
  assert.match(amount.note, /no amount/i);
});

test("after a payment the amount is the one the answer reported", () => {
  const amount = describeOrderAmount(
    { amountCents: null, amountDisplay: null },
    answeredPayment({ amountCents: 2540 }),
  );

  assert.equal(amount.source, "payment");
  assert.equal(amount.amountCents, 2540);
  assert.equal(amount.amountLabel, "25.40");
  assert.equal(amount.known, true);
});

test("a reload prints the amount the read serves, and leaves its text alone", () => {
  const amount = describeOrderAmount(
    { amountCents: 1998, amountDisplay: "19.98" },
    null,
  );

  assert.equal(amount.source, "reservation");
  assert.equal(amount.amountLabel, "19.98");
  assert.match(amount.note, /read/i);
});

test("an amount whose served text is missing is still the read's own number", () => {
  const amount = describeOrderAmount(
    { amountCents: 1250, amountDisplay: null },
    null,
  );

  assert.equal(amount.source, "reservation");
  assert.equal(amount.amountLabel, "12.50");
});

test("a declined answer is not money taken", () => {
  const amount = describeOrderAmount(
    { amountCents: null, amountDisplay: null },
    answeredPayment({ paymentStatus: "declined", amountCents: 1998, ticketIds: [] }),
  );

  assert.equal(amount.source, "none");
  assert.equal(amount.amountLabel, null);
});
