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
  eventInput,
  filmInput,
  readEventSeats,
  scheduleEvent,
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
    createEvent: method("createEvent"),
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
