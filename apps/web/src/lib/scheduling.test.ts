// Film creation and event scheduling orchestration.
//
// These tests work through the public module surface only: the typed payload
// each form produces, the client method each operation delegates to, the route
// each successful write revalidates, and the typed problem returned when the
// API rejects a request. The administrative client is replaced by a recording
// fake, so no network is used and no live Go API is required.
import assert from "node:assert/strict";
import { test } from "node:test";
import type {
  AdminClient,
  AdminErrorCode,
  AdminProblem,
  AdminResult,
} from "./admin-api.ts";
import type { SchedulingDeps } from "./scheduling.ts";
import {
  ADMIN_SCHEDULE_PATH,
  createFilm,
  describeScreeningStatus,
  EVENT_STATUSES,
  EVENT_STATUS_OPTIONS,
  eventInput,
  eventStatusOption,
  fieldProblem,
  filmInput,
  isEventStatus,
  MAX_PRICE_CENTS,
  priceCentsFromText,
  readEventSeats,
  scheduleEvent,
  screeningUpdateInput,
  updateScreening,
} from "./scheduling.ts";

interface RecordedClientCall {
  method: string;
  args: unknown[];
}

interface RecordingClient {
  client: AdminClient;
  calls: RecordedClientCall[];
}

type Responder = (
  method: string,
  args: unknown[],
) => Promise<AdminResult<unknown>>;

/** Builds an administrative client that records calls and answers each one. */
function recordingClient(respond: Responder): RecordingClient {
  const calls: RecordedClientCall[] = [];
  const method =
    (name: string) =>
    (...args: unknown[]) => {
      calls.push({ method: name, args });
      return respond(name, args);
    };
  const client = {
    createCinema: method("createCinema"),
    createRoom: method("createRoom"),
    createLayout: method("createLayout"),
    setRows: method("setRows"),
    publishLayout: method("publishLayout"),
    cloneLayout: method("cloneLayout"),
    getLayout: method("getLayout"),
    createFilm: method("createFilm"),
    listFilms: method("listFilms"),
    createEvent: method("createEvent"),
    listEvents: method("listEvents"),
    getEvent: method("getEvent"),
    updateEvent: method("updateEvent"),
    listEventSeats: method("listEventSeats"),
  } as unknown as AdminClient;
  return { client, calls };
}

interface SchedulingHarness {
  deps: SchedulingDeps;
  calls: RecordedClientCall[];
  revalidated: string[];
}

/** Builds the dependencies the server actions inject, with a recording fake. */
function harness(respond: Responder = () => Promise.resolve({ ok: true, value: {} })): SchedulingHarness {
  const recorder = recordingClient(respond);
  const revalidated: string[] = [];
  return {
    calls: recorder.calls,
    revalidated,
    deps: {
      call: (operation) => operation(recorder.client),
      revalidate: (path) => {
        revalidated.push(path);
      },
    },
  };
}

function problem(code: AdminErrorCode, status: number, detail: string): AdminProblem {
  return {
    type: `https://cinema.local/problems/${code.replace(/_/g, "-")}`,
    title: "Request failed",
    status,
    code,
    detail,
  };
}

/** Asserts a result succeeded and returns its value. */
function unwrap<T>(result: AdminResult<T>): T {
  assert.equal(result.ok, true, `expected success, got ${JSON.stringify(result)}`);
  if (!result.ok) {
    throw new Error("unreachable");
  }
  return result.value;
}

/** Asserts a result failed and returns its typed problem. */
function unwrapError<T>(result: AdminResult<T>): AdminProblem {
  assert.equal(result.ok, false, `expected a typed failure, got ${JSON.stringify(result)}`);
  if (result.ok) {
    throw new Error("unreachable");
  }
  return result.error;
}

const FILM = {
  id: "film-1",
  title: "Metropolis",
  runtimeMinutes: 153,
  rating: "TP",
};

const EVENT = {
  id: "event-1",
  roomId: "room-1",
  filmId: "film-1",
  layoutVersionId: "layout-1",
  startsAt: "2024-05-01T18:00:00Z",
  status: "draft",
  priceCents: 0,
  priceDisplay: "0.00",
};

const FILM_FORM = { title: "Metropolis", runtimeMinutes: "153", rating: "TP" };

const EVENT_FORM = {
  roomId: "room-1",
  filmId: "film-1",
  layoutVersionId: "layout-1",
  startsAt: "2024-05-01T18:00",
};

test("filmInput turns form text into the typed film payload", () => {
  const input = unwrap(filmInput({ ...FILM_FORM, title: "  Metropolis  " }));

  assert.deepEqual(input, {
    title: "Metropolis",
    runtimeMinutes: 153,
    rating: "TP",
  });
});

test("filmInput reports an empty title as a typed problem", () => {
  const problemResult = unwrapError(filmInput({ ...FILM_FORM, title: "   " }));

  assert.equal(problemResult.code, "invalid_input");
  assert.equal(problemResult.status, 400);
  assert.equal(problemResult.detail, "title: must not be empty");
});

test("filmInput reports a runtime outside the backend range as a typed problem", () => {
  for (const runtimeMinutes of ["0", "601", "", "abc", "9.5"]) {
    const problemResult = unwrapError(
      filmInput({ ...FILM_FORM, runtimeMinutes }),
    );

    assert.equal(problemResult.code, "invalid_input");
    assert.equal(
      problemResult.detail,
      "runtimeMinutes: must be between 1 and 600",
    );
  }
});

test("filmInput reports an empty rating as a typed problem", () => {
  const problemResult = unwrapError(filmInput({ ...FILM_FORM, rating: "" }));

  assert.equal(problemResult.code, "invalid_input");
  assert.equal(problemResult.detail, "rating: must not be empty");
});

test("eventInput converts a datetime-local value into a UTC instant", () => {
  const short = unwrap(eventInput(EVENT_FORM));
  assert.deepEqual(short, {
    roomId: "room-1",
    filmId: "film-1",
    layoutVersionId: "layout-1",
    startsAt: "2024-05-01T18:00:00.000Z",
  });

  const withSeconds = unwrap(
    eventInput({ ...EVENT_FORM, startsAt: "2024-05-01T18:00:45" }),
  );
  assert.equal(withSeconds.startsAt, "2024-05-01T18:00:45.000Z");

  const alreadyZoned = unwrap(
    eventInput({ ...EVENT_FORM, startsAt: "2024-05-01T20:00:00+02:00" }),
  );
  assert.equal(alreadyZoned.startsAt, "2024-05-01T18:00:00.000Z");
});

test("eventInput rejects an unusable start time as a typed problem", () => {
  for (const startsAt of ["", "tomorrow", "2024-13-01T18:00", "2024-02-31T18:00"]) {
    const problemResult = unwrapError(eventInput({ ...EVENT_FORM, startsAt }));

    assert.equal(problemResult.code, "invalid_input");
    assert.equal(problemResult.status, 400);
    assert.equal(problemResult.detail, "startsAt: must be a valid timestamp");
  }
});

test("eventInput rejects a blank identifier as a typed problem", () => {
  const problemResult = unwrapError(
    eventInput({ ...EVENT_FORM, layoutVersionId: "  " }),
  );

  assert.equal(problemResult.code, "invalid_input");
  assert.equal(problemResult.detail, "layoutVersionId: must not be empty");
});

test("createFilm delegates to the client and revalidates the schedule route", async () => {
  const run = harness(() =>
    Promise.resolve({ ok: true, value: FILM }),
  );

  const created = unwrap(await createFilm(run.deps, FILM_FORM));

  assert.deepEqual(created, FILM);
  assert.deepEqual(run.calls, [
    {
      method: "createFilm",
      args: [{ title: "Metropolis", runtimeMinutes: 153, rating: "TP" }],
    },
  ]);
  assert.deepEqual(run.revalidated, [ADMIN_SCHEDULE_PATH]);
  assert.equal(ADMIN_SCHEDULE_PATH, "/admin/schedule");
});

test("createFilm returns the API problem instead of throwing and does not revalidate", async () => {
  const run = harness(() =>
    Promise.resolve({
      ok: false,
      error: problem("invalid_input", 400, "title: must not be empty"),
    }),
  );

  const problemResult = unwrapError(
    await createFilm(run.deps, FILM_FORM),
  );

  assert.equal(problemResult.code, "invalid_input");
  assert.equal(problemResult.detail, "title: must not be empty");
  assert.deepEqual(run.revalidated, []);
});

test("createFilm never reaches the API with an invalid form", async () => {
  const run = harness();

  const problemResult = unwrapError(
    await createFilm(run.deps, { ...FILM_FORM, title: "" }),
  );

  assert.equal(problemResult.code, "invalid_input");
  assert.deepEqual(run.calls, []);
  assert.deepEqual(run.revalidated, []);
});

test("scheduleEvent delegates to the client and revalidates the schedule route", async () => {
  const run = harness(() =>
    Promise.resolve({ ok: true, value: EVENT }),
  );

  const scheduled = unwrap(await scheduleEvent(run.deps, EVENT_FORM));

  assert.deepEqual(scheduled, EVENT);
  assert.equal(run.calls.length, 1);
  const [call] = run.calls;
  assert.equal(call.method, "createEvent");
  assert.deepEqual(call.args, [
    {
      roomId: "room-1",
      filmId: "film-1",
      layoutVersionId: "layout-1",
      startsAt: "2024-05-01T18:00:00.000Z",
    },
  ]);
  assert.deepEqual(run.revalidated, [ADMIN_SCHEDULE_PATH]);
});

test("scheduling with a draft layout surfaces the conflict as a typed problem", async () => {
  const run = harness(() =>
    Promise.resolve({
      ok: false,
      error: problem("conflict", 409, "layout layout-1 is not published"),
    }),
  );

  const problemResult = unwrapError(
    await scheduleEvent(run.deps, EVENT_FORM),
  );

  assert.equal(problemResult.code, "conflict");
  assert.equal(problemResult.status, 409);
  assert.equal(problemResult.detail, "layout layout-1 is not published");
  assert.deepEqual(run.revalidated, []);
});

test("scheduleEvent never reaches the API with an invalid form", async () => {
  const run = harness();

  const problemResult = unwrapError(
    await scheduleEvent(run.deps, { ...EVENT_FORM, startsAt: "" }),
  );

  assert.equal(problemResult.code, "invalid_input");
  assert.deepEqual(run.calls, []);
});

test("readEventSeats delegates to the client and revalidates nothing", async () => {
  const seats = {
    eventId: "event-1",
    seats: [{ id: 1, layoutSeatId: "seat-1", state: "available" }],
  };
  const run = harness(() =>
    Promise.resolve({ ok: true, value: seats }),
  );

  const read = unwrap(await readEventSeats(run.deps, "event-1"));

  assert.deepEqual(read, seats);
  assert.deepEqual(run.calls, [
    { method: "listEventSeats", args: ["event-1"] },
  ]);
  assert.deepEqual(run.revalidated, []);
});

test("readEventSeats surfaces a missing event as a typed problem", async () => {
  const run = harness(() =>
    Promise.resolve({
      ok: false,
      error: problem("not_found", 404, "the requested resource does not exist"),
    }),
  );

  const problemResult = unwrapError(
    await readEventSeats(run.deps, "event-missing"),
  );

  assert.equal(problemResult.code, "not_found");
  assert.equal(problemResult.status, 404);
});

// ── The statuses, the price and the update (M3.5 · T8 · S3, S4) ───────────────
//
// The screen can carry the six statuses, but it does not get to invent them: the list is
// the schema's spellings, in the schema's order, and it is what the select offers and what
// the refusal spells. The price form and the PATCH body are the other two rules, and the
// last group is the honesty rule S8 asks for — a screening whose start has passed is never
// called on sale, whatever its stored status says.

const DRAFT = { id: "event-1", priceCents: 0, status: "draft", priceDisplay: "0.00" };

test("EVENT_STATUSES spells the six values cinema.events.status accepts", () => {
  assert.deepEqual(EVENT_STATUSES, [
    "draft",
    "published",
    "on_sale",
    "started",
    "completed",
    "cancelled",
  ]);
});

test("the screen offers every status the column accepts, and each one says what it means", () => {
  assert.deepEqual(
    EVENT_STATUS_OPTIONS.map((option) => option.value),
    [...EVENT_STATUSES],
    "the select offers exactly the statuses the write accepts",
  );
  for (const option of EVENT_STATUS_OPTIONS) {
    assert.notEqual(option.label.trim(), "", `${option.value} needs a word for its badge`);
    assert.notEqual(option.note.trim(), "", `${option.value} needs a sentence about the sale`);
  }
});

test("eventStatusOption does not invent a status the column would refuse", () => {
  assert.equal(eventStatusOption("on_sale")?.label, "On sale");
  assert.equal(eventStatusOption("  ARCHIVED "), undefined);
  assert.equal(isEventStatus("archived"), false);
  assert.equal(isEventStatus("on_sale"), true);
});

test("priceCentsFromText reads the money a person types into minor units", () => {
  assert.equal(unwrap(priceCentsFromText("12.50")), 1250);
  assert.equal(unwrap(priceCentsFromText("12.5")), 1250);
  assert.equal(unwrap(priceCentsFromText("12")), 1200);
  assert.equal(unwrap(priceCentsFromText(" 7.25 ")), 725);
  // A free screening is a legal price, not a missing one.
  assert.equal(unwrap(priceCentsFromText("0")), 0);
});

test("priceCentsFromText refuses a price it cannot represent", () => {
  assert.equal(
    unwrapError(priceCentsFromText("12.505")).detail,
    "priceCents: must have at most two decimal places",
  );
  assert.equal(
    unwrapError(priceCentsFromText("-1")).detail,
    "priceCents: must be zero or greater",
  );
  for (const text of ["", "twelve", "1e3", "12,50"]) {
    assert.equal(
      unwrapError(priceCentsFromText(text)).detail,
      "priceCents: must be a price such as 12.50",
    );
  }
  assert.equal(
    unwrapError(priceCentsFromText("21474836.48")).detail,
    `priceCents: must be at most ${MAX_PRICE_CENTS}`,
  );
});

test("create then publish sends the price and the status in one request", () => {
  // The create stored a draft at price 0 and cannot carry either field, so this is the
  // second and last request of the flow.
  const update = unwrap(
    screeningUpdateInput(DRAFT, { price: "12.50", status: "on_sale" }),
  );

  assert.deepEqual(update, { priceCents: 1250, status: "on_sale" });
});

test("an unchanged field is not sent, so a status-only change keeps the price", () => {
  const current = { id: "event-1", priceCents: 1250, status: "on_sale", priceDisplay: "12.50" };

  assert.deepEqual(
    unwrap(screeningUpdateInput(current, { price: "12.50", status: "cancelled" })),
    { status: "cancelled" },
  );
  assert.deepEqual(
    unwrap(screeningUpdateInput(current, { price: "14.00", status: "on_sale" })),
    { priceCents: 1400 },
  );
});

test("an update that changes nothing is refused in the API's own words", () => {
  const run = harness();
  const refused = unwrapError(
    screeningUpdateInput(DRAFT, { price: "0.00", status: "draft" }),
  );

  assert.equal(refused.code, "invalid_input");
  assert.equal(refused.status, 400);
  assert.equal(refused.detail, "nothing to change: provide priceCents or status");
  assert.deepEqual(run.calls, []);
});

test("a status the column would refuse never reaches the API", () => {
  const refused = unwrapError(
    screeningUpdateInput(DRAFT, { price: "0.00", status: "archived" }),
  );

  assert.equal(
    refused.detail,
    "status: must be one of draft, published, on_sale, started, completed, cancelled",
  );
});

test("updateScreening PATCHes the chosen fields and refreshes the schedule", async () => {
  const run = harness(() =>
    Promise.resolve({ ok: true, value: { ...EVENT, priceCents: 1250, priceDisplay: "12.50", status: "on_sale" } }),
  );

  const updated = unwrap(
    await updateScreening(run.deps, DRAFT, { price: "12.50", status: "on_sale" }),
  );

  assert.equal(updated.priceDisplay, "12.50");
  assert.deepEqual(run.calls, [
    { method: "updateEvent", args: ["event-1", { priceCents: 1250, status: "on_sale" }] },
  ]);
  assert.deepEqual(run.revalidated, [ADMIN_SCHEDULE_PATH]);
});

test("updateScreening prints the API's field-naming refusal and refreshes nothing", async () => {
  const run = harness(() =>
    Promise.resolve({
      ok: false,
      error: problem("invalid_input", 400, "priceCents: must be zero or greater"),
    }),
  );

  const refused = unwrapError(
    await updateScreening(run.deps, DRAFT, { price: "12.50", status: "on_sale" }),
  );

  assert.equal(refused.detail, "priceCents: must be zero or greater");
  assert.deepEqual(run.revalidated, []);
});

test("the screening list prints the stored status and what it means for the sale", () => {
  const now = new Date("2024-05-01T12:00:00Z");

  const draft = describeScreeningStatus(
    { status: "draft", startsAt: "2024-05-01T18:00:00Z" },
    now,
  );
  assert.equal(draft.label, "Draft");
  assert.equal(draft.tone, "neutral");
  assert.match(String(draft.catalogueNote), /Not for sale/);

  const published = describeScreeningStatus(
    { status: "published", startsAt: "2024-05-01T18:00:00Z" },
    now,
  );
  // The catalogue's membership is exactly `on_sale`, so publishing does not sell anything.
  assert.match(String(published.catalogueNote), /not for sale/i);

  const onSale = describeScreeningStatus(
    { status: "on_sale", startsAt: "2024-05-01T18:00:00Z" },
    now,
  );
  assert.equal(onSale.label, "On sale");
  assert.equal(onSale.startPassed, false);
  assert.equal(onSale.catalogueNote, null);
});

test("a screening whose start has passed is never called on sale", () => {
  const now = new Date("2024-05-01T12:00:00Z");

  const passed = describeScreeningStatus(
    { status: "on_sale", startsAt: "2024-05-01T10:00:00Z" },
    now,
  );
  assert.equal(passed.startPassed, true);
  assert.equal(passed.label, "Start passed");
  assert.match(String(passed.catalogueNote), /catalogue no longer lists it/);

  // The API's predicate is `starts_at > now()`, so a start equal to this instant is behind
  // it and the screening is already out of the catalogue.
  const boundary = describeScreeningStatus(
    { status: "on_sale", startsAt: "2024-05-01T12:00:00Z" },
    now,
  );
  assert.equal(boundary.startPassed, true);
  assert.notEqual(boundary.label, "On sale");
});

test("a status this build does not interpret is printed verbatim", () => {
  const view = describeScreeningStatus(
    { status: "archived", startsAt: "2024-05-01T18:00:00Z" },
    new Date("2024-05-01T12:00:00Z"),
  );

  assert.equal(view.status, "archived");
  assert.equal(view.label, "archived", "an uninterpreted status is never translated");
  assert.match(String(view.catalogueNote), /does not interpret/);
});

test("fieldProblem splits the field the API named from its own words", () => {
  assert.deepEqual(
    fieldProblem(problem("invalid_input", 400, "priceCents: must be zero or greater")),
    { field: "priceCents", detail: "priceCents: must be zero or greater" },
  );
  // A refusal about the request as a whole names no single field.
  assert.deepEqual(
    fieldProblem(problem("invalid_input", 400, "nothing to change: provide priceCents or status")),
    { field: "", detail: "nothing to change: provide priceCents or status" },
  );
  // Only `invalid_input` names a field; a conflict's prose is not a field name.
  assert.deepEqual(
    fieldProblem(problem("conflict", 409, "roomId: already scheduled")),
    { field: "", detail: "roomId: already scheduled" },
  );
});

