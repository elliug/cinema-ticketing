// Signing in, as answers turned into states and words (identity-login T6 · S1, S2).
//
// Two screens have a rule worth testing and no layout to test it with:
//
//   - the request screen, whose whole honesty is that **one** confirmation sentence comes back
//     whether a link was mailed, the hourly allowance refused it, or the counter that counts it
//     could not be read. The route answers all three with the same `202`, so a page that
//     distinguished them would be the oracle the route refuses to be;
//   - the page the mailed link opens, whose one opaque `401` means spent, expired **or** never
//     valid, deliberately. That state carries nothing of the refusal into its words, and an
//     outage stays a different state, because a bad minute must never read as a broken link.
//
// No query parameter exchange and no browser route: these are the rules, and the pages render
// them. The sentences live here rather than in JSX for the same reason: a rule that only exists
// inside a component cannot be tested, and prose that only exists inside a component drifts
// from the promise it makes.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { PublicProblem } from "./public-api.ts";
import {
  SIGNED_OUT_PATH,
  SIGN_IN_CONFIRMATION,
  SIGN_IN_PATH,
  SIGN_IN_PROMISES,
  TICKETS_PATH,
  decideSignOut,
  describeSignInLink,
  describeSignInRequest,
} from "./identity.ts";

const ACCEPTED = { ok: true as const, value: { accepted: true as const } };

function problem(overrides: Partial<PublicProblem> = {}): PublicProblem {
  return {
    type: "https://cinema.local/problems/service-unavailable",
    title: "Service unavailable",
    status: 503,
    code: "service_unavailable",
    detail: "the sign-in link cannot be sent right now; retry the request",
    ...overrides,
  };
}

// ── The request screen ──────────────────────────────────────────────────────

test("a screen with nothing asked yet is idle, and one asking knows it is sending", () => {
  assert.equal(describeSignInRequest(null).kind, "idle");
  assert.equal(describeSignInRequest(null, true).kind, "sending");
  // A second click while the first is in flight is still a send, not its old answer.
  assert.equal(describeSignInRequest(ACCEPTED, true).kind, "sending");
});

test("the accepted answer is one confirmation, in one sentence, for every outcome behind it", () => {
  const view = describeSignInRequest(ACCEPTED);

  assert.equal(view.kind, "confirmed");
  if (view.kind !== "confirmed") {
    return;
  }
  // A link that was mailed, one the allowance refused and one whose counter was unreadable all
  // reach this branch, because the client is handed the same `202` for all three. The sentence
  // is therefore the same sentence, and it says only what is known.
  assert.equal(view.headline, SIGN_IN_CONFIRMATION);
  assert.equal(
    SIGN_IN_CONFIRMATION,
    "If that address can receive mail, a link is on its way.",
  );
  assert.equal(JSON.stringify(view).includes("@"), false, "no confirmation may name an address");
});

test("a refused address shows the API's own sentence instead of a second wording of its rule", () => {
  const sentence = "email is not an address this sign-in link can be sent to";
  const view = describeSignInRequest({
    ok: false,
    error: problem({ status: 400, code: "invalid_input", detail: sentence }),
  });

  assert.equal(view.kind, "refused");
  if (view.kind !== "refused") {
    return;
  }
  assert.equal(view.message, sentence);
  assert.equal(JSON.stringify(view).includes("@example.com"), false);
});

test("a service that cannot answer is ours, retryable, and never a verdict on the address", () => {
  for (const failure of [
    problem(),
    problem({ status: 0, code: "network_error", detail: "the reservation service could not be reached" }),
    problem({ status: 200, code: "unexpected_response", detail: "the sign-in link answer does not say the request was accepted" }),
  ]) {
    const view = describeSignInRequest({ ok: false, error: failure });

    assert.equal(view.kind, "unavailable");
    if (view.kind !== "unavailable") {
      continue;
    }
    assert.equal(view.retryable, true);
    assert.match(view.message, /our failure/i);
    // The failure itself travels with the state, so a surface can print a code an operator can
    // act on instead of a generic apology.
    assert.equal(view.problem.code, failure.code);
    // The address is not repeated back, and neither is a claim about the mailbox.
    assert.equal(JSON.stringify(view).includes("@"), false);
  }
});

test("every failure is a state the form survives, and never the confirmation", () => {
  const failures = [
    problem({ status: 400, code: "invalid_input", detail: "email is required" }),
    problem(),
    problem({ status: 0, code: "network_error" }),
  ];

  for (const failure of failures) {
    const view = describeSignInRequest({ ok: false, error: failure });
    assert.notEqual(view.kind, "confirmed", "a failure must never be read as a link on its way");
    assert.notEqual(view.kind, "idle");
  }
});

// ── The page the mailed link opens ──────────────────────────────────────────

test("a link that has not been spent yet is a page with nothing to say about it", () => {
  assert.equal(describeSignInLink(null).kind, "checking");
});

test("a spent link is one state, and the token it opened never enters it", () => {
  const view = describeSignInLink({
    ok: true,
    value: { token: "session-token-1", expiresAt: "2026-04-01T10:00:00Z" },
  });

  assert.equal(view.kind, "opened");
  if (view.kind !== "opened") {
    return;
  }
  assert.equal(view.expiresAt, "2026-04-01T10:00:00Z");
  // The session token is a credential: it goes into the cookie and nowhere else, so no view a
  // surface renders may carry it.
  assert.equal(JSON.stringify(view).includes("session-token-1"), false);
  assert.deepEqual(Object.keys(view).sort(), ["expiresAt", "kind"]);
});

test("the opaque refusal is one page, verbatim, whatever the box office thought happened", () => {
  // Spent, expired and never valid share one `401`. Two answers that differ only in their
  // detail must produce the same page, or the page would leak the distinction the route hides.
  const spent = describeSignInLink({
    ok: false,
    error: problem({ status: 401, code: "unauthorized", detail: "this sign-in link cannot be used" }),
  });
  const neverReal = describeSignInLink({
    ok: false,
    error: problem({ status: 401, code: "unauthorized", detail: "something else entirely" }),
  });

  assert.equal(spent.kind, "unusable");
  assert.deepEqual(spent, neverReal);
  if (spent.kind !== "unusable") {
    return;
  }
  assert.match(spent.nextStep, /new link/i);
  assert.equal(JSON.stringify(spent).includes("401"), false, "the page does not print a status it cannot interpret");
});

test("an outage is the retryable surface, never a claim about the link", () => {
  for (const failure of [
    problem(),
    problem({ status: 0, code: "network_error" }),
    problem({ status: 200, code: "unexpected_response" }),
    problem({ status: 400, code: "invalid_input", detail: "token is required" }),
  ]) {
    const view = describeSignInLink({ ok: false, error: failure });

    assert.equal(view.kind, "unavailable");
    if (view.kind !== "unavailable") {
      continue;
    }
    assert.equal(view.retryable, true);
    assert.match(view.message, /nothing about it is known/i);
    // It prints which failure it was, so an operator reading the page can act on it.
    assert.equal(view.problem.code, failure.code);
  }
});

test("the two screens point at each other without inventing routes", () => {
  assert.equal(SIGN_IN_PATH, "/sign-in");
  assert.equal(TICKETS_PATH, "/tickets");
  const unusable = describeSignInLink({
    ok: false,
    error: problem({ status: 401, code: "unauthorized" }),
  });
  assert.equal(unusable.kind, "unusable");
});

// ── The promises the request screen makes ──────────────────────────────────

test("the screen's standing copy says minutes, one use, one mailbox and one answer", () => {
  const copy = SIGN_IN_PROMISES.join(" ");
  const lowercase = copy.toLowerCase();

  assert.match(lowercase, /minutes/);
  assert.match(lowercase, /works once|once/);
  assert.match(lowercase, /mailbox/);
  assert.match(lowercase, /nothing is revealed|same answer/);
});

test("no sentence claims an address was verified or that a message was sent", () => {
  const confirmed = describeSignInRequest(ACCEPTED);
  const refused = describeSignInRequest({
    ok: false,
    error: problem({ status: 400, code: "invalid_input", detail: "email is required" }),
  });
  const unavailable = describeSignInRequest({ ok: false, error: problem() });

  const sentences = [
    ...SIGN_IN_PROMISES,
    ...[confirmed, refused, unavailable].flatMap((view) =>
      view.kind === "idle" || view.kind === "sending"
        ? []
        : [view.headline, view.message, view.nextStep],
    ),
  ];

  for (const sentence of sentences) {
    const lowercase = sentence.toLowerCase();
    for (const forbidden of ["verified", "we sent", "has been sent", "was sent to", "confirmed your"]) {
      assert.equal(
        lowercase.includes(forbidden),
        false,
        `"${forbidden}" claims something no answer in this flow knows: ${sentence}`,
      );
    }
  }
});

// ── Signing out ─────────────────────────────────────────────────────────────
//
// There is no runner for a server action: `signOutAction` imports `next/navigation` and
// `next/headers`, which this plain-Node suite cannot resolve, and the only test that could reach
// it would be a browser one — which cannot see whether a cookie was cleared or a row was closed.
// So the decision the action makes is here, as a pure function with its own test, and the action
// is the thin shell that performs it.
//
// The rule it keeps is the design and not laziness: **a failed revocation must not leave
// somebody signed in in their own browser**. If the box office cannot be reached, this browser is
// signed out anyway — the cookie goes and the visitor lands on the front page — because the row
// expires on its own and holding a person inside a session they asked to leave, pending an API
// call that may never succeed, is the one outcome worth refusing.

test("a confirmed revocation signs this browser out, and says so", () => {
  const decision = decideSignOut({ ok: true, value: { revoked: true } });

  assert.equal(decision.kind, "closed");
  assert.equal(decision.clearCookie, true);
  assert.equal(decision.redirectTo, SIGNED_OUT_PATH);
});

test("a failed revocation is reported and still signs this browser out", () => {
  const decision = decideSignOut({ ok: false, error: problem() });

  // Reported: the caller learns that the row is still open, so nothing downstream can present
  // this as a revocation that happened.
  assert.equal(decision.kind, "unavailable");
  if (decision.kind !== "unavailable") {
    return;
  }
  assert.equal(decision.problem.code, "service_unavailable");
  // And not used to keep anybody signed in here: the same cookie goes and the same page is
  // reached as for a confirmed one. The row expires on its own; this browser does not wait.
  assert.equal(decision.clearCookie, true);
  assert.equal(decision.redirectTo, SIGNED_OUT_PATH);
});

test("a browser holding no session has nothing to revoke and is not told otherwise", () => {
  const decision = decideSignOut(null);

  // No cookie means no credential was presented, so no answer exists to report: the browser is
  // signed out already, which is the state the visitor asked for.
  assert.equal(decision.kind, "no-session");
  assert.equal(decision.clearCookie, true);
  assert.equal(decision.redirectTo, SIGNED_OUT_PATH);
});

test("every sign-out answer lands on the same page, because the answer changes nothing here", () => {
  const decisions = [
    decideSignOut(null),
    decideSignOut({ ok: true, value: { revoked: true } }),
    decideSignOut({ ok: false, error: problem({ status: 401, code: "unauthorized" }) }),
    decideSignOut({ ok: false, error: problem({ status: 0, code: "network_error" }) }),
  ];

  for (const decision of decisions) {
    assert.equal(decision.clearCookie, true);
    assert.equal(decision.redirectTo, SIGNED_OUT_PATH);
  }
  assert.deepEqual(new Set(decisions.map((decision) => decision.redirectTo)), new Set([SIGNED_OUT_PATH]));
});
