// The receipt of the Marquesina (visual-redesign · T5 · S1, S2, S8).
//
// Two screens print the same money in the same shape — the seat map's running summary and
// the checkout's order summary — so the shape and the wording are a rule here rather than a
// paragraph of JSX repeated twice. What a test can pin, and what a reviewer cannot see in a
// screenshot, is three promises:
//
//   - the amount is *presented*, never computed: the module prints the number it is given
//     (the API's own quote, or a price the caller already multiplied) and never multiplies a
//     seat count by a price itself (S8);
//   - a served decimal is re-punctuated for Spanish and nothing else: a value that is not a
//     plain decimal is printed exactly as it arrived, because a number this module invented
//     would be a number the box office never said;
//   - the deadline line carries its own state in words, so the countdown is never announced
//     by colour alone.
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  BOOKING_FEE_CENTS,
  describeHoldDeadline,
  FEE_LINE_LABEL,
  formatAmount,
  HOLD_DEADLINE_EXPIRED_LABEL,
  HOLD_DEADLINE_LABEL,
  NO_SEATS_LINE_LABEL,
  presentAmount,
  SEATS_LINE_LABEL,
  seatReceipt,
} from "./receipt.ts";

test("an amount is printed the way a Spanish till prints it", () => {
  assert.equal(formatAmount(1250), "12,50 €");
  assert.equal(formatAmount(0), "0,00 €");
  assert.equal(formatAmount(5), "0,05 €");
  assert.equal(formatAmount(2500), "25,00 €");
  assert.equal(formatAmount(100000), "1000,00 €");
});

test("an amount that is not money fails closed instead of printing a fraction", () => {
  for (const notMoney of [-100, Number.NaN, Number.POSITIVE_INFINITY, 12.5]) {
    assert.equal(formatAmount(notMoney), "0,00 €", `${notMoney} is not an amount`);
  }
});

test("a served decimal is re-punctuated, never re-valued", () => {
  assert.equal(presentAmount("12.50"), "12,50 €");
  assert.equal(presentAmount("25,00"), "25,00 €");
  assert.equal(presentAmount(" 9 "), "9,00 €");
  assert.equal(presentAmount("1200"), "1200,00 €");
  assert.equal(presentAmount("12.5"), "12,50 €");
});

test("a served value that is not a decimal is printed exactly as it arrived", () => {
  // Nothing here rounds, guesses or repairs: the API's text is what the buyer is owed.
  for (const served of ["12.5.6", "gratis", "12,50 € y pendiente", "-3.00"]) {
    assert.equal(presentAmount(served), served);
  }
  assert.equal(presentAmount("   "), "");
});

test("the receipt prints the seats, their amount, the fee and the total", () => {
  const receipt = seatReceipt({
    seatLabels: ["A3", "A4"],
    count: 2,
    amountCents: 2500,
    amountDisplay: null,
  });

  assert.deepEqual(
    receipt.lines.map((line) => [line.label, line.value]),
    [
      [SEATS_LINE_LABEL, "A3, A4"],
      ["2 butacas", "25,00 €"],
      [FEE_LINE_LABEL, "0,00 €"],
    ],
  );
  assert.equal(receipt.total, "25,00 €");
});

test("the total is the amount the caller brought, and the fee explains it", () => {
  // The one arithmetic rule of this module: the lines must add up to its own total. The fee
  // is zero in this version, so the total is the amount — never amount plus a fee it invented.
  const receipt = seatReceipt({
    seatLabels: ["B1"],
    count: 1,
    amountCents: 999,
    amountDisplay: null,
  });

  assert.equal(receipt.total, formatAmount(999));
  assert.equal(BOOKING_FEE_CENTS, 0);
  assert.equal(receipt.lines.at(-1)?.value, formatAmount(BOOKING_FEE_CENTS));
});

test("the API's own quote wins over a number this page could compute", () => {
  const receipt = seatReceipt({
    seatLabels: ["A1", "A2"],
    count: 2,
    // A stale price and a fresh quote disagree on purpose: the quote is what is owed.
    amountCents: 2000,
    amountDisplay: "19.98",
  });

  assert.equal(receipt.total, "19,98 €");
  assert.equal(receipt.lines[1].value, "19,98 €");
});

test("one seat is singular and two are not", () => {
  const single = seatReceipt({
    seatLabels: ["A1"],
    count: 1,
    amountCents: 1250,
    amountDisplay: null,
  });
  const two = seatReceipt({
    seatLabels: ["A1", "A2"],
    count: 2,
    amountCents: 2500,
    amountDisplay: null,
  });

  assert.equal(single.lines[1].label, "1 butaca");
  assert.equal(two.lines[1].label, "2 butacas");
});

test("an empty selection says so instead of printing a lone total", () => {
  const receipt = seatReceipt({
    seatLabels: [],
    count: 0,
    amountCents: 0,
    amountDisplay: null,
  });

  assert.deepEqual(
    receipt.lines.map((line) => [line.label, line.value]),
    [[NO_SEATS_LINE_LABEL, "0,00 €"]],
  );
  assert.equal(receipt.total, "0,00 €");
});

test("a receipt with no seat label keeps the money and drops the empty row", () => {
  const receipt = seatReceipt({
    seatLabels: [],
    count: 2,
    amountCents: 2500,
    amountDisplay: null,
  });

  assert.deepEqual(
    receipt.lines.map((line) => line.label),
    ["2 butacas", FEE_LINE_LABEL],
  );
});

test("the deadline line counts down while the hold is live", () => {
  assert.deepEqual(
    describeHoldDeadline({ displayLabel: "8:34", expired: false }),
    { label: HOLD_DEADLINE_LABEL, value: "8:34" },
  );
});

test("an expired hold says so in words, not only in the tone of the clock", () => {
  const deadline = describeHoldDeadline({ displayLabel: "Hold expired", expired: true });

  assert.deepEqual(deadline, { label: HOLD_DEADLINE_EXPIRED_LABEL, value: "0:00" });
});

test("no countdown at all prints no deadline line", () => {
  assert.equal(describeHoldDeadline(null), null);
});
