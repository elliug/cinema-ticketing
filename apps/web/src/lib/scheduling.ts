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
  AdminEventRead,
  AdminEventSeats,
  AdminEventUpdate,
  AdminFilm,
  AdminProblem,
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

/**
 * The largest price `cinema.events.price_cents` can hold: the column is a PostgreSQL
 * integer and a price is never negative. Mirrored from
 * `apps/api/internal/venue.MaxPriceCents` so a price refused locally is refused in the
 * API's own words rather than in a second opinion about the same column.
 */
export const MAX_PRICE_CENTS = 2147483647;

/**
 * Every status `cinema.events.status` accepts, in the migration's order.
 *
 * The administration screen carries this list because no endpoint serves it (milestone
 * log, L14): `PATCH /admin/events/{eventId}` validates against the column and spells the
 * allowed values in its refusal, but nothing answers "which statuses are there". Keeping
 * the list here, in the schema's own spelling, is what lets the screen offer exactly the
 * values the write accepts — no more and no fewer.
 */
export const EVENT_STATUSES = [
  "draft",
  "published",
  "on_sale",
  "started",
  "completed",
  "cancelled",
] as const;

export type EventStatus = (typeof EVENT_STATUSES)[number];

/** A state's tone. It reinforces the word; it never carries the state on its own. */
export type ScreeningTone = "neutral" | "info" | "success" | "warning" | "danger";

/** One status the screen offers, and what it means for the buyer-facing surface. */
export interface EventStatusOption {
  /** The stored spelling, sent to the API unchanged. */
  value: EventStatus;
  /** The word the select and the badge show. */
  label: string;
  /** What choosing it does, in one sentence, for the catalogue or the ticket. */
  note: string;
  tone: ScreeningTone;
}

/**
 * The six statuses, each with the meaning L14 recorded where the route lives.
 *
 * The notes are the honest part: `published` is a recording state the catalogue does not
 * act on, so the screen says so instead of implying that publishing sells a screening.
 */
export const EVENT_STATUS_OPTIONS: readonly EventStatusOption[] = [
  {
    value: "draft",
    label: "Draft",
    note: "Not for sale: the catalogue ignores a draft.",
    tone: "neutral",
  },
  {
    value: "published",
    label: "Published",
    note: "A recording state. It is not for sale — the catalogue lists only on_sale.",
    tone: "info",
  },
  {
    value: "on_sale",
    label: "On sale",
    note: "What the catalogue shows, while the start is still ahead of it.",
    tone: "success",
  },
  {
    value: "started",
    label: "Started",
    note: "A recording state. Once the start has passed the catalogue drops the screening anyway.",
    tone: "warning",
  },
  {
    value: "completed",
    label: "Completed",
    note: "A recording state. Nothing can buy it.",
    tone: "neutral",
  },
  {
    value: "cancelled",
    label: "Cancelled",
    note: "Not for sale. A ticket already sold for it stops admitting its holder.",
    tone: "danger",
  },
];

/**
 * The status a screening is stored with when nothing else is named.
 *
 * It is the column's own default (`draft`), which is why it is the value the scheduling
 * form's select opens on and the one the create request does not have to spell.
 */
export const DEFAULT_EVENT_STATUS: EventStatus = "draft";

/** The option for one stored status, or undefined when this build does not know it. */
export function eventStatusOption(status: string): EventStatusOption | undefined {
  const wanted = status.trim().toLowerCase();
  return EVENT_STATUS_OPTIONS.find((option) => option.value === wanted);
}

/**
 * The badge variant that reinforces a tone.
 *
 * The word inside the badge is the state; this only picks the colour that agrees with it,
 * so a greyscale screen loses nothing. It lives here, in a module both a server page and a
 * client form can import, because a mapping exported from a `"use client"` module cannot be
 * called from a server component.
 */
export function toneBadgeVariant(
  tone: ScreeningTone,
): "secondary" | "info" | "success" | "warning" | "danger" {
  switch (tone) {
    case "info":
      return "info";
    case "success":
      return "success";
    case "warning":
      return "warning";
    case "danger":
      return "danger";
    default:
      return "secondary";
  }
}

/** What the screening list says about one screening's stored status and its start. */
export interface ScreeningStatusView {
  /** The stored status exactly as the API served it, never rewritten. */
  status: string;
  /** The word the badge carries — a state, not a colour. */
  label: string;
  tone: ScreeningTone;
  /** Whether the start time is behind the moment the page was rendered. */
  startPassed: boolean;
  /** What the row says about the catalogue, or null when there is nothing to warn about. */
  catalogueNote: string | null;
}

/**
 * Reads one screening's stored status honestly.
 *
 * The administrative list carries `status` and no `onSale` flag (L14), while the
 * catalogue's membership is `status = 'on_sale'` **and** a start still ahead, decided by
 * the database clock. So this function prints the stored status and, when that status is
 * `on_sale` but the start has passed, refuses to call the screening on sale: the start
 * time is what takes it out of the catalogue, and a page that ignored it would promise a
 * screening nobody can buy.
 *
 * `now` is a parameter rather than a call to the clock so the rule can be tested at the
 * boundary, and a status this build does not interpret is printed verbatim rather than
 * translated into a silence.
 */
export function describeScreeningStatus(
  screening: { status: string; startsAt: string },
  now: Date,
): ScreeningStatusView {
  const status = screening.status;
  const startsAt = Date.parse(screening.startsAt);
  // The API's own predicate is `starts_at > now()`, so a start equal to this instant is
  // already behind it. An unreadable start cannot be compared and is treated as ahead,
  // because the parser that reads the API refuses one rather than passing it on.
  const startPassed = !Number.isNaN(startsAt) && startsAt <= now.getTime();

  const option = eventStatusOption(status);
  if (option === undefined) {
    return {
      status,
      label: status === "" ? "No status" : status,
      tone: "neutral",
      startPassed,
      catalogueNote:
        "This build does not interpret this status, and the catalogue lists only on_sale screenings, so nothing here says it is for sale.",
    };
  }

  if (option.value === "on_sale" && startPassed) {
    return {
      status,
      label: "Start passed",
      tone: "warning",
      startPassed,
      catalogueNote:
        "Its stored status is on_sale, but its start time has passed, so the catalogue no longer lists it and its seats cannot be bought.",
    };
  }

  return {
    status,
    label: option.label,
    tone: option.tone,
    startPassed,
    catalogueNote: option.value === "on_sale" ? null : option.note,
  };
}

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
  /** The price in major units as typed, or absent when the form names no price. */
  price?: string;
  /** One of [EVENT_STATUSES]; absent is the column's default, [DEFAULT_EVENT_STATUS]. */
  status?: string;
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
 *
 * A price and a status are optional and each one is sent only when the operator actually
 * named it, which is what keeps two paths alive at once (M4.1 · T3b · S9):
 *
 *   - **One submit puts a screening on sale.** A price and a status in the form travel in
 *     the same `POST /admin/events`, so nobody has to create a draft and then fill a second
 *     form to sell it.
 *   - **A form that names neither field is the request this screen always sent.** No price
 *     and the draft the column already defaults to are not spelled in the body, so the
 *     create stays a draft at no price and the price-and-status form below is still how a
 *     screening is priced afterwards.
 *
 * Both values are the create's own validation: which minor units a typed price means is
 * [priceCentsFromText], which statuses exist is [EVENT_STATUSES], and a refusal names the
 * field the API names for the same column.
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

  const input: CreateEventInput = {
    roomId,
    filmId,
    layoutVersionId,
    startsAt: startsAt.value,
  };

  const price = (values.price ?? "").trim();
  if (price !== "") {
    const priceCents = priceCentsFromText(price);
    if (!priceCents.ok) {
      return priceCents;
    }
    input.priceCents = priceCents.value;
  }

  const status = (values.status ?? "").trim();
  if (status !== "" && status !== DEFAULT_EVENT_STATUS) {
    if (!isEventStatus(status)) {
      return invalidInput("status", `must be one of ${EVENT_STATUSES.join(", ")}`);
    }
    input.status = status;
  }

  return { ok: true, value: input };
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

/**
 * Reads the repertoire: `GET /admin/films`. A read, so it refreshes nothing.
 *
 * The screen needs it because a screening is scheduled by choosing a film from a list, and
 * a form that asked for a film identifier would be asking the operator to know something
 * the API is already willing to say.
 */
export function readFilms(
  deps: SchedulingDeps,
): Promise<AdminResult<AdminFilm[]>> {
  return deps.call((client) => client.listFilms());
}

/**
 * Reads every screening: `GET /admin/events`, in the API's screening order.
 *
 * A read, so it refreshes nothing. Each entry carries its film, room and cinema names and
 * the price it currently holds, which is what the list prints instead of identifiers.
 */
export function readEvents(
  deps: SchedulingDeps,
): Promise<AdminResult<AdminEventRead[]>> {
  return deps.call((client) => client.listEvents());
}

/** A screening's current writable values, as an administrative read served them. */
export interface ScreeningWritable {
  id: string;
  priceCents: number;
  status: string;
  /**
   * The price as the read spelled it (`12.50`). The form starts from this text rather than
   * from a number this module formatted, so the field opens on the amount the API holds.
   */
  priceDisplay: string;
}

/** The price and status form, exactly as an operator left it. */
export interface ScreeningUpdateValues {
  /** The price in major units, the way a person writes money: `12.50`. */
  price: string;
  /** One of [EVENT_STATUSES]. */
  status: string;
}

/**
 * Parses a price typed in major units into the minor units the API stores.
 *
 * Two decimal places and nothing negative: `12` and `12.5` are `1200` and `1250`, and
 * `12.505` is refused rather than rounded, because rounding money the operator typed is a
 * price they did not enter. The refusals name `priceCents`, the field the API names for
 * the same column, so local and server rejections read the same way.
 */
export function priceCentsFromText(value: string): AdminResult<number> {
  const text = value.trim();
  if (text.startsWith("-")) {
    return invalidInput("priceCents", "must be zero or greater");
  }

  const parsed = PRICE_TEXT.exec(text);
  if (parsed === null) {
    // Money has two decimal places. A third one is refused rather than rounded: rounding
    // would store a price the operator did not type.
    if (PRICE_FRACTION_OVERFLOW.test(text)) {
      return invalidInput("priceCents", "must have at most two decimal places");
    }
    return invalidInput("priceCents", "must be a price such as 12.50");
  }
  const [, whole, fraction = ""] = parsed;
  const cents = Number(whole) * 100 + Number(fraction.padEnd(2, "0"));
  if (!Number.isSafeInteger(cents) || cents > MAX_PRICE_CENTS) {
    return invalidInput("priceCents", `must be at most ${MAX_PRICE_CENTS}`);
  }
  return { ok: true, value: cents };
}

/** Whether a value is one of the statuses `cinema.events.status` accepts. */
export function isEventStatus(value: string): value is EventStatus {
  return (EVENT_STATUSES as readonly string[]).includes(value);
}

/**
 * Builds the `PATCH /admin/events/{eventId}` body from the price and status form.
 *
 * Three rules the screen cannot get wrong on its own:
 *
 *   - **A field that did not change is not sent.** The API writes only the columns the
 *     request names, so a status-only change leaves the price exactly where it was, and a
 *     form that resent an untouched price could only ever rewrite it with the same value.
 *     It is the same per-field rule `POST /admin/events` applies to the fields it accepts
 *     too, which is why a create that names neither of them stores a draft at no price.
 *   - **An update that changes nothing is refused**, in the API's own words, instead of
 *     being sent as a request the server would answer `400`.
 *   - **The status is one of the schema's spellings**, so a value the column would refuse
 *     never reaches the network, and the refusal spells the list the column allows.
 */
export function screeningUpdateInput(
  current: ScreeningWritable,
  values: ScreeningUpdateValues,
): AdminResult<AdminEventUpdate> {
  const price = priceCentsFromText(values.price);
  if (!price.ok) {
    return price;
  }

  const status = values.status.trim();
  if (!isEventStatus(status)) {
    return invalidInput("status", `must be one of ${EVENT_STATUSES.join(", ")}`);
  }

  const update: AdminEventUpdate = {};
  if (price.value !== current.priceCents) {
    update.priceCents = price.value;
  }
  if (status !== current.status) {
    update.status = status;
  }
  if (update.priceCents === undefined && update.status === undefined) {
    return nothingToChange();
  }
  return { ok: true, value: update };
}

/**
 * Sets a screening's price, its status, or both, and refreshes the schedule on success.
 *
 * A screening can now be created already priced and on sale (M4.1 · T3b · S9), so this is no
 * longer the only way to publish one: it is what changes a screening that already exists,
 * including one the create left as a draft at no price.
 */
export async function updateScreening(
  deps: SchedulingDeps,
  current: ScreeningWritable,
  values: ScreeningUpdateValues,
): Promise<AdminResult<AdminEvent>> {
  const id = current.id.trim();
  if (id === "") {
    return invalidInput("eventId", "must not be empty");
  }
  const input = screeningUpdateInput(current, values);
  if (!input.ok) {
    return input;
  }
  const result = await deps.call((client) =>
    client.updateEvent(id, input.value),
  );
  if (result.ok) {
    deps.revalidate(ADMIN_SCHEDULE_PATH);
  }
  return result;
}

/** A rejected request, split into the input the API named and its own words. */
export interface FieldProblem {
  /** The input the refusal named, or `""` when it named no single field. */
  field: string;
  /** The API's `detail`, unchanged. The form prints this and invents no wording of its own. */
  detail: string;
}

/**
 * Splits a field-scoped rejection from the API.
 *
 * Every `400` names its field at the start of `detail` — `priceCents: must be zero or
 * greater`, `status: must be one of …` — so the form can mark that input, or say the
 * refusal was about the request as a whole. `nothing to change: provide priceCents or
 * status` names no single field and stays whole: the field is whatever precedes a colon
 * only when it is one word, which is exactly how the API spells a field.
 */
export function fieldProblem(problem: AdminProblem): FieldProblem {
  if (problem.code !== "invalid_input") {
    return { field: "", detail: problem.detail };
  }
  const named = /^([A-Za-z][A-Za-z0-9]*): ([\s\S]*)$/.exec(problem.detail);
  if (named === null) {
    return { field: "", detail: problem.detail };
  }
  return { field: named[1], detail: problem.detail };
}

const LOCAL_TIMESTAMP = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2}))?$/;

const ZONED_TIMESTAMP =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(?::\d{2})?(?:\.\d{1,3})?(?:Z|[+-]\d{2}:\d{2})$/;

/** A price typed in major units: whole, then optionally one or two decimals. */
const PRICE_TEXT = /^(\d+)(?:\.(\d{1,2}))?$/;

/** A price with more decimals than money has, so it can be refused for that reason. */
const PRICE_FRACTION_OVERFLOW = /^\d+\.\d{3,}$/;

/**
 * Builds the local failure shape the API would return for the same field, so
 * the UI can render local and server rejections identically.
 */
function invalidInput(field: string, message: string): AdminResult<never> {
  return invalidInputDetail(`${field}: ${message}`);
}

/** The API's own wording for a body that would change nothing. */
function nothingToChange(): AdminResult<never> {
  return invalidInputDetail("nothing to change: provide priceCents or status");
}

function invalidInputDetail(detail: string): AdminResult<never> {
  return {
    ok: false,
    error: {
      type: "https://cinema.local/problems/invalid-input",
      title: "Invalid input",
      status: 400,
      code: "invalid_input",
      detail,
    },
  };
}

