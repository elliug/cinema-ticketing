"use client";

// The payment panel of the checkout (M3 · T7, M3.5 · T6 · S3, S6, S8).
//
// This is the only stateful part of the page. It owns one payment attempt at a time, the
// answer it produced, and the clock the hold window is measured against — and it renders
// every outcome as its own actionable state rather than a generic error:
//
//   - **approved** — the tickets the answer issued, each one linking to its ticket page
//     and to its PDF, with no claim that a ticket admits its holder (that is the
//     scanner's answer, not this page's);
//   - **declined** — the provider refused this attempt. The seats are still held until
//     the window closes, and because a declined attempt is *recorded*, the retry must not
//     reuse its key: the stored key is discarded and the next attempt mints a new one;
//   - **`409 payment_in_flight`** — another attempt on this order is open. Only the key
//     that opened it can settle it, so the retry keeps the stored key and says so;
//   - **`409 already_paid`** — not an error: the tickets belong to the order, so the page
//     reads it and shows them instead of printing a refusal over a purchase;
//   - **`409 hold_expired`** — the seats were released; the seat map is offered again and
//     nothing is re-held behind the buyer's back;
//   - **`409 amount_mismatch`**, **`409 reservation_not_payable`**, **`400 not_sellable`**,
//     **`404 not_found`** — four different refusals, four different next steps;
//   - **the `503`s and a transport failure** — retryable with the same key.
//
// Everything that decides what those words are lives in lib/payment-key.ts: the outcome
// rule, the key disposition it implies and the amount rule are tested without a browser,
// and this component only applies them. Two decisions are load-bearing here:
//
//   - **the key is per order, kept in `sessionStorage`** (`ensurePaymentKey`), so a reload
//     resumes the open attempt instead of being refused with `payment_in_flight`; an
//     answer that settles the attempt discards it;
//   - **the countdown is the server's deadline** (`describeHoldWindow`), and it ends in an
//     explicit expired state with the Pay control gone — never a button that quietly
//     stops working, and never a silent re-hold.
//
// Accessibility: every action is a shadcn `Button` (focus ring included) or a link with
// the shell's visible `:focus-visible` outline, and the surface has exactly **one** polite
// live region, which announces the result once. The clock is deliberately outside it: a
// region that announced every second would make the page unusable.
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useCallback, useEffect, useMemo, useState } from "react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader } from "@/components/ui/card";
import type {
  PaymentResponse,
  PublicPayment,
  PublicProblem,
  PublicReservation,
  ReservationResponse,
} from "@/lib/public-api";
import { newIdempotencyKey } from "@/lib/public-api";
import { ticketPDFUrl } from "@/lib/ticket-media";
import { TICKET_PDF_LINK_LABEL } from "@/lib/issued-tickets";
import type { PaymentKeyStorage, PaymentOutcomeView } from "@/lib/payment-key";
import {
  describeHoldWindow,
  describeOrderAmount,
  describePaymentFailure,
  describePaymentOutcome,
  discardPaymentKey,
  ensurePaymentKey,
} from "@/lib/payment-key";
import { COUNTDOWN_TICK_MS, formatCountdown } from "@/lib/seat-selection";

export interface PaymentPanelProps {
  /** The order as the page read it: status, deadline, seats, money taken, tickets. */
  reservation: PublicReservation;
  /**
   * The instant the page was rendered on the server.
   *
   * It seeds the clock so the first client render matches the HTML that was sent, and
   * the effect immediately replaces it with the browser's own reading. Passing it is what
   * keeps the countdown honest without a hydration mismatch.
   */
  renderedAt: number;
  /** Server action: POST /reservations/{id}/payment with this attempt's key. */
  payReservation: (input: {
    reservationId: string;
    idempotencyKey: string;
  }) => Promise<PaymentResponse>;
  /** Server action: read the order again, so `already_paid` can show its tickets. */
  loadReservation: (reservationId: string) => Promise<ReservationResponse>;
}

/** One attempt's rendered state. */
type PaymentView =
  | { kind: "idle" }
  | { kind: "pending" }
  | { kind: "answer"; payment: PublicPayment; outcome: PaymentOutcomeView }
  | { kind: "problem"; problem: PublicProblem; outcome: PaymentOutcomeView }
  | {
      kind: "settled";
      reservation: PublicReservation;
      outcome: PaymentOutcomeView;
    };

/**
 * The tab's payment-key storage, or `null` in an environment without one.
 *
 * `sessionStorage` is read inside the click handler, because the panel is server-rendered
 * first and `window` does not exist there.
 */
function keyStorage(): PaymentKeyStorage | null {
  try {
    return window.sessionStorage;
  } catch {
    // A browsing mode that refuses storage still pays; it only cannot resume.
    return null;
  }
}

export function PaymentPanel({
  reservation,
  renderedAt,
  payReservation,
  loadReservation,
}: PaymentPanelProps) {
  const router = useRouter();
  const [view, setView] = useState<PaymentView>({ kind: "idle" });
  // Seeded with the server's instant, then advanced by the effect below: the first
  // render is the HTML that was streamed, and every later one uses the browser's clock.
  const [nowMs, setNowMs] = useState(() => renderedAt);

  // The order whose status, deadline and seats the panel is about. After `already_paid`
  // the order itself is the answer, so the panel switches to the one it just read.
  const order = view.kind === "settled" ? view.reservation : reservation;
  const window = useMemo(() => describeHoldWindow(order, nowMs), [order, nowMs]);
  const amount = useMemo(
    () =>
      describeOrderAmount(
        order,
        view.kind === "answer" ? view.payment : null,
      ),
    [order, view],
  );

  // The clock is the only timer in the surface: it re-reads the wall clock while the
  // window is live, and stops once the server deadline has passed. No value is ever
  // subtracted from a client-side guess — the deadline is the server's.
  useEffect(() => {
    if (window.kind !== "counting") {
      return;
    }
    setNowMs(Date.now());
    const timer = setInterval(() => setNowMs(Date.now()), COUNTDOWN_TICK_MS);
    return () => clearInterval(timer);
  }, [window.kind]);

  const handlePay = useCallback(async () => {
    setView({ kind: "pending" });

    const storage = keyStorage();
    // The key is taken from the tab when it already has one, so a reload resumes the
    // open attempt instead of starting a second one the schema would refuse.
    const idempotencyKey =
      storage === null
        ? newIdempotencyKey()
        : ensurePaymentKey(storage, order.reservationId, newIdempotencyKey);

    const response = await payReservation({
      reservationId: order.reservationId,
      idempotencyKey,
    });

    // One rule decides what the key does next, and it is the M3 one: a settled attempt
    // frees the order of its key, an open one keeps it because only it can finish the
    // payment.
    const settleKey = (outcome: PaymentOutcomeView) => {
      if (storage !== null && outcome.keyDisposition === "discard") {
        discardPaymentKey(storage, order.reservationId);
      }
    };

    if (response.ok) {
      const outcome = describePaymentOutcome(response.payment);
      settleKey(outcome);
      setView({ kind: "answer", payment: response.payment, outcome });
      return;
    }

    const outcome = describePaymentFailure(response.problem);
    settleKey(outcome);

    if (outcome.kind === "already_paid") {
      // The refusal names no tickets, and a payment answer is not a ticket: the order is
      // what carries them, so it is read. A failed read leaves the refusal on screen
      // rather than turning a purchase into an error.
      const read = await loadReservation(order.reservationId);
      if (read.ok) {
        setView({ kind: "settled", reservation: read.reservation, outcome });
        // The page's own summary was rendered from the state before this payment; the
        // order has moved on, so the frame is refreshed from the same read.
        router.refresh();
        return;
      }
    }
    setView({ kind: "problem", problem: response.problem, outcome });
  }, [loadReservation, order.reservationId, payReservation, router]);

  const windowNotice = window.notice;
  // The one announcement of this surface: what just happened, or what the window just
  // did. It is polite, and it changes once per event instead of once per second.
  const announcement =
    view.kind === "pending"
      ? "Contacting the payment provider…"
      : view.kind === "settled"
        ? `${view.outcome.headline}. The order carries the tickets below.`
        : view.kind === "answer" || view.kind === "problem"
          ? `${view.outcome.headline}. ${view.outcome.message}`
          : windowNotice !== null
            ? windowNotice
            : "Nothing has been charged for this order yet.";

  const seatsHref =
    order.eventId === ""
      ? null
      : `/events/${encodeURIComponent(order.eventId)}/seats`;

  // A payment is offered while the window allows one and the order holds a seat: an
  // order with nothing to charge would be refused (`not_sellable`), and a button that
  // can only fail is not an action. After an outcome, its own action is the one shown.
  const payable = window.payable && order.seats.length > 0;
  const showPay = payable && (view.kind === "idle" || view.kind === "pending");
  const pending = view.kind === "pending";

  return (
    <Card aria-labelledby="pay-order">
      <CardHeader>
        <h2 id="pay-order" className="m-0 text-lg leading-tight font-semibold">
          Pay for this order
        </h2>
        <CardDescription>
          One attempt is one charge. The box office charges this order — the amount is its
          number, not one this page computes — and the key kept in this tab is what makes a
          retry a replay of the same attempt rather than a second payment.
        </CardDescription>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        <p className="m-0 text-sm text-muted-foreground">
          {order.seats.length === 0
            ? "This order holds no seat."
            : `${order.seats.length} seat${order.seats.length === 1 ? "" : "s"}: ${order.seats
                .map(seatLine)
                .join(", ")}`}
        </p>

        <p className="m-0 text-sm">
          {amount.known ? (
            <>
              <strong>{amount.amountLabel}</strong> taken.{" "}
            </>
          ) : null}
          <span className="text-muted-foreground">{amount.note}</span>
        </p>

        {window.kind === "counting" ? (
          <div className="flex flex-col gap-1">
            <p className="m-0 text-sm text-muted-foreground">Hold window</p>
            {/* Deliberately not a live region: it changes every second, and a screen
                reader announcing a clock would make the page unusable. Only the end of
                the window is announced, once, through the region below. */}
            <p className="m-0">
              <strong>{formatCountdown(window.remainingMs)}</strong> left of your hold
            </p>
            <p className="m-0 max-w-prose text-sm text-muted-foreground">
              The window comes from the server, and the seats are released when it ends.
            </p>
          </div>
        ) : (
          <div className="flex flex-col gap-1">
            <p className="m-0 text-sm text-muted-foreground">Hold window</p>
            <p className="m-0">
              <strong>{window.endLabel ?? "Not running"}</strong>
            </p>
          </div>
        )}

        {/* The one polite live region of this surface. Everything visual below is
            rendered outside it, so an announcement is one sentence and not a page. */}
        <p role="status" className="m-0 max-w-prose text-sm font-medium">
          {announcement}
        </p>

        {windowNotice === null ? null : (
          <Alert variant="warning" className="flex flex-col gap-2">
            <AlertDescription className="flex flex-col gap-2">
              <p className="m-0">{windowNotice}</p>
              {seatsHref === null ? null : (
                <div className="flex flex-wrap gap-2">
                  <Button asChild size="sm">
                    <Link href={seatsHref}>Choose the seats again</Link>
                  </Button>
                </div>
              )}
            </AlertDescription>
          </Alert>
        )}

        {showPay ? (
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              onClick={handlePay}
              disabled={pending}
              aria-busy={pending}
            >
              {pending ? "Paying…" : "Pay"}
            </Button>
          </div>
        ) : null}

        {view.kind === "answer" || view.kind === "problem" || view.kind === "settled" ? (
          <Outcome
            outcome={view.outcome}
            amount={amount}
            tickets={
              view.kind === "settled"
                ? view.reservation.ticketIds
                : view.kind === "answer" &&
                    view.outcome.kind === "approved"
                  ? view.payment.ticketIds
                  : []
            }
            seatsHref={seatsHref}
            canRetry={view.outcome.retryable && payable}
            onRetry={handlePay}
          />
        ) : null}
      </CardContent>
    </Card>
  );
}

/**
 * One outcome, with the action it calls for.
 *
 * The block is not a live region: the sentence above already announced it, and a second
 * region would say everything twice. Colour reinforces the state; the headline is the
 * state.
 */
function Outcome({
  outcome,
  amount,
  tickets,
  seatsHref,
  canRetry,
  onRetry,
}: {
  outcome: PaymentOutcomeView;
  amount: { known: boolean; amountLabel: string | null };
  tickets: readonly string[];
  seatsHref: string | null;
  canRetry: boolean;
  onRetry: () => void;
}) {
  return (
    <Alert
      variant={alertVariant(outcome)}
      aria-labelledby="payment-outcome"
      className="flex flex-col gap-2"
    >
      <div className="flex flex-wrap items-center gap-2">
        <h3 id="payment-outcome" className="m-0 text-base font-semibold">
          {outcome.headline}
        </h3>
        {outcome.kind === "approved" ? <Badge variant="success">Approved</Badge> : null}
        {outcome.kind === "declined" ? <Badge variant="warning">Declined</Badge> : null}
      </div>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">{outcome.message}</p>
        {outcome.kind === "approved" && amount.known ? (
          <p className="m-0">
            <strong>{amount.amountLabel}</strong> was taken for this order.
          </p>
        ) : null}
        {tickets.length > 0 ? (
          <IssuedTickets ticketIds={tickets} />
        ) : null}
        {outcome.kind === "approved" && tickets.length === 0 ? (
          <p className="m-0">
            The payment was approved but the answer carried no ticket. Do not pay again:
            reload this page, which reads the order and shows the tickets it holds, and
            contact the box office if it stays empty.
          </p>
        ) : null}
        <p className="m-0">{outcome.nextStep}</p>

        <div className="flex flex-wrap gap-2 pt-1">
          {canRetry ? (
            <Button type="button" size="sm" onClick={onRetry}>
              {retryLabel(outcome.kind)}
            </Button>
          ) : null}
          {seatsHref !== null && outcome.offersSeatMap ? (
            <Button asChild variant="outline" size="sm">
              <Link href={seatsHref}>Choose the seats again</Link>
            </Button>
          ) : null}
          {!canRetry && !outcome.offersSeatMap ? (
            <Button asChild variant="outline" size="sm">
              <Link href="/events">Back to the catalogue</Link>
            </Button>
          ) : null}
        </div>
      </AlertDescription>
    </Alert>
  );
}

/**
 * The tickets of one order: the link that opens each one and the PDF that records it.
 *
 * Shared by the panel (an answer that just issued them) and by the page (a reload of a
 * paid order, whose tickets come from the order read), so both surfaces print the same
 * words about the same thing. Each link is a plain anchor with the shell's visible focus
 * outline, and the PDF is a link target rather than something this page fetches — the API
 * redirects to a URL that declares `Content-Disposition: attachment`, so the sentence
 * both surfaces print comes from `TICKET_PDF_LINK_LABEL` instead of being written twice.
 */
export function IssuedTickets({ ticketIds }: { ticketIds: readonly string[] }) {
  return (
    <ul role="list" className="m-0 flex list-none flex-col gap-2 p-0">
      {ticketIds.map((ticketId) => (
        <li key={ticketId} className="flex flex-wrap items-center gap-x-4 gap-y-1">
          <Link
            href={`/tickets/${encodeURIComponent(ticketId)}`}
            className="rounded-sm font-medium underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background focus-visible:outline-none"
          >
            Open ticket <code>{ticketId}</code>
          </Link>
          <a
            href={ticketPDFUrl(ticketId)}
            className="rounded-sm text-sm underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background focus-visible:outline-none"
          >
            {TICKET_PDF_LINK_LABEL}
          </a>
        </li>
      ))}
    </ul>
  );
}

/** The `Alert` tone for one outcome; the words inside it carry the state. */
function alertVariant(outcome: PaymentOutcomeView) {
  switch (outcome.tone) {
    case "success":
      return "success" as const;
    case "info":
      return "info" as const;
    case "warning":
      return "warning" as const;
    default:
      return "destructive" as const;
  }
}

/**
 * What the retry button says, per state.
 *
 * A decline starts a new attempt, an open payment continues the one that is already
 * running, and a temporary failure resends the same attempt — three different things, so
 * three different labels rather than one "try again" that hides which happened.
 */
function retryLabel(kind: PaymentOutcomeView["kind"]): string {
  switch (kind) {
    case "declined":
      return "Try the payment again";
    case "payment_in_flight":
      return "Continue the same payment";
    case "temporary":
      return "Retry the payment";
    default:
      return "Try again";
  }
}

/** `Row A seat 3`; the values painted at the door, never a reconstructed label. */
function seatLine(seat: PublicReservation["seats"][number]): string {
  const row = seat.rowLabel.trim();
  const number = seat.seatNumber.trim();
  if (row !== "" && number !== "") {
    return `Row ${row} seat ${number}`;
  }
  return `Seat ${seat.eventSeatId}`;
}
