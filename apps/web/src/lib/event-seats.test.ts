// Materialized event seats, prepared for display.
//
// The event seat API exposes one materialized seat per enabled layout seat and
// nothing about rows, so the view is a stable ordered list plus a seat count.
// These tests work through the public module surface only and assert exactly
// what the React view renders.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { AdminEventSeats } from "./admin-api.ts";
import { summarizeEventSeats } from "./event-seats.ts";

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
    ["seat-a1 · available", "seat-b1 · held", "seat-c1 · available"],
  );
});

test("the event seats view states the materialized seat count", () => {
  const summary = summarizeEventSeats(EVENT_SEATS);

  assert.equal(summary.total, 3);
  assert.equal(summary.countLabel, "3 materialized seat(s)");
  assert.equal(summary.seats.length, summary.total);
});

test("the event seats view counts seats per state in first-seen order", () => {
  const summary = summarizeEventSeats(EVENT_SEATS);

  assert.deepEqual(summary.states, [
    { state: "available", count: 2 },
    { state: "held", count: 1 },
  ]);
});

test("an event with no seats renders an empty list and a zero count", () => {
  const summary = summarizeEventSeats({ eventId: "event-2", seats: [] });

  assert.equal(summary.total, 0);
  assert.equal(summary.countLabel, "0 materialized seat(s)");
  assert.deepEqual(summary.seats, []);
  assert.deepEqual(summary.states, []);
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
