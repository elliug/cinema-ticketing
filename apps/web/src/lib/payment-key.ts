// Payment idempotency-key persistence (M3 · T7 · S1, S3).
//
// The checkout route is idempotent by the `Idempotency-Key` header, and the
// schema makes that key load bearing rather than a nicety:
// `0006_one_open_payment_attempt` allows one open payment attempt per
// reservation, so a second key is refused with `409 payment_in_flight` and only
// the key that opened the attempt can settle it (see
// apps/api/internal/checkout/doc.go). A browser that mints a new key on every
// render therefore cannot pay twice — it cannot finish at all — so the key is
// minted once per reservation and kept across reloads, in the browser, under a
// name that carries the reservation id.
//
// The one exception is a decline. A declined attempt is stored, so resending its
// key replays the refusal instead of starting a new attempt; a declined payment
// is not the end of the order, because the reservation is still `held` until its
// window closes. Discarding the stored key is what makes the retry a new attempt
// with a new key — the opposite of the in-flight case, which keeps it.
//
// This module is deliberately storage-agnostic: it is handed a
// `getItem`/`setItem`/`removeItem` triple, so the rules above are testable
// without a DOM, and the one component that renders the Pay button is the only
// place that touches `window.sessionStorage`.
//
// `sessionStorage` rather than `localStorage` is the panel's choice: a payment
// belongs to the tab that started it, and a key left behind in a shared browser
// profile outlives the hold it belongs to.

/** The storage surface this module needs, satisfied by `window.sessionStorage`. */
export interface PaymentKeyStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

/**
 * Storage-name prefix of a reservation's payment key. It names the artifact
 * ("payment-key") and the deployment's other browser state is namespaced the
 * same way, so a key is never mistaken for anything else a tab stores.
 */
export const PAYMENT_KEY_PREFIX = "cinema.payment-key.";

/** The storage name holding the payment key of one reservation. */
export function paymentKeyStorageName(reservationId: string): string {
  return `${PAYMENT_KEY_PREFIX}${reservationId}`;
}

/**
 * The stored key of this reservation, or `null` when this browser has none.
 *
 * A blank stored value is not a key: a half-written entry must not be sent as
 * `Idempotency-Key: ` and be refused by the API.
 */
export function readPaymentKey(
  storage: PaymentKeyStorage,
  reservationId: string,
): string | null {
  let stored: string | null;
  try {
    stored = storage.getItem(paymentKeyStorageName(reservationId));
  } catch {
    // A browsing mode that refuses storage must not block paying: the answer is
    // simply that this tab has no stored key.
    return null;
  }
  if (typeof stored !== "string") {
    return null;
  }
  const trimmed = stored.trim();
  return trimmed === "" ? null : trimmed;
}

/**
 * The key this reservation's next payment attempt must send: the stored one when
 * this browser already started an attempt, a freshly minted one otherwise.
 *
 * Minting happens at most once per reservation until the key is discarded, which
 * is what makes a page reload resume the attempt instead of starting a second
 * one the schema would refuse.
 */
export function ensurePaymentKey(
  storage: PaymentKeyStorage,
  reservationId: string,
  mint: () => string,
): string {
  const stored = readPaymentKey(storage, reservationId);
  if (stored !== null) {
    return stored;
  }
  const minted = mint();
  try {
    storage.setItem(paymentKeyStorageName(reservationId), minted);
  } catch {
    // The key is still usable for the request about to be sent; only a reload
    // loses it, and then a fresh attempt is the best this tab can do.
  }
  return minted;
}

/**
 * Forgets this reservation's key, so the next attempt mints a new one.
 *
 * Only a settled attempt calls this (see [paymentKeyDisposition]): discarding the
 * key of an attempt that is still open would strand that attempt's money behind a
 * key nobody holds.
 */
export function discardPaymentKey(
  storage: PaymentKeyStorage,
  reservationId: string,
): void {
  try {
    storage.removeItem(paymentKeyStorageName(reservationId));
  } catch {
    // Nothing to repair: an unreadable storage holds no usable key either.
  }
}

/** What one payment attempt answered: a verdict, or the failure it reported. */
export interface PaymentKeyOutcome {
  /** `paymentStatus` from an answered attempt, or `null` when it failed. */
  paymentStatus: string | null;
  /** Problem code of a failed attempt, or `null` when the API answered. */
  problemCode: string | null;
}

/** Whether the stored key survives an answer or must be discarded. */
export type PaymentKeyDisposition = "keep" | "discard";

/**
 * Decides what the stored key does after one attempt's answer.
 *
 *   - `approved`: the order is settled, no attempt can follow, so the key has
 *     nothing left to unblock and is discarded;
 *   - `declined`: the attempt is recorded as declined, so its key would replay
 *     the refusal. The reservation is still `held`, so a retry is a new attempt
 *     with a new key, which is what discarding produces;
 *   - anything else, including `409 payment_in_flight`, a closed hold and a
 *     provider outage: the key stays, because the API's own refusal says to
 *     retry with the same key and only that key can settle the open attempt.
 *
 * An unknown status is kept: the safe direction is the one that cannot abandon
 * an open attempt.
 */
export function paymentKeyDisposition(
  outcome: PaymentKeyOutcome,
): PaymentKeyDisposition {
  if (outcome.paymentStatus === "approved" || outcome.paymentStatus === "declined") {
    return "discard";
  }
  return "keep";
}
