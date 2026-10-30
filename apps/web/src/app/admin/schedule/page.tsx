// /admin/schedule — films, screenings, and the price and status that sell one (M3.5 · T8).
//
// The screen exists because a screening created in the administration could not be sold:
// `POST /admin/events` stores a draft at price 0 and cannot carry either writable field, so
// sellable is **create then patch** — and this is the screen that performs both requests,
// the second one being `PATCH /admin/events/{eventId}` with a price, a status, or both.
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
// The route is server-rendered per request because it depends on local administrative
// configuration and live data; it is never prerendered at build time.
import Link from "next/link";

import { PageHeader } from "@/components";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
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
        eyebrow="Local administration · M3.5"
        title="Films and screenings"
        description={
          <>
            Create a film, schedule it in a room against a published layout version, then set
            the price and the status that let the catalogue sell it. Works in local
            development only: the API must run with local administration enabled, and this
            page needs the same <code>{ADMIN_TOKEN_ENV}</code> token as the API.
          </>
        }
      />

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Create a film</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            A film is what a screening is scheduled from, so it is created once here and
            chosen from the list below.
          </p>
        </CardHeader>
        <CardContent>
          {filmsResult.ok ? (
            <FilmForm />
          ) : (
            <ReadProblem
              id="films-problem"
              title="The film repertoire could not be loaded"
              problem={filmsResult.error}
            />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Schedule a screening</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Choose the room, then a published layout version of it, then the film from the
            repertoire. The screening is stored as a draft: its price and status are set in
            the next step.
          </p>
        </CardHeader>
        <CardContent className="flex flex-col gap-5">
          {cinemasResult.ok ? (
            <div className="flex flex-col gap-4">
              <form
                className="flex flex-wrap items-end gap-4"
                action={ADMIN_SCHEDULE_PATH}
                method="get"
                aria-label="Select a cinema"
              >
                <div className="flex min-w-56 flex-col gap-1.5">
                  <label className="text-sm font-medium" htmlFor="schedule-cinema">
                    Cinema
                  </label>
                  <select
                    className={FILTER_SELECT_CLASS}
                    id="schedule-cinema"
                    name="cinemaId"
                    defaultValue={cinemaId}
                  >
                    <option value="">Select a cinema</option>
                    {cinemaRows.map((cinema) => (
                      <option key={cinema.id} value={cinema.id}>
                        {cinema.label}
                      </option>
                    ))}
                  </select>
                </div>
                <Button type="submit" variant="outline">
                  Show this cinema&apos;s rooms
                </Button>
              </form>

              {cinemaId === "" ? (
                <p className="m-0 max-w-prose text-sm text-muted-foreground">
                  Choose a cinema to see its rooms. A screening needs a room and a published
                  layout version, and neither can be guessed.
                </p>
              ) : roomsResult === null || !roomsResult.ok ? (
                <ReadProblem
                  id="rooms-problem"
                  title="The rooms could not be loaded"
                  problem={roomsResult?.error ?? configurationProblem()}
                  selection={`Cinema ${cinemaId}`}
                />
              ) : (
                <form
                  className="flex flex-wrap items-end gap-4"
                  action={ADMIN_SCHEDULE_PATH}
                  method="get"
                  aria-label="Select a room"
                >
                  <input type="hidden" name="cinemaId" value={cinemaId} />
                  <div className="flex min-w-56 flex-col gap-1.5">
                    <label className="text-sm font-medium" htmlFor="schedule-room">
                      Room
                    </label>
                    <select
                      className={FILTER_SELECT_CLASS}
                      id="schedule-room"
                      name="roomId"
                      defaultValue={roomId}
                    >
                      <option value="">Select a room</option>
                      {roomRows.map((room) => (
                        <option key={room.id} value={room.id}>
                          {room.label}
                        </option>
                      ))}
                    </select>
                  </div>
                  <Button type="submit" variant="outline">
                    Show this room&apos;s layout versions
                  </Button>
                </form>
              )}

              {roomId === "" ? null : layoutsResult === null || !layoutsResult.ok ? (
                <ReadProblem
                  id="layouts-problem"
                  title="The layout versions could not be loaded"
                  problem={layoutsResult?.error ?? configurationProblem()}
                  selection={`Room ${roomId}`}
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
              title="The cinemas could not be loaded"
              problem={cinemasResult.error}
            />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Screenings</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Every screening, in the order the API answers them, with the film it shows, the
            room and cinema it is in, its start in UTC, its price per seat and the status
            that is actually stored.
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
              title="The screenings could not be loaded"
              problem={eventsResult.error}
            />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Price and status</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            One request sets the price, the status, or both. A field that did not change is
            not sent, and a status of <code>on_sale</code> is what puts a screening in the
            catalogue while its start is still ahead.
          </p>
        </CardHeader>
        <CardContent>
          {eventId === "" ? (
            <p className="m-0 max-w-prose text-sm text-muted-foreground">
              Pick a screening from the list above, or schedule one, to set what it costs and
              whether it is for sale.
            </p>
          ) : selected === undefined ? (
            <Alert variant="warning" role="alert" className="flex flex-col gap-1">
              <p className="m-0 font-semibold">
                No screening in the list answers to that address
              </p>
              <AlertDescription className="flex flex-col gap-1">
                <p className="m-0">
                  <code>{eventId}</code> is not one of the screenings the API listed, so
                  there is nothing to edit. Pick one from the list above.
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
        No screening is scheduled yet. One created above appears here as a draft at price
        0.00 until its price and status are set.
      </p>
    );
  }

  // One instant for the whole list, so every row answers the same question.
  const now = new Date();

  return (
    <ul role="list" className="m-0 flex list-none flex-col gap-3 p-0">
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
          <li key={screening.id} className="min-w-0">
            <Card
              className={
                editing
                  ? "flex flex-col gap-3 p-4 ring-2 ring-ring"
                  : "flex flex-col gap-3 p-4"
              }
            >
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="flex min-w-0 flex-col gap-1">
                  <h3 className="m-0 text-base leading-tight font-semibold break-words">
                    {screening.filmTitle}
                  </h3>
                  <p className="m-0 text-sm text-muted-foreground">
                    {screening.cinemaName} · {screening.roomName}
                  </p>
                </div>
                {/* The word is the state; the tone only reinforces it, so the row reads the
                    same in greyscale. */}
                <Badge variant={toneBadgeVariant(view.tone)}>{view.label}</Badge>
              </div>

              <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
                <dt>
                  <span className="text-muted-foreground">Starts (UTC)</span>
                </dt>
                <dd className="m-0 font-medium break-words">
                  <time dateTime={screening.startsAt}>{screening.startsAt}</time>
                </dd>

                <dt>
                  <span className="text-muted-foreground">Price per seat</span>
                </dt>
                <dd className="m-0 font-medium">{screening.priceDisplay}</dd>

                <dt>
                  <span className="text-muted-foreground">Stored status</span>
                </dt>
                <dd className="m-0 font-medium">
                  <code>{view.status}</code>
                </dd>
              </dl>

              {view.catalogueNote === null ? null : (
                <p className="m-0 max-w-prose text-sm">{view.catalogueNote}</p>
              )}

              <div className="flex flex-wrap items-center gap-2">
                <Button asChild variant="outline" size="sm">
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
                      ? "Editing the price and status below"
                      : `Set the price and status of ${screening.filmTitle}`}
                  </Link>
                </Button>
              </div>
            </Card>
          </li>
        );
      })}
    </ul>
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
          <h2 className="m-0 text-lg leading-tight font-semibold">Materialized seats</h2>
        </CardHeader>
        <CardContent>
          <ReadProblem
            id="event-seats-problem"
            title="The event seats could not be loaded"
            problem={result.error}
            selection={`Screening ${eventId}`}
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
    title: "Configuration error",
    status: 0,
    code: "configuration_error",
    detail: `${ADMIN_TOKEN_ENV} must be set to use local administration`,
  };
}

/**
 * A read that failed, printed as the API answered it.
 *
 * An unavailable list is never rendered as an empty one: the code, the HTTP status and the
 * API's own detail are printed, and the surface says it is local-development only rather
 * than looking like a screen with nothing in it.
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
          This surface works only in local development: the API must run with local
          administration enabled, and this page needs the same <code>{ADMIN_TOKEN_ENV}</code>{" "}
          token as the API.
        </p>
      </AlertDescription>
    </Alert>
  );
}
