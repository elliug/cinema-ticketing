// /admin/venues — create cinemas and rooms, then create and inspect layout
// versions.
//
// The page is intentionally thin: the three forms are client components that
// call the server actions in ./actions.ts, and every read goes through the same
// actions so the administrative client and its bearer token stay inside the
// server boundary. All orchestration and every render-ready summary lives in
// the tested module lib/venue-admin.ts.
//
// The route is server-rendered per request because it depends on local
// administrative configuration and live data; it is never prerendered at build
// time. Like the API itself, it is a local-development surface only.
import { ADMIN_TOKEN_ENV } from "@/lib/admin-api";
import type { AdminProblem } from "@/lib/admin-api";
import {
  ADMIN_VENUES_PATH,
  summarizeCinemas,
  summarizeLayouts,
  summarizeRooms,
} from "@/lib/venue-admin";
import type { CinemaRow, LayoutVersionRow, RoomRow } from "@/lib/venue-admin";
import type { ReactNode } from "react";
import {
  readCinemasAction,
  readLayoutsAction,
  readRoomsAction,
} from "./actions";
import { CinemaForm, LayoutForm, RoomForm } from "./venue-forms";

export const dynamic = "force-dynamic";

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
    <ProblemCard
      id="cinemas-problem"
      title="The cinemas could not be loaded"
      problem={cinemasResult.error}
    />
  );

  let roomsSection: ReactNode = null;
  if (cinemaId !== "") {
    roomsSection =
      roomsResult === null || !roomsResult.ok ? (
        <ProblemCard
          id="rooms-problem"
          title="The rooms could not be loaded"
          problem={roomsResult?.error ?? configurationProblem()}
          selection={`cinema ${cinemaId}`}
        />
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
        <ProblemCard
          id="layouts-problem"
          title="The layout versions could not be loaded"
          problem={layoutsResult?.error ?? configurationProblem()}
          selection={`room ${roomId}`}
        />
      ) : (
        <LayoutsCard
          roomId={roomId}
          roomRows={roomRows}
          layoutRows={layoutRows}
        />
      );
  }

  return (
    <main id="main-content" className="foundation admin">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>

      <section className="card" aria-labelledby="venues-title">
        <p className="eyebrow">Local administration · M1 venue and layout</p>
        <h1 id="venues-title">Cinemas and rooms</h1>
        <p className="lead">
          Create a cinema, add rooms to it, and create layout versions for a
          room. Every layout version is listed with its status: a published
          version is immutable and changes require a new draft.
        </p>
      </section>

      <section className="card" aria-labelledby="create-cinema">
        <h2 id="create-cinema">Create a cinema</h2>
        <CinemaForm />
      </section>

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
    <section className="card" aria-labelledby="cinemas">
      <h2 id="cinemas">Cinemas</h2>
      {cinemaRows.length === 0 ? (
        <p className="notice">No cinemas exist yet.</p>
      ) : (
        <ul className="list" aria-label="Cinemas">
          {cinemaRows.map((cinema) => (
            <li key={cinema.id}>
              <code>{cinema.label}</code>{" "}
              <span className="path">(cinema {cinema.id})</span>
            </li>
          ))}
        </ul>
      )}

      <form
        className="fields"
        action={ADMIN_VENUES_PATH}
        method="get"
        aria-label="Select a cinema"
      >
        <label>
          Cinema
          <select name="cinemaId" defaultValue={cinemaId}>
            <option value="">Select a cinema</option>
            {cinemaRows.map((cinema) => (
              <option key={cinema.id} value={cinema.id}>
                {cinema.label}
              </option>
            ))}
          </select>
        </label>
        <button type="submit">Show rooms</button>
      </form>
    </section>
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
    <section className="card" aria-labelledby="rooms">
      <h2 id="rooms">Rooms in {cinema?.label ?? `cinema ${cinemaId}`}</h2>

      <RoomForm
        cinemas={cinemaRows.map((row) => ({ id: row.id, label: row.label }))}
        cinemaId={cinemaId}
      />

      {roomRows.length === 0 ? (
        <p className="notice">This cinema has no rooms yet.</p>
      ) : (
        <>
          <ul className="list" aria-label="Rooms">
            {roomRows.map((room) => (
              <li key={room.id}>
                <code>{room.label}</code>{" "}
                <span className="path">(room {room.id})</span>
              </li>
            ))}
          </ul>
          <form
            className="fields"
            action={ADMIN_VENUES_PATH}
            method="get"
            aria-label="Select a room"
          >
            <input type="hidden" name="cinemaId" value={cinemaId} />
            <label>
              Room
              <select name="roomId" defaultValue={roomId}>
                <option value="">Select a room</option>
                {roomRows.map((room) => (
                  <option key={room.id} value={room.id}>
                    {room.label}
                  </option>
                ))}
              </select>
            </label>
            <button type="submit">Show layout versions</button>
          </form>
        </>
      )}
    </section>
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
  return (
    <section className="card" aria-labelledby="layout-versions">
      <h2 id="layout-versions">Layout versions in room {roomId}</h2>
      <p className="notice">
        Creating a layout always adds the next draft version for the room.
        Publish it from <code>/admin/layouts</code> once its rows and seats are
        configured.
      </p>

      <LayoutForm
        rooms={roomRows.map((row) => ({ id: row.id, label: row.label }))}
        roomId={roomId}
      />

      {layoutRows.length === 0 ? (
        <p className="notice">This room has no layout versions yet.</p>
      ) : (
        <ul className="list" aria-label="Layout versions">
          {layoutRows.map((layout) => (
            <li key={layout.id}>
              <code>{layout.label}</code>{" "}
              <span className="path">
                (layout {layout.id} · status {layout.statusLabel})
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
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

function ProblemCard({
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
    <section className="card problems" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      {selection === undefined ? null : <p>{selection}</p>}
      <p>
        <code>{problem.code}</code> · HTTP {problem.status}
      </p>
      <p>{problem.detail}</p>
      <p className="notice">
        This surface works only in local development: the API must run with local
        administration enabled, and this page needs the same{" "}
        <code>{ADMIN_TOKEN_ENV}</code> token as the API.
      </p>
    </section>
  );
}
