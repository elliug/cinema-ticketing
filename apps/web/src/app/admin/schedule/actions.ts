"use server";

// Server actions for the local administrative film and event surface.
//
// Each action is a thin adapter: it resolves the local administrative client
// configuration from server-side environment variables, delegates to the
// orchestration in lib/scheduling.ts with the real client and next/cache's
// revalidatePath, and returns the typed result unchanged. Because the client is
// only ever built here, the bearer token never reaches the client bundle.
import { revalidatePath } from "next/cache";
import type {
  AdminClient,
  AdminEvent,
  AdminEventSeats,
  AdminFilm,
  AdminResult,
} from "@/lib/admin-api";
import { createAdminClient, resolveAdminConfig } from "@/lib/admin-api";
import type { EventFormValues, FilmFormValues } from "@/lib/scheduling";
import { createFilm, readEventSeats, scheduleEvent } from "@/lib/scheduling";

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

/** Reads the materialized seats of one event. */
export async function readEventSeatsAction(
  eventId: string,
): Promise<AdminResult<AdminEventSeats>> {
  return readEventSeats({ call: withClient, revalidate: revalidatePath }, eventId);
}
