// /admin/venues — create cinemas and rooms, then create and inspect layout versions
// (M3.5 · T8 · S3).
//
// The page is intentionally thin: the three forms are client components that call the
// server actions in ./actions.ts, and every read goes through the same actions so the
// administrative client and its bearer token stay inside the server boundary. All
// orchestration and every render-ready summary lives in the tested module
// lib/venue-admin.ts.
//
// The two selections — which cinema's rooms, which room's versions — are plain `GET` forms
// with native selects, so they keep working with or without JavaScript, and each one says
// what it is selecting. A page that showed every room of every cinema at once would answer
// a question nobody asked, and one that asked for an identifier would be asking the
// operator to know something the API is willing to list.
//
// The route is server-rendered per request because it depends on local administrative
// configuration and live data; it is never prerendered at build time. Like the API itself,
// it is a local-development surface only.
import type { ReactNode } from "react";
import Link from "next/link";

import { PageHeader } from "@/components";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { ADMIN_TOKEN_ENV } from "@/lib/admin-api";
import type { AdminProblem } from "@/lib/admin-api";
import {
  ADMIN_VENUES_PATH,
  summarizeCinemas,
  summarizeLayouts,
  summarizeRooms,
} from "@/lib/venue-admin";
import type { CinemaRow, LayoutVersionRow, RoomRow } from "@/lib/venue-admin";
import {
  readCinemasAction,
  readLayoutsAction,
  readRoomsAction,
} from "./actions";
import { CinemaForm, LayoutForm, RoomForm } from "./venue-forms";

export const dynamic = "force-dynamic";

/**
 * The native select's styling.
 *
 * A filter is a plain `GET` form: it has to submit with or without JavaScript, so it keeps
 * a native `<select>` — the element preflight now normalises — rather than the Radix
 * primitive the interactive forms use. It is still Tailwind, not the legacy stylesheet.
 */
const FILTER_SELECT_CLASS =
  "h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm " +
  "focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 " +
  "focus-visible:ring-offset-background";

interface VenuesPageProps {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}

function firstValue(value: string | string[] | undefined): string {
  return (Array.isArray(value) ? value[0] : value)?.trim() ?? "";
}

export default async function VenuesPage({ searchParams }: VenuesPageProps) {
  const params = await searchParams;
  const cinemaId = firstValue(params.cinemaId);
  const roomId = firstValue(params.roomId);

  const cinemasResult = await readCinemasAction();
  const cinemaRows = cinemasResult.ok ? summarizeCinemas(cinemasResult.value) : [];

  const roomsResult = cinemaId === "" ? null : await readRoomsAction(cinemaId);
  const roomRows = roomsResult?.ok ? summarizeRooms(roomsResult.value) : [];

  const layoutsResult = roomId === "" ? null : await readLayoutsAction(roomId);
  const layoutRows = layoutsResult?.ok ? summarizeLayouts(layoutsResult.value) : [];

  const cinemasSection: ReactNode = cinemasResult.ok ? (
    <CinemasCard cinemaRows={cinemaRows} cinemaId={cinemaId} />
  ) : (
    <Card>
      <CardContent className="pt-5">
        <ReadProblem
          id="cinemas-problem"
          title="The cinemas could not be loaded"
          problem={cinemasResult.error}
        />
      </CardContent>
    </Card>
  );

  let roomsSection: ReactNode = null;
  if (cinemaId !== "") {
    roomsSection =
      roomsResult === null || !roomsResult.ok ? (
        <Card>
          <CardContent className="pt-5">
            <ReadProblem
              id="rooms-problem"
              title="The rooms could not be loaded"
              problem={roomsResult?.error ?? configurationProblem()}
              selection={`Cinema ${cinemaId}`}
            />
          </CardContent>
        </Card>
      ) : (
        <RoomsCard
          cinemaId={cinemaId}
          cinemaRows={cinemaRows}
          roomRows={roomRows}
          roomId={roomId}
        />
      );
  }

  let layoutsSection: ReactNode = null;
  if (roomId !== "") {
    layoutsSection =
      layoutsResult === null || !layoutsResult.ok ? (
        <Card>
          <CardContent className="pt-5">
            <ReadProblem
              id="layouts-problem"
              title="The layout versions could not be loaded"
              problem={layoutsResult?.error ?? configurationProblem()}
              selection={`Room ${roomId}`}
            />
          </CardContent>
        </Card>
      ) : (
        <LayoutsCard
          roomId={roomId}
          roomRows={roomRows}
          layoutRows={layoutRows}
        />
      );
  }

  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Local administration · M3.5"
        title="Cinemas, rooms and layouts"
        description={
          <>
            Create a cinema, add rooms to it, and create layout versions for a room. A
            published version is immutable and changes require a new draft. Works in local
            development only: the API must run with local administration enabled, and this
            page needs the same <code>{ADMIN_TOKEN_ENV}</code> token as the API.
          </>
        }
      />

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Create a cinema</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            The timezone is the venue&apos;s own clock, which is what the public catalogue
            prints next to each screening, so it must be an IANA name such as{" "}
            <code>Europe/Madrid</code> — not a city or an abbreviation. The API refuses a
            name it cannot load.
          </p>
        </CardHeader>
        <CardContent>
          <CinemaForm />
        </CardContent>
      </Card>

      {cinemasSection}
      {roomsSection}
      {layoutsSection}
    </main>
  );
}

function CinemasCard({
  cinemaRows,
  cinemaId,
}: {
  cinemaRows: readonly CinemaRow[];
  cinemaId: string;
}) {
  return (
    <Card>
      <CardHeader>
        <h2 className="m-0 text-lg leading-tight font-semibold">Cinemas</h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Every cinema the API lists, then the one whose rooms you want to see.
        </p>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {cinemaRows.length === 0 ? (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            No cinema exists yet. One created above appears here and can then hold rooms.
          </p>
        ) : (
          <ul role="list" className="m-0 flex list-none flex-col gap-1 p-0 text-sm">
            {cinemaRows.map((cinema) => (
              <li key={cinema.id}>
                <span className="font-medium">{cinema.label}</span>{" "}
                <span className="text-muted-foreground">
                  (cinema <code>{cinema.id}</code>)
                </span>
              </li>
            ))}
          </ul>
        )}

        <form
          className="flex flex-wrap items-end gap-4"
          action={ADMIN_VENUES_PATH}
          method="get"
          aria-label="Select a cinema"
        >
          <div className="flex min-w-56 flex-col gap-1.5">
            <label className="text-sm font-medium" htmlFor="venues-cinema">
              Cinema
            </label>
            <select
              className={FILTER_SELECT_CLASS}
              id="venues-cinema"
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
            Show rooms
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

function RoomsCard({
  cinemaId,
  cinemaRows,
  roomRows,
  roomId,
}: {
  cinemaId: string;
  cinemaRows: readonly CinemaRow[];
  roomRows: readonly RoomRow[];
  roomId: string;
}) {
  const cinema = cinemaRows.find((row) => row.id === cinemaId);
  return (
    <Card>
      <CardHeader>
        <h2 className="m-0 text-lg leading-tight font-semibold">
          Rooms in {cinema?.name ?? `cinema ${cinemaId}`}
        </h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          A room belongs to one cinema and holds its own layout versions.
        </p>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <RoomForm
          key={cinemaId}
          cinemas={cinemaRows.map((row) => ({ id: row.id, label: row.label }))}
          cinemaId={cinemaId}
        />

        {roomRows.length === 0 ? (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            This cinema has no rooms yet.
          </p>
        ) : (
          <ul role="list" className="m-0 flex list-none flex-col gap-1 p-0 text-sm">
            {roomRows.map((room) => (
              <li key={room.id}>
                <span className="font-medium">{room.name}</span>{" "}
                <span className="text-muted-foreground">
                  (room <code>{room.id}</code>)
                </span>
              </li>
            ))}
          </ul>
        )}

        {roomRows.length === 0 ? null : (
          <form
            className="flex flex-wrap items-end gap-4"
            action={ADMIN_VENUES_PATH}
            method="get"
            aria-label="Select a room"
          >
            <input type="hidden" name="cinemaId" value={cinemaId} />
            <div className="flex min-w-56 flex-col gap-1.5">
              <label className="text-sm font-medium" htmlFor="venues-room">
                Room
              </label>
              <select
                className={FILTER_SELECT_CLASS}
                id="venues-room"
                name="roomId"
                defaultValue={roomId}
              >
                <option value="">Select a room</option>
                {roomRows.map((room) => (
                  <option key={room.id} value={room.id}>
                    {room.name}
                  </option>
                ))}
              </select>
            </div>
            <Button type="submit" variant="outline">
              Show layout versions
            </Button>
          </form>
        )}
      </CardContent>
    </Card>
  );
}

function LayoutsCard({
  roomId,
  roomRows,
  layoutRows,
}: {
  roomId: string;
  roomRows: readonly RoomRow[];
  layoutRows: readonly LayoutVersionRow[];
}) {
  const room = roomRows.find((row) => row.id === roomId);
  return (
    <Card>
      <CardHeader>
        <h2 className="m-0 text-lg leading-tight font-semibold">
          Layout versions in {room?.name ?? `room ${roomId}`}
        </h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Creating a layout always adds the next draft version for the room. Edit its rows and
          publish it in the <code>/admin/layouts</code> editor; a screening can only be
          scheduled against a published version.
        </p>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <LayoutForm
          key={roomId}
          rooms={roomRows.map((row) => ({ id: row.id, label: row.label }))}
          roomId={roomId}
        />

        {layoutRows.length === 0 ? (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            This room has no layout versions yet.
          </p>
        ) : (
          <ul role="list" className="m-0 flex list-none flex-col gap-2 p-0 text-sm">
            {layoutRows.map((layout) => (
              <li key={layout.id} className="flex flex-wrap items-center gap-2">
                <span className="font-medium">Version {layout.version}</span>
                {/* The word is the state; the tone only reinforces it. */}
                <Badge variant={layout.status === "published" ? "success" : "secondary"}>
                  {layout.statusLabel}
                </Badge>
                <span className="text-muted-foreground">
                  (layout <code>{layout.id}</code>)
                </span>
                <Button asChild variant="link" size="sm" className="h-auto p-0">
                  <Link href={`/admin/layouts?layoutId=${encodeURIComponent(layout.id)}`}>
                    Open version {layout.version} in the layout editor
                  </Link>
                </Button>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
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
 * API's own detail are printed, so a missing token reads as a missing token instead of as a
 * cinema with no rooms.
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
