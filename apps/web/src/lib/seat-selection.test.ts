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
import { MAX_HOLD_SEATS } from "./public-api.ts";
import type { SeatMapLayout } from "./seat-selection.ts";
import {
  applyHoldFailure,
  applyHoldSuccess,
  beginHoldAttempt,
  buildSeatMap,
  dropUnavailableSeats,
  describeCountdown,
  describeHoldFailure,
  describeSaleState,
  describeScreeningFailure,
  formatCents,
  formatCountdown,
  idleAttempt,
  initialSeatFocus,
  layoutFromAvailability,
  mapAvailability,
  priceSummary,
  remainingMilliseconds,
  resolveSeatKey,
  seatStateWord,
  selectionSummary,
  SEAT_STATE_LEGEND,
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

// ── The screening the buyer clicked (M3.5 · T5 · S3, S8) ─────────────────────
//
// The seat page is reached from the catalogue, so it owes the buyer the screening
// they chose: the film, the venue, the room, the venue's own hour and the price of
// a seat. Two rules decide what it may say about the sale. `onSale` is the API's
// answer and is never re-derived from a clock here, and the reason a sale stopped
// comes from the stored status rather than from the page's imagination.

test("a screening that is on sale is stated plainly, with no reason to give", () => {
  const state = describeSaleState({ status: "on_sale", onSale: true });

  assert.equal(state.onSale, true);
  assert.equal(state.reason, null);
  assert.equal(state.headline, null);
  assert.equal(state.message, null);
  assert.equal(state.badge, "On sale");
});

test("a cancelled screening says it was cancelled, never that it does not exist", () => {
  const state = describeSaleState({ status: "cancelled", onSale: false });

  assert.equal(state.onSale, false);
  assert.equal(state.reason, "cancelled");
  assert.equal(state.badge, "Cancelled");
  assert.match(state.headline!, /cancel/i);
  assert.match(state.message!, /cancel/i);
  // The 404 branch owns "does not exist"; a cancellation must not borrow its words.
  assert.doesNotMatch(
    `${state.headline} ${state.message}`,
    /not found|does not exist|could not be loaded/i,
  );
});

test("a draft screening is not on sale yet, and says which reason that is", () => {
  const state = describeSaleState({ status: "draft", onSale: false });

  assert.equal(state.reason, "draft");
  assert.equal(state.badge, "Not on sale yet");
  assert.match(state.message!, /draft|not been published|not published/i);
  assert.doesNotMatch(`${state.headline} ${state.message}`, /cancel/i);
});

test("a screening whose start has passed is no longer on sale, because it started", () => {
  // The API decides `onSale` as `status = 'on_sale'` and still ahead of the database
  // clock, so a screening that is on sale by status and not by the flag has started.
  const state = describeSaleState({ status: "on_sale", onSale: false });

  assert.equal(state.reason, "started");
  assert.equal(state.badge, "No longer on sale");
  assert.match(state.headline!, /started/i);
  assert.doesNotMatch(`${state.headline} ${state.message}`, /cancel/i);
});

test("a status this page does not interpret is printed instead of explained", () => {
  // "completed", "published" and anything a later milestone adds are not guessed
  // at: the page states that no sale is open and prints the served status, which is
  // the one fact it has.
  for (const status of ["completed", "published", "archived"]) {
    const state = describeSaleState({ status, onSale: false });

    assert.equal(state.reason, "closed", status);
    assert.equal(state.badge, "No longer on sale");
    assert.match(
      state.message!,
      new RegExp(status),
      "the served status is printed verbatim",
    );
  }
});

test("every reason a sale stopped reads differently from the others", () => {
  const reasons = ["cancelled", "draft", "on_sale", "completed"].map((status) => {
    const state = describeSaleState({ status, onSale: false });
    assert.notEqual(state.reason, null, status);
    return `${state.badge} ${state.headline} ${state.message}`;
  });

  assert.equal(
    new Set(reasons).size,
    reasons.length,
    "four reasons, four sentences: a buyer can tell them apart",
  );
});

test("a screening that does not exist is a 404, and asking again changes nothing", () => {
  const view = describeScreeningFailure(
    problem({
      status: 404,
      code: "not_found",
      title: "Not found",
      detail: "the requested screening does not exist",
    }),
  );

  assert.equal(view.kind, "missing");
  assert.equal(view.retryable, false);
  assert.match(view.headline, /does not exist/i);
  assert.doesNotMatch(
    `${view.headline} ${view.message}`,
    /temporar|could not be loaded/i,
    "a screening that does not exist is not described as an outage",
  );
});

test("an unavailable screening is retryable, and is never rendered as an empty page", () => {
  for (const failure of [
    problem({
      status: 503,
      code: "service_unavailable",
      title: "Service unavailable",
      detail: "the screening is temporarily unavailable; retry the request",
    }),
    problem({ status: 0, code: "network_error", title: "Network error" }),
    problem({ status: 500, code: "unexpected_response" }),
  ]) {
    const view = describeScreeningFailure(failure);

    assert.equal(view.kind, "temporary", `status ${failure.status}`);
    assert.equal(view.retryable, true, "an outage is worth asking about again");
    assert.match(view.message, /again|retry/i);
    assert.doesNotMatch(
      `${view.headline} ${view.message}`,
      /does not exist/i,
      "an outage is not a missing screening",
    );
  }
});

test("a screening read the API refused is a refusal, not an outage", () => {
  const view = describeScreeningFailure(
    problem({
      status: 400,
      code: "invalid_input",
      title: "Invalid input",
      detail: "this page needs an event id",
    }),
  );

  assert.equal(view.kind, "refused");
  assert.equal(view.retryable, false);
  assert.match(view.message, /invalid_input/, "the API's own code is kept");
});

// ── The running amount (M3.5 · T5 · S3, S8) ──────────────────────────────────
//
// The page shows what the selected seats cost at the price the screening was read
// with. Money is never derived from a count of availability: the amount is the
// screening's own price per seat times the seats in the selection, and the copy
// says plainly that the order, not this sentence, is what gets paid.

test("formatCents renders minor units the way the API renders a price", () => {
  assert.equal(formatCents(1250), "12.50");
  assert.equal(formatCents(0), "0.00");
  assert.equal(formatCents(5), "0.05");
  assert.equal(formatCents(3750), "37.50");
  assert.equal(formatCents(100000), "1000.00");
  // Fail closed: an amount that is not a whole, non-negative number is not money.
  assert.equal(formatCents(-100), "0.00");
  assert.equal(formatCents(Number.NaN), "0.00");
  assert.equal(formatCents(Number.POSITIVE_INFINITY), "0.00");
  assert.equal(formatCents(12.5), "0.00");
});

test("the running amount is the screening's price times the seats selected", () => {
  const seats = [11, 21, 31];
  const summary = selectionSummary(seats, LAYOUT);

  const priced = priceSummary(summary, { priceCents: 1250, priceDisplay: "12.50" });

  assert.equal(priced.count, 3);
  assert.equal(priced.amountCents, 3750);
  assert.equal(priced.amountLabel, "37.50");
  assert.equal(
    priced.amountCents,
    summary.count * 1250,
    "the price per seat is the screening's, never a count of availability",
  );
  assert.match(priced.lineLabel, /3 seats/);
  assert.match(priced.lineLabel, /12\.50/);
  assert.match(priced.lineLabel, /37\.50/);
  assert.equal(priced.overLimit, false);
  assert.equal(priced.limitNote, null);
});

test("one seat is priced as one seat, in the singular", () => {
  const priced = priceSummary(selectionSummary([11], LAYOUT), {
    priceCents: 900,
    priceDisplay: "9.00",
  });

  assert.equal(priced.count, 1);
  assert.equal(priced.amountCents, 900);
  assert.equal(priced.amountLabel, "9.00");
  assert.match(priced.lineLabel, /1 seat/);
});

test("an empty selection states nothing rather than a cost", () => {
  const priced = priceSummary(selectionSummary(EMPTY_SELECTION, LAYOUT), {
    priceCents: 1250,
    priceDisplay: "12.50",
  });

  assert.equal(priced.count, 0);
  assert.equal(priced.amountCents, 0);
  assert.equal(priced.amountLabel, "0.00");
  assert.match(priced.lineLabel, /no seats selected/i);
  assert.match(priced.lineLabel, /12\.50 per seat/);
});

test("the running amount never promises the amount the payment will take", () => {
  const priced = priceSummary(selectionSummary([11, 21], LAYOUT), {
    priceCents: 1250,
    priceDisplay: "12.50",
  });

  // The order the API creates is authoritative; this sentence is a summary, and it
  // says so. Nothing here may read as a promise about the charge.
  assert.match(priced.note, /price per seat|per seat/i);
  assert.match(priced.note, /order|payment|charged/i);
  assert.doesNotMatch(
    priced.note,
    /guarantee|final|exactly what you will pay|we will charge you/i,
    "the summary is not the charge",
  );
});

test("a selection over the hold limit is named as over it", () => {
  const longRowLayout: SeatMapLayout = {
    geometryKnown: true,
    rows: [
      {
        label: "A",
        seats: Array.from({ length: MAX_HOLD_SEATS + 1 }, (_, index) => ({
          eventSeatId: 100 + index,
          label: `A${index + 1}`,
          kind: "standard" as const,
        })),
      },
    ],
  };
  const everySeat = longRowLayout.rows[0].seats.map((seat) => seat.eventSeatId);
  assert.equal(everySeat.length, MAX_HOLD_SEATS + 1);

  const priced = priceSummary(selectionSummary(everySeat, longRowLayout), {
    priceCents: 500,
    priceDisplay: "5.00",
  });

  assert.equal(priced.count, MAX_HOLD_SEATS + 1);
  assert.equal(priced.overLimit, true);
  assert.match(priced.limitNote!, new RegExp(String(MAX_HOLD_SEATS)));
  // The amount is still the honest product; only the hold refuses the selection.
  assert.equal(priced.amountCents, (MAX_HOLD_SEATS + 1) * 500);
});

// ── Which number the page prints (M3.5 · T5 · S8) ────────────────────────────
//
// A screening read carries a count of available seats and the seat read carries the
// map. The map is the later answer and the only one this page sells, so the page
// prints the map's numbers; when the two disagree it says so instead of pretending
// the count was still true.

test("the page counts the seats the map read, not the screening's number", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);
  const map = mapAvailability(view, { seatCount: 40, availableSeats: 12 });

  assert.equal(map.seatCount, view.seatCount);
  assert.equal(map.availableSeats, view.availableCount);
  assert.equal(map.label, "8 of 10 seats available");
  assert.equal(map.matchesScreening, false);
  assert.match(map.note!, /seat map|map/i, "the disagreement is attributed to the map");
  assert.match(map.note!, /8|10/, "the note names the numbers the page printed");
});

test("two reads that agree need no note, and still print the map's numbers", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);
  const map = mapAvailability(view, {
    seatCount: view.seatCount,
    availableSeats: view.availableCount,
  });

  assert.equal(map.matchesScreening, true);
  assert.equal(map.note, null);
  assert.equal(map.label, "8 of 10 seats available");
});

test("a sold-out map says so in words", () => {
  const soldOut = AVAILABILITY.map((seat) => ({ ...seat, held: true }));
  const view = buildSeatMap(LAYOUT, soldOut);
  const map = mapAvailability(view, {
    seatCount: view.seatCount,
    availableSeats: 0,
  });

  assert.equal(map.availableSeats, 0);
  assert.equal(map.label, "0 of 10 seats available — sold out");
});

// ── The keyboard contract (M3.5 · T5 · S6) ───────────────────────────────────
//
// The map is movable without a mouse: arrows walk rows and seats, Home and End jump
// inside a row, and Space or Enter toggles the seat the focus is on. The focus never
// lands where the map cannot draw it, and it does reach seats that cannot be
// selected, because a keyboard user must be able to hear why a seat is unavailable.

test("the first tab stop is the first seat that can be selected", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);
  // A1 is free, A2 is held, so the entry point is A1 and not the held seat.
  assert.equal(initialSeatFocus(view), 11);

  const nothingFree = buildSeatMap(
    LAYOUT,
    AVAILABILITY.map((seat) => ({ ...seat, held: true })),
  );
  assert.equal(
    initialSeatFocus(nothingFree),
    11,
    "with nothing selectable the map still has an entry point to read",
  );

  assert.equal(initialSeatFocus(buildSeatMap(layoutFromAvailability([]), [])), null);
});

test("arrow keys move inside a row and stop at its ends", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);

  assert.equal(resolveSeatKey(view, 11, "ArrowRight").focusSeatId, 12);
  assert.equal(resolveSeatKey(view, 13, "ArrowRight").focusSeatId, 13, "row A ends at A3");
  assert.equal(resolveSeatKey(view, 11, "ArrowLeft").focusSeatId, 11, "row A starts at A1");
  assert.equal(resolveSeatKey(view, 22, "ArrowLeft").focusSeatId, 21);
});

test("arrow keys move between rows, clamped to the shorter row", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);

  assert.equal(resolveSeatKey(view, 11, "ArrowDown").focusSeatId, 21, "A1 -> B1");
  assert.equal(resolveSeatKey(view, 22, "ArrowDown").focusSeatId, 32, "B2 -> C2");
  assert.equal(
    resolveSeatKey(view, 25, "ArrowDown").focusSeatId,
    32,
    "B5 has no C5, so the focus lands on the last seat of row C",
  );
  assert.equal(resolveSeatKey(view, 31, "ArrowDown").focusSeatId, 31, "the last row has no floor");
  assert.equal(resolveSeatKey(view, 11, "ArrowUp").focusSeatId, 11, "the first row has no ceiling");
  assert.equal(resolveSeatKey(view, 31, "ArrowUp").focusSeatId, 21);
});

test("Home and End move to the ends of the focused row", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);

  assert.equal(resolveSeatKey(view, 23, "Home").focusSeatId, 21);
  assert.equal(resolveSeatKey(view, 21, "End").focusSeatId, 25);
  assert.equal(resolveSeatKey(view, 32, "Home").focusSeatId, 31);
});

test("space and enter ask to toggle the focused seat and leave the focus where it is", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);

  for (const key of [" ", "Enter"]) {
    const result = resolveSeatKey(view, 11, key);
    assert.equal(result.toggle, true, `${key} toggles`);
    assert.equal(result.focusSeatId, 11, `${key} does not move the focus`);
  }

  const moved = resolveSeatKey(view, 11, "ArrowRight");
  assert.equal(moved.toggle, false, "moving is not selecting");
});

test("arrow keys reach a seat that cannot be selected, so its state can be heard", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);

  // A2 is held. The focus still moves onto it: the announcement is the reason the
  // seat cannot be picked, and a focus that skipped it could not deliver that.
  const ontoHeld = resolveSeatKey(view, 11, "ArrowRight");
  assert.equal(ontoHeld.focusSeatId, 12);
  assert.equal(view.rows[0].cells[1].selectable, false);
  assert.deepEqual(
    toggleSeat(EMPTY_SELECTION, ontoHeld.focusSeatId!, "held"),
    EMPTY_SELECTION,
    "the toggle the focus asked for is refused by the selection rule",
  );
});

test("a key the map does not use changes nothing", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);

  for (const key of ["Tab", "PageDown", "a", "Escape"]) {
    assert.deepEqual(resolveSeatKey(view, 22, key), {
      focusSeatId: 22,
      toggle: false,
    });
  }
});

test("an empty map has no focus and no movement", () => {
  const view = buildSeatMap(layoutFromAvailability([]), []);

  assert.equal(initialSeatFocus(view), null);
  assert.deepEqual(resolveSeatKey(view, null, "ArrowRight"), {
    focusSeatId: null,
    toggle: false,
  });
  assert.deepEqual(resolveSeatKey(view, null, " "), {
    focusSeatId: null,
    toggle: false,
  });
});

test("a key pressed with nothing focused enters the map", () => {
  const view = buildSeatMap(LAYOUT, AVAILABILITY);

  assert.equal(resolveSeatKey(view, null, "ArrowDown").focusSeatId, 11);
  assert.equal(resolveSeatKey(view, 999, "ArrowRight").focusSeatId, 11, "a seat not on the map");
});

// ── Every seat state, without colour (M3.5 · T5 · S6, S8) ────────────────────
//
// A seat the API does not report as available is not selectable, whatever the map
// would like to offer: `sold` is paid for, `held` belongs to a live hold, and
// `unavailable` (or a state this milestone does not interpret) is not sold here. A
// snapshot that carries no state at all keeps the older rule, where the `held` flag
// was the only statement the API made.

test("a sold seat is never offered for selection", () => {
  const withSold = [
    { id: 11, held: false, state: "available" },
    { id: 12, held: false, state: "sold" },
    { id: 13, held: false, state: "available" },
  ];
  const view = buildSeatMap(LAYOUT, withSold);

  assert.equal(view.rows[0].cells[1].status, "sold");
  assert.equal(view.rows[0].cells[1].selectable, false);
  assert.equal(view.soldCount, 1);
  assert.deepEqual(toggleSeat(EMPTY_SELECTION, 12, "sold"), EMPTY_SELECTION);
});

test("a seat the API calls unavailable is not offered, and neither is a state this map does not know", () => {
  const view = buildSeatMap(LAYOUT, [
    { id: 11, held: false, state: "available" },
    { id: 12, held: false, state: "unavailable" },
    { id: 13, held: false, state: "reserved_for_press" },
  ]);

  assert.equal(view.rows[0].cells[1].status, "blocked");
  assert.equal(view.rows[0].cells[2].status, "blocked", "an unknown state is never sold");
  assert.equal(view.rows[0].cells[1].selectable, false);
  assert.equal(view.rows[0].cells[2].selectable, false);
  assert.equal(view.blockedCount, 2);
  assert.equal(view.availableCount, 1);
});

test("a snapshot with no state at all keeps the held flag's answer", () => {
  // An older API serves availability without a per-seat state. The map keeps the
  // rule it had: the `held` flag decides, and a free seat is selectable.
  const view = buildSeatMap(LAYOUT, [
    { id: 11, held: false },
    { id: 12, held: true },
  ]);

  assert.equal(view.rows[0].cells[0].status, "available");
  assert.equal(view.rows[0].cells[0].selectable, true);
  assert.equal(view.rows[0].cells[1].status, "held");
  assert.equal(view.rows[0].cells[1].selectable, false);
});

test("a held flag beats a state that says available", () => {
  // The two fields can disagree inside one response; the live hold is the one that
  // decides, because selling a held seat is the failure that costs a buyer.
  const view = buildSeatMap(LAYOUT, [{ id: 11, held: true, state: "available" }]);

  assert.equal(view.rows[0].cells[0].status, "held");
  assert.equal(view.rows[0].cells[0].selectable, false);
});

test("the selection is still decorated by position, and a lost seat never renders", () => {
  const view = buildSeatMap(
    LAYOUT,
    [
      { id: 11, held: false, state: "available" },
      { id: 12, held: true, state: "held" },
    ],
    [11, 12],
  );

  assert.equal(view.rows[0].cells[0].status, "selected");
  assert.equal(view.rows[0].cells[1].status, "held", "held wins over stale client state");
});

test("each seat's accessible name is its row and number plus its state", () => {
  const projected = layoutFromAvailability([
    { id: 11, held: false, state: "available", rowLabel: "A", seatNumber: "1", kind: "standard" },
    { id: 12, held: false, state: "sold", rowLabel: "A", seatNumber: "2", kind: "standard" },
    { id: 13, held: false, state: "unavailable", rowLabel: "A", seatNumber: "3", kind: "standard" },
  ]);
  const view = buildSeatMap(
    projected,
    [
      { id: 11, held: false, state: "available" },
      { id: 12, held: false, state: "sold" },
      { id: 13, held: false, state: "unavailable" },
    ],
    [11],
  );

  assert.deepEqual(
    view.rows[0].cells.map((cell) => cell.description),
    [
      "Row A seat 1, selected",
      "Row A seat 2, sold",
      "Row A seat 3, not for sale",
    ],
  );

  const held = buildSeatMap(projected, [{ id: 11, held: true, state: "held" }]);
  assert.equal(held.rows[0].cells[0].description, "Row A seat 1, held by another guest");
});

test("the legend names every state with words, never with a tone alone", () => {
  const statuses = SEAT_STATE_LEGEND.map((entry) => entry.status);

  assert.deepEqual(statuses, [
    "available",
    "selected",
    "held",
    "sold",
    "blocked",
    "unknown",
  ]);
  for (const entry of SEAT_STATE_LEGEND) {
    assert.ok(entry.legend.trim() !== "", `${entry.status} has a legend word`);
    assert.ok(entry.legendDescription.trim() !== "", `${entry.status} is explained`);
    assert.ok(entry.spoken.trim() !== "", `${entry.status} can be announced`);
  }
  // The word a cell prints is the state's own, so the map reads without colour.
  assert.equal(seatStateWord("sold"), "sold");
  assert.equal(seatStateWord("blocked"), "n/a");
  assert.equal(seatStateWord("held"), "held");
});

test("a screening with no seats is not called sold out", () => {
  // Nothing sold and nothing to sell are different answers, and only the first one is
  // "sold out": an empty room would be a claim about demand the API never made.
  const view = buildSeatMap(layoutFromAvailability([]), []);
  const map = mapAvailability(view, { seatCount: 0, availableSeats: 0 });

  assert.equal(map.seatCount, 0);
  assert.equal(map.label, "No seats are sold for this screening");
  assert.doesNotMatch(map.label, /sold out/i);
  assert.equal(map.matchesScreening, true);
  assert.equal(map.note, null);
});
