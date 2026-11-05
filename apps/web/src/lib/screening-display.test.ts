// The board's two words (Marquesina).
//
// The catalogue prints how a session is going in words, and only then reinforces it with
// colour: "quedan 4", "agotada". This module is that decision on its own, so the rule can
// be tested without a browser, and so the row and the admin ledger cannot drift apart.
import test from "node:test";
import assert from "node:assert/strict";

import { FEW_SEATS, describeAvailability, describeSaleStatus, localHour } from "./screening-display.ts";

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

test("the hour a board prints is read out of the venue's own timestamp", () => {
  assert.equal(localHour("2030-01-15T19:00:00+01:00"), "19:00");
  assert.equal(localHour("2030-01-15T21:30:00+01:00"), "21:30");
});

test("a value that is not a full timestamp is never turned into an invented hour", () => {
  assert.equal(localHour("19:00"), "19:00");
  assert.equal(localHour(""), "");
  assert.equal(localHour("mañana"), "mañana");
});

// ── The box office's status word (Marquesina) ───────────────────────────────
//
// The ledger and the seat plan print the state of a session as a word, and the word has to
// be the same one the board prints: the box office may not invent a second vocabulary for a
// number the board already decided. `describeSaleStatus` is that one wording, built on
// `describeAvailability`, so the two can never disagree about the threshold or the count.

test("the sale status is the board's own two words, plus its plain on-sale case", () => {
  assert.deepEqual(describeSaleStatus(FEW_SEATS + 24), {
    state: "on-sale",
    word: "En venta",
  });
  assert.deepEqual(describeSaleStatus(FEW_SEATS), { state: "few", word: "Quedan 10" });
  assert.deepEqual(describeSaleStatus(4), { state: "few", word: "Quedan 4" });
  assert.deepEqual(describeSaleStatus(0), { state: "sold-out", word: "Agotada" });
});

test("the sale status keeps the board's boundary and its reading of a negative count", () => {
  assert.equal(describeSaleStatus(FEW_SEATS + 1).word, "En venta");
  assert.equal(describeSaleStatus(FEW_SEATS).word, "Quedan 10");
  // Nothing can be sold below zero, and "Quedan -3" is not a sentence this screen prints.
  assert.deepEqual(describeSaleStatus(-3), { state: "sold-out", word: "Agotada" });
});

test("the sale status says the same state as the board, never a second opinion", () => {
  for (const seats of [-1, 0, 1, FEW_SEATS, FEW_SEATS + 1, 40]) {
    assert.equal(describeSaleStatus(seats).state, describeAvailability(seats).state);
  }
});
