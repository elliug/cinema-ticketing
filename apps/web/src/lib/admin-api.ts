// Typed administrative API client for the local venue, layout and screening surface.
//
// The Go API serves these routes under /api/v1/admin/, is reachable only from
// loopback in development, and requires an `Authorization: Bearer <token>`
// header. This client therefore:
//
//   - carries the bearer token on every request and never logs it (it never
//     logs at all, and token-bearing request data stays inside this module);
//   - targets a configurable base URL resolved from server-side environment
//     variables, defaulting to http://127.0.0.1:8080;
//   - converts failures into typed results instead of throwing, so callers must
//     handle RFC 9457 problem details explicitly.
//
// The client depends only on the standard fetch API, so it is usable from a
// server component, a server action, or a test with an injected fetch.

/** Physical seat kinds supported by the M1 domain. */
export type SeatKind = "standard" | "accessible" | "companion";

/** The seat kinds the backend accepts, in a stable order. */
export const SEAT_KINDS: readonly SeatKind[] = [
  "standard",
  "accessible",
  "companion",
];

/** Layout version lifecycle: a published layout version is immutable. */
export type LayoutStatus = "draft" | "published";

export interface AdminCinema {
  id: string;
  name: string;
  timezone: string;
  address: string;
}

export interface AdminRoom {
  id: string;
  cinemaId: string;
  name: string;
}

/** One layout version as the room layout listing reports it. */
export interface AdminLayoutVersion {
  id: string;
  version: number;
  status: LayoutStatus;
}

/** GET /cinemas. */
export interface AdminCinemaList {
  cinemas: AdminCinema[];
}

/** GET /cinemas/{cinemaId}/rooms. */
export interface AdminRoomList {
  cinemaId: string;
  rooms: AdminRoom[];
}

/** GET /rooms/{roomId}/layouts. */
export interface AdminLayoutVersionList {
  roomId: string;
  layouts: AdminLayoutVersion[];
}

export interface AdminSeat {
  id: string;
  number: string;
  position: number;
  kind: SeatKind;
  enabled: boolean;
}

export interface AdminRow {
  id: string;
  label: string;
  position: number;
  seats: AdminSeat[];
}

export interface AdminLayout {
  id: string;
  roomId: string;
  version: number;
  status: LayoutStatus;
  rows: AdminRow[];
}

export interface AdminFilm {
  id: string;
  title: string;
  runtimeMinutes: number;
  rating: string;
}

export interface AdminEvent {
  id: string;
  roomId: string;
  filmId: string;
  layoutVersionId: string;
  startsAt: string;
  status: string;
  /** The stored price in minor units. Zero is a free screening, never a missing price. */
  priceCents: number;
  /** The same amount as the API spells it (`12.50`). This client never formats money. */
  priceDisplay: string;
}

/**
 * A screening as the two administrative reads serve it: the stored columns and the
 * price, plus the three names the store joined in the same statement. A write answers
 * [AdminEvent] without them, because `RETURNING` cannot join and a name nobody read is
 * not a name to answer.
 */
export interface AdminEventRead extends AdminEvent {
  filmTitle: string;
  roomName: string;
  cinemaName: string;
}

/**
 * The writable fields of a screening, as `PATCH /events/{eventId}` accepts them.
 *
 * A field that is absent is not written at all, so `{priceCents: 0}` makes a screening
 * free and `{status: "on_sale"}` leaves its price exactly where it was. A body with
 * neither field is the API's `400`, not a request that changes nothing.
 */
export interface AdminEventUpdate {
  priceCents?: number;
  status?: string;
}

export interface AdminEventSeat {
  id: number;
  layoutSeatId: string;
  state: string;
}

export interface AdminEventSeats {
  eventId: string;
  seats: AdminEventSeat[];
}

/** A seat as accepted by PUT /layouts/{layoutId}/rows. */
export interface SeatPayload {
  id?: string;
  number: string;
  position: number;
  kind: SeatKind;
  enabled: boolean;
}

/** A row as accepted by PUT /layouts/{layoutId}/rows. */
export interface RowPayload {
  id?: string;
  label: string;
  position: number;
  seats: SeatPayload[];
}

export interface CreateCinemaInput {
  name: string;
  timezone: string;
  address: string;
}

export interface CreateRoomInput {
  cinemaId: string;
  name: string;
}

export interface CreateLayoutInput {
  roomId: string;
}

export interface CreateFilmInput {
  title: string;
  runtimeMinutes: number;
  rating: string;
}

export interface CreateEventInput {
  roomId: string;
  filmId: string;
  layoutVersionId: string;
  startsAt: string;
}

/**
 * Error codes a caller must handle.
 *
 * The first five are the problem codes the API itself returns; the last three
 * describe local failures this client detects before or after the response.
 */
export type AdminErrorCode =
  | "invalid_input"
  | "invalid_request"
  | "not_found"
  | "conflict"
  | "internal_error"
  | "network_error"
  | "unexpected_response"
  | "configuration_error";

/** RFC 9457 problem details, normalized into a typed value. */
export interface AdminProblem {
  type: string;
  title: string;
  status: number;
  code: AdminErrorCode;
  detail: string;
}

/** Every client call returns a typed result and never throws. */
export type AdminResult<T> =
  | { ok: true; value: T }
  | { ok: false; error: AdminProblem };

export const DEFAULT_ADMIN_BASE_URL = "http://127.0.0.1:8080";

/** Server-side environment variable holding the API base URL. */
export const ADMIN_BASE_URL_ENV = "API_BASE_URL";

/** Server-side environment variable holding the local administrative token. */
export const ADMIN_TOKEN_ENV = "API_LOCAL_ADMIN_TOKEN";

const ADMIN_PATH_PREFIX = "/api/v1/admin";

const PROBLEM_CODES: readonly AdminErrorCode[] = [
  "invalid_input",
  "invalid_request",
  "not_found",
  "conflict",
  "internal_error",
];

export interface AdminConfig {
  baseUrl: string;
  token: string;
}

/** A fetch-compatible function; injected in tests. */
export type FetchLike = (
  input: string,
  init?: RequestInit,
) => Promise<Response>;

export interface AdminClientConfig extends AdminConfig {
  fetch?: FetchLike;
}

export interface AdminClient {
  createCinema(input: CreateCinemaInput): Promise<AdminResult<AdminCinema>>;
  listCinemas(): Promise<AdminResult<AdminCinemaList>>;
  createRoom(input: CreateRoomInput): Promise<AdminResult<AdminRoom>>;
  listRooms(cinemaId: string): Promise<AdminResult<AdminRoomList>>;
  createLayout(input: CreateLayoutInput): Promise<AdminResult<AdminLayout>>;
  listLayouts(roomId: string): Promise<AdminResult<AdminLayoutVersionList>>;
  setRows(
    layoutId: string,
    rows: readonly RowPayload[],
  ): Promise<AdminResult<AdminLayout>>;
  publishLayout(layoutId: string): Promise<AdminResult<AdminLayout>>;
  cloneLayout(layoutId: string): Promise<AdminResult<AdminLayout>>;
  getLayout(layoutId: string): Promise<AdminResult<AdminLayout>>;
  createFilm(input: CreateFilmInput): Promise<AdminResult<AdminFilm>>;
  /** The repertoire, in the order the API answers it (`title`, then identifier). */
  listFilms(): Promise<AdminResult<AdminFilm[]>>;
  createEvent(input: CreateEventInput): Promise<AdminResult<AdminEvent>>;
  /** Every screening, in the API's screening order, with its film, room and cinema. */
  listEvents(): Promise<AdminResult<AdminEventRead[]>>;
  getEvent(eventId: string): Promise<AdminResult<AdminEventRead>>;
  /** Sets the price, the status, or both. Fields that are absent are not written. */
  updateEvent(
    eventId: string,
    update: AdminEventUpdate,
  ): Promise<AdminResult<AdminEvent>>;
  listEventSeats(eventId: string): Promise<AdminResult<AdminEventSeats>>;
}

/** The environment shape this module reads; kept narrow for testability. */
export type AdminEnvironment = Record<string, string | undefined>;

/**
 * Resolves the administrative client configuration from server-side
 * environment variables.
 *
 * The base URL falls back to the local API default. A missing token is a typed
 * configuration failure rather than an exception or an unauthenticated request.
 */
export function resolveAdminConfig(
  env: AdminEnvironment,
): AdminResult<AdminConfig> {
  const token = (env[ADMIN_TOKEN_ENV] ?? "").trim();
  if (token === "") {
    return {
      ok: false,
      error: {
        type: "https://cinema.local/problems/configuration",
        title: "Configuration error",
        status: 0,
        code: "configuration_error",
        detail: `${ADMIN_TOKEN_ENV} must be set to use local administration`,
      },
    };
  }

  const configuredBaseUrl = (env[ADMIN_BASE_URL_ENV] ?? "").trim();
  const baseUrl = normalizeBaseUrl(
    configuredBaseUrl === "" ? DEFAULT_ADMIN_BASE_URL : configuredBaseUrl,
  );

  return { ok: true, value: { baseUrl, token } };
}

/** Builds a client; callers normally obtain it from resolveAdminConfig. */
export function createAdminClient(config: AdminClientConfig): AdminClient {
  const baseUrl = normalizeBaseUrl(config.baseUrl);
  const doFetch: FetchLike = config.fetch ?? ((input, init) => fetch(input, init));

  async function send<T>(
    path: string,
    method: string,
    body?: unknown,
  ): Promise<AdminResult<T>> {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${config.token}`,
      Accept: "application/json",
    };
    const init: RequestInit = { method, headers, cache: "no-store" };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }

    let response: Response;
    try {
      response = await doFetch(`${baseUrl}${ADMIN_PATH_PREFIX}${path}`, init);
    } catch {
      // The underlying error is deliberately discarded: it may echo request
      // material, and it can carry the credential in a message or URL.
      return {
        ok: false,
        error: {
          type: "https://cinema.local/problems/network",
          title: "Network error",
          status: 0,
          code: "network_error",
          detail: "the cinema API could not be reached",
        },
      };
    }

    const parsed = await readJson(response);

    if (!response.ok) {
      return { ok: false, error: toProblem(response.status, parsed) };
    }
    if (!parsed.ok) {
      return {
        ok: false,
        error: unexpectedResponse(
          response.status,
          "the cinema API returned a body that is not valid JSON",
        ),
      };
    }
    return { ok: true, value: parsed.value as T };
  }

  return {
    createCinema: (input) => send("/cinemas", "POST", input),
    listCinemas: () => send("/cinemas", "GET"),
    createRoom: (input) => send("/rooms", "POST", input),
    listRooms: (cinemaId) =>
      send(`/cinemas/${encodeURIComponent(cinemaId)}/rooms`, "GET"),
    createLayout: (input) => send("/layouts", "POST", input),
    listLayouts: (roomId) =>
      send(`/rooms/${encodeURIComponent(roomId)}/layouts`, "GET"),
    setRows: (layoutId, rows) =>
      send(`/layouts/${encodeURIComponent(layoutId)}/rows`, "PUT", { rows }),
    publishLayout: (layoutId) =>
      send(`/layouts/${encodeURIComponent(layoutId)}/publish`, "POST"),
    cloneLayout: (layoutId) =>
      send(`/layouts/${encodeURIComponent(layoutId)}/clone`, "POST"),
    getLayout: (layoutId) =>
      send(`/layouts/${encodeURIComponent(layoutId)}`, "GET"),

    async createFilm(input) {
      const result = await send<unknown>("/films", "POST", input);
      if (!result.ok) {
        return result;
      }
      return parseFilm(result.value);
    },

    async listFilms() {
      const result = await send<unknown>("/films", "GET");
      if (!result.ok) {
        return result;
      }
      return parseFilmList(result.value);
    },

    async createEvent(input) {
      const result = await send<unknown>("/events", "POST", input);
      if (!result.ok) {
        return result;
      }
      // A create answers the stored columns and the price: the names are the reads' own,
      // because the write's `RETURNING` cannot join them.
      return parseEvent(result.value, 201);
    },

    async listEvents() {
      const result = await send<unknown>("/events", "GET");
      if (!result.ok) {
        return result;
      }
      return parseEventList(result.value);
    },

    async getEvent(eventId) {
      const id = eventId.trim();
      if (id === "") {
        return { ok: false, error: invalidEventId() };
      }
      const result = await send<unknown>(
        `/events/${encodeURIComponent(id)}`,
        "GET",
      );
      if (!result.ok) {
        return result;
      }
      return parseEventRead(result.value, 200);
    },

    async updateEvent(eventId, update) {
      const id = eventId.trim();
      if (id === "") {
        return { ok: false, error: invalidEventId() };
      }
      const result = await send<unknown>(
        `/events/${encodeURIComponent(id)}`,
        "PATCH",
        update,
      );
      if (!result.ok) {
        return result;
      }
      return parseEvent(result.value, 200);
    },

    listEventSeats: (eventId) =>
      send(`/events/${encodeURIComponent(eventId)}/seats`, "GET"),
  };
}

// ── Parsers ─────────────────────────────────────────────────────────────────
//
// A parser refuses what the screen cannot print, and names the field it could not read.
// The alternative — defaulting a missing price to 0, or dropping an entry that carries no
// film title — would put a number nobody served, or a screening nobody described, in front
// of an operator who is about to publish it.

function parseFilmList(value: unknown): AdminResult<AdminFilm[]> {
  if (!isRecord(value) || !Array.isArray(value.films)) {
    return refused(200, "the administrative film list has no film list");
  }
  const films: AdminFilm[] = [];
  for (const item of value.films) {
    const film = parseFilm(item);
    if (!film.ok) {
      // One unreadable entry refuses the list: a select that silently lost a film would
      // offer a repertoire smaller than the one the API answered.
      return film;
    }
    films.push(film.value);
  }
  return { ok: true, value: films };
}

function parseFilm(value: unknown): AdminResult<AdminFilm> {
  if (!isRecord(value)) {
    return refused(200, "the film list contains an entry without an id");
  }
  const id = servedString(value.id);
  if (id === undefined) {
    return refused(200, "the film list contains an entry without an id");
  }
  const title = servedString(value.title);
  if (title === undefined) {
    return refused(200, `the film ${id} carries no title`);
  }
  const runtimeMinutes = servedInteger(value.runtimeMinutes);
  if (runtimeMinutes === undefined) {
    return refused(200, `the film ${id} carries no runtime`);
  }
  const rating = servedString(value.rating);
  if (rating === undefined) {
    return refused(200, `the film ${id} carries no rating`);
  }
  return { ok: true, value: { id, title, runtimeMinutes, rating } };
}

function parseEventList(value: unknown): AdminResult<AdminEventRead[]> {
  if (!isRecord(value) || !Array.isArray(value.events)) {
    return refused(200, "the administrative screening list has no screening list");
  }
  const events: AdminEventRead[] = [];
  for (const item of value.events) {
    const event = parseEventRead(item, 200);
    if (!event.ok) {
      return event;
    }
    events.push(event.value);
  }
  return { ok: true, value: events };
}

/** The two administrative reads: the stored columns, the price, and the three names. */
function parseEventRead(value: unknown, status: number): AdminResult<AdminEventRead> {
  const stored = parseEvent(value, status);
  if (!stored.ok) {
    return stored;
  }
  const { id } = stored.value;
  const filmTitle = servedString(isRecord(value) ? value.filmTitle : undefined);
  if (filmTitle === undefined) {
    return refused(status, `the screening ${id} carries no film title`);
  }
  const roomName = servedString(isRecord(value) ? value.roomName : undefined);
  if (roomName === undefined) {
    return refused(status, `the screening ${id} carries no room name`);
  }
  const cinemaName = servedString(isRecord(value) ? value.cinemaName : undefined);
  if (cinemaName === undefined) {
    return refused(status, `the screening ${id} carries no cinema name`);
  }
  return {
    ok: true,
    value: { ...stored.value, filmTitle, roomName, cinemaName },
  };
}

/** The stored columns of one screening, plus the price every event answer carries. */
function parseEvent(value: unknown, status: number): AdminResult<AdminEvent> {
  if (!isRecord(value)) {
    return refused(status, "a screening answer that is not an object");
  }
  const id = servedString(value.id);
  if (id === undefined) {
    return refused(status, "a screening answer without an id");
  }

  const stored: Record<string, string> = {};
  for (const field of STORED_EVENT_FIELDS) {
    const served = servedString(value[field]);
    if (served === undefined) {
      return refused(status, `the screening ${id} carries no ${field}`);
    }
    stored[field] = served;
  }

  // The screen prints this value in a `<time>` and reads it to decide whether the start
  // has passed, so a start nobody can parse is refused instead of printed as text.
  if (Number.isNaN(Date.parse(stored.startsAt))) {
    return refused(
      status,
      `the screening ${id} carries a start time this screen cannot read`,
    );
  }

  const priceCents = servedInteger(value.priceCents);
  if (priceCents === undefined || priceCents < 0) {
    return refused(status, `the screening ${id} carries no price in minor units`);
  }
  const priceDisplay = servedString(value.priceDisplay);
  if (priceDisplay === undefined) {
    return refused(status, `the screening ${id} carries no price text`);
  }

  return {
    ok: true,
    value: {
      id,
      roomId: stored.roomId,
      filmId: stored.filmId,
      layoutVersionId: stored.layoutVersionId,
      startsAt: stored.startsAt,
      status: stored.status,
      priceCents,
      priceDisplay,
    },
  };
}

/** The stored columns every screening answer carries; the names are the reads' own. */
const STORED_EVENT_FIELDS = [
  "roomId",
  "filmId",
  "layoutVersionId",
  "startsAt",
  "status",
] as const;

function refused(status: number, detail: string): AdminResult<never> {
  return { ok: false, error: unexpectedResponse(status, detail) };
}

function invalidEventId(): AdminProblem {
  return {
    type: "https://cinema.local/problems/invalid-input",
    title: "Invalid input",
    status: 400,
    code: "invalid_input",
    detail: "eventId: must not be empty",
  };
}

function servedString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() !== "" ? value : undefined;
}

function servedInteger(value: unknown): number | undefined {
  return typeof value === "number" && Number.isInteger(value) ? value : undefined;
}

/** Strips trailing slashes so path joins never double up. */
function normalizeBaseUrl(value: string): string {
  return value.trim().replace(/\/+$/, "");
}

type JsonRead =
  | { ok: true; value: unknown }
  | { ok: false };

async function readJson(response: Response): Promise<JsonRead> {
  try {
    const text = await response.text();
    return { ok: true, value: JSON.parse(text) as unknown };
  } catch {
    return { ok: false };
  }
}

function toProblem(status: number, parsed: JsonRead): AdminProblem {
  if (parsed.ok && isRecord(parsed.value)) {
    const body = parsed.value;
    const code = body.code;
    if (typeof code === "string" && isProblemCode(code)) {
      return {
        type: stringOr(body.type, "about:blank"),
        title: stringOr(body.title, "Request failed"),
        status: typeof body.status === "number" ? body.status : status,
        code,
        detail: stringOr(body.detail, "the request could not be completed"),
      };
    }
  }
  if (status === 404) {
    // The API's local administrative guard answers 404, not 401, so that a caller can
    // never tell an absent surface from a wrong token. Both causes are administration
    // that was not mounted: the API started without API_LOCAL_ADMIN=true, or it was
    // started with an API_LOCAL_ADMIN_TOKEN this web does not hold. Naming them is the
    // difference between fixing a deployment setting and re-reading a form that was fine.
    return unexpectedResponse(status, ADMINISTRATION_ABSENT_DETAIL);
  }
  return unexpectedResponse(
    status,
    "the cinema API returned an unrecognised error response",
  );
}

/**
 * The two reasons the administrative surface answers 404, in the words an operator can
 * act on. It names the settings and never the token: the credential must not reach a
 * problem detail, a log, or the screen.
 */
const ADMINISTRATION_ABSENT_DETAIL =
  "the API has no administration at that address: either it runs without API_LOCAL_ADMIN=true, or API_LOCAL_ADMIN_TOKEN does not match the token it was started with";

function unexpectedResponse(status: number, detail: string): AdminProblem {
  return {
    type: "https://cinema.local/problems/unexpected-response",
    title: "Unexpected response",
    status,
    code: "unexpected_response",
    detail,
  };
}

function isProblemCode(value: string): value is AdminErrorCode {
  return (PROBLEM_CODES as readonly string[]).includes(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function stringOr(value: unknown, fallback: string): string {
  return typeof value === "string" ? value : fallback;
}
