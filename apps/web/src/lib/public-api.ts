// Public (guest) reservation API client.
//
// The customer facing reservation routes live under /api/v1, are mounted
// outside the administrative guard, and require no credential: a guest selects
// seats without an account. This client therefore:
//
//   - sends no Authorization header and reads no token; the only header it adds
//     besides the usual JSON ones is the Idempotency-Key of a hold attempt;
//   - targets a configurable base URL resolved from server-side environment
//     variables, defaulting to http://127.0.0.1:8080;
//   - converts failures into typed results instead of throwing, so callers must
//     handle RFC 9457 problem details explicitly;
//   - rejects a selection the API would reject anyway (no seats, too many
//     seats, no idempotency key) before any request leaves the process.
//
// The client depends only on the standard fetch API, so it is usable from a
// server component, a server action, or a test with an injected fetch.
//
// Per ARCHITECTURE.md the field names below are the M2 shapes the API actually
// serves (see apps/api/internal/publicapi/holds.go). The availability snapshot
// carries the layout projection (`rowLabel`, `seatNumber`, `kind`) because the
// public read joins the layout, so the seat map can render the event's real
// rows. Those three fields stay optional: an older API serves availability only,
// and the map then degrades to its honest flat fallback instead of inventing
// geometry.

/** Error codes the reservation API and this client can report. */
export type PublicErrorCode =
  | "invalid_input"
  | "invalid_request"
  | "seat_unavailable"
  | "hold_window_expired"
  | "idempotency_key_reused"
  | "duplicate_seat"
  | "not_found"
  | "coordination_unavailable"
  | "network_error"
  | "unexpected_response";

/** RFC 9457 problem details, normalized into a typed value. */
export interface PublicProblem {
  type: string;
  title: string;
  status: number;
  code: PublicErrorCode;
  detail: string;
}

/** Every client call returns a typed result and never throws. */
export type PublicResult<T> =
  | { ok: true; value: T }
  | { ok: false; error: PublicProblem };

/** One event seat as the public availability snapshot reports it. */
export interface PublicEventSeat {
  /** Event seat id; the hold request refers to seats by this value. */
  id: number;
  /** The physical layout seat this event seat materializes. */
  layoutSeatId: string;
  /** Stored event seat state, for example `available`. */
  state: string;
  /** Whether a live hold currently owns the seat (the authoritative flag). */
  held: boolean;
  /**
   * Label of the layout row the seat belongs to, for example `A`, when the API
   * serves the layout projection. Absent from an older API.
   */
  rowLabel?: string;
  /** Seat number inside its own row, when the API serves the projection. */
  seatNumber?: string;
  /** Physical seat kind (`standard`, `accessible`, `companion`), when served. */
  kind?: string;
}

/** GET /events/{eventID}/seats. */
export interface PublicEventSeats {
  eventId: string;
  seats: PublicEventSeat[];
}

export interface CreateHoldInput {
  eventId: string;
  seatIds: readonly number[];
  /** A fresh key per attempt; a retry must never reuse the failed one. */
  idempotencyKey: string;
}

/** POST /reservations/holds. */
export interface PublicHold {
  id: string;
  eventId: string;
  seatIds: number[];
  /** Server deadline of the hold; the countdown is derived from this value. */
  expiresAt: string;
}

export interface PublicClient {
  listEventSeats(eventId: string): Promise<PublicResult<PublicEventSeats>>;
  createHold(input: CreateHoldInput): Promise<PublicResult<PublicHold>>;
}

/**
 * The serializable answer a server action returns to the seat page.
 *
 * A server action cannot hand back a thrown error or a Response, so the typed
 * result is flattened into plain data: the page and its client components then
 * decide what to render from the same values the client returns.
 */
export type AvailabilityResponse =
  | { ok: true; seats: PublicEventSeat[] }
  | { ok: false; problem: PublicProblem };

/** The serializable answer of a hold attempt. */
export type HoldResponse =
  | { ok: true; hold: PublicHold }
  | { ok: false; problem: PublicProblem };

export const DEFAULT_PUBLIC_BASE_URL = "http://127.0.0.1:8080";

/** Server-side environment variable holding the API base URL. */
export const PUBLIC_BASE_URL_ENV = "API_BASE_URL";

/**
 * The largest selection the API accepts, mirrored here so an oversized
 * selection is rejected before it is sent. apps/api/internal/reservation owns
 * the authoritative limit.
 */
export const MAX_HOLD_SEATS = 20;

const PUBLIC_PATH_PREFIX = "/api/v1";

const PROBLEM_CODES: readonly PublicErrorCode[] = [
  "invalid_input",
  "invalid_request",
  "seat_unavailable",
  "hold_window_expired",
  "idempotency_key_reused",
  "duplicate_seat",
  "not_found",
  "coordination_unavailable",
  "unexpected_response",
];

/** A fetch-compatible function; injected in tests. */
export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

/** The environment shape this module reads; kept narrow for testability. */
export type PublicEnvironment = Record<string, string | undefined>;

export interface PublicConfig {
  baseUrl: string;
}

export interface PublicClientConfig extends PublicConfig {
  fetch?: FetchLike;
}

/**
 * Resolves the public client configuration from server-side environment
 * variables. There is no credential to resolve: reaching the reservation
 * surface never requires one.
 */
export function resolvePublicConfig(env: PublicEnvironment): PublicResult<PublicConfig> {
  const configured = (env[PUBLIC_BASE_URL_ENV] ?? "").trim();
  return {
    ok: true,
    value: {
      baseUrl: normalizeBaseUrl(
        configured === "" ? DEFAULT_PUBLIC_BASE_URL : configured,
      ),
    },
  };
}

/**
 * Mints an Idempotency-Key for one hold attempt.
 *
 * The API replays a stored hold for a repeated key, so every attempt the user
 * makes gets its own key; reusing the key of a failed attempt would replay the
 * failure instead of starting a new hold.
 */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

/** Builds a client; callers normally obtain it from resolvePublicConfig. */
export function createPublicClient(config: PublicClientConfig): PublicClient {
  const baseUrl = normalizeBaseUrl(config.baseUrl);
  const doFetch: FetchLike = config.fetch ?? ((input, init) => fetch(input, init));

  async function send<T>(
    path: string,
    init: RequestInit,
  ): Promise<PublicResult<T>> {
    let response: Response;
    try {
      response = await doFetch(`${baseUrl}${PUBLIC_PATH_PREFIX}${path}`, init);
    } catch {
      // The underlying error is discarded: it can echo request material, and the
      // UI only needs to know that the service is temporarily unreachable.
      return {
        ok: false,
        error: {
          type: "https://cinema.local/problems/network",
          title: "Network error",
          status: 0,
          code: "network_error",
          detail: "the reservation service could not be reached",
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
          "the reservation API returned a body that is not valid JSON",
        ),
      };
    }
    return { ok: true, value: parsed.value as T };
  }

  return {
    async listEventSeats(eventId) {
      const result = await send<unknown>(
        `/events/${encodeURIComponent(eventId)}/seats`,
        {
          method: "GET",
          headers: { Accept: "application/json" },
          cache: "no-store",
        },
      );
      if (!result.ok) {
        return result;
      }
      return parseAvailability(result.value);
    },

    async createHold(input) {
      const invalid = validateHoldInput(input);
      if (invalid !== null) {
        return { ok: false, error: invalid };
      }

      const result = await send<unknown>("/reservations/holds", {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
          "Idempotency-Key": input.idempotencyKey,
        },
        cache: "no-store",
        body: JSON.stringify({
          eventId: input.eventId,
          seatIds: [...input.seatIds],
        }),
      });
      if (!result.ok) {
        return result;
      }
      return parseHold(result.value);
    },
  };
}

/** Rejects a selection that must never reach the network. */
function validateHoldInput(input: CreateHoldInput): PublicProblem | null {
  const key = input.idempotencyKey.trim();
  if (key === "") {
    return invalidInput("an Idempotency-Key is required for every hold attempt");
  }
  if (input.eventId.trim() === "") {
    return invalidInput("the hold request needs an event id");
  }
  if (input.seatIds.length === 0) {
    return invalidInput("select at least one seat before holding");
  }
  if (input.seatIds.length > MAX_HOLD_SEATS) {
    return invalidInput(
      `a single hold can reserve at most ${MAX_HOLD_SEATS} seats`,
    );
  }
  return null;
}

function invalidInput(detail: string): PublicProblem {
  return {
    type: "https://cinema.local/problems/invalid-input",
    title: "Invalid input",
    status: 0,
    code: "invalid_input",
    detail,
  };
}

function parseAvailability(value: unknown): PublicResult<PublicEventSeats> {
  if (!isRecord(value) || !Array.isArray(value.seats)) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the availability snapshot has no seat list"),
    };
  }

  const seats: PublicEventSeat[] = [];
  for (const item of value.seats) {
    if (!isRecord(item) || typeof item.id !== "number") {
      return {
        ok: false,
        error: unexpectedResponse(
          200,
          "the availability snapshot contains a seat without a numeric id",
        ),
      };
    }
    const seat: PublicEventSeat = {
      id: item.id,
      layoutSeatId: stringOr(item.layoutSeatId, ""),
      state: stringOr(item.state, "unknown"),
      // Occupancy is fail-closed: only an explicit `false` reads as free, and a
      // missing flag is reported as not held because the API always sends it.
      held: item.held === true,
    };
    // The layout projection is additive and optional: a field the API did not
    // send stays absent, so the map can tell "no projection" from "empty label"
    // and never invents geometry.
    const rowLabel = optionalString(item.rowLabel);
    if (rowLabel !== undefined) {
      seat.rowLabel = rowLabel;
    }
    const seatNumber = optionalString(item.seatNumber);
    if (seatNumber !== undefined) {
      seat.seatNumber = seatNumber;
    }
    const kind = optionalString(item.kind);
    if (kind !== undefined) {
      seat.kind = kind;
    }
    seats.push(seat);
  }

  return {
    ok: true,
    value: { eventId: stringOr(value.eventId, ""), seats },
  };
}

function parseHold(value: unknown): PublicResult<PublicHold> {
  if (!isRecord(value) || typeof value.id !== "string") {
    return {
      ok: false,
      error: unexpectedResponse(201, "the hold response has no reservation id"),
    };
  }
  const expiresAt = value.expiresAt;
  if (typeof expiresAt !== "string" || expiresAt.trim() === "") {
    // Without the server deadline there is no countdown, and a client-side guess
    // would promise a window the server never granted.
    return {
      ok: false,
      error: unexpectedResponse(
        201,
        "the hold response has no expiresAt deadline",
      ),
    };
  }
  return {
    ok: true,
    value: {
      id: value.id,
      eventId: stringOr(value.eventId, ""),
      seatIds: Array.isArray(value.seatIds)
        ? value.seatIds.filter((id): id is number => typeof id === "number")
        : [],
      expiresAt,
    },
  };
}

/** Strips trailing slashes so path joins never double up. */
function normalizeBaseUrl(value: string): string {
  return value.trim().replace(/\/+$/, "");
}

type JsonRead = { ok: true; value: unknown } | { ok: false };

async function readJson(response: Response): Promise<JsonRead> {
  try {
    const text = await response.text();
    return { ok: true, value: JSON.parse(text) as unknown };
  } catch {
    return { ok: false };
  }
}

function toProblem(status: number, parsed: JsonRead): PublicProblem {
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
    "the reservation API returned an unrecognised error response",
  );
}

function unexpectedResponse(status: number, detail: string): PublicProblem {
  return {
    type: "https://cinema.local/problems/unexpected-response",
    title: "Unexpected response",
    status,
    code: "unexpected_response",
    detail,
  };
}

function isProblemCode(value: string): value is PublicErrorCode {
  return (PROBLEM_CODES as readonly string[]).includes(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function stringOr(value: unknown, fallback: string): string {
  return typeof value === "string" ? value : fallback;
}

/** A served string, or undefined when the field is absent or blank. */
function optionalString(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  return value.trim() === "" ? undefined : value;
}
