// The door's answer, as the cashier reads it (M4.1 · T8 · S1, S3, S4, S5).
//
// One code, one action, one answer — and the answer cannot be misread. Four verdicts exist
// and each is asserted here as its own words: an admission that shows where the person is
// going, a second presentation that says when the ticket was first used and that this is not
// an admission, a code that is not a ticket of ours (in the API's words, never a friendlier
// substitute), and a ticket that cannot admit its holder with the reason named. A fifth
// answer exists for the case where the door could not ask at all, and it claims no verdict.
//
// The words are Castilian since the Marquesina redesign; the API's own refusal is still
// printed exactly as the API sent it, because that sentence is the box office's and not
// this screen's.
//
// The screen's promises that no type checker can see are pinned from its file text, as this
// repository has no DOM test runner: one labelled field and one action, the answer in a live
// region, and nothing at all kept in the browser.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import type { PublicProblem, PublicRedemption, PublicTicketSeat } from "../../../lib/public-api.ts";
import { describeRedemption } from "./door-outcome.ts";

const SOURCE_OF_FORM = readFileSync(new URL("./door-form.tsx", import.meta.url), "utf8");
const SOURCE_OF_PAGE = readFileSync(new URL("./page.tsx", import.meta.url), "utf8");
const SOURCE_OF_ACTION = readFileSync(new URL("./actions.ts", import.meta.url), "utf8");

function redemption(overrides: Partial<PublicRedemption> = {}): PublicRedemption {
  return {
    ticketId: "ticket-1",
    ticketStatus: "issued",
    eventId: "event-a",
    filmTitle: "The Long Take",
    roomName: "Room 1",
    cinemaName: "Cinema Centro",
    startsAt: "2026-05-04T18:30:00Z",
    startsAtLocal: "2026-05-04T20:30:00+02:00",
    seats: [{ eventSeatId: 11, rowLabel: "A", seatNumber: "1" }],
    redeemedAt: "2026-05-04T20:14:00Z",
    redeemedAtLocal: "2026-05-04T22:14:00+02:00",
    redeemedBy: "till-1",
    alreadyRedeemed: false,
    ...overrides,
  };
}

function problem(overrides: Partial<PublicProblem> = {}): PublicProblem {
  return {
    type: "https://cinema.local/problems/invalid-token",
    title: "Invalid token",
    status: 400,
    code: "invalid_token",
    detail: "the presented code is not a valid ticket",
    ...overrides,
  };
}

test("an admission says so and shows the seat and the screening", () => {
  const answer = describeRedemption({ ok: true, redemption: redemption() });

  assert.equal(answer.kind, "admitted");
  assert.equal(answer.admits, true);
  assert.match(answer.headline, /admitida/i);
  assert.deepEqual(answer.seatLabels, ["Fila A, butaca 1"]);
  assert.equal(answer.screening?.filmTitle, "The Long Take");
  // The venue's own hour, exactly as the API rendered it: reformatting it here would print
  // the browser's zone instead.
  assert.equal(answer.screening?.startsAtLocal, "2026-05-04T20:30:00+02:00");
  assert.equal(answer.screening?.startsAt, "2026-05-04T18:30:00Z");
  assert.equal(answer.screening?.venue, "Cinema Centro · Room 1");
  assert.equal(answer.usedAt, null);
  assert.equal(answer.retryable, false);
});

test("a seat is named the way the rest of the product names it to a person", () => {
  const labels = (seats: PublicTicketSeat[]) =>
    describeRedemption({ ok: true, redemption: redemption({ seats }) }).seatLabels;

  assert.deepEqual(labels([{ eventSeatId: 11, rowLabel: "A", seatNumber: "1" }]), [
    "Fila A, butaca 1",
  ]);
  // The pieces the API may leave blank, each answered without inventing the other:
  assert.deepEqual(labels([{ eventSeatId: 12, rowLabel: "", seatNumber: "7" }]), [
    "Butaca 7",
  ]);
  assert.deepEqual(labels([{ eventSeatId: 13, rowLabel: "B", seatNumber: "" }]), ["Fila B"]);
  assert.deepEqual(labels([{ eventSeatId: 14, rowLabel: "", seatNumber: "" }]), [
    "Butaca 14",
  ]);
  // Two seats are two sentences, in the order the API served them.
  assert.deepEqual(
    labels([
      { eventSeatId: 15, rowLabel: "A", seatNumber: "2" },
      { eventSeatId: 16, rowLabel: "A", seatNumber: "3" },
    ]),
    ["Fila A, butaca 2", "Fila A, butaca 3"],
  );
});

test("a ticket already used says when, and that this is not an admission", () => {
  const answer = describeRedemption({
    ok: true,
    redemption: redemption({ alreadyRedeemed: true }),
  });

  assert.equal(answer.kind, "already_used");
  assert.equal(answer.admits, false);
  assert.match(answer.headline, /ya se usó/i);
  // The first admission's hour, from the served instant, with the venue's own offset printed
  // beside it because that is the clock the admission route serves for it.
  assert.equal(answer.usedAt, "2026-05-04 22:14:00+02:00");
  assert.match(answer.message, /no es una admisión/i);
  assert.match(answer.message, /22:14/);
});

test("a code that is not ours is refused in the API's own sentence", () => {
  const answer = describeRedemption({ ok: false, problem: problem() });

  assert.equal(answer.kind, "not_ours");
  assert.equal(answer.admits, false);
  // The API's sentence, unreworded: the screen names the verdict and quotes the box office.
  assert.equal(answer.message, "the presented code is not a valid ticket");
  assert.match(answer.headline, /no es una entrada nuestra/i);
  assert.equal(answer.retryable, false);
});

test("a void ticket and a cancelled screening say which one it is", () => {
  const voided = describeRedemption({
    ok: false,
    problem: problem({
      status: 409,
      code: "voided",
      detail: "the ticket was voided, so it does not admit its holder",
    }),
  });
  assert.equal(voided.kind, "not_admissible");
  assert.equal(voided.admits, false);
  assert.match(voided.headline, /anulada/i);
  assert.equal(voided.message, "the ticket was voided, so it does not admit its holder");

  const cancelled = describeRedemption({
    ok: false,
    problem: problem({
      status: 409,
      code: "event_cancelled",
      detail: "the screening was cancelled, so the ticket does not admit its holder",
    }),
  });
  assert.equal(cancelled.kind, "not_admissible");
  assert.match(cancelled.headline, /cancelada/i);
  assert.notEqual(cancelled.headline, voided.headline);
});

test("a door that could not ask gives no verdict, only an invitation to ask again", () => {
  for (const unreachable of [
    problem({ status: 503, code: "service_unavailable", detail: "retry the request" }),
    problem({ status: 0, code: "network_error", detail: "unreachable" }),
  ]) {
    const answer = describeRedemption({ ok: false, problem: unreachable });
    assert.equal(answer.kind, "unavailable");
    assert.equal(answer.admits, false);
    assert.equal(answer.retryable, true);
    // No verdict, in either language: not a bad code, not a used one, not a refusal.
    assert.doesNotMatch(
      `${answer.headline} ${answer.message}`,
      /no se admite|ya se usó|inválid|falsificad/i,
    );
    assert.match(answer.headline, /no pudo comprobar/i);
  }
});

test("every verdict is its own word, so the answer does not rely on colour", () => {
  const seen = new Map<string, string>();
  for (const answer of [
    describeRedemption({ ok: true, redemption: redemption() }),
    describeRedemption({ ok: true, redemption: redemption({ alreadyRedeemed: true }) }),
    describeRedemption({ ok: false, problem: problem() }),
    describeRedemption({ ok: false, problem: problem({ status: 409, code: "voided" }) }),
    describeRedemption({
      ok: false,
      problem: problem({ status: 409, code: "event_cancelled" }),
    }),
  ]) {
    assert.equal(seen.has(answer.headline), false, `${answer.headline} is used twice`);
    seen.set(answer.headline, answer.kind);
  }
  assert.equal(seen.size, 5);
});

test("the screen has one labelled field, one action and a live region", () => {
  assert.match(SOURCE_OF_FORM, /<Label htmlFor="door-code"/);
  assert.match(SOURCE_OF_FORM, /id="door-code"/);
  // The answer is announced instead of hunted for, and politely: a door that interrupts a
  // screen reader mid-sentence for every code is a door nobody listens to.
  assert.match(SOURCE_OF_FORM, /aria-live="polite"/);
  assert.match(SOURCE_OF_FORM, /role="status"/);
  // One button, and it is the only submit: no second form and no second action.
  assert.equal(SOURCE_OF_FORM.match(/<Button/g)?.length, 1);
  assert.equal(SOURCE_OF_FORM.match(/<form/g)?.length, 1);
  // Nothing may keep the code: a door screen that stores codes is a copy of every ticket
  // that walked past it.
  for (const source of [SOURCE_OF_FORM, SOURCE_OF_PAGE, SOURCE_OF_ACTION]) {
    for (const storage of ["sessionStorage", "localStorage", "indexedDB", "document.cookie"]) {
      assert.equal(source.includes(storage), false, `the door screen must not use ${storage}`);
    }
  }
  // The code leaves the field once it has been answered, so it is not left on the till.
  assert.match(SOURCE_OF_FORM, /setCode\(""\)/);
});

test("the door asks the public redemption route, with no administrative token", () => {
  assert.match(SOURCE_OF_ACTION, /redeemTicket\(/);
  assert.equal(SOURCE_OF_ACTION.includes("ADMIN_TOKEN"), false);
  assert.equal(SOURCE_OF_ACTION.includes("Authorization"), false);
  // The route is not re-verified on the client: the server's answer is the only verdict.
  assert.equal(SOURCE_OF_FORM.includes("verifyTicket"), false);
});

test("the page is one screen with one h1, and it is never prerendered", () => {
  assert.match(SOURCE_OF_PAGE, /<PageHeader/);
  assert.equal(SOURCE_OF_PAGE.includes("<h1"), false, "the h1 belongs to PageHeader");
  assert.match(SOURCE_OF_PAGE, /export const dynamic = "force-dynamic"/);
  assert.match(SOURCE_OF_PAGE, /id="main-content"/);
});
