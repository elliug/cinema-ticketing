"use client";

// Payment panel for the guest confirmation page (M3 · T7 · S1, S3).
//
// This is the only stateful part of the page. It owns one payment attempt at a
// time and the answer it produced, and it renders every outcome as its own,
// actionable thing rather than a generic error:
//
//   - **approved** — the tickets the API issued, each linking to its ticket page
//     and to its PDF;
//   - **declined** — the provider refused this attempt. The seats are still held
//     until the window closes, and because a declined attempt is *recorded*, the
//     retry must not reuse its key: the stored key is discarded and the next
//     attempt mints a new one (lib/payment-key.ts);
//   - **`409 payment_in_flight`** — another attempt on this reservation is open.
//     Only the key that opened it can settle it, so the retry keeps the stored
//     key and says so;
//   - **`409 hold_expired` / `409 already_paid` / `409 reservation_not_payable`**
//     — nothing can be paid on this reservation, and the way back to the event's
//     seats is offered;
//   - a temporary provider or transport failure — retryable, with the same key.
//
// Where the key lives is the whole point: it is minted once per reservation and
// kept in `sessionStorage` under the reservation id, so a page reload resumes the
// attempt instead of being told, correctly, that a payment is under way. The
// storage rules live in lib/payment-key.ts and are tested without a DOM; this
// component only passes `window.sessionStorage` to them.
import { useCallback, useState } from "react";
import type {
  PaymentResponse,
  PublicProblem,
  PublicPayment,
} from "@/lib/public-api";
import { newIdempotencyKey, ticketPDFUrl } from "@/lib/public-api";
import type { PaymentKeyStorage } from "@/lib/payment-key";
import {
  discardPaymentKey,
  ensurePaymentKey,
  paymentKeyDisposition,
} from "@/lib/payment-key";

export interface PaymentPanelProps {
  reservationId: string;
  /** The event this order belongs to, when the page was reached from its seats. */
  eventId: string;
  /** Normalized public API base URL, used to build the ticket links. */
  apiBaseUrl: string;
  /** Server action: POST /reservations/{id}/payment with this attempt's key. */
  payReservation: (input: {
    reservationId: string;
    idempotencyKey: string;
  }) => Promise<PaymentResponse>;
}

/** One attempt's rendered state. */
type PaymentView =
  | { kind: "idle" }
  | { kind: "pending" }
  | { kind: "answered"; payment: PublicPayment }
  | { kind: "failed"; problem: PublicProblem };

/**
 * The tab's payment-key storage, or `null` in an environment without one.
 *
 * `sessionStorage` is read lazily, inside the click handler, because the panel is
 * server-rendered first and `window` does not exist there.
 */
function keyStorage(): PaymentKeyStorage | null {
  try {
    return window.sessionStorage;
  } catch {
    // A browsing mode that refuses storage still pays; it only cannot resume.
    return null;
  }
}

/** Renders minor units as `19.98`, with no currency: v1 stores none. */
function formatMinorUnits(cents: number): string {
  const sign = cents < 0 ? "-" : "";
  const absolute = Math.abs(Math.trunc(cents));
  return `${sign}${Math.floor(absolute / 100)}.${String(absolute % 100).padStart(2, "0")}`;
}

export function PaymentPanel({
  reservationId,
  eventId,
  apiBaseUrl,
  payReservation,
}: PaymentPanelProps) {
  const [view, setView] = useState<PaymentView>({ kind: "idle" });

  const handlePay = useCallback(async () => {
    setView({ kind: "pending" });

    const storage = keyStorage();
    // The key is taken from the tab when it already has one, so a reload resumes
    // the open attempt instead of starting a second one the schema would refuse.
    const idempotencyKey =
      storage === null
        ? newIdempotencyKey()
        : ensurePaymentKey(storage, reservationId, newIdempotencyKey);

    const response = await payReservation({ reservationId, idempotencyKey });

    if (response.ok) {
      // A settled attempt needs no key; a declined one must not reuse it, because
      // resending it would replay the refusal instead of charging again.
      if (
        storage !== null &&
        paymentKeyDisposition({
          paymentStatus: response.payment.paymentStatus,
          problemCode: null,
        }) === "discard"
      ) {
        discardPaymentKey(storage, reservationId);
      }
      setView({ kind: "answered", payment: response.payment });
      return;
    }

    if (
      storage !== null &&
      paymentKeyDisposition({
        paymentStatus: null,
        problemCode: response.problem.code,
      }) === "discard"
    ) {
      discardPaymentKey(storage, reservationId);
    }
    setView({ kind: "failed", problem: response.problem });
  }, [payReservation, reservationId]);

  const pending = view.kind === "pending";
  const seatsHref = eventId === "" ? null : `/events/${encodeURIComponent(eventId)}/seats`;

  return (
    <section className="card" aria-labelledby="payment">
      <h2 id="payment">Pay for this order</h2>
      <p className="meta">
        Reservation <code>{reservationId}</code>
      </p>
      <p className="notice">
        The amount is calculated by the box office from the event&apos;s price and
        the seats this reservation holds, so nothing is sent from this page but the
        attempt&apos;s idempotency key. One attempt is one charge: retrying resumes
        it rather than paying twice.
      </p>

      {view.kind === "idle" || pending ? (
        <div className="actions">
          <button type="button" onClick={handlePay} disabled={pending}>
            {pending ? "Paying…" : "Pay"}
          </button>
        </div>
      ) : null}

      {/* Every answered or failed state renders its own action, or deliberately
          none: only the states where another attempt is the right thing to do
          (a decline, an open attempt, a temporary failure) offer one. */}

      <div role="status" aria-live="polite">
        {view.kind === "idle" ? (
          <p className="notice">
            Your seats are held for a limited window. Paying inside that window is
            what turns the hold into tickets.
          </p>
        ) : null}

        {pending ? <p className="meta">Contacting the payment provider…</p> : null}

        {view.kind === "answered" ? (
          <PaymentAnswer
            payment={view.payment}
            apiBaseUrl={apiBaseUrl}
            onRetry={handlePay}
          />
        ) : null}

        {view.kind === "failed" ? (
          <PaymentFailure
            problem={view.problem}
            seatsHref={seatsHref}
            onRetry={handlePay}
          />
        ) : null}
      </div>
    </section>
  );
}

function PaymentAnswer({
  payment,
  apiBaseUrl,
  onRetry,
}: {
  payment: PublicPayment;
  apiBaseUrl: string;
  onRetry: () => void;
}) {
  if (payment.paymentStatus === "declined") {
    return (
      <section className="problems feedback error" aria-labelledby="payment-declined">
        <h2 id="payment-declined">The payment was refused</h2>
        <p>
          The provider declined this attempt for{" "}
          <strong>{formatMinorUnits(payment.amountCents)}</strong>, so no money was
          taken and no ticket was issued.
        </p>
        <p>
          Your seats are still held: the reservation stays valid until its hold
          window closes, and the box office has not released the seats.
        </p>
        <p className="notice">
          A refused attempt is finished for good, so trying again starts a{" "}
          <strong>new</strong> payment attempt with a new key rather than replaying
          this refusal.
        </p>
        <div className="actions">
          <button type="button" onClick={onRetry}>
            Try the payment again
          </button>
        </div>
      </section>
    );
  }

  return (
    <section className="feedback" aria-labelledby="payment-approved">
      <h2 id="payment-approved">Payment approved</h2>
      <p>
        <strong>{formatMinorUnits(payment.amountCents)}</strong> was taken for this
        order. Your tickets:
      </p>
      {payment.ticketIds.length === 0 ? (
        <p className="notice">
          The payment was approved but the answer carried no ticket. Do not pay
          again: reload this page once, and contact the box office if it stays
          empty.
        </p>
      ) : (
        <ul className="list">
          {payment.ticketIds.map((ticketId) => (
            <li key={ticketId}>
              <a href={`/tickets/${encodeURIComponent(ticketId)}`}>
                Open ticket <code>{ticketId}</code>
              </a>{" "}
              ·{" "}
              <a href={ticketPDFUrl(apiBaseUrl, ticketId)}>Download PDF</a>
            </li>
          ))}
        </ul>
      )}
      <p className="notice">
        Keep these links: nothing lists a guest&apos;s tickets, so the id of a
        ticket is how it is opened again.
      </p>
    </section>
  );
}

function PaymentFailure({
  problem,
  seatsHref,
  onRetry,
}: {
  problem: PublicProblem;
  seatsHref: string | null;
  onRetry: () => void;
}) {
  const seatsLink =
    seatsHref === null ? null : (
      <a href={seatsHref}>Back to the event&apos;s seats</a>
    );

  switch (problem.code) {
    case "payment_in_flight":
      return (
        <section className="problems feedback" aria-labelledby="payment-in-flight">
          <h2 id="payment-in-flight">A payment is already being processed</h2>
          <p>
            This reservation already has a payment attempt in progress, and only
            the request that started it can settle it. This page is holding that
            key, so retrying it continues the same attempt.
          </p>
          <div className="actions">
            <button type="button" onClick={onRetry}>
              Continue the same payment
            </button>
          </div>
        </section>
      );

    case "hold_expired":
      return (
        <section className="problems feedback error" aria-labelledby="payment-expired">
          <h2 id="payment-expired">The hold window closed</h2>
          <p>
            The seats were held for a limited window and that window ended before
            the payment completed, so the reservation can no longer be paid and the
            seats were released.
          </p>
          <p className="notice">
            {seatsLink === null
              ? "Choose the seats again from the event page."
              : seatsLink}
          </p>
        </section>
      );

    case "already_paid":
      return (
        <section className="problems feedback" aria-labelledby="payment-already-paid">
          <h2 id="payment-already-paid">This order is already paid</h2>
          <p>
            The reservation already carries a captured payment, so nothing more can
            be charged. The tickets were issued when it was captured.
          </p>
          <p className="notice">
            This page cannot list them again: a guest&apos;s tickets are reached by
            the links the paying session was given. Nothing lists tickets by
            reservation.
          </p>
        </section>
      );

    case "reservation_not_payable":
    case "not_sellable":
      return (
        <section className="problems feedback error" aria-labelledby="payment-not-payable">
          <h2 id="payment-not-payable">This reservation cannot be paid</h2>
          <p>
            {problem.code === "not_sellable"
              ? "The event has no price or the reservation holds no seat, so there is nothing to charge."
              : "The reservation is not awaiting payment, so this page cannot charge it."}
          </p>
          <p className="notice">{seatsLink ?? "Start again from the event page."}</p>
        </section>
      );

    case "not_found":
      return (
        <section className="problems feedback error" aria-labelledby="payment-not-found">
          <h2 id="payment-not-found">This reservation does not exist</h2>
          <p>
            The reservation id in this address is unknown to the box office, so
            there is nothing to pay.
          </p>
          <p className="notice">{seatsLink ?? "Start again from the event page."}</p>
        </section>
      );

    default:
      // A temporary failure (provider, storage, network). The key is kept, which is
      // what the API's own refusal asks for: only the key of an open attempt can
      // settle it.
      return (
        <section className="problems feedback error" aria-labelledby="payment-failed">
          <h2 id="payment-failed">The payment could not be completed</h2>
          <p>
            <code>{problem.code}</code> · HTTP {problem.status}
          </p>
          <p>{problem.detail}</p>
          <div className="actions">
            <button type="button" onClick={onRetry}>
              Retry the payment
            </button>
          </div>
          <p className="notice">
            This retries the same payment attempt with the same key, so it can never
            charge twice. If your seats were released in the meantime, the answer
            will say so.
          </p>
        </section>
      );
  }
}
