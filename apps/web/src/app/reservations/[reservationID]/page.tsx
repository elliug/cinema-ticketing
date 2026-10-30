// /reservations/[reservationID] — the checkout (M3 · T7, M3.5 · T6 · S3, S6, S8).
//
// The page a buyer reaches after holding seats, and the last purchase screen. It owes
// three things M3 could not give it:
//
//   1. it survives a reload. M3 read nothing: the hold response and the payment answer
//      were one-shot, so a refresh left the page guessing. It now reads
//      `GET /api/v1/reservations/{reservationID}`, which answers the screening (film,
//      venue, room and the venue's own hour), the seats, the server deadline, the money
//      actually captured and the tickets issued — so a returning buyer sees their order
//      without paying twice and without losing the context they arrived with;
//   2. it keeps the money honest. The amount printed before a payment is the API's own,
//      and the read answers `null` while nothing has been captured; after a payment it is
//      the number the payment answer reported. Nothing here multiplies a price by a seat
//      count (`describeOrderAmount`, lib/payment-key.ts);
//   3. it is off the legacy stylesheet: shadcn primitives and utilities, the `h1` is the
//      screening (or the order, when there is no screening to name), the page has one
//      polite live region (inside the panel) and every action keeps a visible focus ring.
//
// The states this page can render, each one a different answer:
//
//   1. the address names no reservation      nothing to read; where to start again;
//   2. loading                               a real region, not a blank page;
//   3. the order does not exist (404)        final: asking again cannot answer it;
//   4. the order could not be read (503)     retryable, and never drawn as an order;
//   5. the order is paid                     its tickets, the amount taken, on a reload;
//   6. the order is not paid                 the panel: pay, countdown, every outcome.
//
// The demo session never reaches this page (S2, S8): a guest order is addressed by its
// identifier, the identifier is the capability, and nothing here checks who you are.
// The page says so rather than implying otherwise.
//
// The route is server-rendered per request: a hold window is live data.
import Link from "next/link";
import { Suspense } from "react";

import { PageHeader } from "@/components";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { PublicProblem, PublicReservation } from "@/lib/public-api";
import type { HoldWindowView } from "@/lib/payment-key";
import { describeHoldWindow, describeOrderAmount } from "@/lib/payment-key";
import { loadReservation, payReservationAction } from "./actions";
import { IssuedTickets, PaymentPanel } from "./payment-panel";

export const dynamic = "force-dynamic";

interface ReservationPageProps {
  params: Promise<{ reservationID: string }>;
}

export default async function ReservationPage({ params }: ReservationPageProps) {
  const { reservationID } = await params;
  const reservationId = reservationID.trim();

  // The skip link and the `#main-content` target belong to the root layout now: this
  // page used to render a second one, which is one skip link too many for a keyboard.
  return (
    <main id="main-content" className="foundation">
      <Suspense fallback={<OrderLoading />}>
        <Order reservationId={reservationId} />
      </Suspense>
    </main>
  );
}

/** Reads the order and decides which of the states above is the truthful one. */
async function Order({ reservationId }: { reservationId: string }) {
  if (reservationId === "") {
    return <MissingOrder />;
  }

  const read = await loadReservation(reservationId);
  if (!read.ok) {
    return <ReadProblem problem={read.problem} reservationId={reservationId} />;
  }

  const order = read.reservation;
  // The server's clock decides what the order's own deadline means right now, and the
  // same instant seeds the panel's clock so its first render matches this one; the panel
  // repeats the rule on the browser's clock while the window runs.
  const renderedAt = Date.now();
  const window = describeHoldWindow(order, renderedAt);
  // Resolved server side and required by no credential: it only builds the ticket links.

  return (
    <>
      <PageHeader
        eyebrow="Checkout · M3.5"
        title={order.filmTitle}
        description={`${order.cinemaName} · ${order.roomName}. Times are the venue's own clock. This is a guest checkout: nothing here checks who you are, and the order's identifier is what opens it.`}
      />

      <OrderSummary order={order} window={window} />

      {window.kind === "paid" ? (
        <PaidOrder order={order} />
      ) : (
        <PaymentPanel
          reservation={order}
          renderedAt={renderedAt}
          payReservation={payReservationAction}
          loadReservation={loadReservation}
        />
      )}
    </>
  );
}

/**
 * What the order is: the screening it is for, its seats, its deadline and the money
 * actually taken.
 *
 * The amount row is the S8 rule in one line: a number appears only when the API states
 * one, and the sentence beside it says which number it is. While nothing has been taken
 * the row says exactly that instead of a figure the page would have had to invent.
 */
function OrderSummary({
  order,
  window,
}: {
  order: PublicReservation;
  window: HoldWindowView;
}) {
  const amount = describeOrderAmount(order, null);
  const status = statusBadge(window, order);
  const startsAt = order.startsAt === "" ? undefined : order.startsAt;

  return (
    <Card aria-labelledby="order-summary">
      <CardHeader>
        <div className="flex flex-wrap items-center gap-2">
          {/* An `h2`, not the card primitive's `h3`: the outline is the screening (h1),
              then the order and the payment, as the page reads. */}
          <h2 id="order-summary" className="m-0 text-lg leading-tight font-semibold">
            Your order
          </h2>
          {/* The word is the state; the tone only reinforces it. */}
          <Badge variant={status.variant}>{status.word}</Badge>
        </div>
        <CardDescription>
          Read from the box office when this page loaded, so reloading it loses nothing:
          the same order answers with the same screening, seats and deadline.
        </CardDescription>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">Order</dt>
          <dd className="m-0 font-medium break-all">
            <code>{order.reservationId}</code>
          </dd>

          <dt className="text-muted-foreground">Seats</dt>
          <dd className="m-0 font-medium break-words">
            {order.seats.length === 0
              ? "No seat: this order holds none."
              : order.seats.map(seatLabel).join(", ")}
          </dd>

          <dt className="text-muted-foreground">Starts</dt>
          <dd className="m-0 font-medium break-words">
            <time dateTime={startsAt}>{order.startsAtLocal}</time>
          </dd>

          <dt className="text-muted-foreground">Amount</dt>
          <dd className="m-0 font-medium">
            {amount.known
              ? `${amount.amountLabel} taken`
              : order.amountDueDisplay
                ? `${order.amountDueDisplay} at the current price`
                : "Not charged yet"}
          </dd>
        </dl>

        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          {amount.note}
        </p>
      </CardContent>
    </Card>
  );
}

/**
 * The order is paid: the tickets, and how to open them again.
 *
 * This is what makes a reload of a paid order honest. The payment answer is gone and the
 * read is the only thing left, so the tickets come from the order itself — and nothing
 * here claims more than a record of sale: whether a ticket still admits its holder is
 * answered by the box office when it is scanned, never by this page (S8).
 */
function PaidOrder({
  order,
}: {
  order: PublicReservation;
}) {
  return (
    <Card aria-labelledby="paid-order">
      <CardHeader>
        <div className="flex flex-wrap items-center gap-2">
          <h2 id="paid-order" className="m-0 text-lg leading-tight font-semibold">
            Your tickets
          </h2>
          <Badge variant="success">Paid</Badge>
        </div>
        <CardDescription>
          The order carries a captured payment, so there is nothing left to charge. These
          are the tickets the box office issued for it.
        </CardDescription>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        {order.ticketIds.length === 0 ? (
          <p className="m-0 max-w-prose">
            The order is paid but its read carries no ticket. Do not pay again: reload
            this page once, and contact the box office if it stays empty.
          </p>
        ) : (
          <IssuedTickets ticketIds={order.ticketIds} />
        )}

        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          A ticket is the record of a sale. Whether it still admits you is answered by the
          box office when it is scanned — this page cannot and does not promise it.
        </p>

        <div className="flex flex-wrap gap-2">
          <Button asChild variant="outline" size="sm">
            <Link href="/events">Back to the catalogue</Link>
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

/** The address names no reservation: there is nothing to read and nothing to pay. */
function MissingOrder() {
  return (
    <>
      <PageHeader
        eyebrow="Checkout · M3.5"
        title="Your order"
        description="This address does not name an order, so there is nothing to read and nothing to pay."
      />
      <Alert variant="destructive" role="alert" aria-labelledby="missing-order">
        <h2 id="missing-order" className="m-0 text-base font-semibold">
          This address names no reservation
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">
            Open this page from the seat map after holding seats: the hold creates the
            order and its identifier is what this page is addressed by.
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            <Button asChild size="sm">
              <Link href="/events">See what is on sale</Link>
            </Button>
          </div>
        </AlertDescription>
      </Alert>
    </>
  );
}

/**
 * The order could not be read, and the two reasons stay apart.
 *
 * A `404` is final: no retry answers it, and the way forward is the catalogue. A `503`
 * (or an unreachable API) is retryable, and it is never drawn as an order with no seats
 * or no amount — an empty order and an unreadable one are different facts, and this page
 * prints the code and the API's own detail rather than smoothing them over.
 */
function ReadProblem({
  problem,
  reservationId,
}: {
  problem: PublicProblem;
  reservationId: string;
}) {
  const missing = problem.status === 404 || problem.code === "not_found";
  const temporary =
    problem.code === "network_error" ||
    problem.code === "service_unavailable" ||
    problem.code === "coordination_unavailable" ||
    problem.status >= 500;

  return (
    <>
      <PageHeader
        eyebrow="Checkout · M3.5"
        title="Your order"
        description="The box office was asked for this order, and the answer did not describe one."
      />
      <Alert
        variant="destructive"
        role="alert"
        aria-labelledby="order-problem"
        className="flex flex-col gap-2"
      >
        <h2 id="order-problem" className="m-0 text-base font-semibold">
          {missing ? "This order does not exist" : "The order could not be loaded"}
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">
            <code>{problem.code}</code> · HTTP {problem.status}
          </p>
          <p className="m-0">{problem.detail}</p>
          <p className="m-0">
            {missing
              ? "No order answers to this address, so there is nothing to pay and nothing to show. Check the link you followed: the seat map creates the order and hands out its address."
              : temporary
                ? "The box office is temporarily unavailable. Nothing is lost by asking again — this page only reads, so a retry cannot charge, hold or release anything."
                : "The API refused this read. Asking again is worth one try; if it persists, the order needs the box office."}
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            {temporary ? (
              <Button asChild size="sm">
                <Link
                  href={`/reservations/${encodeURIComponent(reservationId)}`}
                >
                  Ask again
                </Link>
              </Button>
            ) : null}
            <Button asChild variant="outline" size="sm">
              <Link href="/events">Back to the catalogue</Link>
            </Button>
          </div>
        </AlertDescription>
      </Alert>
    </>
  );
}

/**
 * The order while it is still being read.
 *
 * A real region with a heading and a sentence, so a slow read is announced instead of
 * looking like a broken page; the skeletons are decorative and hidden from assistive
 * technology by the primitive itself.
 */
function OrderLoading() {
  return (
    <>
      <PageHeader
        eyebrow="Checkout · M3.5"
        title="Your order"
        description="Loading the order, its screening, its seats and its deadline."
      />
      <section aria-labelledby="order-loading" className="flex flex-col gap-3">
        <h2 id="order-loading">Your order</h2>
        <p role="status" className="m-0 max-w-prose">
          <span className="text-muted-foreground">Loading your order…</span>
        </p>
        <Card aria-hidden="true" className="flex flex-col gap-3 p-5">
          {[0, 1, 2, 3].map((row) => (
            <Skeleton key={row} className="h-4 w-2/3" />
          ))}
        </Card>
      </section>
    </>
  );
}

/** `Row A seat 3`, from the values painted at the door; never a reconstructed label. */
function seatLabel(seat: PublicReservation["seats"][number]): string {
  const row = seat.rowLabel.trim();
  const number = seat.seatNumber.trim();
  if (row !== "" && number !== "") {
    return `Row ${row} seat ${number}`;
  }
  return `Seat ${seat.eventSeatId}`;
}

/**
 * The order's status as one word plus a tone.
 *
 * The words come from the tested window rule (`describeHoldWindow`), so the badge cannot
 * disagree with the block underneath it. The one case the rule deliberately does not
 * name is a status this milestone does not interpret: it is printed verbatim, because a
 * second vocabulary would be a second claim.
 */
function statusBadge(
  window: HoldWindowView,
  order: PublicReservation,
): { word: string; variant: BadgeProps["variant"] } {
  switch (window.kind) {
    case "paid":
      return { word: "Paid", variant: "success" };
    case "cancelled":
      return { word: "Cancelled", variant: "danger" };
    case "expired":
      return { word: "Hold expired", variant: "warning" };
    case "counting":
      return { word: "Held", variant: "info" };
    default:
      return {
        word: order.status.trim() === "" ? "Unknown" : order.status.trim(),
        variant: "outline",
      };
  }
}
