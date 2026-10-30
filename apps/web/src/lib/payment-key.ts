// Payment idempotency-key persistence, and the decisions that go with it
// (M3 · T7 and M3.5 · T6 · S1, S3, S8).
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
//
// What T6 added below is marked off from the M3 key rules, which are unchanged: the
// confirmation page used to decide in JSX what an answer meant, what the deadline
// was worth and which number it could print, and none of that was reachable by a
// test. Those are rules, so they live here beside the key rule they answer to:
//
//   - `describePaymentOutcome` / `describePaymentFailure` turn one answer into one
//     state a person can act on, and carry the key disposition that answer implies —
//     obtained from `paymentKeyDisposition` above, never restated, so the two can
//     never disagree;
//   - `describeHoldWindow` turns the read's status and deadline into a running
//     countdown or an explicit end state, and never invents a deadline;
//   - `describeOrderAmount` decides which number the page may print, and has no price
//     and no seat count among its inputs, so `price × seats` is not a state it can
//     reach (S8).
import { formatCents, remainingMilliseconds } from "./seat-selection.ts";
import type { PublicPayment, PublicProblem } from "./public-api.ts";

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

// ── What one payment attempt answered (M3.5 · T6 · S3, S8) ───────────────────
//
// The API's payment contract answers thirteen distinguishable things, and the panel
// that shows them is only honest if they stay distinguishable: a decline frees the
// reservation for a new attempt, an open attempt needs the very same key, an expired
// hold needs the seat map again, and a provider outage needs nothing but patience.
// Each one is one state with a headline, a sentence about what happened, the next
// thing a person can do, and the key disposition the answer implies.
//
// Money is deliberately absent from every sentence here: `describeOrderAmount` below
// is the one place that produces a number, so no state can acquire a second opinion
// about what was charged.

/** What one payment attempt's answer was, in the terms the panel renders. */
export type PaymentOutcomeKind =
  /** The provider charged, and the order became tickets. */
  | "approved"
  /** The provider refused this attempt; the order is still held. */
  | "declined"
  /** Another attempt on this order is open; only its key can settle it. */
  | "payment_in_flight"
  /** The order already carries a captured payment; its tickets belong to the order. */
  | "already_paid"
  /** The hold window closed: the seats are gone and nothing may be re-held here. */
  | "hold_expired"
  /** The order is not awaiting payment. */
  | "not_payable"
  /** The attempt and the order disagree about the amount, so nothing was charged. */
  | "amount_mismatch"
  /** There is no payable amount: no price, or no seat. */
  | "not_sellable"
  /** The order named by this address does not exist. */
  | "not_found"
  /** The provider or the reservation service is temporarily unavailable. */
  | "temporary"
  /** The API refused the request with a code this milestone does not interpret. */
  | "refused"
  /** The API answered a verdict this milestone does not interpret. */
  | "unrecognised";

/** One payment outcome, as the surface renders it. */
export interface PaymentOutcomeView {
  kind: PaymentOutcomeKind;
  /** The word that leads: the state itself, not a tone. */
  headline: string;
  /** What happened, in the API's own terms. */
  message: string;
  /** What the person can do about it. */
  nextStep: string;
  /** The tone reinforces the words; it never carries them on its own. */
  tone: "success" | "info" | "warning" | "error";
  /** Whether another attempt is the right thing to offer. */
  retryable: boolean;
  /**
   * What the stored key does after this answer: the M3 rule above, applied rather
   * than restated, so an outcome can never disagree with the key it sends.
   */
  keyDisposition: PaymentKeyDisposition;
  /** Whether the seat map is the honest next place to go. */
  offersSeatMap: boolean;
}

/**
 * The state of an answered attempt (`200`, whose body says approved or declined).
 *
 * A verdict this milestone does not know is neither an approval nor a failure: the
 * served status is printed verbatim and the key is kept, because the safe direction
 * is the one that cannot abandon an open attempt.
 */
export function describePaymentOutcome(
  payment: PublicPayment,
): PaymentOutcomeView {
  if (payment.paymentStatus === "approved") {
    return {
      kind: "approved",
      headline: "Payment approved",
      message:
        "The box office took the money for this order and issued one ticket per seat.",
      nextStep:
        "Open each ticket below, or keep its link: nothing lists a guest's tickets, so the id of a ticket is how it is opened again. A ticket is the record of a sale — whether it still admits you is answered by the box office when it is scanned.",
      tone: "success",
      retryable: false,
      keyDisposition: paymentKeyDisposition({
        paymentStatus: payment.paymentStatus,
        problemCode: null,
      }),
      offersSeatMap: false,
    };
  }

  if (payment.paymentStatus === "declined") {
    return {
      kind: "declined",
      headline: "The payment was declined",
      message: `The provider refused this attempt, so no money was taken and no ticket was issued.`,
      nextStep:
        "Your seats are still held until the window closes. Trying again starts a new attempt with a new key, so this refusal is never replayed — the order is not lost.",
      tone: "warning",
      retryable: true,
      keyDisposition: paymentKeyDisposition({
        paymentStatus: payment.paymentStatus,
        problemCode: null,
      }),
      offersSeatMap: false,
    };
  }

  return {
    kind: "unrecognised",
    headline: "The payment answer was not a verdict this page knows",
    message: `The box office answered "${payment.paymentStatus}".`,
    nextStep:
      "Nothing is concluded from it: reload this page to read the order's stored state. This page does not decide on its own whether you were charged.",
    tone: "info",
    retryable: false,
    keyDisposition: paymentKeyDisposition({
      paymentStatus: payment.paymentStatus,
      problemCode: null,
    }),
    offersSeatMap: false,
  };
}

/**
 * The state of a refused payment attempt, one problem code at a time.
 *
 * The cases are the API's own (`409 payment_in_flight`, `409 already_paid`, `409
 * hold_expired`, `409 reservation_not_payable`, `409 amount_mismatch`, `400
 * not_sellable`, `404 not_found`, the two `503`s) plus the transport failures, and
 * they stay apart because each needs a different thing from the buyer. A 404 is never
 * rendered as a 503: one is final and the other is worth asking again.
 */
export function describePaymentFailure(
  problem: PublicProblem,
): PaymentOutcomeView {
  const disposition = paymentKeyDisposition({
    paymentStatus: null,
    problemCode: problem.code,
  });

  switch (problem.code) {
    case "payment_in_flight":
      return {
        kind: "payment_in_flight",
        headline: "A payment is already under way",
        message:
          "This order already has a payment attempt in progress. Only the request that started it can settle it, so a second key would be refused — and this page is holding the first.",
        nextStep:
          "Retry from the tab that opened it, or reload this page once the provider has answered: the order read then says what happened. Nothing is charged twice either way.",
        tone: "warning",
        retryable: true,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "already_paid":
      return {
        kind: "already_paid",
        headline: "This order is already paid",
        message:
          "The order already carries a captured payment, so there is nothing left to charge.",
        nextStep:
          "The tickets belong to the order, not to this answer: the page reads the order and shows them, so no ticket is lost by a reload.",
        tone: "info",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "hold_expired":
      return {
        kind: "hold_expired",
        headline: "This hold has expired",
        message:
          "The hold window closed before the payment completed, so the seats were released and no money was taken.",
        nextStep:
          "Choose the seats again from the seat map. This page never takes a hold on its own: only the seat map can do that.",
        tone: "error",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: true,
      };

    case "reservation_not_payable":
      return {
        kind: "not_payable",
        headline: "This order cannot be paid",
        message:
          "The order is not awaiting payment, so this page cannot charge it.",
        nextStep:
          "Read the order's state above. If its hold has ended, the seats are released and choosing them again is the only way back.",
        tone: "error",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "amount_mismatch":
      return {
        kind: "amount_mismatch",
        headline: "The amount did not match",
        message:
          "The amount recorded for this attempt disagrees with the amount the order would be charged now, so the checkout refused to charge at all.",
        nextStep:
          "Do not pay again: this needs the box office, with this order's reference. Nothing was taken.",
        tone: "error",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "not_sellable":
      return {
        kind: "not_sellable",
        headline: "There is nothing to charge",
        message:
          "The screening has no price, or the order holds no seat, so the box office has no amount to take.",
        nextStep:
          "Back to the catalogue to pick a screening that is on sale.",
        tone: "error",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "not_found":
      return {
        kind: "not_found",
        headline: "This order does not exist",
        message:
          "The reservation identifier in this address is unknown to the box office, so there is nothing to pay.",
        nextStep:
          "Open the order from the seat map that created it, or start again from the catalogue.",
        tone: "error",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "payment_unavailable":
    case "service_unavailable":
    case "coordination_unavailable":
    case "network_error":
      return {
        kind: "temporary",
        headline: "The payment could not be completed",
        message:
          "The payment provider or the reservation service is temporarily unavailable, so this attempt has not finished.",
        nextStep:
          "Retry with the same key: it settles the attempt this page already opened and cannot charge twice. If your window closed in the meantime, the answer will say so.",
        tone: "warning",
        retryable: true,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    default:
      if (problem.status >= 500) {
        return {
          kind: "temporary",
          headline: "The payment could not be completed",
          message:
            "The payment provider or the reservation service is temporarily unavailable, so this attempt has not finished.",
          nextStep:
            "Retry with the same key: it settles the attempt this page already opened and cannot charge twice. If your window closed in the meantime, the answer will say so.",
          tone: "warning",
          retryable: true,
          keyDisposition: disposition,
          offersSeatMap: false,
        };
      }
      return {
        kind: "refused",
        headline: "The payment was refused",
        message: `The box office answered ${problem.code}: ${problem.detail}`,
        nextStep:
          "Nothing was assumed about it. A retry resends the attempt this page already opened with its own key, so it cannot charge twice.",
        tone: "error",
        retryable: true,
        keyDisposition: disposition,
        offersSeatMap: false,
      };
  }
}

// ── The hold window, from the server's deadline (M3.5 · T6 · S3, S8) ────────
//
// The countdown the confirmation page shows is the server's, and so is the moment it
// ends: `expiresAt` is the durable deadline the API recorded, and the page only
// subtracts the clock from it. The end is an explicit state, because the alternative —
// a page that quietly stops working — leaves a buyer pressing a button that cannot do
// anything. Reaching that state never re-holds the seats: a hold is the seat map's
// business, and taking one behind the buyer's back would be a reservation nobody asked
// for.

/** How an order's hold window currently stands. */
export type HoldWindowKind =
  /** The window is live: count it down from the server deadline. */
  | "counting"
  /** The deadline passed (or was unreadable): the seats are gone. */
  | "expired"
  /** The order was paid, so the deadline no longer limits anything. */
  | "paid"
  /** The order was cancelled, so its seats are released. */
  | "cancelled"
  /** A status this milestone does not interpret; printed verbatim, never guessed. */
  | "unknown";

export interface HoldWindowView {
  kind: HoldWindowKind;
  /** Milliseconds left, and 0 unless the window is counting. */
  remainingMs: number;
  /** The end state's own word (`Hold expired`, `Paid`), or null while counting. */
  endLabel: string | null;
  /** One sentence about the end state, or null while the window is live. */
  notice: string | null;
  /** Whether offering a payment is honest right now. */
  payable: boolean;
}

/**
 * The state of one order's hold window at one moment.
 *
 * `paid` is checked before the deadline, because the API keeps the deadline of a paid
 * order: its state is the status, and a page that read "expired" from a clock that has
 * since passed it would tell a buyer their purchase was undone. Every other live status
 * ends at the server's deadline, and an unreadable deadline counts as elapsed, which
 * fails closed.
 */
export function describeHoldWindow(
  order: { status: string; expiresAt: string },
  nowMs: number,
): HoldWindowView {
  const status = order.status.trim().toLowerCase();

  if (status === "paid") {
    return {
      kind: "paid",
      remainingMs: 0,
      endLabel: "Paid",
      notice: null,
      payable: false,
    };
  }
  if (status === "cancelled") {
    return {
      kind: "cancelled",
      remainingMs: 0,
      endLabel: "Cancelled",
      notice:
        "This order was cancelled, so its seats are released and nothing can be paid on it.",
      payable: false,
    };
  }

  const live = status === "held" || status === "payment_pending";
  if (!live) {
    return {
      kind: "unknown",
      remainingMs: 0,
      endLabel: null,
      notice: `The box office reports this order as "${order.status.trim() === "" ? "unknown" : order.status.trim()}". This page does not interpret that state, so it offers no payment and assumes nothing about the seats.`,
      payable: false,
    };
  }

  const remainingMs = remainingMilliseconds(order.expiresAt, nowMs);
  if (remainingMs <= 0) {
    return {
      kind: "expired",
      remainingMs: 0,
      endLabel: "Hold expired",
      notice:
        "This hold has expired. The seats were released, so nothing can be paid on this order: choose them again from the seat map.",
      payable: false,
    };
  }

  return {
    kind: "counting",
    remainingMs,
    endLabel: null,
    notice: null,
    payable: true,
  };
}

// ── Which number the page may print (M3.5 · T6 · S3, S8) ────────────────────
//
// The checkout shows money twice in its life, and never its own opinion of it. Before a
// charge, the order read answers `amountCents: null` — an unpaid order has no amount,
// and the number a client could offer instead is `price × seats`, which is a summary of
// a price that may already have moved, not a charge. After a charge, the payment answer
// reports the amount that was actually taken, and that is the number. On a reload of a
// paid order, the read reports the captured amount with the text the API renders for it,
// and that text is printed verbatim.

/** Where the number on screen came from. */
export type OrderAmountSource =
  /** The payment answer's own amount: money that was just taken. */
  | "payment"
  /** The order read's captured amount: money taken before this page loaded. */
  | "reservation"
  /** Nothing has been taken, so the page prints no number at all. */
  | "none";

export interface OrderAmountView {
  source: OrderAmountSource;
  amountCents: number | null;
  /** The number as it is printed, or null when nothing has been taken. */
  amountLabel: string | null;
  /** Whether a number may be printed. */
  known: boolean;
  /** What this number is, and where it came from. */
  note: string;
}

/**
 * The amount the page may show, and the sentence that says what it is.
 *
 * An approved answer wins over the read, because it is the charge that just happened.
 * A declined one never becomes an amount — the payment answer carries the amount the
 * provider refused, and printing it as taken would be a lie about money. With neither,
 * the page prints no number and says why: the box office computes the charge when it
 * takes it.
 */
export function describeOrderAmount(
  reservation: { amountCents: number | null; amountDisplay: string | null } | null,
  payment: PublicPayment | null,
): OrderAmountView {
  if (
    payment !== null &&
    payment.paymentStatus === "approved" &&
    Number.isInteger(payment.amountCents) &&
    payment.amountCents >= 0
  ) {
    return {
      source: "payment",
      amountCents: payment.amountCents,
      amountLabel: formatCents(payment.amountCents),
      known: true,
      note: "The amount the payment answer reported as taken for this order. It is the box office's number, reported back — this page computes none.",
    };
  }

  const captured = reservation?.amountCents ?? null;
  if (captured !== null && Number.isInteger(captured) && captured >= 0) {
    const served = (reservation?.amountDisplay ?? "").trim();
    return {
      source: "reservation",
      amountCents: captured,
      // The API's own text is printed when it served one; otherwise the number is still
      // the API's, and only its formatting is local.
      amountLabel: served === "" ? formatCents(captured) : served,
      known: true,
      note: "The money the order read reports as taken. This page does not re-price an order: a price that moved after the sale cannot change what was charged.",
    };
  }

  return {
    source: "none",
    amountCents: null,
    amountLabel: null,
    known: false,
    note: "No amount has been taken for this order yet. The box office computes the charge when it is paid and its answer reports what it took; nothing here multiplies a price by a seat count.",
  };
}
