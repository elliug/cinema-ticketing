// /events — the public catalogue (M3.5 · T4 · S3, S5, S8).
//
// The first screen a stranger can reach without knowing an identifier: it reads
// `GET /api/v1/events` through lib/public-api.ts and lists the screenings that are
// on sale, each one linking to its own seat page. It is a server component and
// nothing on it is interactive, so no client boundary is added here.
//
// What it prints is served, never derived:
//
//   - the screening time is `startsAtLocal`, the API's presentation in the venue's
//     own zone. Re-formatting `startsAt` here would print the server's or the
//     browser's zone on a page whose whole purpose is to print the venue's, so the
//     parser refuses a response without it rather than guessing;
//   - the price is `priceDisplay`, the one formatter the catalogue and the ticket
//     share. No currency symbol is added and no amount is computed;
//   - availability is the read model's pair of numbers, printed as words as well as
//     digits ("12 of 40 seats available"), with "Sold out" spelled out when it is
//     zero. Colour reinforces that sentence and never carries it (S8);
//   - the order is the API's screening order. This page does not sort: a second
//     opinion about the sequence would be a second answer to a question one read
//     already decided.
//
// What it deliberately does not do is fill a gap (S8). An empty catalogue says it
// is empty, an unreadable one says it could not be read, and neither invents a
// film, a time or a "coming soon" row that would look like data. A slow catalogue
// streams: the page header arrives, and the list's own region says it is loading
// while the read is in flight.
//
// The route is server-rendered per request: availability is live and must never be
// prerendered or cached.
import Link from "next/link";
import { Suspense } from "react";

import { PageHeader } from "@/components";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { PublicProblem, PublicScreening } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";

export const dynamic = "force-dynamic";

export default function EventsPage() {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Catalogue · M3.5"
        title="Now showing"
        description="Every screening on sale right now, in screening order. Times are the venue's own clock, prices are per seat, and the seats available are the number the API counted when this page was loaded."
      />

      <Suspense fallback={<CatalogueLoading />}>
        <Catalogue />
      </Suspense>
    </main>
  );
}

/** Reads the catalogue and decides which of its four honest states to render. */
async function Catalogue() {
  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return <CatalogueProblem problem={config.error} />;
  }

  const result = await createPublicClient(config.value).listEvents();
  if (!result.ok) {
    return <CatalogueProblem problem={result.error} />;
  }
  if (result.value.length === 0) {
    return <CatalogueEmpty />;
  }

  return <CatalogueList screenings={result.value} />;
}

function CatalogueList({ screenings }: { screenings: PublicScreening[] }) {
  return (
    <section aria-labelledby="screenings-title" className="flex flex-col gap-3">
      <h2 id="screenings-title">Screenings on sale</h2>
      <p className="m-0 max-w-prose">
        {screenings.length === 1
          ? "1 screening is on sale."
          : `${screenings.length} screenings are on sale.`}{" "}
        <span className="text-muted-foreground">
          Each entry opens that screening&apos;s seat map, where the seats are
          chosen and held.
        </span>
      </p>

      {/* `role="list"` keeps the list announced as one: `list-none` removes the
          marker and, in Safari, the list semantics with it. */}
      <ul role="list" className="m-0 grid list-none grid-cols-1 gap-4 p-0 sm:grid-cols-2">
        {screenings.map((screening) => (
          <ScreeningEntry key={screening.id} screening={screening} />
        ))}
      </ul>
    </section>
  );
}

function ScreeningEntry({ screening }: { screening: PublicScreening }) {
  const soldOut = screening.availableSeats === 0;
  const href = `/events/${encodeURIComponent(screening.id)}/seats`;
  // The machine-readable instant is the UTC one; the text a person reads is the
  // venue's own presentation, exactly as served.
  const startsAt = screening.startsAt === "" ? undefined : screening.startsAt;

  return (
    <li className="min-w-0">
      {/* `relative` is the positioning context for the link's overlay below, which
          is what makes the whole entry clickable while the link keeps a short,
          descriptive name for a screen reader. */}
      <Card className="relative flex h-full flex-col">
        <CardHeader>
          <CardTitle className="break-words">{screening.filmTitle}</CardTitle>
          <CardDescription>
            {screening.cinemaName} · {screening.roomName}
          </CardDescription>
        </CardHeader>

        <CardContent className="flex flex-1 flex-col gap-3">
          <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
            <dt>
              <span className="text-muted-foreground">Starts</span>
            </dt>
            <dd className="m-0 font-medium break-words">
              <time dateTime={startsAt}>{screening.startsAtLocal}</time>
            </dd>

            <dt>
              <span className="text-muted-foreground">Price per seat</span>
            </dt>
            <dd className="m-0 font-medium">{screening.priceDisplay}</dd>

            <dt>
              <span className="text-muted-foreground">Availability</span>
            </dt>
            <dd className="m-0 font-medium">
              {screening.availableSeats} of {screening.seatCount} seats available
              {soldOut ? " — sold out" : ""}
            </dd>
          </dl>

          <div className="mt-auto flex flex-wrap items-center gap-2 pt-1">
            {/* The word is the state; the tone only reinforces it, so the sold-out
                entry reads the same in greyscale. */}
            <Badge variant={soldOut ? "danger" : "success"}>
              {soldOut ? "Sold out" : "On sale"}
            </Badge>
            {/* `h-auto` and `whitespace-normal` are merged over the primitive's
                fixed height and nowrap, so a long film title wraps at 360px
                instead of pushing the entry sideways. */}
            <Button
              asChild
              variant="outline"
              size="sm"
              className="h-auto whitespace-normal py-1.5 text-left"
            >
              <Link href={href} className="after:absolute after:inset-0">
                {soldOut
                  ? `See the seat map for ${screening.filmTitle}`
                  : `Choose seats for ${screening.filmTitle}`}
              </Link>
            </Button>
          </div>
        </CardContent>
      </Card>
    </li>
  );
}

/**
 * The catalogue while it is still being read.
 *
 * It is a real region with a heading and a sentence, so a slow read is announced
 * instead of looking like an empty page; the skeletons are decorative and hidden
 * from assistive technology by the primitive itself.
 */
function CatalogueLoading() {
  return (
    <section aria-labelledby="catalogue-loading" className="flex flex-col gap-3">
      <h2 id="catalogue-loading">Screenings on sale</h2>
      <p role="status" className="m-0 max-w-prose">
        <span className="text-muted-foreground">Loading the catalogue…</span>
      </p>
      <ul role="list" aria-hidden="true" className="m-0 grid list-none grid-cols-1 gap-4 p-0 sm:grid-cols-2">
        {[0, 1, 2, 3].map((slot) => (
          <li key={slot} className="min-w-0">
            <Card className="flex flex-col gap-3 p-5">
              <Skeleton className="h-5 w-2/3" />
              <Skeleton className="h-4 w-1/2" />
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-8 w-40" />
            </Card>
          </li>
        ))}
      </ul>
    </section>
  );
}

/**
 * The catalogue answered, and the answer was nothing.
 *
 * This is not a failure and it is not a teaser: it is the API saying no screening
 * is on sale, and the page says exactly that. No film, time or price appears,
 * because none was served, and "coming soon" would be a claim about the programme
 * that nobody made.
 */
function CatalogueEmpty() {
  return (
    <section aria-labelledby="catalogue-empty" className="flex flex-col gap-3">
      <Card className="flex flex-col gap-2 p-5">
        <h2 id="catalogue-empty">No screenings are on sale right now</h2>
        <p className="m-0 max-w-prose">
          <span className="text-muted-foreground">
            The catalogue answered an empty list. A screening appears here while it
            is on sale and has not started yet, so this means there is nothing to
            sell at the moment rather than that the page failed.
          </span>
        </p>
        <p className="m-0 max-w-prose">
          <span className="text-muted-foreground">
            Nothing is listed in the meantime, and no placeholder screening is
            shown: a film title, a time or a price here would be a claim the system
            does not make.
          </span>
        </p>
        <p className="m-0 max-w-prose text-sm">
          <span className="text-muted-foreground">
            Scheduling happens in the local administration surface,{" "}
          </span>
          <Link href="/admin/schedule">films and screenings</Link>
          <span className="text-muted-foreground">
            , which is available in this development environment only.
          </span>
        </p>
      </Card>
    </section>
  );
}

/**
 * The catalogue could not be read.
 *
 * An unavailable catalogue is not an empty one, and the page says which of the two
 * happened: the code, the HTTP status and the API's own detail are printed so the
 * failure is visible rather than smoothed over, and nothing is listed. The code
 * decides whether asking again is worth it, so a temporary failure says so and a
 * refusal does not promise a retry that would not help.
 */
function CatalogueProblem({ problem }: { problem: PublicProblem }) {
  const temporary =
    problem.code === "network_error" ||
    problem.code === "service_unavailable" ||
    problem.code === "coordination_unavailable" ||
    problem.status >= 500;

  return (
    <Alert
      variant="destructive"
      role="alert"
      aria-labelledby="catalogue-problem"
      className="flex flex-col gap-2"
    >
      <h2 id="catalogue-problem">The catalogue could not be loaded</h2>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">
          <code>{problem.code}</code> · HTTP {problem.status}
        </p>
        <p className="m-0">{problem.detail}</p>
        <p className="m-0">
          {temporary
            ? "The catalogue is temporarily unavailable. Reload the page to ask it again; this page only reads, so nothing was lost."
            : "The API refused this read. Reload the page to try again."}
        </p>
        <p className="m-0">
          No screening is listed while the catalogue is unreadable. An empty list
          would mean there is nothing on sale, which is not what happened here.
        </p>
      </AlertDescription>
    </Alert>
  );
}
