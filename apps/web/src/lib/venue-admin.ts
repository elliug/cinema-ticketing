// Venue administration orchestration for /admin/venues.
//
// The local administrative forms post text, so this module owns the three
// things that must happen between a form and the typed API client:
//
//   - turning form text into the validated payload the API accepts, mirroring
//     the backend rules in apps/api/internal/venue so the operator sees the
//     same wording locally;
//   - delegating every write to the administrative client and revalidating the
//     page that shows the result;
//   - turning the listed cinemas, rooms, and layout versions into render-ready
//     rows that state each layout version's status.
//
// Reads never revalidate: their result is rendered directly. Dependencies are
// injected so the orchestration is testable without Next.js request scope, and
// every API failure stays a typed result: nothing here throws.
import type {
  AdminCinema,
  AdminCinemaList,
  AdminClient,
  AdminLayout,
  AdminLayoutVersionList,
  AdminLayoutVersion,
  AdminResult,
  AdminRoom,
  AdminRoomList,
  CreateCinemaInput,
  CreateLayoutInput,
  CreateRoomInput,
  LayoutStatus,
} from "./admin-api.ts";

/** The administrative route these operations keep in sync. */
export const ADMIN_VENUES_PATH = "/admin/venues";

/** Backend limit, mirrored so local validation matches the API. */
const MAX_TEXT_LENGTH = 120;

/** Cinema form values exactly as an operator typed them. */
export interface CinemaFormValues {
  name: string;
  timezone: string;
  address: string;
}

/** Room form values exactly as an operator typed them. */
export interface RoomFormValues {
  cinemaId: string;
  name: string;
}

/** Layout form values exactly as an operator typed them. */
export interface LayoutFormValues {
  roomId: string;
}

/** What a caller must provide to run these operations. */
export interface VenueDeps {
  /** Runs one call against the configured administrative client. */
  call<T>(
    operation: (client: AdminClient) => Promise<AdminResult<T>>,
  ): Promise<AdminResult<T>>;
  /** Refreshes the named route after a successful write. */
  revalidate(path: string): void;
}

/** One cinema, ready to render. */
export interface CinemaRow {
  id: string;
  name: string;
  timezone: string;
  address: string;
  /** The single line the view renders for this cinema. */
  label: string;
}

/** One room, ready to render. */
export interface RoomRow {
  id: string;
  cinemaId: string;
  name: string;
  /** The single line the view renders for this room. */
  label: string;
}

/** One layout version, ready to render with its status. */
export interface LayoutVersionRow {
  id: string;
  version: number;
  status: LayoutStatus;
  /** The status as the view states it. */
  statusLabel: string;
  /** The single line the view renders for this version. */
  label: string;
}

/**
 * Builds the cinema payload from form text.
 *
 * The rules mirror the backend: a non-empty name and timezone of at most 120
 * characters, and an optional address of at most 120 characters.
 */
export function cinemaInput(
  values: CinemaFormValues,
): AdminResult<CreateCinemaInput> {
  const name = values.name.trim();
  if (name === "") {
    return invalidInput("name", "must not be empty");
  }
  if (name.length > MAX_TEXT_LENGTH) {
    return invalidInput("name", atMost());
  }

  const timezone = values.timezone.trim();
  if (timezone === "") {
    return invalidInput("timezone", "must not be empty");
  }
  if (timezone.length > MAX_TEXT_LENGTH) {
    return invalidInput("timezone", atMost());
  }

  const address = values.address.trim();
  if (address.length > MAX_TEXT_LENGTH) {
    return invalidInput("address", atMost());
  }

  return { ok: true, value: { name, timezone, address } };
}

/** Builds the room payload from form text and the selected cinema. */
export function roomInput(values: RoomFormValues): AdminResult<CreateRoomInput> {
  const cinemaId = values.cinemaId.trim();
  if (cinemaId === "") {
    return invalidInput("cinemaId", "must not be empty");
  }

  const name = values.name.trim();
  if (name === "") {
    return invalidInput("name", "must not be empty");
  }
  if (name.length > MAX_TEXT_LENGTH) {
    return invalidInput("name", atMost());
  }

  return { ok: true, value: { cinemaId, name } };
}

/** Builds the layout payload from the selected room. */
export function layoutInput(
  values: LayoutFormValues,
): AdminResult<CreateLayoutInput> {
  const roomId = values.roomId.trim();
  if (roomId === "") {
    return invalidInput("roomId", "must not be empty");
  }
  return { ok: true, value: { roomId } };
}

/** Creates one cinema and refreshes the venues page on success. */
export async function createCinema(
  deps: VenueDeps,
  values: CinemaFormValues,
): Promise<AdminResult<AdminCinema>> {
  const input = cinemaInput(values);
  if (!input.ok) {
    return input;
  }
  const result = await deps.call((client) => client.createCinema(input.value));
  if (result.ok) {
    deps.revalidate(ADMIN_VENUES_PATH);
  }
  return result;
}

/** Creates one room for the selected cinema and refreshes the venues page. */
export async function createRoom(
  deps: VenueDeps,
  values: RoomFormValues,
): Promise<AdminResult<AdminRoom>> {
  const input = roomInput(values);
  if (!input.ok) {
    return input;
  }
  const result = await deps.call((client) => client.createRoom(input.value));
  if (result.ok) {
    deps.revalidate(ADMIN_VENUES_PATH);
  }
  return result;
}

/** Creates the next draft layout version for the selected room. */
export async function createLayout(
  deps: VenueDeps,
  values: LayoutFormValues,
): Promise<AdminResult<AdminLayout>> {
  const input = layoutInput(values);
  if (!input.ok) {
    return input;
  }
  const result = await deps.call((client) => client.createLayout(input.value));
  if (result.ok) {
    deps.revalidate(ADMIN_VENUES_PATH);
  }
  return result;
}

/** Reads every cinema. This is a read, so it refreshes nothing. */
export function readCinemas(deps: VenueDeps): Promise<AdminResult<AdminCinemaList>> {
  return deps.call((client) => client.listCinemas());
}

/** Reads the rooms of one cinema. A missing cinema arrives as a typed problem. */
export function readRooms(
  deps: VenueDeps,
  cinemaId: string,
): Promise<AdminResult<AdminRoomList>> {
  const id = cinemaId.trim();
  if (id === "") {
    return Promise.resolve(invalidInput("cinemaId", "must not be empty"));
  }
  return deps.call((client) => client.listRooms(id));
}

/** Reads the layout versions of one room, each with its status. */
export function readLayouts(
  deps: VenueDeps,
  roomId: string,
): Promise<AdminResult<AdminLayoutVersionList>> {
  const id = roomId.trim();
  if (id === "") {
    return Promise.resolve(invalidInput("roomId", "must not be empty"));
  }
  return deps.call((client) => client.listLayouts(id));
}

/** Prepares the listed cinemas for rendering, in the order the API returned. */
export function summarizeCinemas(value: AdminCinemaList): CinemaRow[] {
  const cinemas = Array.isArray(value.cinemas) ? value.cinemas : [];
  return cinemas.map((cinema) => {
    const parts = [cinema.name, cinema.timezone];
    if (cinema.address !== "") {
      parts.push(cinema.address);
    }
    return {
      id: cinema.id,
      name: cinema.name,
      timezone: cinema.timezone,
      address: cinema.address,
      label: parts.join(" · "),
    };
  });
}

/** Prepares the listed rooms for rendering, in the order the API returned. */
export function summarizeRooms(value: AdminRoomList): RoomRow[] {
  const rooms = Array.isArray(value.rooms) ? value.rooms : [];
  return rooms.map((room) => ({
    id: room.id,
    cinemaId: room.cinemaId,
    name: room.name,
    label: room.name,
  }));
}

/**
 * Prepares the listed layout versions for rendering with their status, in the
 * version order the API returned.
 */
export function summarizeLayouts(value: AdminLayoutVersionList): LayoutVersionRow[] {
  const layouts = Array.isArray(value.layouts) ? value.layouts : [];
  return layouts.map((layout: AdminLayoutVersion) => ({
    id: layout.id,
    version: layout.version,
    status: layout.status,
    statusLabel: layout.status,
    label: `v${layout.version} · ${layout.status}`,
  }));
}

function atMost(): string {
  return `must be at most ${MAX_TEXT_LENGTH} characters`;
}

/**
 * Builds the local failure shape the API would return for the same field, so
 * the UI can render local and server rejections identically.
 */
function invalidInput(field: string, message: string): AdminResult<never> {
  return {
    ok: false,
    error: {
      type: "https://cinema.local/problems/invalid-input",
      title: "Invalid input",
      status: 400,
      code: "invalid_input",
      detail: `${field}: ${message}`,
    },
  };
}
