// Film creation and event scheduling orchestration.
//
// The local administrative forms post text, so this module owns the two things
// that must happen between a form and the typed API client:
//
//   - turning form text into the validated payload the API accepts, mirroring
//     the backend rules in apps/api/internal/venue so the operator sees the
//     same wording locally;
//   - delegating the write to the administrative client and revalidating the
//     page that shows the result.
//
// Dependencies are injected so the orchestration is testable without Next.js
// request scope: `call` runs one operation against the configured client and
// `revalidate` refreshes a route. The server actions pass the real client and
// next/cache's revalidatePath, and every API failure stays a typed result:
// nothing here throws.
import type {
  AdminClient,
  AdminEvent,
  AdminEventSeats,
  AdminFilm,
  AdminResult,
  CreateEventInput,
  CreateFilmInput,
} from "./admin-api.ts";

/** The administrative route these operations keep in sync. */
export const ADMIN_SCHEDULE_PATH = "/admin/schedule";

/** Backend limits, mirrored so local validation matches the API. */
const MAX_TEXT_LENGTH = 120;
const MIN_RUNTIME_MINUTES = 1;
const MAX_RUNTIME_MINUTES = 600;

/** Film form values exactly as an operator typed them. */
export interface FilmFormValues {
  title: string;
  runtimeMinutes: string;
  rating: string;
}

/** Event form values exactly as an operator typed them. */
export interface EventFormValues {
  roomId: string;
  filmId: string;
  layoutVersionId: string;
  startsAt: string;
}

/** What a caller must provide to run these operations. */
export interface SchedulingDeps {
  /** Runs one call against the configured administrative client. */
  call<T>(
    operation: (client: AdminClient) => Promise<AdminResult<T>>,
  ): Promise<AdminResult<T>>;
  /** Refreshes the named route after a successful write. */
  revalidate(path: string): void;
}

/**
 * Builds the film payload from form text.
 *
 * The rules mirror the backend: a non-empty title and rating of at most 120
 * characters, and a runtime between 1 and 600 whole minutes.
 */
export function filmInput(values: FilmFormValues): AdminResult<CreateFilmInput> {
  const title = values.title.trim();
  if (title === "") {
    return invalidInput("title", "must not be empty");
  }
  if (title.length > MAX_TEXT_LENGTH) {
    return invalidInput("title", `must be at most ${MAX_TEXT_LENGTH} characters`);
  }

  const runtimeText = values.runtimeMinutes.trim();
  const runtimeMinutes = Number(runtimeText);
  if (
    runtimeText === "" ||
    !/^\d+$/.test(runtimeText) ||
    !Number.isInteger(runtimeMinutes) ||
    runtimeMinutes < MIN_RUNTIME_MINUTES ||
    runtimeMinutes > MAX_RUNTIME_MINUTES
  ) {
    return invalidInput(
      "runtimeMinutes",
      `must be between ${MIN_RUNTIME_MINUTES} and ${MAX_RUNTIME_MINUTES}`,
    );
  }

  const rating = values.rating.trim();
  if (rating === "") {
    return invalidInput("rating", "must not be empty");
  }
  if (rating.length > MAX_TEXT_LENGTH) {
    return invalidInput("rating", `must be at most ${MAX_TEXT_LENGTH} characters`);
  }

  return { ok: true, value: { title, runtimeMinutes, rating } };
}

/**
 * Builds the event payload from form text.
 *
 * The form asks for a UTC start time because a `datetime-local` value carries no
 * zone; the returned instant is the RFC 3339 form the API expects.
 */
export function eventInput(values: EventFormValues): AdminResult<CreateEventInput> {
  const roomId = values.roomId.trim();
  if (roomId === "") {
    return invalidInput("roomId", "must not be empty");
  }
  const filmId = values.filmId.trim();
  if (filmId === "") {
    return invalidInput("filmId", "must not be empty");
  }
  const layoutVersionId = values.layoutVersionId.trim();
  if (layoutVersionId === "") {
    return invalidInput("layoutVersionId", "must not be empty");
  }

  const startsAt = parseStartsAt(values.startsAt);
  if (!startsAt.ok) {
    return startsAt;
  }

  return {
    ok: true,
    value: { roomId, filmId, layoutVersionId, startsAt: startsAt.value },
  };
}

/**
 * Parses a start time into an RFC 3339 instant.
 *
 * A value without a zone (what `datetime-local` submits) is read as UTC, and a
 * value that already carries `Z` or an offset keeps its instant. Calendar
 * overflow such as `2024-02-31` is rejected instead of being normalized, because
 * `Date` would silently roll it into March.
 */
export function parseStartsAt(value: string): AdminResult<string> {
  const trimmed = value.trim();

  const local = LOCAL_TIMESTAMP.exec(trimmed);
  if (local) {
    const [year, month, day, hour, minute, second] = local.slice(1);
    const seconds = second ?? "00";
    const parsed = new Date(
      `${year}-${month}-${day}T${hour}:${minute}:${seconds}Z`,
    );
    if (
      Number.isNaN(parsed.getTime()) ||
      parsed.getUTCFullYear() !== Number(year) ||
      parsed.getUTCMonth() !== Number(month) - 1 ||
      parsed.getUTCDate() !== Number(day) ||
      parsed.getUTCHours() !== Number(hour) ||
      parsed.getUTCMinutes() !== Number(minute) ||
      parsed.getUTCSeconds() !== Number(seconds)
    ) {
      return invalidInput("startsAt", "must be a valid timestamp");
    }
    return { ok: true, value: parsed.toISOString() };
  }

  if (ZONED_TIMESTAMP.test(trimmed)) {
    const parsed = new Date(trimmed);
    if (!Number.isNaN(parsed.getTime())) {
      return { ok: true, value: parsed.toISOString() };
    }
  }

  return invalidInput("startsAt", "must be a valid timestamp");
}

/** Creates one film and refreshes the schedule route on success. */
export async function createFilm(
  deps: SchedulingDeps,
  values: FilmFormValues,
): Promise<AdminResult<AdminFilm>> {
  const input = filmInput(values);
  if (!input.ok) {
    return input;
  }
  const result = await deps.call((client) => client.createFilm(input.value));
  if (result.ok) {
    deps.revalidate(ADMIN_SCHEDULE_PATH);
  }
  return result;
}

/**
 * Schedules one event from a published layout version and refreshes the
 * schedule route on success. A draft layout version is the API's conflict, and
 * it arrives here as a typed problem rather than an exception.
 */
export async function scheduleEvent(
  deps: SchedulingDeps,
  values: EventFormValues,
): Promise<AdminResult<AdminEvent>> {
  const input = eventInput(values);
  if (!input.ok) {
    return input;
  }
  const result = await deps.call((client) => client.createEvent(input.value));
  if (result.ok) {
    deps.revalidate(ADMIN_SCHEDULE_PATH);
  }
  return result;
}

/**
 * Reads the materialized seats of one event.
 *
 * This is a read, so it refreshes nothing: the caller renders the result
 * directly.
 */
export async function readEventSeats(
  deps: SchedulingDeps,
  eventId: string,
): Promise<AdminResult<AdminEventSeats>> {
  const id = eventId.trim();
  if (id === "") {
    return invalidInput("eventId", "must not be empty");
  }
  return deps.call((client) => client.listEventSeats(id));
}

const LOCAL_TIMESTAMP = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/;

const ZONED_TIMESTAMP =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2})?(?:\.\d{1,3})?(?:Z|[+-]\d{2}:\d{2})$/;

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
