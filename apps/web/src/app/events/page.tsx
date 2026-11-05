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
//   - the price is the API's `priceCents`, formatted by the one money formatter the
//     catalogue, the receipt and the ticket share. No amount is computed;
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
// The catalogue pages, so this screen reads one page of it and says so (M3.5 · T5b):
//
//   - the cursor lives in the URL, and the next page is offered as a link at the address
//     that reaches it. A page a buyer can bookmark, share and reload is a page whose
//     address says where it is, which an appended list can never be;
//   - the last page says nothing about a next page. No disabled control, and no "page 1
//     of N": the API says whether more exists, never how much, and a count would be an
//     invented number;
//   - a cursor the API refuses (400, field `cursor`) is answered with a sentence and the
//     beginning of the catalogue. A stale bookmark is not a dead end, and it is not
//     quietly rendered as page one either, which would look like the page it asked for.
//
// The route is server-rendered per request: availability is live and must never be
// prerendered or cached.
import Link from "next/link";
import { Suspense } from "react";

import { PageHeader, ScreeningRow } from "@/components";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { PublicProblem, PublicScreening } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";
import {
  CATALOGUE_PATH,
  cursorFromParams,
  isRefusedCursor,
  nextPageHref,
} from "./catalogue-paging";

export const dynamic = "force-dynamic";

interface EventsPageProps {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}

export default function EventsPage({ searchParams }: EventsPageProps) {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Cartelera"
        title="Sesiones en venta"
        description="Las sesiones que están a la venta desde ahora, en el orden en que las programa el cine, una página cada vez. La hora es la del propio local, el precio es por butaca y las butacas libres son las que la API contó al cargar esta página."
      />

      {/* The URL is read inside the boundary, so the header still arrives while the page
          itself is being read and a slow catalogue streams instead of blanking the screen. */}
      <Suspense fallback={<CatalogueLoading />}>
        <Catalogue searchParams={searchParams} />
      </Suspense>
    </main>
  );
}

/** Reads one page of the catalogue and decides which of its honest states to render. */
async function Catalogue({ searchParams }: EventsPageProps) {
  // The address decides which page this is, so a reload, a bookmark and a shared link all
  // land on the same screenings instead of on the first page again.
  const cursor = cursorFromParams(await searchParams);

  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return <CatalogueProblem problem={config.error} />;
  }

  const result = await createPublicClient(config.value).listCataloguePage({
    cursor: cursor ?? undefined,
  });
  if (!result.ok) {
    return isRefusedCursor(result.error, cursor) ? (
      <CatalogueStaleLink problem={result.error} />
    ) : (
      <CatalogueProblem problem={result.error} />
    );
  }
  if (result.value.events.length === 0) {
    return <CatalogueEmpty />;
  }

  return (
    <CatalogueList
      screenings={result.value.events}
      nextHref={nextPageHref(result.value.page)}
    />
  );
}

function CatalogueList({
  screenings,
  nextHref,
}: {
  screenings: PublicScreening[];
  nextHref: string | null;
}) {
  return (
    <section aria-labelledby="screenings-title" className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h2 id="screenings-title">Programación</h2>
        {/* The count is a page count, not a programme count: with more pages behind this
            one, "12 sesiones están en venta" would be a number the screen cannot know.
            `hasMore` is the only thing this page may say about the rest. */}
        <p className="m-0 text-sm text-muted-foreground">
          {screenings.length === 1
            ? "1 sesión en esta página"
            : `${screenings.length} sesiones en esta página`}
        </p>
      </div>

      <p className="m-0 max-w-prose text-sm text-muted-foreground">
        Cada fila abre el plano de butacas de esa sesión, donde se eligen y se reservan.
      </p>

      {/* `role="list"` keeps the list announced as one: the class removes the marker and,
          in Safari, the list semantics with it. */}
      <ul role="list" className="listing">
        {screenings.map((screening) => (
          <ScreeningRow
            key={screening.id}
            filmTitle={screening.filmTitle}
            cinemaName={screening.cinemaName}
            roomName={screening.roomName}
            startsAtLocal={screening.startsAtLocal}
            startsAt={screening.startsAt === "" ? undefined : screening.startsAt}
            priceCents={screening.priceCents}
            availableSeats={screening.availableSeats}
            seatCount={screening.seatCount}
            href={`/events/${encodeURIComponent(screening.id)}/seats`}
          />
        ))}
      </ul>

      {/* Nothing at all on the last page, rather than a control that cannot be used: the
          API said this is the end, and it never says how far the end is, so there is no
          "page 1 of N" to print either. The link is an address, so the page it reaches can
          be bookmarked, shared and reloaded like any other. */}
      {nextHref === null ? null : (
        <p className="m-0 max-w-prose">
          Hay más sesiones en venta de las que caben en esta página.{" "}
          <Link href={nextHref}>Ver las siguientes sesiones</Link>{" "}
          <span className="text-muted-foreground">
            — la dirección lleva dónde terminó esta página, así que la siguiente se puede
            seguir, guardar y recargar.
          </span>
        </p>
      )}
    </section>
  );
}

function CatalogueLoading() {
  return (
    <section aria-labelledby="catalogue-loading" className="flex flex-col gap-3">
      <h2 id="catalogue-loading">Programación</h2>
      <p role="status" className="m-0 max-w-prose">
        <span className="text-muted-foreground">Cargando la cartelera…</span>
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
        <h2 id="catalogue-empty">Ahora mismo no hay sesiones en venta</h2>
        <p className="m-0 max-w-prose">
          <span className="text-muted-foreground">
            La cartelera respondió una lista vacía. Una sesión aparece aquí mientras está
            a la venta y no ha empezado, así que esto significa que ahora mismo no hay nada
            que vender, no que la página haya fallado.
          </span>
        </p>
        <p className="m-0 max-w-prose">
          <span className="text-muted-foreground">
            Mientras tanto no se lista nada, y no se muestra una sesión de relleno: un
            título, una hora o un precio aquí serían una afirmación que el sistema no hace.
          </span>
        </p>
        <p className="m-0 max-w-prose text-sm">
          <span className="text-muted-foreground">
            La programación se hace en la administración local,{" "}
          </span>
          <Link href="/admin/schedule">películas y sesiones</Link>
          <span className="text-muted-foreground">
            , disponible solo en este entorno de desarrollo.
          </span>
        </p>
      </Card>
    </section>
  );
}

/**
 * The catalogue link outlived the page it named.
 *
 * A cursor is where one page ended, so it is only as durable as the screenings it counted:
 * a shared or bookmarked link can name a screening that has since started, and the API
 * refuses that cursor (400, naming the field) instead of answering a page it cannot mean.
 *
 * The sentence carries the state — nothing here is said by colour, so greyscale and a screen
 * reader hear the same thing — and the way out is the catalogue's own address, the first
 * page, which is the one that names no cursor. It is not rendered as page one silently:
 * that would look like the page the link asked for.
 */
function CatalogueStaleLink({ problem }: { problem: PublicProblem }) {
  return (
    <section aria-labelledby="catalogue-stale-link" className="flex flex-col gap-3">
      <Card className="flex flex-col gap-2 p-5">
        <h2 id="catalogue-stale-link">Este enlace de cartelera ya no vale</h2>
        <p className="m-0 max-w-prose">
          El cursor de esta dirección nombra una página que la cartelera ya no puede mostrar,
          porque las sesiones que contó han avanzado. La cartelera no tiene nada malo.
        </p>
        <p className="m-0 max-w-prose">
          <code>{problem.code}</code> · HTTP {problem.status}
        </p>
        <p className="m-0">{problem.detail}</p>
        <p className="m-0 max-w-prose">
          <Link href={CATALOGUE_PATH}>Ver la cartelera desde el principio</Link>
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
      <h2 id="catalogue-problem">No se pudo cargar la cartelera</h2>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">
          <code>{problem.code}</code> · HTTP {problem.status}
        </p>
        <p className="m-0">{problem.detail}</p>
        <p className="m-0">
          {temporary
            ? "La cartelera no está disponible temporalmente. Recarga la página para volver a preguntar; esta página solo lee, así que no se ha perdido nada."
            : "La API rechazó esta lectura. Recarga la página para intentarlo otra vez."}
        </p>
        <p className="m-0">
          Mientras la cartelera no se puede leer no se lista ninguna sesión. Una lista
          vacía significaría que no hay nada a la venta, que no es lo que ha pasado aquí.
        </p>
      </AlertDescription>
    </Alert>
  );
}
