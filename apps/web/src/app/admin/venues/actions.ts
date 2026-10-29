"use server";

// Server actions for the local administrative venue surface.
//
// Each action is a thin adapter: it resolves the local administrative client
// configuration from server-side environment variables, delegates to the
// orchestration in lib/venue-admin.ts with the real client and next/cache's
// revalidatePath, and returns the typed result unchanged. Because the client is
// only ever built here, the bearer token never reaches the client bundle.
import { revalidatePath } from "next/cache";
import type {
  AdminCinema,
  AdminCinemaList,
  AdminClient,
  AdminLayout,
  AdminLayoutVersionList,
  AdminResult,
  AdminRoom,
  AdminRoomList,
} from "@/lib/admin-api";
import { createAdminClient, resolveAdminConfig } from "@/lib/admin-api";
import type {
  CinemaFormValues,
  LayoutFormValues,
  RoomFormValues,
  VenueDeps,
} from "@/lib/venue-admin";
import {
  createCinema,
  createLayout,
  createRoom,
  readCinemas,
  readLayouts,
  readRooms,
} from "@/lib/venue-admin";

async function withClient<T>(
  call: (client: AdminClient) => Promise<AdminResult<T>>,
): Promise<AdminResult<T>> {
  const config = resolveAdminConfig(process.env);
  if (!config.ok) {
    return { ok: false, error: config.error };
  }
  return call(createAdminClient(config.value));
}

/** Dependency bundle the venue module expects; writes refresh the page. */
function venueDeps(): VenueDeps {
  return {
    call: withClient,
    revalidate: (path) => revalidatePath(path),
  };
}

/** Creates one cinema from the administrative form. */
export async function createCinemaAction(
  values: CinemaFormValues,
): Promise<AdminResult<AdminCinema>> {
  return createCinema(venueDeps(), values);
}

/** Creates one room for the selected cinema. */
export async function createRoomAction(
  values: RoomFormValues,
): Promise<AdminResult<AdminRoom>> {
  return createRoom(venueDeps(), values);
}

/** Creates the next draft layout version for the selected room. */
export async function createLayoutAction(
  values: LayoutFormValues,
): Promise<AdminResult<AdminLayout>> {
  return createLayout(venueDeps(), values);
}

/** Reads every cinema for the venue overview. */
export async function readCinemasAction(): Promise<AdminResult<AdminCinemaList>> {
  return readCinemas(venueDeps());
}

/** Reads the rooms of one cinema. */
export async function readRoomsAction(
  cinemaId: string,
): Promise<AdminResult<AdminRoomList>> {
  return readRooms(venueDeps(), cinemaId);
}

/** Reads the layout versions of one room. */
export async function readLayoutsAction(
  roomId: string,
): Promise<AdminResult<AdminLayoutVersionList>> {
  return readLayouts(venueDeps(), roomId);
}
