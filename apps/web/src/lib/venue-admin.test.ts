// Venue administration orchestration contract.
//
// These tests work through the public module surface only: the payloads it
// builds from form text, the calls it makes against the administrative client,
// the route it revalidates after a successful write, and the render-ready
// summaries it produces from the listed cinemas, rooms, and layout versions.
// Dependencies are injected, so no network and no Next.js request scope is
// needed.
import assert from "node:assert/strict";
import { test } from "node:test";
import type {
  AdminCinema,
  AdminCinemaList,
  AdminClient,
  AdminLayout,
  AdminLayoutVersionList,
  AdminProblem,
  AdminResult,
  AdminRoom,
  AdminRoomList,
  LayoutStatus,
} from "./admin-api.ts";
import type {
  CinemaFormValues,
  LayoutFormValues,
  RoomFormValues,
  VenueDeps,
} from "./venue-admin.ts";
import {
  ADMIN_VENUES_PATH,
  cinemaInput,
  createCinema,
  createLayout,
  createRoom,
  layoutInput,
  layoutStatusWord,
  readCinemas,
  readLayouts,
  readRooms,
  roomInput,
  publishedLayoutOptions,
  summarizeCinemas,
  summarizeLayouts,
  summarizeRooms,
} from "./venue-admin.ts";

const CINEMA: AdminCinema = {
  id: "cinema-1",
  name: "Central",
  timezone: "Europe/Madrid",
  address: "Gran Via 1",
};

const ROOM: AdminRoom = { id: "room-1", cinemaId: "cinema-1", name: "Sala 1" };

const LAYOUT: AdminLayout = {
  id: "layout-1",
  roomId: "room-1",
  version: 1,
  status: "draft",
  rows: [],
};

/** Records every operation the module runs, without any network access. */
interface Recorder {
  deps: VenueDeps;
  operations: string[];
  revalidated: string[];
}

/**
 * Builds dependencies whose client answers every call with the same result.
 *
 * The operation name is recorded from the public client method that was called,
 * so a test can prove which read or write the module delegated.
 */
function recorderFor<T>(result: AdminResult<T>): Recorder {
  const operations: string[] = [];
  const revalidated: string[] = [];
  const client = new Proxy({} as AdminClient, {
    get: (_target, property: string) => () => {
      operations.push(property);
      return Promise.resolve(result);
    },
  });
  return {
    operations,
    revalidated,
    deps: {
      call: (operation) => {
        const previous = operations.length;
        const pending = operation(client);
        if (operations.length === previous) {
          operations.push("no-call");
        }
        return pending;
      },
      revalidate: (path) => {
        revalidated.push(path);
      },
    },
  };
}

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

const CINEMA_VALUES: CinemaFormValues = {
  name: "  Central  ",
  timezone: " Europe/Madrid ",
  address: "  Gran Via 1  ",
};

const ROOM_VALUES: RoomFormValues = { cinemaId: " cinema-1 ", name: " Sala 1 " };

const LAYOUT_VALUES: LayoutFormValues = { roomId: " room-1 " };

test("cinemaInput trims the form text and keeps the address optional", () => {
  assert.deepEqual(unwrap(cinemaInput(CINEMA_VALUES)), {
    name: "Central",
    timezone: "Europe/Madrid",
    address: "Gran Via 1",
  });
  assert.deepEqual(
    unwrap(cinemaInput({ ...CINEMA_VALUES, address: "   " })),
    { name: "Central", timezone: "Europe/Madrid", address: "" },
  );
});

test("cinemaInput rejects an empty name, an empty timezone, and over-long text", () => {
  const cases: Array<[CinemaFormValues, string]> = [
    [{ ...CINEMA_VALUES, name: "   " }, "name: must not be empty"],
    [{ ...CINEMA_VALUES, timezone: "" }, "timezone: must not be empty"],
    [
      { ...CINEMA_VALUES, name: "x".repeat(121) },
      "name: must be at most 120 characters",
    ],
    [
      { ...CINEMA_VALUES, timezone: "x".repeat(121) },
      "timezone: must be at most 120 characters",
    ],
    [
      { ...CINEMA_VALUES, address: "x".repeat(121) },
      "address: must be at most 120 characters",
    ],
  ];

  for (const [values, detail] of cases) {
    const problem = unwrapError(cinemaInput(values));
    assert.equal(problem.code, "invalid_input");
    assert.equal(problem.detail, detail);
  }
});

test("roomInput requires the selected cinema and trims the room name", () => {
  assert.deepEqual(unwrap(roomInput(ROOM_VALUES)), {
    cinemaId: "cinema-1",
    name: "Sala 1",
  });

  assert.equal(
    unwrapError(roomInput({ ...ROOM_VALUES, cinemaId: " " })).detail,
    "cinemaId: must not be empty",
  );
  assert.equal(
    unwrapError(roomInput({ ...ROOM_VALUES, name: "" })).detail,
    "name: must not be empty",
  );
  assert.equal(
    unwrapError(roomInput({ ...ROOM_VALUES, name: "x".repeat(121) })).detail,
    "name: must be at most 120 characters",
  );
});

test("layoutInput requires the selected room", () => {
  assert.deepEqual(unwrap(layoutInput(LAYOUT_VALUES)), { roomId: "room-1" });
  assert.equal(
    unwrapError(layoutInput({ roomId: "  " })).detail,
    "roomId: must not be empty",
  );
});

test("createCinema delegates the payload and revalidates the venues page", async () => {
  const recorder = recorderFor({ ok: true as const, value: CINEMA });

  const created = unwrap(await createCinema(recorder.deps, CINEMA_VALUES));

  assert.deepEqual(created, CINEMA);
  assert.deepEqual(recorder.operations, ["createCinema"]);
  assert.deepEqual(recorder.revalidated, [ADMIN_VENUES_PATH]);
});

test("createRoom delegates the selected cinema and revalidates the venues page", async () => {
  const recorder = recorderFor({ ok: true as const, value: ROOM });

  const created = unwrap(await createRoom(recorder.deps, ROOM_VALUES));

  assert.deepEqual(created, ROOM);
  assert.deepEqual(recorder.operations, ["createRoom"]);
  assert.deepEqual(recorder.revalidated, [ADMIN_VENUES_PATH]);
});

test("createLayout delegates the selected room and revalidates the venues page", async () => {
  const recorder = recorderFor({ ok: true as const, value: LAYOUT });

  const created = unwrap(await createLayout(recorder.deps, LAYOUT_VALUES));

  assert.deepEqual(created, LAYOUT);
  assert.deepEqual(recorder.operations, ["createLayout"]);
  assert.deepEqual(recorder.revalidated, [ADMIN_VENUES_PATH]);
});

test("invalid form text never reaches the client and revalidates nothing", async () => {
  const recorder = recorderFor({ ok: true as const, value: CINEMA });

  const problem = unwrapError(
    await createCinema(recorder.deps, { ...CINEMA_VALUES, name: "" }),
  );

  assert.equal(problem.code, "invalid_input");
  assert.deepEqual(recorder.operations, []);
  assert.deepEqual(recorder.revalidated, []);
});

test("a failed write returns the typed problem and revalidates nothing", async () => {
  const conflict: AdminProblem = {
    type: "https://cinema.local/problems/conflict",
    title: "Conflict",
    status: 409,
    code: "conflict",
    detail: "conflict: duplicate key value violates unique constraint",
  };
  const recorder = recorderFor({ ok: false as const, error: conflict });

  const problem = unwrapError(await createRoom(recorder.deps, ROOM_VALUES));

  assert.deepEqual(problem, conflict);
  assert.deepEqual(recorder.operations, ["createRoom"]);
  assert.deepEqual(recorder.revalidated, []);
});

test("the read functions delegate and never revalidate", async () => {
  const cinemas: AdminCinemaList = { cinemas: [CINEMA] };
  const rooms: AdminRoomList = { cinemaId: "cinema-1", rooms: [ROOM] };
  const layouts: AdminLayoutVersionList = {
    roomId: "room-1",
    layouts: [{ id: "layout-1", version: 1, status: "published" }],
  };

  const cinemaRead = recorderFor({ ok: true as const, value: cinemas });
  const roomRead = recorderFor({ ok: true as const, value: rooms });
  const layoutRead = recorderFor({ ok: true as const, value: layouts });

  assert.deepEqual(unwrap(await readCinemas(cinemaRead.deps)), cinemas);
  assert.deepEqual(unwrap(await readRooms(roomRead.deps, " cinema-1 ")), rooms);
  assert.deepEqual(unwrap(await readLayouts(layoutRead.deps, " room-1 ")), layouts);

  assert.deepEqual(cinemaRead.operations, ["listCinemas"]);
  assert.deepEqual(roomRead.operations, ["listRooms"]);
  assert.deepEqual(layoutRead.operations, ["listLayouts"]);
  assert.deepEqual(cinemaRead.revalidated, []);
  assert.deepEqual(roomRead.revalidated, []);
  assert.deepEqual(layoutRead.revalidated, []);
});

test("a read of a missing venue stays a typed not_found failure", async () => {
  const notFound: AdminProblem = {
    type: "https://cinema.local/problems/not-found",
    title: "Not found",
    status: 404,
    code: "not_found",
    detail: "the requested resource does not exist",
  };
  const recorder = recorderFor({ ok: false as const, error: notFound });

  const problem = unwrapError(await readRooms(recorder.deps, "cinema-9"));

  assert.deepEqual(problem, notFound);
  assert.deepEqual(recorder.operations, ["listRooms"]);
});

test("a read without a selection is rejected locally instead of hitting the API", async () => {
  const recorder = recorderFor({ ok: true as const, value: { rooms: [] } });

  const problem = unwrapError(await readRooms(recorder.deps, "   "));

  assert.equal(problem.code, "invalid_input");
  assert.equal(problem.detail, "cinemaId: must not be empty");
  assert.deepEqual(recorder.operations, []);
});

test("summarizeCinemas states each cinema's name, timezone, and address", () => {
  const rows = summarizeCinemas({
    cinemas: [CINEMA, { ...CINEMA, id: "cinema-2", name: "Norte", address: "" }],
  });

  assert.deepEqual(rows, [
    {
      id: "cinema-1",
      name: "Central",
      timezone: "Europe/Madrid",
      address: "Gran Via 1",
      label: "Central · Europe/Madrid · Gran Via 1",
    },
    {
      id: "cinema-2",
      name: "Norte",
      timezone: "Europe/Madrid",
      address: "",
      label: "Norte · Europe/Madrid",
    },
  ]);
});

test("summarizeRooms keeps the listed order and states each room's name", () => {
  const rows = summarizeRooms({
    cinemaId: "cinema-1",
    rooms: [ROOM, { ...ROOM, id: "room-2", name: "Sala 2" }],
  });

  assert.deepEqual(
    rows.map((row) => row.id),
    ["room-1", "room-2"],
  );
  assert.deepEqual(
    rows.map((row) => row.label),
    ["Sala 1", "Sala 2"],
  );
});

test("summarizeLayouts states every version with its status", () => {
  const rows = summarizeLayouts({
    roomId: "room-1",
    layouts: [
      { id: "layout-1", version: 1, status: "published" },
      { id: "layout-2", version: 2, status: "draft" },
    ],
  });

  assert.deepEqual(rows, [
    {
      id: "layout-1",
      version: 1,
      status: "published",
      statusLabel: "publicada",
      label: "v1 · publicada",
    },
    {
      id: "layout-2",
      version: 2,
      status: "draft",
      statusLabel: "borrador",
      label: "v2 · borrador",
    },
  ]);
});

test("a layout version's status is a word a reader understands, and only the word moves", () => {
  // The stored value is what the API serves and what this screen sends back; the label is
  // what a person reads on the badge and in the scheduling select.
  assert.equal(layoutStatusWord("published"), "publicada");
  assert.equal(layoutStatusWord("draft"), "borrador");
  // A value the union did not anticipate is printed verbatim, never mapped to a silence.
  assert.equal(layoutStatusWord("archived" as LayoutStatus), "archived");
});

test("publishedLayoutOptions offers only the versions a screening may use", () => {
  // A draft layout version is the API's conflict, so the scheduling select cannot offer
  // one: the form would be inviting a request the API refuses.
  const rows = summarizeLayouts({
    roomId: "room-1",
    layouts: [
      { id: "layout-1", version: 1, status: "published" },
      { id: "layout-2", version: 2, status: "draft" },
      { id: "layout-3", version: 3, status: "published" },
    ],
  });

  assert.deepEqual(
    publishedLayoutOptions(rows).map((row) => row.id),
    ["layout-1", "layout-3"],
  );
  assert.deepEqual(publishedLayoutOptions([]), []);
});

test("empty lists summarize to empty arrays, never to undefined", () => {
  assert.deepEqual(summarizeCinemas({ cinemas: [] }), []);
  assert.deepEqual(summarizeRooms({ cinemaId: "cinema-1", rooms: [] }), []);
  assert.deepEqual(summarizeLayouts({ roomId: "room-1", layouts: [] }), []);
});
