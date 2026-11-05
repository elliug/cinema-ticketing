// /admin/schedule — films, screenings, and the price and status that sell one (M3.5 · T8;
// Marquesina since visual-redesign).
//
// The screen exists because a screening created in the administration has to become
// sellable without a SQL prompt. `POST /admin/events` carries an optional price and an
// optional status (M4.1 · T3b · S9), so one submit can put a screening on sale; the same
// screen also keeps `PATCH /admin/events/{eventId}` for a screening that already exists,
// which is what changes the price or the status afterwards.
//
// What it reads, and why each read is here:
//
//   - the repertoire (`GET /admin/films`), because a screening is scheduled by choosing a
//     film from a list rather than by pasting an identifier;
//   - the screenings (`GET /admin/events`), which answer each one's film, room, cinema,
//     price and status — the five things a list of screenings is for;
//   - the cinemas, rooms and layout versions, so the room and the published version a
//     create needs are chosen too. A screening is scheduled against a *published* layout,
//     so only published versions are offered: a draft is the API's conflict.
//
// What it deliberately does not do (S8): the administrative list carries `status` and no
// `onSale` flag, and the catalogue's membership is `status = 'on_sale'` **and** a start
// still ahead. So the list prints the stored status and, when that status says on sale but
// the start has passed, says so — it never calls a screening on sale on its own authority
// (that rule is `describeScreeningStatus`, tested in lib/scheduling.ts).
//
// Since the Marquesina redesign the list is a ledger, not a wall of cards: an uppercase head,
// one hairline per session, the hour and the price in the mono face, and the state in a word.
// Every value is still the one the read served — the ledger added no number, and the box
// office's own counts live in the seats card below, where the read actually carries them.
//
// The route is server-rendered per request because it depends on local administrative
// configuration and live data; it is never prerendered at build time.
import Link from "next/link";

import { PageHeader } from "@/components";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { formatAmount } from "@/lib/receipt";
import { ADMIN_TOKEN_ENV } from "@/lib/admin-api";
import type { AdminEventRead, AdminProblem } from "@/lib/admin-api";
import { summarizeEventSeats } from "@/lib/event-seats";
import {
  ADMIN_SCHEDULE_PATH,
  describeScreeningStatus,
  toneBadgeVariant,
} from "@/lib/scheduling";
import {
  publishedLayoutOptions,
  summarizeCinemas,
  summarizeLayouts,
  summarizeRooms,
} from "@/lib/venue-admin";
import {
  readCinemasAction,
  readEventSeatsAction,
  readEventsAction,
  readFilmsAction,
  readLayoutsAction,
  readRoomsAction,
} from "./actions";
import { EventSeatsView } from "./event-seats-view";
import { EventForm, FilmForm, SaleStateForm } from "./schedule-forms";

export const dynamic = "force-dynamic";

/**
 * The native select's styling, shared by the two filter forms below.
 *
 * A filter is a plain `GET` form: it has to submit with or without JavaScript, so it keeps
 * a native `<select>` — the element preflight now normalises — rather than the Radix
 * primitive the interactive forms use. It is still Tailwind, not the legacy stylesheet.
 */
const FILTER_SELECT_CLASS =
  "h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm " +
  "focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 " +
  "focus-visible:ring-offset-background";

interface SchedulePageProps {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}

function firstValue(value: string | string[] | undefined): string {
  return (Array.isArray(value) ? value[0] : value)?.trim() ?? "";
}

export default async function SchedulePage({ searchParams }: SchedulePageProps) {
  const params = await searchParams;
  const cinemaId = firstValue(params.cinemaId);
  const roomId = firstValue(params.roomId);
  const eventId = firstValue(params.eventId);

  const [filmsResult, eventsResult, cinemasResult] = await Promise.all([
    readFilmsAction(),
    readEventsAction(),
    readCinemasAction(),
  ]);
  const roomsResult = cinemaId === "" ? null : await readRoomsAction(cinemaId);
  const layoutsResult = roomId === "" ? null : await readLayoutsAction(roomId);

  const cinemaRows = cinemasResult.ok ? summarizeCinemas(cinemasResult.value) : [];
  const roomRows = roomsResult?.ok ? summarizeRooms(roomsResult.value) : [];
  const layoutRows = layoutsResult?.ok
    ? publishedLayoutOptions(summarizeLayouts(layoutsResult.value))
    : [];

  const screenings = eventsResult.ok ? eventsResult.value : [];
  const selected = screenings.find((screening) => screening.id === eventId);

  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Administración local · M3.5"
        title="Películas y sesiones"
        description={
          <>
            Crea una película y prográmala después en una sala, contra una versión publicada
            del plano, con el precio y el estado que dejan que la cartelera la venda. Una
            sesión también se puede programar como borrador y ponerle precio más tarde. Sólo
            funciona en desarrollo local: la API tiene que correr con la administración local
            activada, y esta página necesita el mismo <code>{ADMIN_TOKEN_ENV}</code> que la
            API.
          </>
        }
      />

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Crear una película</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Una película es aquello de lo que se programa una sesión, así que se crea una vez
            aquí y luego se elige de la lista.
          </p>
        </CardHeader>
        <CardContent>
          {filmsResult.ok ? (
            <FilmForm />
          ) : (
            <ReadProblem
              id="films-problem"
              title="No se pudo cargar el repertorio de películas"
              problem={filmsResult.error}
            />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Programar una sesión</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Elige la sala, luego una versión publicada del plano de esa sala y por último la
            película del repertorio. El precio y el estado son opcionales aquí: con los dos
            puestos, este único envío programa una sesión que la cartelera ya vende; sin
            ninguno, es el borrador sin precio al que el formulario de abajo pone precio.
          </p>
        </CardHeader>
        <CardContent className="flex flex-col gap-5">
          {cinemasResult.ok ? (
            <div className="flex flex-col gap-4">
              <form
                className="flex flex-wrap items-end gap-4"
                action={ADMIN_SCHEDULE_PATH}
                method="get"
                aria-label="Elegir un cine"
              >
                <div className="flex min-w-56 flex-col gap-1.5">
                  <label className="text-sm font-medium" htmlFor="schedule-cinema">
                    Cine
                  </label>
                  <select
                    className={FILTER_SELECT_CLASS}
                    id="schedule-cinema"
                    name="cinemaId"
                    defaultValue={cinemaId}
                  >
                    <option value="">Elige un cine</option>
                    {cinemaRows.map((cinema) => (
                      <option key={cinema.id} value={cinema.id}>
                        {cinema.label}
                      </option>
                    ))}
                  </select>
                </div>
                <Button type="submit" variant="outline">
                  Ver las salas de este cine
                </Button>
              </form>

              {cinemaId === "" ? (
                <p className="m-0 max-w-prose text-sm text-muted-foreground">
                  Elige un cine para ver sus salas. Una sesión necesita una sala y una versión
                  publicada del plano, y ninguna de las dos se puede adivinar.
                </p>
              ) : roomsResult === null || !roomsResult.ok ? (
                <ReadProblem
                  id="rooms-problem"
                  title="No se pudieron cargar las salas"
                  problem={roomsResult?.error ?? configurationProblem()}
                  selection={`Cine ${cinemaId}`}
                />
              ) : (
                <form
                  className="flex flex-wrap items-end gap-4"
                  action={ADMIN_SCHEDULE_PATH}
                  method="get"
                  aria-label="Elegir una sala"
                >
                  <input type="hidden" name="cinemaId" value={cinemaId} />
                  <div className="flex min-w-56 flex-col gap-1.5">
                    <label className="text-sm font-medium" htmlFor="schedule-room">
                      Sala
                    </label>
                    <select
                      className={FILTER_SELECT_CLASS}
                      id="schedule-room"
                      name="roomId"
                      defaultValue={roomId}
                    >
                      <option value="">Elige una sala</option>
                      {roomRows.map((room) => (
                        <option key={room.id} value={room.id}>
                          {room.label}
                        </option>
                      ))}
                    </select>
                  </div>
                  <Button type="submit" variant="outline">
                    Ver las versiones del plano
                  </Button>
                </form>
              )}

              {roomId === "" ? null : layoutsResult === null || !layoutsResult.ok ? (
                <ReadProblem
                  id="layouts-problem"
                  title="No se pudieron cargar las versiones del plano"
                  problem={layoutsResult?.error ?? configurationProblem()}
                  selection={`Sala ${roomId}`}
                />
              ) : (
                <EventForm
                  // A different room is a different set of layout versions, so the form is
                  // remounted rather than left holding the previous room's choice.
                  key={`${cinemaId}/${roomId}`}
                  films={
                    filmsResult.ok
                      ? filmsResult.value.map((film) => ({
                          id: film.id,
                          label: `${film.title} (${film.runtimeMinutes} min, ${film.rating})`,
                        }))
                      : []
                  }
                  layouts={layoutRows.map((layout) => ({
                    id: layout.id,
                    label: layout.label,
                  }))}
                  cinemaId={cinemaId}
                  roomId={roomId}
                />
              )}
            </div>
          ) : (
            <ReadProblem
              id="scheduling-cinemas-problem"
              title="No se pudieron cargar los cines"
              problem={cinemasResult.error}
            />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Sesiones</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Todas las sesiones, en el orden en que las responde la API, con la película que
            proyectan, la sala y el cine en que están, su inicio en UTC, su precio por butaca y
            el estado que hay realmente guardado.
          </p>
        </CardHeader>
        <CardContent>
          {eventsResult.ok ? (
            <ScreeningsList
              screenings={screenings}
              eventId={eventId}
              cinemaId={cinemaId}
              roomId={roomId}
            />
          ) : (
            <ReadProblem
              id="screenings-problem"
              title="No se pudieron cargar las sesiones"
              problem={eventsResult.error}
            />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Precio y estado</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Una petición fija el precio, el estado o los dos. Un campo que no cambió no se
            envía, y un estado de <code>on_sale</code> es lo que pone una sesión en la
            cartelera mientras su inicio siga por delante.
          </p>
        </CardHeader>
        <CardContent>
          {eventId === "" ? (
            <p className="m-0 max-w-prose text-sm text-muted-foreground">
              Elige una sesión de la lista de arriba, o programa una, para fijar cuánto cuesta
              y si está a la venta.
            </p>
          ) : selected === undefined ? (
            <Alert variant="warning" role="alert" className="flex flex-col gap-1">
              <p className="m-0 font-semibold">
                Ninguna sesión de la lista responde a esa dirección
              </p>
              <AlertDescription className="flex flex-col gap-1">
                <p className="m-0">
                  <code>{eventId}</code> no es ninguna de las sesiones que la API listó, así
                  que no hay nada que editar. Elige una de la lista de arriba.
                </p>
              </AlertDescription>
            </Alert>
          ) : (
            <SaleStateForm
              // One screening's form per screening: switching the selection starts from
              // that screening's own price and status instead of the previous one's.
              key={selected.id}
              screening={{
                id: selected.id,
                priceCents: selected.priceCents,
                priceDisplay: selected.priceDisplay,
                status: selected.status,
              }}
              heading={`${selected.filmTitle} · ${selected.startsAt}`}
            />
          )}
        </CardContent>
      </Card>

      {eventId === "" ? null : await renderEventSeats(eventId)}
    </main>
  );
}

/** The screenings, each with its stored status and what that status means for the sale. */
function ScreeningsList({
  screenings,
  eventId,
  cinemaId,
  roomId,
}: {
  screenings: readonly AdminEventRead[];
  eventId: string;
  cinemaId: string;
  roomId: string;
}) {
  if (screenings.length === 0) {
    return (
      <p className="m-0 max-w-prose text-sm text-muted-foreground">
        Todavía no hay ninguna sesión programada. La que se cree arriba aparece aquí con el
        precio y el estado con que se creó — un borrador sin precio cuando el formulario no
        nombró ninguno.
      </p>
    );
  }

  // One instant for the whole list, so every row answers the same question.
  const now = new Date();

  return (
    <div className="overflow-x-auto">
      <table className="ledger">
        {/* The card's heading already names the list, so the caption is for a screen reader
            only: it says what the rows are, in one word, instead of repeating the h2. */}
        <caption className="visually-hidden">
          Sesiones programadas, con su inicio, su precio y su estado
        </caption>
        <thead>
          <tr>
            <th scope="col">Inicio (UTC)</th>
            <th scope="col">Película</th>
            <th scope="col">Precio por butaca</th>
            <th scope="col">Estado</th>
          </tr>
        </thead>
        <tbody>
          {screenings.map((screening) => {
            const view = describeScreeningStatus(screening, now);
            const editing = screening.id === eventId;
            const query = new URLSearchParams({ eventId: screening.id });
            if (cinemaId !== "") {
              query.set("cinemaId", cinemaId);
            }
            if (roomId !== "") {
              query.set("roomId", roomId);
            }

            return (
              <tr key={screening.id} className={editing ? "ledger__row--current" : undefined}>
                <td className="ledger__num">
                  <time dateTime={screening.startsAt}>{screening.startsAt}</time>
                </td>
                <td>
                  <span className="block font-medium break-words">
                    {screening.filmTitle}
                  </span>
                  <span className="block text-muted-foreground">
                    {screening.cinemaName} · {screening.roomName}
                  </span>
                </td>
                <td className="ledger__num">{formatAmount(screening.priceCents)}</td>
                <td>
                  <div className="flex flex-col gap-1">
                    {/* The word is the state; the tone only reinforces it, so the row reads the
                        same in greyscale. */}
                    <div>
                      <Badge variant={toneBadgeVariant(view.tone)}>{view.label}</Badge>
                    </div>
                    <span className="text-xs text-muted-foreground">
                      Estado guardado: <code>{view.status}</code>
                    </span>
                    {view.catalogueNote === null ? null : (
                      <span className="block max-w-prose text-sm">{view.catalogueNote}</span>
                    )}
                    <Button asChild variant="outline" size="sm" className="mt-1 self-start">
                      <Link
                        href={`${ADMIN_SCHEDULE_PATH}?${query.toString()}`}
                        aria-current={editing ? "true" : undefined}
                        // Not prefetched, deliberately. This screen renders one of these per
                        // screening, and every one of them is *this same route* with a different
                        // query: prefetching them buys next to nothing (the payload is a full
                        // render, not a cheap fragment) and costs one request per screening — a
                        // month of programming is dozens. It also made the browser gate fail on
                        // Firefox alone: Firefox reports the aborted prefetches that a navigation
                        // away leaves behind as console errors, Chromium swallows them. Removing
                        // the waste removes the symptom; filtering the console would have hidden
                        // the next real error that looked like this one.
                        prefetch={false}
                      >
                        {editing
                          ? "Editando el precio y el estado abajo"
                          : `Poner precio y estado a ${screening.filmTitle}`}
                      </Link>
                    </Button>
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

async function renderEventSeats(eventId: string) {
  // The read goes through the same server action the rest of the surface uses, so the
  // administrative client and its token stay inside the server boundary.
  const result = await readEventSeatsAction(eventId);
  if (!result.ok) {
    return (
      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Butacas materializadas</h2>
        </CardHeader>
        <CardContent>
          <ReadProblem
            id="event-seats-problem"
            title="No se pudieron cargar las butacas de la sesión"
            problem={result.error}
            selection={`Sesión ${eventId}`}
          />
        </CardContent>
      </Card>
    );
  }

  return <EventSeatsView summary={summarizeEventSeats(result.value)} />;
}

/** A failed read with no typed problem can only be a configuration failure. */
function configurationProblem(): AdminProblem {
  return {
    type: "https://cinema.local/problems/configuration",
    title: "Error de configuración",
    status: 0,
    code: "configuration_error",
    detail: `${ADMIN_TOKEN_ENV} tiene que estar definido para usar la administración local`,
  };
}

/**
 * A read that failed, printed as the API answered it.
 *
 * An unavailable list is never rendered as an empty one: the code, the HTTP status and the
 * API's own detail are printed, and the surface says it is local-development only rather
 * than looking like a screen with nothing in it. The API's detail stays in the API's own
 * words: it is the box office's sentence, not this screen's.
 */
function ReadProblem({
  id,
  title,
  problem,
  selection,
}: {
  id: string;
  title: string;
  problem: AdminProblem;
  selection?: string;
}) {
  return (
    <Alert variant="destructive" role="alert" aria-labelledby={id} className="flex flex-col gap-2">
      <h3 id={id} className="m-0 text-base leading-tight font-semibold">
        {title}
      </h3>
      <AlertDescription className="flex flex-col gap-1">
        {selection === undefined ? null : <p className="m-0">{selection}</p>}
        <p className="m-0">
          <code>{problem.code}</code> · HTTP {problem.status}
        </p>
        <p className="m-0">{problem.detail}</p>
        <p className="m-0">
          Esta pantalla sólo funciona en desarrollo local: la API tiene que correr con la
          administración local activada, y esta página necesita el mismo{" "}
          <code>{ADMIN_TOKEN_ENV}</code> que la API.
        </p>
      </AlertDescription>
    </Alert>
  );
}
