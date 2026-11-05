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
//   3. the review of it is visual: the `h1` is what the buyer is doing ("Tu compra"), the
//      order's identifier leads the eyebrow, the screening is the shared `ScreeningBanner`,
//      and the receipt is the shared `SummaryPanel` — the same two components the seat plan
//      prints (visual-redesign · T5). The page has one polite live region (inside the
//      panel) and every action keeps a visible focus ring.
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
import { ScreeningBanner } from "@/components/screening-banner";
import { SummaryPanel } from "@/components/summary-panel";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import type { PublicProblem, PublicReservation } from "@/lib/public-api";
import type { HoldWindowView } from "@/lib/payment-key";
import {
  describeHoldWindow,
  describeOrderAmount,
} from "@/lib/payment-key";
import { seatReceipt } from "@/lib/receipt";
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
        eyebrow={`Pago · pedido ${order.reservationId}`}
        title="Tu compra"
        description="La hora es la del propio local. Este pago es de invitado: nada aquí comprueba quién eres, y el identificador del pedido es lo que lo abre."
      />

      {/* The same strip the seat plan prints: which session, where and when. The price of a
          seat is not part of it because a reservation read does not carry one. */}
      <ScreeningBanner
        filmTitle={order.filmTitle}
        cinemaName={order.cinemaName}
        roomName={order.roomName}
        startsAtLocal={order.startsAtLocal}
        startsAt={order.startsAt === "" ? undefined : order.startsAt}
      />

      <OrderState order={order} window={window} />

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
 * Where the order stands, in one word plus a tone and the read it came from.
 *
 * This is what the old order card said above its table, minus the table: the seats, the
 * money and the deadline are the receipt's business now, and the screening's hour is the
 * strip's. What is left here is the one thing neither of them answers — the state — and the
 * sentence about reloading, which is the whole reason this page reads the order at all.
 */
function OrderState({
  order,
  window,
}: {
  order: PublicReservation;
  window: HoldWindowView;
}) {
  const status = statusBadge(window, order);

  return (
    <div className="flex flex-wrap items-center gap-3">
      {/* The word is the state; the tone only reinforces it. */}
      <Badge variant={status.variant}>{status.word}</Badge>
      <p className="m-0 max-w-prose text-sm text-muted-foreground">
        Leído de la taquilla al cargar esta página, así que recargarla no pierde nada: el
        mismo pedido responde con la misma sesión, las mismas butacas y el mismo plazo.
      </p>
    </div>
  );
}

/**
 * The order is paid: the receipt of what was taken, and the tickets.
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
  const amount = describeOrderAmount(order, null);
  const receipt = seatReceipt({
    seatLabels: order.seats.map(seatLabel),
    count: order.seats.length,
    amountCents: order.amountCents,
    amountDisplay: order.amountDisplay,
  });

  return (
    <>
      <SummaryPanel
        title="Tu pedido"
        lines={receipt.lines}
        total={receipt.total}
        note={amount.note}
      >
        <Button asChild variant="outline" size="sm">
          <Link href="/events">Volver a la cartelera</Link>
        </Button>
      </SummaryPanel>

      <section
        aria-labelledby="paid-order"
        className="flex flex-col gap-3 rounded-lg border border-border bg-card p-5"
      >
        <div className="flex flex-wrap items-center gap-2">
          <h2 id="paid-order" className="m-0 text-base font-semibold leading-tight">
            Tus entradas
          </h2>
          <Badge variant="success">Pagada</Badge>
        </div>

        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          El pedido lleva un pago capturado, así que no queda nada por cobrar. Estas son las
          entradas que emitió la taquilla por él.
        </p>

        {order.ticketIds.length === 0 ? (
          <p className="m-0 max-w-prose">
            El pedido está pagado pero su lectura no trae ninguna entrada. No vuelvas a pagar:
            recarga esta página una vez, y contacta con la taquilla si sigue vacía.
          </p>
        ) : (
          <IssuedTickets ticketIds={order.ticketIds} />
        )}

        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Una entrada es el registro de una venta. Que todavía te deje pasar lo responde la
          taquilla al escanearla: esta página no puede prometerlo y no lo promete.
        </p>
      </section>
    </>
  );
}

/** The address names no reservation: there is nothing to read and nothing to pay. */
function MissingOrder() {
  return (
    <>
      <PageHeader
        eyebrow="Pago"
        title="Tu compra"
        description="Esta dirección no nombra ningún pedido, así que no hay nada que leer ni nada que pagar."
      />
      <Alert variant="destructive" role="alert" aria-labelledby="missing-order">
        <h2 id="missing-order" className="m-0 text-base font-semibold">
          Esta dirección no nombra ninguna reserva
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">
            Abre esta página desde el plano de butacas, después de reservar: la reserva crea
            el pedido, y su identificador es lo que esta página necesita.
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            <Button asChild size="sm">
              <Link href="/events">Ver qué está a la venta</Link>
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
        eyebrow="Pago"
        title="Tu compra"
        description="Se le ha pedido este pedido a la taquilla, y la respuesta no describía ninguno."
      />
      <Alert
        variant="destructive"
        role="alert"
        aria-labelledby="order-problem"
        className="flex flex-col gap-2"
      >
        <h2 id="order-problem" className="m-0 text-base font-semibold">
          {missing ? "Este pedido no existe" : "No se pudo cargar el pedido"}
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">
            <code>{problem.code}</code> · HTTP {problem.status}
          </p>
          <p className="m-0">{problem.detail}</p>
          <p className="m-0">
            {missing
              ? "Ningún pedido responde a esta dirección, así que no hay nada que pagar ni nada que mostrar. Revisa el enlace que has seguido: el plano de butacas crea el pedido y reparte su dirección."
              : temporary
                ? "La taquilla no está disponible temporalmente. Volver a preguntar no pierde nada: esta página solo lee, así que un reintento no puede cobrar, reservar ni liberar nada."
                : "La API rechazó esta lectura. Volver a preguntar merece un intento; si sigue así, el pedido necesita a la taquilla."}
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            {temporary ? (
              <Button asChild size="sm">
                <Link
                  href={`/reservations/${encodeURIComponent(reservationId)}`}
                >
                  Volver a preguntar
                </Link>
              </Button>
            ) : null}
            <Button asChild variant="outline" size="sm">
              <Link href="/events">Volver a la cartelera</Link>
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
        eyebrow="Pago"
        title="Tu compra"
        description="Cargando el pedido, su sesión, sus butacas y su plazo."
      />
      <section aria-labelledby="order-loading" className="flex flex-col gap-3">
        <h2 id="order-loading">Tu pedido</h2>
        <p role="status" className="m-0 max-w-prose">
          <span className="text-muted-foreground">Cargando tu pedido…</span>
        </p>
        <div
          aria-hidden="true"
          className="flex flex-col gap-3 rounded-lg border border-border bg-card p-5"
        >
          {[0, 1, 2, 3].map((row) => (
            <Skeleton key={row} className="h-4 w-2/3" />
          ))}
        </div>
      </section>
    </>
  );
}

/**
 * One seat as the door paints it: "Fila A, butaca 3", from the values the API served.
 *
 * Never a reconstructed label: a seat without a row or a number keeps the event seat id it
 * does have, and the union of the two shapes is what the receipt prints.
 */
function seatLabel(seat: PublicReservation["seats"][number]): string {
  const row = seat.rowLabel.trim();
  const number = seat.seatNumber.trim();
  if (row !== "" && number !== "") {
    return `Fila ${row}, butaca ${number}`;
  }
  return `Butaca ${seat.eventSeatId}`;
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
      return { word: "Pagada", variant: "success" };
    case "cancelled":
      return { word: "Cancelada", variant: "danger" };
    case "expired":
      return { word: "Reserva caducada", variant: "warning" };
    case "counting":
      return { word: "Reservada", variant: "info" };
    default:
      return {
        word: order.status.trim() === "" ? "Desconocido" : order.status.trim(),
        variant: "outline",
      };
  }
}
