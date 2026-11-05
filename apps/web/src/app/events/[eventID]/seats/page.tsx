// /events/[eventID]/seats — one screening's seat selection (M3.5 · T5 · S3, S6, S8).
//
// This page is reached from the catalogue, so it owes the buyer the screening they
// clicked: the film, the venue, the room, the hour in the venue's own zone and the
// price of a seat. It reads two things through ./actions.ts and rules on both here:
//
//   GET /api/v1/events/{eventID}         the screening, including `onSale` and `status`
//   GET /api/v1/events/{eventID}/seats   the map, one entry per seat
//
// The states it can render are deliberately distinct, because each one calls for a
// different thing from the buyer:
//
//   1. loading              the shell is up and the screening is being read;
//   2. the screening does not exist (404)
//                           nothing to seat; no retry will change that;
//   3. the screening could not be read (503, network, 5xx)
//                           retryable, and never drawn as an empty page;
//   4. not on sale          the screening exists, `onSale` is false, and the reason is
//                           derived from `status` (cancelled, still a draft, already
//                           started, or a status this page does not interpret and so
//                           prints verbatim). The context is still shown and the seat
//                           map and the continue action are not, because there is
//                           nothing to sell (S8);
//   5. the map could not be read
//                           the same retry state, because a screening that is on sale
//                           with an unreadable map is still not an empty room;
//   6. the map            the real layout, keyboard-operable, with the running amount.
//
// Two rules are load-bearing. `availableSeats` from the screening read is a count taken
// at a different moment from the map, so it is never printed as the truth and never
// decides what can be selected: the map is the later answer, selection comes from the
// seat read alone, and the panel says so when the two disagree. And the amount on this
// page is a summary of the screening's price per seat — the order the API creates is
// what gets paid (T6), and the copy does not pretend otherwise.
//
// Since the Marquesina redesign (visual-redesign · T5) this screen is one of two that open
// with the shared `ScreeningBanner` and close with the shared receipt, so the vocabulary of
// "which session this is" and "what it costs" is one component each instead of two copies.
// The copy is Castilian Spanish (L4): the film title and the hours come from the API and are
// never translated; everything this screen says is.
//
// The route is server-rendered per request: availability is live data and must never be
// prerendered or cached.
import Link from "next/link";
import { Suspense } from "react";

import { PageHeader } from "@/components";
import { ScreeningBanner } from "@/components/screening-banner";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { type BadgeProps } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import type { PublicProblem, PublicScreening } from "@/lib/public-api";
import type { SaleState } from "@/lib/seat-selection";
import {
  describeSaleState,
  describeScreeningFailure,
  layoutFromAvailability,
} from "@/lib/seat-selection";
import { loadAvailability, loadScreening, requestHold } from "./actions";
import { SeatSelectionPanel } from "./seat-map";

export const dynamic = "force-dynamic";

interface SeatsPageProps {
  params: Promise<{ eventID: string }>;
}

export default async function EventSeatsPage({ params }: SeatsPageProps) {
  const { eventID } = await params;
  const eventId = eventID.trim();

  return (
    <main id="main-content" className="foundation">
      <Suspense fallback={<SeatsLoading />}>
        <Screening eventId={eventId} />
      </Suspense>
    </main>
  );
}

/** Reads the screening, then decides which of the six states above is the truthful one. */
async function Screening({ eventId }: { eventId: string }) {
  const read = await loadScreening(eventId);
  if (!read.ok) {
    return <ScreeningProblem problem={read.problem} eventId={eventId} />;
  }

  const screening = read.screening;
  const sale = describeSaleState(screening);

  // Not on sale is not an empty seat map: the buyer keeps the context they arrived
  // with, is told why, and is not offered a control that could not work.
  if (!sale.onSale) {
    return (
      <>
        <ScreeningContext screening={screening} sale={sale} />
        <NotOnSale sale={sale} />
      </>
    );
  }

  const availability = await loadAvailability(eventId);
  if (!availability.ok) {
    return (
      <>
        <ScreeningContext screening={screening} sale={sale} />
        <ScreeningProblem problem={availability.problem} eventId={eventId} />
      </>
    );
  }

  return (
    <>
      <ScreeningContext screening={screening} sale={sale} />
      <SeatSelectionPanel
        eventId={eventId}
        screening={screening}
        initialSeats={availability.seats}
        layout={layoutFromAvailability(availability.seats)}
        loadAvailability={loadAvailability}
        requestHold={requestHold}
      />
    </>
  );
}

/**
 * The top of the page: what the buyer clicked on, printed as served.
 *
 * The hour is `startsAtLocal`, the API's presentation in the venue's own zone — a page
 * whose whole purpose is to print the venue's clock must never re-format the UTC
 * instant into the browser's. The price per seat is `priceDisplay`, the same text the
 * catalogue showed, and availability is not stated here: it belongs to the map, which
 * is read later and is the answer this page sells.
 *
 * The `h1` is the screen's own deed ("Elegir butacas") and the film moved into the strip:
 * a buyer who shares this address shares the screening, and the title of the page is the
 * thing they are doing on it. The state of the sale is the badge's word, so the strip never
 * relies on its tone.
 */
function ScreeningContext({
  screening,
  sale,
}: {
  screening: PublicScreening;
  sale: SaleState;
}) {
  const startsAt = screening.startsAt === "" ? undefined : screening.startsAt;

  return (
    <>
      <PageHeader
        eyebrow={`Cartelera / ${screening.filmTitle}`}
        title="Elegir butacas"
        description="La hora es la del propio local y el precio es por butaca. Las butacas libres las cuenta el plano, que se lee al cargar esta página."
      />

      <ScreeningBanner
        filmTitle={screening.filmTitle}
        cinemaName={screening.cinemaName}
        roomName={screening.roomName}
        startsAtLocal={screening.startsAtLocal}
        startsAt={startsAt}
        priceDisplay={screening.priceDisplay}
        statusWord={sale.badge}
        statusTone={badgeVariant(sale)}
      />
    </>
  );
}

/**
 * The screening exists and the API is not selling it.
 *
 * This is the honest answer to a stale link, and it is not a failure: the reason comes
 * from `describeSaleState`, which reads `status` rather than guessing, and no seat map
 * is drawn — an empty room and a closed sale look nothing alike, and showing seats here
 * would invite a click that could never be honoured.
 */
function NotOnSale({ sale }: { sale: SaleState }) {
  return (
    <Alert
      variant={sale.reason === "cancelled" ? "destructive" : "warning"}
      role="status"
      aria-labelledby="not-on-sale"
      className="flex flex-col gap-2"
    >
      <h2 id="not-on-sale" className="m-0 text-base font-semibold">
        {sale.headline ?? "Esta sesión no está a la venta"}
      </h2>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">{sale.message}</p>
        <p className="m-0">
          No se muestra ningún plano de butacas, porque no hay nada que elegir: las mismas
          butacas solo parecerían seleccionables y luego se negarían a reservarse.
        </p>
        <div className="flex flex-wrap gap-2 pt-1">
          <Button asChild variant="outline" size="sm">
            <Link href="/events">Ver qué está a la venta</Link>
          </Button>
        </div>
      </AlertDescription>
    </Alert>
  );
}

/**
 * A read that did not answer: the screening does not exist, or the API could not be
 * reached.
 *
 * The two are kept apart by `describeScreeningFailure`. A missing screening offers no
 * retry, because asking again would get the same answer; a temporary failure offers
 * one, because that is the only thing that helps. Neither is rendered as an empty page.
 */
function ScreeningProblem({
  problem,
  eventId,
}: {
  problem: PublicProblem;
  eventId: string;
}) {
  const view = describeScreeningFailure(problem);

  return (
    <Alert
      variant="destructive"
      role="alert"
      aria-labelledby="screening-problem"
      className="flex flex-col gap-2"
    >
      <h2 id="screening-problem" className="m-0 text-base font-semibold">
        {view.headline}
      </h2>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">
          <code>{problem.code}</code> · HTTP {problem.status}
        </p>
        <p className="m-0">{view.message}</p>
        <div className="flex flex-wrap gap-2 pt-1">
          {view.retryable ? (
            <Button asChild size="sm">
              <Link href={`/events/${encodeURIComponent(eventId)}/seats`}>
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
  );
}

/**
 * The page while the screening is still being read.
 *
 * A real region with a heading and a sentence, so a slow read is announced instead of
 * looking like a broken page; the skeletons are decorative and hidden from assistive
 * technology by the primitive itself.
 */
function SeatsLoading() {
  return (
    <>
      <PageHeader
        eyebrow="Cartelera"
        title="Elegir butacas"
        description="Cargando la sesión, su precio y sus butacas."
      />
      <section aria-labelledby="seats-loading" className="flex flex-col gap-3">
        <h2 id="seats-loading">Plano de butacas</h2>
        <p role="status" className="m-0 max-w-prose">
          <span className="text-muted-foreground">Cargando el plano de butacas…</span>
        </p>
        <div
          aria-hidden="true"
          className="flex flex-col gap-3 rounded-lg border border-border bg-card p-5"
        >
          {[0, 1, 2].map((row) => (
            <div key={row} className="flex flex-wrap gap-2">
              {[0, 1, 2, 3, 4, 5].map((seat) => (
                <Skeleton key={seat} className="size-11 rounded-sm" />
              ))}
            </div>
          ))}
        </div>
      </section>
    </>
  );
}

/** The badge's tone for a sale state; the words in `sale.badge` carry the meaning. */
function badgeVariant(sale: SaleState): BadgeProps["variant"] {
  if (sale.onSale) {
    return "success";
  }
  return sale.reason === "cancelled" ? "danger" : "warning";
}
