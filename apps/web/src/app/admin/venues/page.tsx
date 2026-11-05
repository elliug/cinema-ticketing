// /admin/venues — create cinemas and rooms, then create and inspect layout versions
// (M3.5 · T8 · S3; the ledger since visual-redesign).
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
// Since the Marquesina redesign the three listings are ledgers — an uppercase head, one
// hairline per row, the identifiers in the mono face — rather than bulleted paragraphs: an
// operator reads a column of names, not a run of sentences.
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
          title="No se pudieron cargar los cines"
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
              title="No se pudieron cargar las salas"
              problem={roomsResult?.error ?? configurationProblem()}
              selection={`Cine ${cinemaId}`}
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
              title="No se pudieron cargar las versiones del plano"
              problem={layoutsResult?.error ?? configurationProblem()}
              selection={`Sala ${roomId}`}
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
        eyebrow="Administración local · M3.5"
        title="Cines, salas y planos"
        description={
          <>
            Crea un cine, añádele salas y crea versiones del plano para una sala. Una versión
            publicada es inmutable y cambiarla exige un borrador nuevo. Sólo funciona en
            desarrollo local: la API tiene que correr con la administración local activada, y
            esta página necesita el mismo <code>{ADMIN_TOKEN_ENV}</code> que la API.
          </>
        }
      />

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Crear un cine</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            La zona horaria es el reloj del propio local, que es lo que la cartelera pública
            imprime junto a cada sesión, así que tiene que ser un nombre IANA como{" "}
            <code>Europe/Madrid</code> — no una ciudad ni una abreviatura. La API rechaza un
            nombre que no pueda cargar.
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
        <h2 className="m-0 text-lg leading-tight font-semibold">Cines</h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Todos los cines que lista la API y, después, aquel cuyas salas quieres ver.
        </p>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {cinemaRows.length === 0 ? (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Todavía no existe ningún cine. El que se cree arriba aparece aquí y puede tener
            salas después.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="ledger">
              <caption className="visually-hidden">Cines que lista la API</caption>
              <thead>
                <tr>
                  <th scope="col">Cine</th>
                  <th scope="col">Zona horaria</th>
                  <th scope="col">Dirección</th>
                  <th scope="col">Identificador</th>
                </tr>
              </thead>
              <tbody>
                {cinemaRows.map((cinema) => (
                  <tr key={cinema.id}>
                    <td className="font-medium break-words">{cinema.name}</td>
                    <td className="ledger__num">{cinema.timezone}</td>
                    <td className="break-words">{cinema.address}</td>
                    <td>
                      <code>{cinema.id}</code>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <form
          className="flex flex-wrap items-end gap-4"
          action={ADMIN_VENUES_PATH}
          method="get"
          aria-label="Elegir un cine"
        >
          <div className="flex min-w-56 flex-col gap-1.5">
            <label className="text-sm font-medium" htmlFor="venues-cinema">
              Cine
            </label>
            <select
              className={FILTER_SELECT_CLASS}
              id="venues-cinema"
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
            Ver las salas
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
          Salas de {cinema?.name ?? `cine ${cinemaId}`}
        </h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Una sala pertenece a un cine y tiene sus propias versiones del plano.
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
            Este cine todavía no tiene salas.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="ledger">
              <caption className="visually-hidden">Salas de este cine</caption>
              <thead>
                <tr>
                  <th scope="col">Sala</th>
                  <th scope="col">Identificador</th>
                </tr>
              </thead>
              <tbody>
                {roomRows.map((room) => (
                  <tr key={room.id}>
                    <td className="font-medium break-words">{room.name}</td>
                    <td>
                      <code>{room.id}</code>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {roomRows.length === 0 ? null : (
          <form
            className="flex flex-wrap items-end gap-4"
            action={ADMIN_VENUES_PATH}
            method="get"
            aria-label="Elegir una sala"
          >
            <input type="hidden" name="cinemaId" value={cinemaId} />
            <div className="flex min-w-56 flex-col gap-1.5">
              <label className="text-sm font-medium" htmlFor="venues-room">
                Sala
              </label>
              <select
                className={FILTER_SELECT_CLASS}
                id="venues-room"
                name="roomId"
                defaultValue={roomId}
              >
                <option value="">Elige una sala</option>
                {roomRows.map((room) => (
                  <option key={room.id} value={room.id}>
                    {room.name}
                  </option>
                ))}
              </select>
            </div>
            <Button type="submit" variant="outline">
              Ver las versiones del plano
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
          Versiones del plano de {room?.name ?? `sala ${roomId}`}
        </h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Crear un plano añade siempre la siguiente versión en borrador de la sala. Edita sus
          filas y publícala en el editor de <code>/admin/layouts</code>; una sesión sólo se
          puede programar contra una versión publicada.
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
            Esta sala todavía no tiene versiones del plano.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="ledger">
              <caption className="visually-hidden">Versiones del plano de esta sala</caption>
              <thead>
                <tr>
                  <th scope="col">Versión</th>
                  <th scope="col">Estado</th>
                  <th scope="col">Identificador</th>
                  <th scope="col">Editor</th>
                </tr>
              </thead>
              <tbody>
                {layoutRows.map((layout) => (
                  <tr key={layout.id}>
                    <td className="ledger__num">{layout.version}</td>
                    <td>
                      {/* The word is the state; the tone only reinforces it. */}
                      <Badge variant={layout.status === "published" ? "success" : "secondary"}>
                        {layout.statusLabel}
                      </Badge>
                    </td>
                    <td>
                      <code>{layout.id}</code>
                    </td>
                    <td>
                      <Button asChild variant="link" size="sm" className="h-auto p-0">
                        <Link href={`/admin/layouts?layoutId=${encodeURIComponent(layout.id)}`}>
                          Abrir la versión {layout.version} en el editor de planos
                        </Link>
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </CardContent>
    </Card>
  );
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
 * API's own detail are printed, so a missing token reads as a missing token instead of as a
 * cinema with no rooms. The API's detail stays in the API's own words: it is the box office's
 * sentence, not this screen's.
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
