// Seat selection contract for the public event page (S9, S10, S11).
//
// Everything the seat page renders or decides is computed here first, so the
// tests can pin the rules without a browser, a timer, or a network:
//
//   - irregular geometry: rows hold different seat counts and no function pads
//     them into a rectangle;
//   - selection toggling and the running total;
//   - the countdown, which is derived from the server deadline and ends in an
//     explicit expired state;
//   - the mapping of each API failure to a calm, specific user-facing state.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { PublicProblem } from "./public-api.ts";
import type { SeatMapLayout } from "./seat-selection.ts";
import {
  applyHoldFailure,
  applyHoldSuccess,
  beginHoldAttempt,
  buildSeatMap,
  dropUnavailableSeats,
  describeCountdown,
  describeHoldFailure,
  formatCountdown,
  idleAttempt,
  layoutFromAvailability,
  remainingMilliseconds,
  selectionSummary,
  toggleSeat,
  EMPTY_SELECTION,
} from "./seat-selection.ts";

/** A deliberately irregular map: 3, 5 and 2 seats per row. */
const LAYOUT: SeatMapLayout = {
  geometryKnown: true,
  rows: [
    {
      label: "A",
      seats: [
        { eventSeatId: 11, label: "A1", kind: "standard" },
        { eventSeatId: 12, label: "A2", kind: "standard" },
        { eventSeatId: 13, label: "A3", kind: "accessible" },
      ],
    },
    {
      label: "B",
      seats: [
        { eventSeatId: 21, label: "B1", kind: "standard" },
        { eventSeatId: 22, label: "B2", kind: "companion" },
        { eventSeatId: 23, label: "B3", kind: "standard" },
        { eventSeatId: 24, label: "B4", kind: "standard" },
        { eventSeatId: 25, label: "B5", kind: "accessible" },
      ],
    },
    {
      label: "C",
      seats: [
        { eventSeatId: 31, label: "C1", kind: "standard" },
        { eventSeatId: 32, label: "C2", kind: "standard" },
      ],
    },
  ],
};

/** Availability snapshot: 12 is held, 32 is missing (unknown), rest are free. */
const AVAILABILITY = [
  { id: 11, held: false },
  { id: 12, held: true },
  { id: 13, held: false },
  { id: 21, held: false },
  { id: 22, held: false },
  { id: 23, held: false },
  { id: 24, held: false },
  { id: 25, held: false },
  { id: 31, held: false },
];

function cellLabels(
  view: ReturnType<typeof buildSeatMap>,
  rowIndex: number,
): string[] {
  return view.rows[rowIndex].cells.map((cell) => cell.label);
}

test("buildSeatMap keeps every row's own seat count instead of a rectangle", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);

  assert.equal(view.rows.length, 3);
  assert.deepEqual(
    view.rows.map((row) => row.seatCount),
    [3, 5, 2],
  );
  assert.deepEqual(
    view.rows.map((row) => row.label),
    ["A", "B", "C"],
  );
  // No padding cell is invented for the shorter rows.
  assert.equal(
    view.rows.reduce((total, row) => total + row.cells.length, 0),
    LAYOUT.rows.reduce((total, row) => total + row.seats.length, 0),
  );
  assert.deepEqual(cellLabels(view, 0), ["A1", "A2", "A3"]);
  assert.deepEqual(cellLabels(view, 1), ["B1", "B2", "B3", "B4", "B5"]);
  assert.deepEqual(cellLabels(view, 2), ["C1", "C2"]);
});

test("buildSeatMap shows each seat's label and kind", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);

  assert.deepEqual(
    view.rows[1].cells.map((cell) => cell.kind),
    ["standard", "companion", "standard", "standard", "accessible"],
  );
  assert.equal(view.rows[0].cells[2].kind, "accessible");
  assert.equal(view.rows[0].cells[2].label, "A3");
});

test("buildSeatMap resolves availability, holding and unknown seats", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);
  const statuses = view.rows.flatMap((row) =>
    row.cells.map((cell) => [cell.label, cell.status, cell.selectable]),
  );

  assert.deepEqual(statuses, [
    ["A1", "available", true],
    ["A2", "held", false],
    ["A3", "available", true],
    ["B1", "available", true],
    ["B2", "available", true],
    ["B3", "available", true],
    ["B4", "available", true],
    ["B5", "available", true],
    ["C1", "available", true],
    // Not in the snapshot: selectable seats are never guessed.
    ["C2", "unknown", false],
  ]);
  assert.equal(view.seatCount, 10);
  assert.equal(view.availableCount, 8);
  assert.equal(view.heldCount, 1);
  assert.equal(view.unknownCount, 1);
});

test("buildSeatMap marks selected seats and never mutates its inputs", () => {
  const layoutBefore = JSON.stringify(LAYOUT);
  const availabilityBefore = JSON.stringify(AVAILABILITY);

  const view = buildSeatMap(LAYOUT, AVAILABILITY, [11, 23]);

  assert.equal(JSON.stringify(LAYOUT), layoutBefore);
  assert.equal(JSON.stringify(AVAILABILITY), availabilityBefore);
  assert.equal(view.rows[0].cells[0].status, "selected");
  assert.equal(view.rows[0].cells[0].selectable, true);
  assert.equal(view.rows[1].cells[2].status, "selected");
  assert.equal(view.rows[0].cells[1].status, "held");

  // A held seat is never presented as selected, even if it was selected before
  // the refetch: occupancy wins over stale client state.
  const stale = buildSeatMap(LAYOUT, AVAILABILITY, [12]);
  assert.equal(stale.rows[0].cells[1].status, "held");
});

test("toggleSeat selects, deselects, and refuses unavailable seats", () => {
  const first = toggleSeat(EMPTY_SELECTION, 11, "available");
  assert.deepEqual(first, [11]);

  const second = toggleSeat(first, 21, "available");
  assert.deepEqual(second, [11, 21]);

  const removed = toggleSeat(second, 11, "selected");
  assert.deepEqual(removed, [21]);

  assert.deepEqual(toggleSeat(second, 21, "available"), [11], "toggles off");
  assert.deepEqual(toggleSeat(second, 12, "held"), second, "held is not selectable");
  assert.deepEqual(
    toggleSeat(second, 32, "unknown"),
    second,
    "unknown is not selectable",
  );
  assert.equal(EMPTY_SELECTION.length, 0, "the empty selection stays empty");
});

test("selectionSummary reports the running total without duplicates", () => {
  const empty = selectionSummary(EMPTY_SELECTION, LAYOUT);
  assert.equal(empty.count, 0);
  assert.equal(empty.totalLabel, "0 seats selected");
  assert.deepEqual(empty.labels, []);

  const single = selectionSummary([11], LAYOUT);
  assert.equal(single.count, 1);
  assert.equal(single.totalLabel, "1 seat selected");
  assert.deepEqual(single.labels, ["A1"]);

  const three = selectionSummary([11, 21, 31], LAYOUT);
  assert.equal(three.count, 3);
  assert.equal(three.totalLabel, "3 seats selected");
  assert.deepEqual(three.seatIds, [11, 21, 31]);
  assert.deepEqual(three.labels, ["A1", "B1", "C1"]);
});

test("formatCountdown renders minutes and seconds, never negative time", () => {
  assert.equal(formatCountdown(600_000), "10:00");
  assert.equal(formatCountdown(599_000), "9:59");
  assert.equal(formatCountdown(9_000), "0:09");
  assert.equal(formatCountdown(1_000), "0:01");
  assert.equal(formatCountdown(1), "0:00");
  assert.equal(formatCountdown(0), "0:00");
  assert.equal(formatCountdown(-1_000), "0:00");
});

const EXPIRES_AT = "2026-03-01T10:00:10.000Z";
const T0 = Date.parse("2026-03-01T10:00:00.000Z");

test("remainingMilliseconds is derived from the server deadline", () => {
  assert.equal(remainingMilliseconds(EXPIRES_AT, T0), 10_000);
  assert.equal(remainingMilliseconds(EXPIRES_AT, T0 + 9_000), 1_000);
  assert.equal(remainingMilliseconds(EXPIRES_AT, T0 + 10_000), 0);
  assert.equal(remainingMilliseconds(EXPIRES_AT, T0 + 60_000), 0);
  assert.equal(
    remainingMilliseconds("not-a-timestamp", T0),
    0,
    "an unreadable deadline is treated as elapsed",
  );
});

test("describeCountdown switches to the expired state at the deadline", () => {
  const running = describeCountdown(EXPIRES_AT, T0);
  assert.equal(running.expired, false);
  assert.equal(running.remainingMs, 10_000);
  assert.equal(running.label, "0:10");
  assert.equal(running.displayLabel, "0:10");
  assert.equal(running.notice, null);

  const lastSecond = describeCountdown(EXPIRES_AT, T0 + 9_000);
  assert.equal(lastSecond.expired, false);
  assert.equal(lastSecond.label, "0:01");

  // One millisecond before the deadline the hold is still live.
  const almost = describeCountdown(EXPIRES_AT, T0 + 9_999);
  assert.equal(almost.expired, false);
  assert.equal(almost.remainingMs, 1);

  const expired = describeCountdown(EXPIRES_AT, T0 + 10_000);
  assert.equal(expired.expired, true);
  assert.equal(expired.remainingMs, 0);
  assert.equal(expired.label, "0:00");
  assert.equal(expired.displayLabel, "Hold expired");
  assert.ok(expired.notice !== null);
  assert.match(expired.notice, /expired/i);
  assert.match(expired.notice, /no longer reserved/i);
});

function problem(overrides: Partial<PublicProblem>): PublicProblem {
  return {
    type: "about:blank",
    title: "Request failed",
    status: 500,
    code: "unexpected_response",
    detail: "no detail",
    ...overrides,
  };
}

test("a 409 seat_unavailable asks for a refetch and names no culprit", () => {
  const view = describeHoldFailure(
    problem({
      status: 409,
      code: "seat_unavailable",
      title: "Seat unavailable",
      detail: "at least one selected seat is already held by another reservation",
    }),
  );

  assert.equal(view.kind, "seat_unavailable");
  assert.equal(view.refetchAvailability, true);
  assert.equal(view.retryable, true);
  assert.equal(view.tone, "warning");
  assert.match(view.message, /no longer selected|removed/i);
  assert.doesNotMatch(view.message, /@|email|account/i);
});

test("a 400 hold_window_expired is an expired window, not a taken seat", () => {
  const view = describeHoldFailure(
    problem({
      status: 400,
      code: "hold_window_expired",
      title: "Hold window expired",
      detail: "the hold window closed before the seats could be claimed",
    }),
  );

  assert.equal(view.kind, "hold_window_expired");
  assert.equal(view.refetchAvailability, false);
  assert.equal(view.retryable, true);
  assert.match(view.headline + view.message, /window/i);
  assert.doesNotMatch(
    view.message,
    /another guest|someone else|taken/i,
    "an expired window is never reported as a seat taken by someone else",
  );
  assert.notEqual(
    view.headline + view.message,
    describeHoldFailure(problem({ status: 409, code: "seat_unavailable" }))
      .headline,
  );
});

test("a 503 or a network failure is temporary and retryable", () => {
  const unavailable = describeHoldFailure(
    problem({
      status: 503,
      code: "coordination_unavailable",
      title: "Service unavailable",
      detail: "the reservation service is temporarily unavailable; retry the request",
    }),
  );
  assert.equal(unavailable.kind, "temporary");
  assert.equal(unavailable.retryable, true);
  assert.equal(unavailable.refetchAvailability, false);
  assert.match(unavailable.message, /try again/i);

  const offline = describeHoldFailure(
    problem({ status: 0, code: "network_error", title: "Network error" }),
  );
  assert.equal(offline.kind, "temporary");
  assert.equal(offline.retryable, true);
});

test("caller errors are rejected without inviting a blind retry", () => {
  for (const code of [
    "idempotency_key_reused",
    "duplicate_seat",
    "invalid_input",
    "not_found",
  ] as const) {
    const view = describeHoldFailure(
      problem({ status: 400, code, detail: `server said: ${code}` }),
    );
    assert.equal(view.kind, "rejected", code);
    assert.equal(view.retryable, false, code);
    assert.equal(view.refetchAvailability, false, code);
    assert.match(view.message, new RegExp(code), "the API detail is kept");
  }

  const unknown = describeHoldFailure(problem({ status: 500 }));
  assert.equal(unknown.kind, "unknown");
  assert.equal(unknown.retryable, false);
});

test("dropUnavailableSeats removes only the seats lost since the snapshot", () => {
  const refetched = AVAILABILITY.map((seat) =>
    // A1 and B3 were taken by another guest in the meantime.
    seat.id === 11 || seat.id === 23 ? { id: seat.id, held: true } : seat,
  );

  const result = dropUnavailableSeats([11, 21, 23], refetched, LAYOUT);

  assert.deepEqual(result.selection, [21]);
  assert.deepEqual(result.lostEventSeatIds, [11, 23]);
  assert.deepEqual(result.lostLabels, ["A1", "B3"]);

  const untouched = dropUnavailableSeats([21], refetched, LAYOUT);
  assert.deepEqual(untouched.selection, [21]);
  assert.deepEqual(untouched.lostLabels, []);
});

test("a seat missing from the refetched snapshot is dropped too", () => {
  const refetched = AVAILABILITY.filter((seat) => seat.id !== 31);
  const result = dropUnavailableSeats([31], refetched, LAYOUT);

  assert.deepEqual(result.selection, []);
  assert.deepEqual(result.lostLabels, ["C1"]);
});

test("layoutFromAvailability builds the real rows when the API serves the projection", () => {
  // The irregular layout the API projects: a two-seat row, a one-seat row, and a
  // last row whose position skips a number. Seat ids are deliberately not in
  // row order, so nothing may re-sort the map by event seat id.
  const projected = [
    { id: 41, held: false, state: "available", rowLabel: "A", seatNumber: "1", kind: "standard" },
    { id: 42, held: true, state: "available", rowLabel: "A", seatNumber: "2", kind: "accessible" },
    { id: 11, held: false, state: "available", rowLabel: "B", seatNumber: "1", kind: "companion" },
    { id: 91, held: false, state: "available", rowLabel: "D", seatNumber: "1", kind: "standard" },
  ];

  const layout = layoutFromAvailability(projected);

  assert.equal(layout.geometryKnown, true, "a projected response is real geometry");
  assert.deepEqual(
    layout.rows.map((row) => row.label),
    ["A", "B", "D"],
    "rows keep the row-position order the server served",
  );
  assert.deepEqual(
    layout.rows.map((row) => row.seats.length),
    [2, 1, 1],
    "every row keeps its own seat count: nothing is padded into a rectangle",
  );
  assert.deepEqual(
    layout.rows.map((row) => row.seats.map((seat) => seat.eventSeatId)),
    [[41, 42], [11], [91]],
    "seats keep the seat-position order of their own row",
  );
  assert.deepEqual(
    layout.rows[0].seats.map((seat) => seat.kind),
    ["standard", "accessible"],
    "the projected kind is preserved",
  );
  assert.equal(layout.rows[1].seats[0].kind, "companion");
  assert.equal(layout.rows[1].seats[0].label, "B1");
  assert.equal(layout.rows[0].seats[1].label, "A2");
  assert.equal(layout.rows[2].seats[0].label, "D1");

  // The map renders it as it is: per-row counts, the held seat inert.
  const view = buildSeatMap(
    layout,
    projected.map((seat) => ({ id: seat.id, held: seat.held })),
  );
  assert.deepEqual(
    view.rows.map((row) => row.seatCount),
    [2, 1, 1],
  );
  assert.equal(view.rows[0].cells[1].status, "held");
  assert.equal(view.rows[0].cells[1].selectable, false);
  assert.equal(view.rows[1].cells[0].status, "available");
  assert.equal(view.geometryKnown, true);
});

test("layoutFromAvailability keeps the fallback when the projection is absent", () => {
  // An older server serves availability only. One honest group, unknown kinds,
  // and geometryKnown false so the page can say the projection is missing.
  const layout = layoutFromAvailability([
    { id: 32, layoutSeatId: "layout-seat-c2", held: false },
    { id: 11, layoutSeatId: "layout-seat-a1", held: true },
  ]);
  assert.equal(layout.geometryKnown, false);
  assert.deepEqual(
    layout.rows.map((row) => row.label),
    ["Seats"],
  );
  assert.deepEqual(
    layout.rows[0].seats.map((seat) => seat.label),
    ["layout-seat-a1", "layout-seat-c2"],
  );
});

test("layoutFromAvailability never projects a half-labelled response", () => {
  // A response that carries a row label for only some seats is not a projection
  // the map can trust, so it degrades to the fallback instead of inventing a row
  // for the seats that have none.
  const partial = [
    { id: 11, held: false, rowLabel: "A", seatNumber: "1", kind: "standard" },
    { id: 12, held: false, layoutSeatId: "layout-seat-a2" },
  ];

  const layout = layoutFromAvailability(partial);

  assert.equal(layout.geometryKnown, false);
  assert.deepEqual(
    layout.rows.map((row) => row.label),
    ["Seats"],
  );
  assert.deepEqual(
    layout.rows[0].seats.map((seat) => seat.kind),
    ["unknown", "unknown"],
  );
});

test("layoutFromAvailability keeps an unknown seat kind honest", () => {
  const layout = layoutFromAvailability([
    { id: 11, held: false, rowLabel: "A", seatNumber: "1", kind: "recliner" },
    { id: 12, held: false, rowLabel: "A", seatNumber: "2" },
  ]);

  assert.equal(layout.geometryKnown, true);
  assert.deepEqual(
    layout.rows[0].seats.map((seat) => seat.kind),
    ["unknown", "unknown"],
    "a kind the domain does not know is never echoed as if it were supported",
  );
  assert.deepEqual(
    layout.rows[0].seats.map((seat) => seat.label),
    ["A1", "A2"],
  );
});

test("layoutFromAvailability keeps the fallback for an event with no seats", () => {
  const layout = layoutFromAvailability([]);
  assert.equal(layout.geometryKnown, false, "an empty response proves no geometry");
  assert.deepEqual(
    layout.rows.map((row) => row.label),
    ["Seats"],
    "today's empty-list shape is preserved",
  );
});

test("layoutFromAvailability degrades honestly when the API has no geometry", () => {
  const layout = layoutFromAvailability([
    { id: 32, layoutSeatId: "layout-seat-c2", held: false },
    { id: 11, layoutSeatId: "layout-seat-a1", held: true },
  ]);

  assert.equal(layout.geometryKnown, false);
  assert.equal(layout.rows.length, 1);
  assert.deepEqual(
    layout.rows[0].seats.map((seat) => seat.label),
    ["layout-seat-a1", "layout-seat-c2"],
    "seats are ordered by event seat id, not by the response order",
  );
  assert.deepEqual(
    layout.rows[0].seats.map((seat) => seat.kind),
    ["unknown", "unknown"],
    "a kind the API does not send is never invented",
  );
  const view = buildSeatMap(layout, [
    { id: 11, held: true },
    { id: 32, held: false },
  ]);
  assert.equal(view.rows[0].cells[0].status, "held");
  assert.equal(view.rows[0].cells[1].status, "available");
});

test("beginHoldAttempt always mints a fresh Idempotency-Key", () => {
  let counter = 0;
  const keyFactory = () => `key-${++counter}`;

  const idle = idleAttempt();
  assert.equal(idle.status, "idle");
  assert.equal(idle.idempotencyKey, null);

  const first = beginHoldAttempt(idle, keyFactory);
  assert.equal(first.status, "submitting");
  assert.equal(first.idempotencyKey, "key-1");
  assert.equal(first.failure, null);

  const second = beginHoldAttempt(first, keyFactory);
  assert.equal(second.idempotencyKey, "key-2");
  assert.notEqual(second.idempotencyKey, first.idempotencyKey);

  const third = beginHoldAttempt(
    applyHoldFailure(second, problem({ status: 503, code: "coordination_unavailable" })),
    keyFactory,
  );
  assert.equal(third.idempotencyKey, "key-3");
  assert.equal(third.status, "submitting");
  assert.equal(third.failure, null, "a retry clears the previous failure");
});

test("applyHoldSuccess stores the hold and its server deadline", () => {
  const attempt = applyHoldSuccess(
    beginHoldAttempt(idleAttempt(), () => "key-1"),
    {
      id: "hold-1",
      eventId: "event-a",
      seatIds: [11, 21],
      expiresAt: EXPIRES_AT,
    },
  );

  assert.equal(attempt.status, "held");
  assert.equal(attempt.hold?.expiresAt, EXPIRES_AT);
  assert.equal(attempt.failure, null);
  assert.equal(describeCountdown(attempt.hold!.expiresAt, T0).label, "0:10");
});

test("applyHoldFailure keeps the calm view and stops the attempt", () => {
  const attempt = applyHoldFailure(
    beginHoldAttempt(idleAttempt(), () => "key-1"),
    problem({ status: 409, code: "seat_unavailable" }),
  );

  assert.equal(attempt.status, "failed");
  assert.equal(attempt.hold, null);
  assert.equal(attempt.failure?.kind, "seat_unavailable");
  assert.equal(attempt.failure?.refetchAvailability, true);
});
