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
// Since the Marquesina redesign (visual-redesign · T5) the order's facts are the shared
// receipt: `SummaryPanel` prints the seats, the money and the deadline as rows, with the Pay
// control as its action, exactly as the seat plan prints its running summary. What stays
// around it is what is this panel's own business: the address field, the one announcement,
// the window's own notice and every outcome.
//
// Accessibility: every action is a `Button` (focus ring included) or a link with the shell's
// visible `:focus-visible` outline, and the surface has exactly **one** polite live region,
// which announces the result once. The clock is deliberately outside it: a region that
// announced every second would make the page unusable.
//
// The one field this panel owns is the address the tickets are to be mailed to (T5 · S1,
// S2, D1). It is asked for **before** the charge, because the API stores it in the same
// transaction that captures the payment: a buyer who closes the tab right after paying still
// has a copy of the tickets on its way. It is optional — forcing it would store an address
// for every abandoned order — and it is unverified, so the copy beside it says what the
// system actually does and nothing more. The rule that reads it lives in
// lib/payment-key.ts (`describeContactEmail`), so the field stops a value that cannot be an
// address at all before a payment attempt is spent on it, and the API stays the authority
// for everything that gets through.
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useCallback, useEffect, useMemo, useState } from "react";

import { SummaryPanel } from "@/components/summary-panel";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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
  describeContactEmail,
  describeHoldWindow,
  describeOrderAmount,
  describePaymentFailure,
  describePaymentOutcome,
  discardPaymentKey,
  ensurePaymentKey,
} from "@/lib/payment-key";
import {
  describeHoldDeadline,
  presentAmount,
  seatReceipt,
} from "@/lib/receipt";
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
  /** Server action: POST /reservations/{id}/payment with this attempt's key and, when the
   * buyer gave one, the address the tickets are to be mailed to. Nothing about the address
   * is decided here: it travels to the action and the API decides what to store. */
  payReservation: (input: {
    reservationId: string;
    idempotencyKey: string;
    email?: string;
  }) => Promise<PaymentResponse>;
  /** Server action: read the order again, so `already_paid` can show its tickets. */
  loadReservation: (reservationId: string) => Promise<ReservationResponse>;
}

/** One attempt's rendered state. */
type PaymentView =
  | { kind: "idle" }
  | { kind: "pending" }
  // Nothing was sent: the address field holds a value that cannot be an address, so the
  // payment was not attempted at all. It is its own state rather than a refusal from the
  // API, because no attempt was spent and nothing was decided by the box office.
  | { kind: "address_problem"; problem: string }
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
  // The address the buyer typed, kept as typed: it is trimmed when it is read and when it
  // is sent, never while a person is still typing in it.
  const [contactEmail, setContactEmail] = useState("");
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
    // The address is read before anything is spent on it: a value that cannot be an address
    // would only earn a 400 once the attempt had been opened, and a payment attempt is not
    // something to spend on a typo. Everything this rule lets through goes to the API, which
    // stays the authority on what an address is.
    const contact = describeContactEmail(contactEmail);
    if (contact.kind === "refused") {
      setView({ kind: "address_problem", problem: contact.problem });
      return;
    }

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
      // The address travels only when the buyer gave one: an empty field is the documented
      // way to say "mail nothing", and the request then carries no body at all.
      ...(contact.kind === "address" ? { email: contact.email } : {}),
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
  }, [contactEmail, loadReservation, order.reservationId, payReservation, router]);

  const windowNotice = window.notice;
  // The one announcement of this surface: what just happened, or what the window just
  // did. It is polite, and it changes once per event instead of once per second.
  const announcement =
    view.kind === "pending"
      ? "Contactando con el proveedor de pago…"
      : view.kind === "address_problem"
        ? `No se ha cobrado nada. ${view.problem}`
        : view.kind === "settled"
          ? `${view.outcome.headline}. El pedido trae las entradas de aquí abajo.`
          : view.kind === "answer" || view.kind === "problem"
            ? `${view.outcome.headline}. ${view.outcome.message}`
            : windowNotice !== null
              ? windowNotice
              : "Todavía no se ha cobrado nada por este pedido.";

  const seatsHref =
    order.eventId === ""
      ? null
      : `/events/${encodeURIComponent(order.eventId)}/seats`;

  // A payment is offered while the window allows one and the order holds a seat: an
  // order with nothing to charge would be refused (`not_sellable`), and a button that
  // can only fail is not an action. After an outcome, its own action is the one shown — and
  // a refused address keeps the Pay control in place, because fixing the field is how the
  // buyer pays and a control that vanished would leave nothing to press.
  const payable = window.payable && order.seats.length > 0;
  const showPay =
    payable &&
    (view.kind === "idle" ||
      view.kind === "pending" ||
      view.kind === "address_problem");
  const pending = view.kind === "pending";
  const addressProblem = view.kind === "address_problem" ? view.problem : null;

  // The order's own facts, as the receipt prints them: the seats, what paying costs right
  // now (the API's quote, presented — never a price multiplied here) and the deadline the
  // server granted. The lines and the total agree because the fee this version charges is
  // zero, which `seatReceipt` states out loud.
  const receipt = seatReceipt({
    seatLabels: order.seats.map(seatLine),
    count: order.seats.length,
    amountCents: order.amountDueCents ?? order.amountCents,
    amountDisplay: order.amountDueDisplay ?? order.amountDisplay,
  });
  const deadline =
    window.kind === "counting"
      ? describeHoldDeadline({
          displayLabel: formatCountdown(window.remainingMs),
          expired: false,
        })
      : window.kind === "expired"
        ? describeHoldDeadline({
            displayLabel: window.endLabel ?? "0:00",
            expired: true,
          })
        : null;
  const payLabel =
    order.amountDueDisplay === null
      ? "Pagar"
      : `Pagar ${presentAmount(order.amountDueDisplay)}`;

  return (
    // The boards' checkout grid: what this order is and what paying costs on the right, the
    // rest of the payment on the left. The two columns are placed explicitly, so the receipt
    // can stay first in the source (a narrow screen reads what it is buying before it reads
    // the form) without becoming the first column on a wide one.
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem] lg:items-start">
      <div className="flex min-w-0 flex-col lg:col-start-1 lg:row-start-1">
        <SummaryPanel
          title="Tu pedido"
          lines={receipt.lines}
          total={receipt.total}
          {...(deadline === null ? {} : { deadline })}
          note={amount.note}
        >
          {showPay ? (
            <Button
              type="button"
              onClick={handlePay}
              disabled={pending}
              aria-busy={pending}
            >
              {pending ? "Pagando…" : payLabel}
            </Button>
          ) : null}

          {/* The way back to the plan, as the boards print it under the amount. It is a link
              and nothing else: this page never re-holds a seat on its own. */}
          {seatsHref === null ? null : (
            <Link
              href={seatsHref}
              className="rounded-sm text-center text-sm underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background focus-visible:outline-none"
            >
              Cambiar butacas
            </Link>
          )}
        </SummaryPanel>
      </div>

      <div className="flex min-w-0 flex-col lg:col-start-2 lg:row-start-1 lg:sticky lg:top-4">
        <Card>
          <CardHeader>
            <h2 id="pay-order" className="m-0 text-base font-semibold leading-tight">
              Pagar este pedido
            </h2>
            <p className="m-0 max-w-prose text-sm text-muted-foreground">
              Un intento es un cobro. La taquilla cobra este pedido —la cantidad es suya, no
              la calcula esta página— y la clave que se guarda en esta pestaña hace que un
              reintento repita el mismo intento en lugar de ser un segundo pago.
            </p>
          </CardHeader>

          <CardContent className="flex flex-col gap-3">
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
                        <Link href={seatsHref}>Elegir las butacas otra vez</Link>
                      </Button>
                    </div>
                  )}
                </AlertDescription>
              </Alert>
            )}

            {/* The field belongs to the order, not to one attempt: it is shown for as long as a
                payment can still be made, so an address a buyer wants to correct after a decline
                is corrected here rather than being resent out of reach. */}
            {payable ? (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="payment-contact-email">
                  Correo para tus entradas{" "}
                  <span className="text-muted-foreground">(opcional)</span>
                </Label>
                <Input
                  id="payment-contact-email"
                  type="email"
                  autoComplete="email"
                  value={contactEmail}
                  onChange={(event) => setContactEmail(event.target.value)}
                  aria-invalid={addressProblem === null ? undefined : true}
                  aria-describedby={
                    addressProblem === null
                      ? "payment-contact-email-help"
                      : "payment-contact-email-help payment-contact-email-problem"
                  }
                  className="max-w-md"
                />
                {/* The conditions, in the order they matter: where the link goes, that it
                    expires, that it is not tied to the buyer, that the field is optional, and
                    that nothing checked the address. Every sentence is something the API and
                    the email worker actually do — none of them promises a message already sent,
                    because nothing here has been sent: the worker mails after the sale. */}
                <ul
                  id="payment-contact-email-help"
                  className="m-0 flex max-w-prose list-disc flex-col gap-1 pl-5 text-sm text-muted-foreground"
                >
                  <li>
                    Cuando el pago se apruebe, la taquilla enviará a esta dirección un enlace
                    que abre tus entradas.
                  </li>
                  <li>El enlace caduca, así que ábrelo mientras tengas el correo.</li>
                  <li>
                    Cualquiera con ese enlace puede abrir las entradas: no está atado a ti, ni a
                    este navegador, ni a esta página.
                  </li>
                  <li>
                    Si dejas el campo vacío no se envía nada. El PDF se sigue descargando desde
                    esta página, igual que ahora.
                  </li>
                  <li>
                    La dirección no se verifica: aquí nada comprueba que sea de quien la ha
                    escrito.
                  </li>
                </ul>
                {addressProblem === null ? null : (
                  <p
                    id="payment-contact-email-problem"
                    className="m-0 max-w-prose text-sm font-medium text-destructive"
                  >
                    {addressProblem}
                  </p>
                )}
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
      </div>
    </div>
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
        {outcome.kind === "approved" ? <Badge variant="success">Aprobado</Badge> : null}
        {outcome.kind === "declined" ? <Badge variant="warning">Rechazado</Badge> : null}
      </div>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">{outcome.message}</p>
        {outcome.kind === "approved" && amount.known ? (
          <p className="m-0">
            Se han cobrado <strong>{amount.amountLabel}</strong> por este pedido.
          </p>
        ) : null}
        {tickets.length > 0 ? (
          <IssuedTickets ticketIds={tickets} />
        ) : null}
        {outcome.kind === "approved" && tickets.length === 0 ? (
          <p className="m-0">
            El pago se aprobó pero la respuesta no traía ninguna entrada. No vuelvas a pagar:
            recarga esta página, que lee el pedido y muestra las entradas que tiene, y
            contacta con la taquilla si sigue vacía.
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
              <Link href={seatsHref}>Elegir las butacas otra vez</Link>
            </Button>
          ) : null}
          {!canRetry && !outcome.offersSeatMap ? (
            <Button asChild variant="outline" size="sm">
              <Link href="/events">Volver a la cartelera</Link>
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
            Abrir la entrada <code>{ticketId}</code>
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
      return "Intentar el pago otra vez";
    case "payment_in_flight":
      return "Continuar el mismo pago";
    case "temporary":
      return "Reintentar el pago";
    default:
      return "Volver a intentarlo";
  }
}

/** `Fila A, butaca 3`; the values painted at the door, never a reconstructed label. */
function seatLine(seat: PublicReservation["seats"][number]): string {
  const row = seat.rowLabel.trim();
  const number = seat.seatNumber.trim();
  if (row !== "" && number !== "") {
    return `Fila ${row}, butaca ${number}`;
  }
  return `Butaca ${seat.eventSeatId}`;
}
