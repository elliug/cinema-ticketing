// The board's two words (Marquesina).
//
// The catalogue prints how a session is going in words, and only then reinforces it with
// colour: "quedan 4", "agotada". This module is that decision on its own, so the rule can
// be tested without a browser, and so the row and the admin ledger cannot drift apart.
import test from "node:test";
import assert from "node:assert/strict";

import { FEW_SEATS, describeAvailability } from "./screening-display.ts";

test("a session with seats left prints how many", () => {
  assert.deepEqual(describeAvailability(34), { state: "on-sale", left: "quedan 34" });
  assert.deepEqual(describeAvailability(1_000), {
    state: "on-sale",
    left: "quedan 1000",
  });
});

test("a session at or below the threshold warns, and still prints the number", () => {
  assert.deepEqual(describeAvailability(FEW_SEATS), {
    state: "few",
    left: `quedan ${FEW_SEATS}`,
  });
  assert.deepEqual(describeAvailability(1), { state: "few", left: "quedan 1" });
});

test("the threshold is a boundary, not an estimate", () => {
  assert.equal(describeAvailability(FEW_SEATS + 1).state, "on-sale");
  assert.equal(describeAvailability(FEW_SEATS).state, "few");
});

test("a sold-out session says so in words, never as a zero", () => {
  assert.deepEqual(describeAvailability(0), { state: "sold-out", left: "agotada" });
});

test("a negative count is read as sold out, because nothing can be sold below zero", () => {
  assert.deepEqual(describeAvailability(-3), { state: "sold-out", left: "agotada" });
});
