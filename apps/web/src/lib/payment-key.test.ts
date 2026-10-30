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
