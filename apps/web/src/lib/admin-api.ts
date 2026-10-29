// Typed administrative API client for the local M1 venue and layout surface.
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
  createEvent(input: CreateEventInput): Promise<AdminResult<AdminEvent>>;
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
    createFilm: (input) => send("/films", "POST", input),
    createEvent: (input) => send("/events", "POST", input),
    listEventSeats: (eventId) =>
      send(`/events/${encodeURIComponent(eventId)}/seats`, "GET"),
  };
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
  return unexpectedResponse(
    status,
    "the cinema API returned an unrecognised error response",
  );
}

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
