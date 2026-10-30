// Administrative API client contract.
//
// These tests exercise the public client interface only: the HTTP request it
// builds (method, URL, headers, body) and the typed result it returns. The
// client is constructed with an injected fetch so no network is used and no
// live Go API is required.
import assert from "node:assert/strict";
import { test } from "node:test";
import type {
  AdminClient,
  AdminProblem,
  AdminResult,
  RowPayload,
} from "./admin-api.ts";
import {
  createAdminClient,
  DEFAULT_ADMIN_BASE_URL,
  resolveAdminConfig,
} from "./admin-api.ts";

const TOKEN = "local-admin-token-for-tests";
const BASE_URL = "http://127.0.0.1:8080";

interface RecordedCall {
  url: string;
  init: RequestInit;
}

interface RecordingFetch {
  fetch: (input: string, init?: RequestInit) => Promise<Response>;
  calls: RecordedCall[];
}

/** Builds an injected fetch that answers every call with one response. */
function recordingFetch(
  respond: (call: RecordedCall) => Response,
): RecordingFetch {
  const calls: RecordedCall[] = [];
  return {
    calls,
    fetch: (input, init) => {
      const call: RecordedCall = { url: input, init: init ?? {} };
      calls.push(call);
      return Promise.resolve(respond(call));
    },
  };
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function makeClient(recorder: RecordingFetch): AdminClient {
  return createAdminClient({
    baseUrl: BASE_URL,
    token: TOKEN,
    fetch: recorder.fetch,
  });
}

/** Asserts a result succeeded and returns its value. */
function unwrap<T>(result: AdminResult<T>): T {
  assert.equal(
    result.ok,
    true,
    `expected success, got ${JSON.stringify(result)}`,
  );
  if (!result.ok) {
    throw new Error("unreachable");
  }
  return result.value;
}

/** Asserts a result failed and returns its typed problem. */
function unwrapError<T>(result: AdminResult<T>): AdminProblem {
  assert.equal(
    result.ok,
    false,
    `expected a typed failure, got ${JSON.stringify(result)}`,
  );
  if (result.ok) {
    throw new Error("unreachable");
  }
  return result.error;
}

function headerValue(init: RequestInit, name: string): string | undefined {
  const headers = init.headers as Record<string, string> | undefined;
  return headers?.[name];
}

function bodyOf(init: RequestInit): unknown {
  assert.equal(typeof init.body, "string");
  return JSON.parse(init.body as string);
}

test("resolveAdminConfig defaults the base URL and requires a token", () => {
  const missing = resolveAdminConfig({});
  assert.equal(missing.ok, false);
  assert.equal(unwrapError(missing).code, "configuration_error");

  const configured = unwrap(resolveAdminConfig({ API_LOCAL_ADMIN_TOKEN: TOKEN }));
  assert.equal(configured.baseUrl, DEFAULT_ADMIN_BASE_URL);
  assert.equal(configured.baseUrl, "http://127.0.0.1:8080");
  assert.equal(configured.token, TOKEN);
});

test("resolveAdminConfig honors API_BASE_URL and normalizes a trailing slash", () => {
  const configured = unwrap(
    resolveAdminConfig({
      API_LOCAL_ADMIN_TOKEN: TOKEN,
      API_BASE_URL: "http://127.0.0.1:9999/",
    }),
  );
  assert.equal(configured.baseUrl, "http://127.0.0.1:9999");
});

test("createCinema posts the cinema payload and returns the created cinema", async () => {
  const cinema = {
    id: "cinema-1",
    name: "Central",
    timezone: "Europe/Madrid",
    address: "Gran Via 1",
  };
  const recorder = recordingFetch(() => jsonResponse(201, cinema));
  const client = makeClient(recorder);

  const created = unwrap(
    await client.createCinema({
      name: "Central",
      timezone: "Europe/Madrid",
      address: "Gran Via 1",
    }),
  );

  assert.deepEqual(created, cinema);
  assert.equal(recorder.calls.length, 1);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/cinemas`);
  assert.equal(call.init.method, "POST");
  assert.equal(headerValue(call.init, "Authorization"), `Bearer ${TOKEN}`);
  assert.equal(headerValue(call.init, "Content-Type"), "application/json");
  assert.deepEqual(bodyOf(call.init), {
    name: "Central",
    timezone: "Europe/Madrid",
    address: "Gran Via 1",
  });
});

test("createRoom posts the room payload and returns the created room", async () => {
  const room = { id: "room-1", cinemaId: "cinema-1", name: "Sala 1" };
  const recorder = recordingFetch(() => jsonResponse(201, room));
  const client = makeClient(recorder);

  const created = unwrap(
    await client.createRoom({ cinemaId: "cinema-1", name: "Sala 1" }),
  );

  assert.deepEqual(created, room);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/rooms`);
  assert.equal(call.init.method, "POST");
  assert.deepEqual(bodyOf(call.init), { cinemaId: "cinema-1", name: "Sala 1" });
});

test("createLayout posts the room id and returns a draft layout", async () => {
  const layout = {
    id: "layout-1",
    roomId: "room-1",
    version: 1,
    status: "draft",
    rows: [],
  };
  const recorder = recordingFetch(() => jsonResponse(201, layout));
  const client = makeClient(recorder);

  const created = unwrap(
    await client.createLayout({ roomId: "room-1" }),
  );

  assert.deepEqual(created, layout);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/layouts`);
  assert.equal(call.init.method, "POST");
  assert.deepEqual(bodyOf(call.init), { roomId: "room-1" });
});

test("getLayout reads one layout version without a request body", async () => {
  const layout = {
    id: "layout-7",
    roomId: "room-1",
    version: 3,
    status: "published",
    rows: [],
  };
  const recorder = recordingFetch(() => jsonResponse(200, layout));
  const client = makeClient(recorder);

  const fetched = unwrap(await client.getLayout("layout-7"));

  assert.deepEqual(fetched, layout);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/layouts/layout-7`);
  assert.equal(call.init.method, "GET");
  assert.equal(call.init.body, undefined);
  assert.equal(headerValue(call.init, "Authorization"), `Bearer ${TOKEN}`);
});

test("listCinemas reads every cinema without a request body", async () => {
  const cinemas = {
    cinemas: [
      {
        id: "cinema-1",
        name: "Central",
        timezone: "Europe/Madrid",
        address: "Gran Via 1",
      },
      {
        id: "cinema-2",
        name: "Norte",
        timezone: "Europe/Madrid",
        address: "",
      },
    ],
  };
  const recorder = recordingFetch(() => jsonResponse(200, cinemas));
  const client = makeClient(recorder);

  const listed = unwrap(await client.listCinemas());

  assert.deepEqual(listed, cinemas);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/cinemas`);
  assert.equal(call.init.method, "GET");
  assert.equal(call.init.body, undefined);
  assert.equal(headerValue(call.init, "Authorization"), `Bearer ${TOKEN}`);
});

test("listRooms reads the rooms of one cinema", async () => {
  const rooms = {
    cinemaId: "cinema-1",
    rooms: [{ id: "room-1", cinemaId: "cinema-1", name: "Sala 1" }],
  };
  const recorder = recordingFetch(() => jsonResponse(200, rooms));
  const client = makeClient(recorder);

  const listed = unwrap(await client.listRooms("cinema-1"));

  assert.deepEqual(listed, rooms);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/cinemas/cinema-1/rooms`);
  assert.equal(call.init.method, "GET");
  assert.equal(call.init.body, undefined);
});

test("listLayouts reads a room's layout versions with their status", async () => {
  const layouts = {
    roomId: "room-1",
    layouts: [
      { id: "layout-1", version: 1, status: "published" },
      { id: "layout-2", version: 2, status: "draft" },
    ],
  };
  const recorder = recordingFetch(() => jsonResponse(200, layouts));
  const client = makeClient(recorder);

  const listed = unwrap(await client.listLayouts("room-1"));

  assert.deepEqual(listed, layouts);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/rooms/room-1/layouts`);
  assert.equal(call.init.method, "GET");
  assert.equal(call.init.body, undefined);
});

test("a list read of a missing venue becomes a typed not_found failure", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(404, {
      type: "https://cinema.local/problems/not-found",
      title: "Not found",
      status: 404,
      code: "not_found",
      detail: "the requested resource does not exist",
    }),
  );
  const client = makeClient(recorder);

  const problem = unwrapError(await client.listRooms("cinema-9"));

  assert.equal(problem.code, "not_found");
  assert.equal(problem.status, 404);
});

test("setRows replaces draft geometry with heterogeneous rows", async () => {
  const rows: RowPayload[] = [
    {
      label: "A",
      position: 1,
      seats: [
        { number: "1", position: 1, kind: "standard", enabled: true },
        { number: "2", position: 2, kind: "accessible", enabled: true },
      ],
    },
    {
      label: "B",
      position: 2,
      seats: [{ number: "1", position: 1, kind: "companion", enabled: false }],
    },
  ];
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      id: "layout-1",
      roomId: "room-1",
      version: 1,
      status: "draft",
      rows,
    }),
  );
  const client = makeClient(recorder);

  const updated = unwrap(await client.setRows("layout-1", rows));

  assert.equal(updated.rows.length, 2);
  assert.equal(updated.rows[0].seats.length, 2);
  assert.equal(updated.rows[1].seats.length, 1);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/layouts/layout-1/rows`);
  assert.equal(call.init.method, "PUT");
  assert.deepEqual(bodyOf(call.init), { rows });
});

test("publishLayout freezes a draft layout", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      id: "layout-1",
      roomId: "room-1",
      version: 1,
      status: "published",
      rows: [],
    }),
  );
  const client = makeClient(recorder);

  const published = unwrap(await client.publishLayout("layout-1"));

  assert.equal(published.status, "published");
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/layouts/layout-1/publish`);
  assert.equal(call.init.method, "POST");
});

test("cloneLayout versions a published layout into a new draft", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(201, {
      id: "layout-2",
      roomId: "room-1",
      version: 2,
      status: "draft",
      rows: [],
    }),
  );
  const client = makeClient(recorder);

  const cloned = unwrap(await client.cloneLayout("layout-1"));

  assert.equal(cloned.id, "layout-2");
  assert.equal(cloned.version, 2);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/layouts/layout-1/clone`);
  assert.equal(call.init.method, "POST");
});

test("createFilm posts the catalog entry", async () => {
  const film = {
    id: "film-1",
    title: "Metropolis",
    runtimeMinutes: 153,
    rating: "TP",
  };
  const recorder = recordingFetch(() => jsonResponse(201, film));
  const client = makeClient(recorder);

  const created = unwrap(
    await client.createFilm({
      title: "Metropolis",
      runtimeMinutes: 153,
      rating: "TP",
    }),
  );

  assert.deepEqual(created, film);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/films`);
  assert.equal(call.init.method, "POST");
  assert.deepEqual(bodyOf(call.init), {
    title: "Metropolis",
    runtimeMinutes: 153,
    rating: "TP",
  });
});

test("createEvent posts the schedule and returns the draft event", async () => {
  const event = {
    id: "event-1",
    roomId: "room-1",
    filmId: "film-1",
    layoutVersionId: "layout-1",
    startsAt: "2024-05-01T18:00:00Z",
    status: "draft",
    priceCents: 0,
    priceDisplay: "0.00",
  };
  const recorder = recordingFetch(() => jsonResponse(201, event));
  const client = makeClient(recorder);

  const created = unwrap(
    await client.createEvent({
      roomId: "room-1",
      filmId: "film-1",
      layoutVersionId: "layout-1",
      startsAt: "2024-05-01T18:00:00Z",
    }),
  );

  assert.deepEqual(created, event);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/events`);
  assert.equal(call.init.method, "POST");
  assert.deepEqual(bodyOf(call.init), {
    roomId: "room-1",
    filmId: "film-1",
    layoutVersionId: "layout-1",
    startsAt: "2024-05-01T18:00:00Z",
  });
});

// ── The repertoire and the screening reads (M3.5 · T8 · S3, S4) ───────────────
//
// These tests hold the parser to its promise: the shapes the administration screen cannot
// print are refused, and the field it could not read is named. A dropped film would offer
// a repertoire smaller than the API answered; a screening without a price would put a
// number nobody served in front of an operator who is about to publish it.

const FILM = {
  id: "film-1",
  title: "The Quiet Cartographer",
  runtimeMinutes: 104,
  rating: "PG",
};

const SCREENING = {
  id: "5f1b7c1e-0000-4000-8000-000000000001",
  roomId: "room-1",
  filmId: "film-1",
  layoutVersionId: "layout-1",
  startsAt: "2024-05-01T18:00:00Z",
  status: "on_sale",
  priceCents: 1250,
  priceDisplay: "12.50",
  filmTitle: "The Quiet Cartographer",
  roomName: "Sala 2",
  cinemaName: "Cine Central",
};

test("listFilms reads the repertoire in the order the API answered it", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      films: [FILM, { ...FILM, id: "film-2", title: "Zulu" }],
    }),
  );
  const client = makeClient(recorder);

  const films = unwrap(await client.listFilms());

  assert.deepEqual(films, [FILM, { ...FILM, id: "film-2", title: "Zulu" }]);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/films`);
  assert.equal(call.init.method, "GET");
  assert.equal(headerValue(call.init, "Authorization"), `Bearer ${TOKEN}`);
});

test("listFilms refuses a film it cannot name instead of offering it", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, { films: [{ ...FILM, title: "" }] }),
  );
  const client = makeClient(recorder);

  const problem = unwrapError(await client.listFilms());

  assert.equal(problem.code, "unexpected_response");
  assert.equal(problem.detail, "the film film-1 carries no title");
});

test("listEvents reads screenings with their names and price", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, { events: [SCREENING] }),
  );
  const client = makeClient(recorder);

  const events = unwrap(await client.listEvents());

  assert.deepEqual(events, [SCREENING]);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/events`);
  assert.equal(call.init.method, "GET");
});

test("getEvent reads one named screening", async () => {
  const recorder = recordingFetch(() => jsonResponse(200, SCREENING));
  const client = makeClient(recorder);

  const event = unwrap(await client.getEvent(SCREENING.id));

  assert.deepEqual(event, SCREENING);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/events/${SCREENING.id}`);
  assert.equal(call.init.method, "GET");
});

// Every field the list prints: an entry the screen cannot print refuses the whole read,
// because dropping it would hide a screening the API offered.
test("a screening read refuses an entry it cannot print, naming the field", async () => {
  for (const [field, detail] of [
    ["filmTitle", `the screening ${SCREENING.id} carries no film title`],
    ["roomName", `the screening ${SCREENING.id} carries no room name`],
    ["cinemaName", `the screening ${SCREENING.id} carries no cinema name`],
    ["priceDisplay", `the screening ${SCREENING.id} carries no price text`],
    ["priceCents", `the screening ${SCREENING.id} carries no price in minor units`],
  ] as const) {
    const recorder = recordingFetch(() =>
      jsonResponse(200, { events: [SCREENING, { ...SCREENING, [field]: undefined }] }),
    );
    const client = makeClient(recorder);

    const problem = unwrapError(await client.listEvents());

    assert.equal(problem.code, "unexpected_response");
    assert.equal(problem.detail, detail);
  }
});

test("a screening read refuses a start time this screen cannot read", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, { events: [{ ...SCREENING, startsAt: "next tuesday" }] }),
  );
  const client = makeClient(recorder);

  const problem = unwrapError(await client.listEvents());

  assert.equal(
    problem.detail,
    `the screening ${SCREENING.id} carries a start time this screen cannot read`,
  );
});

test("updateEvent PATCHes exactly the fields it was given", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      ...SCREENING,
      priceCents: 1400,
      priceDisplay: "14.00",
      status: "on_sale",
    }),
  );
  const client = makeClient(recorder);

  const updated = unwrap(
    await client.updateEvent(SCREENING.id, {
      priceCents: 1400,
      status: "on_sale",
    }),
  );

  assert.equal(updated.priceCents, 1400);
  assert.equal(updated.priceDisplay, "14.00");
  assert.equal(updated.status, "on_sale");
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/events/${SCREENING.id}`);
  assert.equal(call.init.method, "PATCH");
  assert.deepEqual(bodyOf(call.init), { priceCents: 1400, status: "on_sale" });
});

test("a status-only update carries no price at all", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, { ...SCREENING, status: "cancelled" }),
  );
  const client = makeClient(recorder);

  unwrap(await client.updateEvent(SCREENING.id, { status: "cancelled" }));

  const [call] = recorder.calls;
  // The body is the request: a price that was not sent is not written, so the assertion
  // is on the JSON the API receives and not on the object this client was handed.
  assert.equal(call.init.body, JSON.stringify({ status: "cancelled" }));
});

test("updateEvent refuses an answer that carries no price text", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, { ...SCREENING, priceDisplay: undefined }),
  );
  const client = makeClient(recorder);

  const problem = unwrapError(
    await client.updateEvent(SCREENING.id, { status: "cancelled" }),
  );

  assert.equal(
    problem.detail,
    `the screening ${SCREENING.id} carries no price text`,
  );
});

test("a blank screening identifier never reaches the API", async () => {
  const recorder = recordingFetch(() => jsonResponse(200, SCREENING));
  const client = makeClient(recorder);

  const problem = unwrapError(await client.getEvent("  "));

  assert.equal(problem.code, "invalid_input");
  assert.equal(problem.detail, "eventId: must not be empty");
  assert.deepEqual(recorder.calls, []);
});

test("an update the API refuses keeps its field-naming detail", async () => {
  const detail = "priceCents: must be zero or greater";
  const recorder = recordingFetch(() =>
    jsonResponse(400, {
      type: "https://cinema.local/problems/invalid-input",
      title: "Invalid input",
      status: 400,
      code: "invalid_input",
      detail,
    }),
  );
  const client = makeClient(recorder);

  const problem = unwrapError(
    await client.updateEvent(SCREENING.id, { priceCents: -1 }),
  );

  assert.equal(problem.code, "invalid_input");
  // Printed, not paraphrased: the client hands the API's wording to the form unchanged.
  assert.equal(problem.detail, detail);
});

test("listEventSeats reads the materialized seats of one event", async () => {
  const seats = {
    eventId: "event-1",
    seats: [
      { id: 1, layoutSeatId: "seat-1", state: "available" },
      { id: 2, layoutSeatId: "seat-2", state: "available" },
    ],
  };
  const recorder = recordingFetch(() => jsonResponse(200, seats));
  const client = makeClient(recorder);

  const listed = unwrap(await client.listEventSeats("event-1"));

  assert.deepEqual(listed, seats);
  const [call] = recorder.calls;
  assert.equal(call.url, `${BASE_URL}/api/v1/admin/events/event-1/seats`);
  assert.equal(call.init.method, "GET");
});

// Each RFC 9457 problem code the API can return must become a typed result.
const PROBLEM_CASES = [
  {
    name: "invalid_input",
    status: 400,
    detail: "seat.number: must be unique within its row",
  },
  {
    name: "invalid_request",
    status: 400,
    detail: "request body must be valid JSON",
  },
  {
    name: "not_found",
    status: 404,
    detail: "the requested resource does not exist",
  },
  {
    name: "conflict",
    status: 409,
    detail: "layout layout-1 is already published",
  },
  {
    name: "internal_error",
    status: 500,
    detail: "the request could not be completed",
  },
] as const;

for (const problemCase of PROBLEM_CASES) {
  test(`a ${problemCase.name} problem becomes a typed failure`, async () => {
    const recorder = recordingFetch(() =>
      jsonResponse(problemCase.status, {
        type: `https://cinema.local/problems/${problemCase.name.replace("_", "-")}`,
        title: problemCase.name,
        status: problemCase.status,
        code: problemCase.name,
        detail: problemCase.detail,
      }),
    );
    const client = makeClient(recorder);

    const problem = unwrapError(await client.publishLayout("layout-1"));

    assert.equal(problem.code, problemCase.name);
    assert.equal(problem.status, problemCase.status);
    assert.equal(problem.detail, problemCase.detail);
    assert.equal(
      problem.type,
      `https://cinema.local/problems/${problemCase.name.replace("_", "-")}`,
    );
  });
}

test("an unreachable API becomes a typed network failure, never a throw", async () => {
  const client = createAdminClient({
    baseUrl: BASE_URL,
    token: TOKEN,
    fetch: () => Promise.reject(new Error(`connect failed for ${TOKEN}`)),
  });

  const problem = unwrapError(await client.getLayout("layout-1"));

  assert.equal(problem.code, "network_error");
  assert.equal(problem.status, 0);
  assert.equal(problem.detail.includes(TOKEN), false);
});

test("a non-problem error body becomes a typed unexpected response", async () => {
  const recorder = recordingFetch(
    () => new Response("<html>bad gateway</html>", { status: 502 }),
  );
  const client = makeClient(recorder);

  const problem = unwrapError(await client.getLayout("layout-1"));

  assert.equal(problem.code, "unexpected_response");
  assert.equal(problem.status, 502);
});

test("a success body that is not JSON becomes a typed unexpected response", async () => {
  const recorder = recordingFetch(
    () => new Response("not json", { status: 200 }),
  );
  const client = makeClient(recorder);

  const problem = unwrapError(await client.createLayout({ roomId: "room-1" }));

  assert.equal(problem.code, "unexpected_response");
  assert.equal(problem.status, 200);
});

test("the bearer token never appears in a result or in console output", async () => {
  const originalConsole = {
    log: console.log,
    info: console.info,
    warn: console.warn,
    error: console.error,
    debug: console.debug,
  };
  const logged: unknown[][] = [];
  const capture = (...args: unknown[]) => {
    logged.push(args);
  };
  console.log = capture;
  console.info = capture;
  console.warn = capture;
  console.error = capture;
  console.debug = capture;

  try {
    const recorder = recordingFetch(() =>
      jsonResponse(404, {
        type: "https://cinema.local/problems/not-found",
        title: "Not found",
        status: 404,
        code: "not_found",
        detail: "the requested resource does not exist",
      }),
    );
    const client = makeClient(recorder);

    const success = await client.createLayout({ roomId: "room-1" });
    const failure = await client.getLayout("layout-1");

    assert.equal(JSON.stringify(success).includes(TOKEN), false);
    assert.equal(JSON.stringify(failure).includes(TOKEN), false);
    assert.deepEqual(logged, []);
  } finally {
    console.log = originalConsole.log;
    console.info = originalConsole.info;
    console.warn = originalConsole.warn;
    console.error = originalConsole.error;
    console.debug = originalConsole.debug;
  }
});
