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
// The route is server-rendered per request: availability is live data and must never be
// prerendered or cached.
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
        eyebrow="Seat selection · M3.5"
        title={screening.filmTitle}
        description={`${screening.cinemaName} · ${screening.roomName}. Times are the venue's own clock, and the price is per seat.`}
      />

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center gap-2">
            {/* An `h2`, not the card primitive's `h3`: the page's outline is the film
                (h1), then the screening, the map and the selection, so a screen reader's
                heading list reads as the page does. */}
            <h2 className="m-0 text-lg leading-tight font-semibold">Your screening</h2>
            {/* The word is the state; the tone only reinforces it. */}
            <Badge variant={badgeVariant(sale)}>{sale.badge}</Badge>
          </div>
          <CardDescription>
            Read from the catalogue when this page was loaded, so nothing you clicked on
            is lost on the way here.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
            <dt className="text-muted-foreground">Film</dt>
            <dd className="m-0 font-medium break-words">{screening.filmTitle}</dd>

            <dt className="text-muted-foreground">Venue</dt>
            <dd className="m-0 font-medium break-words">
              {screening.cinemaName} · {screening.roomName}
            </dd>

            <dt className="text-muted-foreground">Starts</dt>
            <dd className="m-0 font-medium break-words">
              <time dateTime={startsAt}>{screening.startsAtLocal}</time>
            </dd>

            <dt className="text-muted-foreground">Price per seat</dt>
            <dd className="m-0 font-medium">{screening.priceDisplay}</dd>
          </dl>
        </CardContent>
      </Card>
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
      <h2 id="not-on-sale">{sale.headline ?? "This screening is not on sale"}</h2>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">{sale.message}</p>
        <p className="m-0">
          No seat map is shown, because there is nothing to choose: the same seats would
          only look selectable and then refuse to be held.
        </p>
        <div className="flex flex-wrap gap-2 pt-1">
          <Button asChild variant="outline" size="sm">
            <Link href="/events">See what is on sale</Link>
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
      <h2 id="screening-problem">{view.headline}</h2>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">
          <code>{problem.code}</code> · HTTP {problem.status}
        </p>
        <p className="m-0">{view.message}</p>
        <div className="flex flex-wrap gap-2 pt-1">
          {view.retryable ? (
            <Button asChild size="sm">
              <Link href={`/events/${encodeURIComponent(eventId)}/seats`}>
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
        eyebrow="Seat selection · M3.5"
        title="Choose your seats"
        description="Loading the screening, its price and its seats."
      />
      <section aria-labelledby="seats-loading" className="flex flex-col gap-3">
        <h2 id="seats-loading">Seat map</h2>
        <p role="status" className="m-0 max-w-prose">
          <span className="text-muted-foreground">Loading the seat map…</span>
        </p>
        <Card aria-hidden="true" className="flex flex-col gap-3 p-5">
          {[0, 1, 2].map((row) => (
            <div key={row} className="flex flex-wrap gap-2">
              {[0, 1, 2, 3, 4, 5].map((seat) => (
                <Skeleton key={seat} className="size-11 rounded-md" />
              ))}
            </div>
          ))}
        </Card>
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
