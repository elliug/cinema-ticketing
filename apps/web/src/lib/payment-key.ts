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
//
// The checkout's other rule lives here too, at the bottom: `describeContactEmail` is what
// the optional address field is worth before it is sent, so a value that cannot be an
// address at all is refused before a payment attempt is spent on it. It restates the API's
// own rule and is never stricter than it — the API stays the authority on what an address
// is, and nothing here claims one was verified.
import { presentAmount } from "./receipt.ts";
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
      headline: "Pago aprobado",
      message:
        "La taquilla ha cobrado este pedido y ha emitido una entrada por butaca.",
      nextStep:
        "Abre cada entrada aquí abajo, o guarda su enlace: nada lista las entradas de un invitado, así que el identificador de la entrada es la forma de abrirla otra vez. Una entrada es el registro de una venta: si todavía da acceso lo responde la taquilla al escanearla.",
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
      headline: "El pago se ha rechazado",
      message: `El proveedor rechazó este intento, así que no se ha cobrado nada y no se ha emitido ninguna entrada.`,
      nextStep:
        "Tus butacas siguen reservadas hasta que se cierre el plazo. Volver a intentarlo empieza un intento nuevo con una clave nueva, así que este rechazo no se repite: el pedido no se pierde.",
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
    headline: "La respuesta del pago no es un veredicto que esta página conozca",
    message: `La taquilla contestó "${payment.paymentStatus}".`,
    nextStep:
      "No se concluye nada de ella: recarga esta página para leer el estado guardado del pedido. Esta página no decide por su cuenta si te han cobrado.",
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
        headline: "Ya hay un pago en curso",
        message:
          "Este pedido ya tiene un intento de pago en marcha. Solo la petición que lo abrió puede terminarlo, así que una segunda clave se rechazaría: esta página guarda la primera.",
        nextStep:
          "Reinténtalo desde la pestaña que lo abrió, o recarga esta página cuando el proveedor haya respondido: la lectura del pedido dirá entonces qué pasó. En ningún caso se cobra dos veces.",
        tone: "warning",
        retryable: true,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "already_paid":
      return {
        kind: "already_paid",
        headline: "Este pedido ya está pagado",
        message:
          "El pedido ya lleva un pago capturado, así que no queda nada que cobrar.",
        nextStep:
          "Las entradas pertenecen al pedido, no a esta respuesta: la página lee el pedido y las muestra, así que recargar no pierde ninguna entrada.",
        tone: "info",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "hold_expired":
      return {
        kind: "hold_expired",
        headline: "Esta reserva ha caducado",
        message:
          "El plazo de la reserva se cerró antes de que terminara el pago, así que las butacas se liberaron y no se ha cobrado nada.",
        nextStep:
          "Vuelve a elegir las butacas en el plano. Esta página nunca reserva por su cuenta: solo el plano de butacas puede hacerlo.",
        tone: "error",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: true,
      };

    case "reservation_not_payable":
      return {
        kind: "not_payable",
        headline: "Este pedido no se puede pagar",
        message:
          "El pedido no está esperando un pago, así que esta página no puede cobrarlo.",
        nextStep:
          "Mira el estado del pedido aquí arriba. Si su reserva ha terminado, las butacas están liberadas y elegirlas otra vez es la única forma de volver.",
        tone: "error",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "amount_mismatch":
      return {
        kind: "amount_mismatch",
        headline: "La cantidad no coincidía",
        message:
          "La cantidad registrada para este intento no coincide con la que se cobraría ahora por el pedido, así que el pago se negó a cobrar nada.",
        nextStep:
          "No vuelvas a pagar: esto necesita a la taquilla, con la referencia de este pedido. No se ha cobrado nada.",
        tone: "error",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "not_sellable":
      return {
        kind: "not_sellable",
        headline: "No hay nada que cobrar",
        message:
          "La sesión no tiene precio, o el pedido no tiene ninguna butaca, así que la taquilla no tiene cantidad que cobrar.",
        nextStep:
          "Vuelve a la cartelera para elegir una sesión que esté a la venta.",
        tone: "error",
        retryable: false,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    case "not_found":
      return {
        kind: "not_found",
        headline: "Este pedido no existe",
        message:
          "La taquilla no conoce el identificador de reserva de esta dirección, así que no hay nada que pagar.",
        nextStep:
          "Abre el pedido desde el plano de butacas que lo creó, o empieza de nuevo desde la cartelera.",
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
        headline: "El pago no se pudo completar",
        message:
          "El proveedor de pago o el servicio de reservas no está disponible temporalmente, así que este intento no ha terminado.",
        nextStep:
          "Reinténtalo con la misma clave: termina el intento que esta página ya abrió y no puede cobrar dos veces. Si tu plazo se cerró mientras tanto, la respuesta lo dirá.",
        tone: "warning",
        retryable: true,
        keyDisposition: disposition,
        offersSeatMap: false,
      };

    default:
      if (problem.status >= 500) {
        return {
          kind: "temporary",
          headline: "El pago no se pudo completar",
          message:
            "El proveedor de pago o el servicio de reservas no está disponible temporalmente, así que este intento no ha terminado.",
          nextStep:
            "Reinténtalo con la misma clave: termina el intento que esta página ya abrió y no puede cobrar dos veces. Si tu plazo se cerró mientras tanto, la respuesta lo dirá.",
          tone: "warning",
          retryable: true,
          keyDisposition: disposition,
          offersSeatMap: false,
        };
      }
      return {
        kind: "refused",
        headline: "La taquilla rechazó el pago",
        message: `La taquilla contestó ${problem.code}: ${problem.detail}`,
        nextStep:
          "No se ha dado por hecho nada. Un reintento reenvía el intento que esta página ya abrió, con su propia clave, así que no puede cobrar dos veces.",
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
      endLabel: "Pagado",
      notice: null,
      payable: false,
    };
  }
  if (status === "cancelled") {
    return {
      kind: "cancelled",
      remainingMs: 0,
      endLabel: "Cancelado",
      notice:
        "Este pedido se canceló, así que sus butacas están liberadas y no se puede pagar nada por él.",
      payable: false,
    };
  }

  const live = status === "held" || status === "payment_pending";
  if (!live) {
    return {
      kind: "unknown",
      remainingMs: 0,
      endLabel: null,
      notice: `La taquilla informa de este pedido como "${order.status.trim() === "" ? "desconocido" : order.status.trim()}". Esta página no interpreta ese estado, así que no ofrece ningún pago y no da por hecho nada sobre las butacas.`,
      payable: false,
    };
  }

  const remainingMs = remainingMilliseconds(order.expiresAt, nowMs);
  if (remainingMs <= 0) {
    return {
      kind: "expired",
      remainingMs: 0,
      endLabel: "Reserva caducada",
      notice:
        "Esta reserva ha caducado. Las butacas se liberaron, así que no se puede pagar nada por este pedido: vuelve a elegirlas en el plano de butacas.",
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
      note: "La cantidad que la respuesta del pago informa como cobrada por este pedido. Es el número de la taquilla, devuelto tal cual: esta página no calcula ninguno.",
    };
  }

  const captured = reservation?.amountCents ?? null;
  if (captured !== null && Number.isInteger(captured) && captured >= 0) {
    const served = (reservation?.amountDisplay ?? "").trim();
    return {
      source: "reservation",
      amountCents: captured,
      // The API's own text is printed when it served one, re-punctuated for Spanish;
      // otherwise the number is still the API's, and only its formatting is local.
      amountLabel: served === "" ? formatCents(captured) : presentAmount(served),
      known: true,
      note: "El dinero que la lectura del pedido informa como cobrado. Esta página no vuelve a poner precio a un pedido: un precio que se movió después de la venta no puede cambiar lo que se cobró.",
    };
  }

  return {
    source: "none",
    amountCents: null,
    amountLabel: null,
    known: false,
    note: "Todavía no se ha cobrado ninguna cantidad por este pedido. La taquilla calcula el cobro cuando se paga, y su respuesta informa de lo que cobró; aquí no se multiplica ningún precio por un número de butacas.",
  };
}

// ── The address the tickets are mailed to (T5 · S1, S2, D1) ─────────────────
//
// The payment route accepts one optional field besides the key, and it is not money: the
// address the buyer asked the tickets to be mailed to (see
// apps/api/internal/publicapi/payment.go). It cannot change what is charged — the amount
// stays the box office's — only where a copy of the ticket goes, which is exactly why it is
// asked for *before* paying (D1): a buyer whose tab closes right after the charge has still
// left an address behind, so the sale the email worker reads is never one with nowhere to
// send it.
//
// The rule below is the API's own, restated so the field can stop an obvious mistake before
// it spends a payment attempt on it. It must never become stricter than the API: an address
// refused here is one the API would have refused too, and everything the API accepts gets
// through. The API's comment says why that matters — Go is not an email validator either, a
// strict pattern rejects valid addresses (quoted local parts, internationalised domains) and
// still accepts nonsense — so both sides refuse only what cannot be an address at all.
//
// Two things this module cannot say, and the panel's copy therefore does not claim: the
// address is stored **unverified** (S2), and the link it is mailed opens the tickets for
// whoever holds it.

/** The RFC's ceiling for one address, mirrored from the API's own limit. */
export const MAX_CONTACT_EMAIL_LENGTH = 320;

/**
 * What the field says when it holds something the API would refuse.
 *
 * One sentence for every refusal, deliberately: the API's own detail names the precise rule
 * once it refuses, and the field only owes a person the reason Pay did nothing.
 */
const CONTACT_EMAIL_PROBLEM =
  "Esa dirección no sirve para enviar las entradas. Una dirección necesita un nombre, una @ y un dominio con un punto, y no puede contener espacios.";

/** What the optional address field currently holds. */
export type ContactEmailView =
  /** Left empty. The field is optional, so this is not a mistake and needs no sentence. */
  | { kind: "empty" }
  /** An address the API would accept, trimmed as the payment request will carry it. */
  | { kind: "address"; email: string }
  /** Something that cannot be an address at all: paying would be refused for it. */
  | { kind: "refused"; problem: string };

/**
 * What one value of the address field is worth.
 *
 * `empty` and `refused` are different answers, and that difference is the whole reason this
 * returns a value instead of a boolean: leaving the field empty is a choice the buyer is
 * allowed to make — the PDF still downloads from the page — while a value that cannot be an
 * address would be refused by the API only after the attempt had been spent.
 */
export function describeContactEmail(value: string): ContactEmailView {
  const email = value.trim();
  if (email === "") {
    return { kind: "empty" };
  }

  // The API's checks in the API's order. The whitespace test names the same four bytes Go's
  // `ContainsAny` names: a wider pattern would refuse a value the API stores, which is the
  // one direction that must not happen.
  const at = email.lastIndexOf("@");
  const addressable =
    email.length <= MAX_CONTACT_EMAIL_LENGTH &&
    !/[ \t\r\n]/.test(email) &&
    at > 0 &&
    at < email.length - 1 &&
    email.slice(at + 1).includes(".");

  return addressable
    ? { kind: "address", email }
    : { kind: "refused", problem: CONTACT_EMAIL_PROBLEM };
}
