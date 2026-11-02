// /tickets with an account, as answers turned into states and words (identity-login T7 · S5).
//
// The screen has one rule and two framings, and both are worth testing here rather than inside
// JSX:
//
//   - **which list this is** is decided by whether this browser holds a session cookie. Without
//     one, the screen is the tab's own note and says why an account would be better and that one
//     is optional; with one, the screen is the box office's answer for the account, and no
//     sentence may still claim the tab keeps the ids. A list nobody can name honestly is the
//     whole failure this task exists to remove;
//   - **what one read means** has four answers that must never collapse into each other. An
//     account that bought nothing is an empty list and a real answer; a refused session is a
//     session that ended, said plainly and without the opaque refusal's detail; and a store that
//     could not answer is our failure, retryable, and never an empty list — "you bought nothing"
//     is a claim a broken read has not earned.
//
// The component that draws these states has no runner in this application (there is no React
// renderer in `node --test`), so what is pinned here is the rule and the words, not the markup.
// The words are the part that must not drift: none of them may claim an address was verified, and
// none may carry a credential.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
  describeAccountTickets,
  describeTicketsScreen,
} from "./account-tickets.ts";
import type { PublicProblem, PublicTicket } from "./public-api.ts";

/** One ticket, as `GET /me/tickets` serves it: the ticket page's own projection. */
function ticket(ticketId = "ticket-1"): PublicTicket {
  return {
    ticketId,
    status: "issued",
    valid: true,
    issuedAt: "2026-05-04T18:30:00Z",
    reservationId: "reservation-a",
    event: {
      id: "event-a",
      filmTitle: "The Long Take",
      startsAt: "2026-05-04T18:30:00Z",
      startsAtLocal: "2026-05-04T20:30:00+02:00",
      roomName: "Room 1",
      cinemaName: "Cinema Centro",
      timezone: "Europe/Madrid",
      status: "on_sale",
    },
    seats: [{ eventSeatId: 11, rowLabel: "A", seatNumber: "1" }],
    orderAmountCents: 1998,
    orderAmountDisplay: "19.98",
    qrToken: "cGF5bG9hZA.c2ln",
  };
}

function problem(overrides: Partial<PublicProblem> = {}): PublicProblem {
  return {
    type: "https://cinema.local/problems/service-unavailable",
    title: "Service unavailable",
    status: 503,
    code: "service_unavailable",
    detail: "your tickets cannot be read right now; retry the request",
    ...overrides,
  };
}

/** Everything a state would print, as one string, for the prohibitions below. */
function copyOf(view: unknown): string {
  return JSON.stringify(view);
}

// ── Which list the screen is ────────────────────────────────────────────────

test("with no session the screen is this tab's own note, and it says an account is optional", () => {
  const screen = describeTicketsScreen(false);

  assert.equal(screen.kind, "guest");
  const copy = `${screen.eyebrow} ${screen.headline} ${screen.description}`;
  const lowercase = copy.toLowerCase();

  // Whose list it is, said in words: this tab's, and nobody else's.
  assert.match(lowercase, /this browser tab/);
  assert.match(lowercase, /not a list the box office keeps for you/);
  // Why an account would be worth it, and that it is not required.
  assert.match(lowercase, /across devices/);
  assert.match(lowercase, /optional/);
  assert.match(lowercase, /no account/);
  // The heading is the same screen either way; what changes is underneath it.
  assert.equal(screen.headline, "My tickets");
});

test("with a session the screen is the account's list, and no sentence says the tab keeps it", () => {
  const screen = describeTicketsScreen(true);

  assert.equal(screen.kind, "account");
  const copy = `${screen.eyebrow} ${screen.headline} ${screen.description}`;
  const lowercase = copy.toLowerCase();

  assert.match(lowercase, /account/);
  assert.match(lowercase, /box office/);
  // The ids are the account's now, so the tab's own note is not what this screen is.
  assert.doesNotMatch(lowercase, /this browser tab kept/);
  assert.doesNotMatch(lowercase, /not a list the box office keeps/);
  assert.equal(screen.headline, "My tickets");
});

// ── What one read means ────────────────────────────────────────────────────

test("a read that answers tickets is the list, in the order the box office served it", () => {
  const view = describeAccountTickets({
    ok: true,
    tickets: [ticket("ticket-1"), ticket("ticket-2")],
  });

  assert.equal(view.kind, "loaded");
  if (view.kind !== "loaded") {
    return;
  }
  // The API orders the list (newest first, the ticket id breaking ties); a screen that sorted
  // again would be a second opinion about somebody's history.
  assert.deepEqual(
    view.tickets.map((entry) => entry.ticketId),
    ["ticket-1", "ticket-2"],
  );
  assert.match(view.message, /2 tickets/);
  assert.match(view.message, /newest first/);
});

test("an account that bought nothing is an empty list, and an answer rather than a failure", () => {
  const view = describeAccountTickets({ ok: true, tickets: [] });

  assert.equal(view.kind, "empty");
  if (view.kind !== "empty") {
    return;
  }
  assert.match(view.headline.toLowerCase(), /no ticket/);
  // The difference from a broken read is the whole point of this state.
  assert.match(view.message.toLowerCase(), /not a failed read/);
  assert.match(view.nextStep.toLowerCase(), /no account/);
});

test("a refused session is one page, plainly, with none of the opaque refusal in it", () => {
  // The API answers a missing header, an expired session and a revoked one with the same fixed
  // 401. Two answers that differ only in their detail must produce the same page, or the page
  // would leak the distinction the route refuses to make.
  const expired = describeAccountTickets({
    ok: false,
    problem: problem({
      status: 401,
      code: "unauthorized",
      title: "Unauthorized",
      detail: "this sign-in link cannot be used",
    }),
  });
  const revoked = describeAccountTickets({
    ok: false,
    problem: problem({ status: 401, code: "unauthorized", detail: "something else entirely" }),
  });

  assert.equal(expired.kind, "ended");
  assert.deepEqual(expired, revoked);
  if (expired.kind !== "ended") {
    return;
  }
  assert.match(expired.headline.toLowerCase(), /session has ended/);
  // Said plainly: the list is unknown, and the ticket itself is untouched.
  assert.match(expired.message.toLowerCase(), /nothing about a ticket you bought has changed/);
  // The way out is signing in again, and the dead cookie goes with it.
  assert.match(expired.nextStep.toLowerCase(), /sign in again/);
  assert.match(expired.nextStep.toLowerCase(), /dead cookie/);
  // Nothing of the refusal is repeated: a status and a code are not what a person reads here.
  const copy = copyOf(expired);
  assert.equal(copy.includes("401"), false);
  assert.equal(copy.includes("unauthorized"), false);
});

test("an outage is retryable, carries its problem, and is never an empty list", () => {
  for (const failure of [
    problem(),
    problem({ status: 0, code: "network_error", detail: "the reservation service could not be reached" }),
    problem({ status: 200, code: "unexpected_response", detail: "the account list carries no ticket list" }),
  ]) {
    const view = describeAccountTickets({ ok: false, problem: failure });

    assert.equal(view.kind, "unavailable");
    if (view.kind !== "unavailable") {
      continue;
    }
    assert.equal(view.retryable, true);
    assert.equal(view.problem.code, failure.code);
    assert.match(view.message.toLowerCase(), /our failure/);
    assert.match(view.message.toLowerCase(), /will not show an empty list/);
    assert.notEqual(view.kind, "empty");
  }
});

test("no state claims an address was verified, and none carries a credential", () => {
  const views = [
    describeTicketsScreen(true),
    describeTicketsScreen(false),
    describeAccountTickets({ ok: true, tickets: [ticket("ticket-1")] }),
    describeAccountTickets({ ok: true, tickets: [] }),
    describeAccountTickets({
      ok: false,
      problem: problem({ status: 401, code: "unauthorized" }),
    }),
    describeAccountTickets({ ok: false, problem: problem() }),
  ];

  for (const view of views) {
    const copy = copyOf(view);
    const lowercase = copy.toLowerCase();
    for (const forbidden of ["verified", "@", "session token", "bearer"]) {
      assert.equal(
        lowercase.includes(forbidden),
        false,
        `"${forbidden}" is a claim or a credential no state here holds: ${copy}`,
      );
    }
  }
});

test("the account's screen offers the way out, through the one sign-out there is", () => {
  const page = readFileSync(new URL("../app/tickets/page.tsx", import.meta.url), "utf8");

  // A signed-in person on this screen has no other exit: the header cannot know the session
  // exists, and `/sign-in` is a screen they have no reason to visit. The action is imported
  // rather than reimplemented, so "ask the box office, then clear this browser" stays one piece
  // of code with one order.
  assert.match(
    page,
    /import \{ signOutAction \} from "@\/app\/sign-in\/actions"/,
    "the tickets screen must use the existing sign-out action, not invent a second one",
  );
  assert.equal(
    (page.match(/<form action=\{signOutAction\}/g) ?? []).length,
    2,
    "both signed-in states (a list, and an account that bought nothing) must offer sign-out",
  );
});
