// Materialized event seats, prepared for display.
//
// The event seat API exposes one materialized seat per enabled layout seat and
// nothing about rows, so the view is a stable ordered list plus a seat count.
// These tests work through the public module surface only and assert exactly
// what the React view renders.
//
// Since the Marquesina redesign the view is the box office's ledger for one session: the
// counts it states, the share of the room that is held or sold, and each seat's state in a
// word instead of the API's identifier.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { AdminEventSeats } from "./admin-api.ts";
import { eventSeatStateWord, summarizeEventSeats } from "./event-seats.ts";

/** Seats deliberately arrive out of order, as a database might return them. */
const EVENT_SEATS: AdminEventSeats = {
  eventId: "event-1",
  seats: [
    { id: 3, layoutSeatId: "seat-c1", state: "available" },
    { id: 1, layoutSeatId: "seat-a1", state: "available" },
    { id: 2, layoutSeatId: "seat-b1", state: "held" },
  ],
};

test("the event seats view receives every materialized seat in a stable order", () => {
  const summary = summarizeEventSeats(EVENT_SEATS);

  assert.equal(summary.eventId, "event-1");
  assert.deepEqual(
    summary.seats.map((seat) => seat.id),
    [1, 2, 3],
  );
  assert.deepEqual(
    summary.seats.map((seat) => seat.layoutSeatId),
    ["seat-a1", "seat-b1", "seat-c1"],
  );
  assert.deepEqual(
    summary.seats.map((seat) => seat.label),
    ["seat-a1 · libre", "seat-b1 · reservada", "seat-c1 · libre"],
  );
});

test("the event seats view states the materialized seat count", () => {
  const summary = summarizeEventSeats(EVENT_SEATS);

  assert.equal(summary.total, 3);
  assert.equal(summary.countLabel, "3 butacas materializadas");
  assert.equal(summary.seats.length, summary.total);
});

test("a single materialized seat is counted in the singular", () => {
  const summary = summarizeEventSeats({
    eventId: "event-3",
    seats: [{ id: 1, layoutSeatId: "seat-a1", state: "available" }],
  });

  assert.equal(summary.countLabel, "1 butaca materializada");
});

test("the event seats view counts seats per state in first-seen order", () => {
  const summary = summarizeEventSeats(EVENT_SEATS);

  assert.deepEqual(summary.states, [
    { state: "available", word: "libre", count: 2 },
    { state: "held", word: "reservada", count: 1 },
  ]);
});

test("the ledger's named counts are the states the column actually stores", () => {
  const summary = summarizeEventSeats({
    eventId: "event-4",
    seats: [
      { id: 1, layoutSeatId: "seat-a1", state: "sold" },
      { id: 2, layoutSeatId: "seat-a2", state: "sold" },
      { id: 3, layoutSeatId: "seat-a3", state: "held" },
      { id: 4, layoutSeatId: "seat-a4", state: "available" },
      { id: 5, layoutSeatId: "seat-a5", state: "unavailable" },
    ],
  });

  assert.deepEqual(summary.counts, {
    total: 5,
    available: 1,
    held: 1,
    sold: 2,
    unavailable: 1,
    // Held and sold are the two ways a seat leaves the sale; `available` and `unavailable`
    // are not taken by anybody.
    taken: 3,
    occupancyPercent: 60,
  });
});

test("the occupancy share is a whole percent of the materialized seats", () => {
  // One of three is 33% (rounded), and the two extremes are exact.
  assert.equal(
    summarizeEventSeats({
      eventId: "event-5",
      seats: [
        { id: 1, layoutSeatId: "seat-a1", state: "sold" },
        { id: 2, layoutSeatId: "seat-a2", state: "available" },
        { id: 3, layoutSeatId: "seat-a3", state: "available" },
      ],
    }).counts.occupancyPercent,
    33,
  );
  assert.equal(summarizeEventSeats(EVENT_SEATS).counts.occupancyPercent, 33);
  assert.equal(
    summarizeEventSeats({
      eventId: "event-6",
      seats: [{ id: 1, layoutSeatId: "seat-a1", state: "sold" }],
    }).counts.occupancyPercent,
    100,
  );
});

test("a state this build does not interpret is printed verbatim, never translated", () => {
  assert.equal(eventSeatStateWord("available"), "libre");
  assert.equal(eventSeatStateWord("held"), "reservada");
  assert.equal(eventSeatStateWord("sold"), "vendida");
  assert.equal(eventSeatStateWord("unavailable"), "no disponible");
  // A state the column grew and this build has not learned is still a word, not a blank.
  assert.equal(eventSeatStateWord("refunded"), "refunded");
  assert.equal(eventSeatStateWord(""), "");
});

test("an event with no seats renders an empty list and a zero count", () => {
  const summary = summarizeEventSeats({ eventId: "event-2", seats: [] });

  assert.equal(summary.total, 0);
  assert.equal(summary.countLabel, "0 butacas materializadas");
  assert.deepEqual(summary.seats, []);
  assert.deepEqual(summary.states, []);
  // A room with nothing materialized has no share to paint, and 0% is the honest one:
  // an occupancy bar must never be drawn from a division by zero.
  assert.equal(summary.counts.occupancyPercent, 0);
  assert.equal(summary.counts.taken, 0);
});

test("the summary never mutates the seats it receives", () => {
  const value: AdminEventSeats = {
    eventId: "event-1",
    seats: [
      { id: 2, layoutSeatId: "seat-b1", state: "available" },
      { id: 1, layoutSeatId: "seat-a1", state: "available" },
    ],
  };

  summarizeEventSeats(value);

  assert.deepEqual(
    value.seats.map((seat) => seat.id),
    [2, 1],
  );
});
