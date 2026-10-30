"use server";

// Server actions for the local administrative film and screening surface (M3.5 · T8).
//
// Each action is a thin adapter: it resolves the local administrative client
// configuration from server-side environment variables, delegates to the orchestration in
// lib/scheduling.ts and lib/venue-admin.ts with the real client and next/cache's
// revalidatePath, and returns the typed result unchanged. Because the client is only ever
// built here, the bearer token never reaches the client bundle.
//
// The venue and layout reads are the same two modules the venues screen uses: a screening
// is scheduled against a room's published layout version, so this screen reads what that
// one already knows how to read instead of restating how to ask.
import { revalidatePath } from "next/cache";
import type {
  AdminCinemaList,
  AdminClient,
  AdminEvent,
  AdminEventRead,
  AdminEventSeats,
  AdminFilm,
  AdminLayoutVersionList,
  AdminResult,
  AdminRoomList,
} from "@/lib/admin-api";
import { createAdminClient, resolveAdminConfig } from "@/lib/admin-api";
import type {
  EventFormValues,
  FilmFormValues,
  ScreeningUpdateValues,
  ScreeningWritable,
} from "@/lib/scheduling";
import {
  createFilm,
  readEventSeats,
  readEvents,
  readFilms,
  scheduleEvent,
  updateScreening,
} from "@/lib/scheduling";
import { readCinemas, readLayouts, readRooms } from "@/lib/venue-admin";

async function withClient<T>(
  call: (client: AdminClient) => Promise<AdminResult<T>>,
): Promise<AdminResult<T>> {
  const config = resolveAdminConfig(process.env);
  if (!config.ok) {
    return { ok: false, error: config.error };
  }
  return call(createAdminClient(config.value));
}

/** Creates one film from the administrative form. */
export async function createFilmAction(
  values: FilmFormValues,
): Promise<AdminResult<AdminFilm>> {
  return createFilm({ call: withClient, revalidate: revalidatePath }, values);
}

/** Schedules one event for a published layout version and a start time. */
export async function scheduleEventAction(
  values: EventFormValues,
): Promise<AdminResult<AdminEvent>> {
  return scheduleEvent(
    { call: withClient, revalidate: revalidatePath },
    values,
  );
}

/**
 * Sets a screening's price, its status, or both.
 *
 * The second request of create-then-publish: the screen that just created a draft is the
 * screen that publishes it.
 */
export async function updateScreeningAction(
  current: ScreeningWritable,
  values: ScreeningUpdateValues,
): Promise<AdminResult<AdminEvent>> {
  return updateScreening(
    { call: withClient, revalidate: revalidatePath },
    current,
    values,
  );
}

/** Reads the repertoire the screening form offers. */
export async function readFilmsAction(): Promise<AdminResult<AdminFilm[]>> {
  return readFilms({ call: withClient, revalidate: revalidatePath });
}

/** Reads every screening, each one named and priced. */
export async function readEventsAction(): Promise<AdminResult<AdminEventRead[]>> {
  return readEvents({ call: withClient, revalidate: revalidatePath });
}

/** Reads the cinemas a screening can be scheduled in. */
export async function readCinemasAction(): Promise<AdminResult<AdminCinemaList>> {
  return readCinemas({ call: withClient, revalidate: revalidatePath });
}

/** Reads the rooms of the selected cinema. */
export async function readRoomsAction(
  cinemaId: string,
): Promise<AdminResult<AdminRoomList>> {
  return readRooms({ call: withClient, revalidate: revalidatePath }, cinemaId);
}

/** Reads the layout versions of the selected room, each with its status. */
export async function readLayoutsAction(
  roomId: string,
): Promise<AdminResult<AdminLayoutVersionList>> {
  return readLayouts({ call: withClient, revalidate: revalidatePath }, roomId);
}

/** Reads the materialized seats of one event. */
export async function readEventSeatsAction(
  eventId: string,
): Promise<AdminResult<AdminEventSeats>> {
  return readEventSeats({ call: withClient, revalidate: revalidatePath }, eventId);
}
