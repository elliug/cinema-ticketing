// The signed link a purchase email carries, as the page behind it decides (T4 · S3).
//
// `GET /api/v1/ticket-links/{token}` answers one of three things, and the page must never
// merge them:
//
//   1. the paid order's tickets and the instant the signature stops verifying;
//   2. one opaque refusal — a forged token, a tampered one, an expired one, an order this
//      database never sold — which the API answers with the same `404` body on purpose;
//   3. our own failure (`503`, a network failure, a body that is not the answer): no
//      verdict on the link at all, and worth asking again.
//
// The second case is the one worth pinning: D3/S3 put a signed, expiring link in a mailbox
// instead of a permanent key, and the honest consequence is that an old email stops
// opening. A page that told "expired" apart from "never existed" would claim a knowledge
// this system deliberately does not have, so the closed state is reached by both and is
// *identical*, verbatim. The test makes the two refusals differ in the fields a careless
// view would print, because the promise is that the page cannot tell them apart — not
// merely that it does not today.
//
// What is decided here is only what the API's answer means. Every sentence a person reads
// on that page comes from this module, so none of them can claim an address was verified
// (it never is, S2) or that anything was mailed to this reader: the link arrived, and that
// is all this page knows.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import type { PublicProblem } from "./public-api.ts";
import {
  describeLinkRetention,
  describeTicketLink,
} from "./ticket-link.ts";

/** One refusal, as the API spells it; the body is the ticket route's own. */
function refusal(overrides: Partial<PublicProblem> = {}): PublicProblem {
  return {
    type: "https://cinema.local/problems/not-found",
    title: "Not found",
    status: 404,
    code: "not_found",
    detail: "the requested ticket does not exist",
    ...overrides,
  };
}

/** One failure of ours, which says nothing about the link. */
function failure(overrides: Partial<PublicProblem> = {}): PublicProblem {
  return {
    type: "https://cinema.local/problems/service-unavailable",
    title: "Service unavailable",
    status: 503,
    code: "service_unavailable",
    detail: "the ticket link cannot be opened right now; retry the request",
    ...overrides,
  };
}

// ── The link that opens ─────────────────────────────────────────────────────

test("an opened link carries the tickets it names and the expiry the API spelled", () => {
  const view = describeTicketLink({
    ok: true,
    value: {
      reservationId: "reservation-a",
      expiresAt: "2026-05-11T18:30:00Z",
      ticketIds: ["ticket-1", "ticket-2", "ticket-3"],
    },
  });

  assert.equal(view.kind, "opened");
  if (view.kind !== "opened") {
    return;
  }
  assert.equal(view.reservationId, "reservation-a");
  // Printed, not recomputed: the page says when the email stops opening, and the answer
  // is the instant the signature carries so the two cannot disagree.
  assert.equal(view.expiresAt, "2026-05-11T18:30:00Z");
  // Served order, untouched: the API answers ascending so two readers of one link see one
  // list, and re-sorting here would be a second opinion about it.
  assert.deepEqual(view.ticketIds, ["ticket-1", "ticket-2", "ticket-3"]);
  assert.equal(view.retryable, false);
  // Nothing on this page knows who the reader is, and nothing may imply it does.
  assert.doesNotMatch(`${view.message} ${view.nextStep}`, /verified|we sent|your email/i);
});

// ── The link that no longer opens ───────────────────────────────────────────

test("an aged-out link and one that never existed reach one identical closed view", () => {
  // The API answers both with the same body; these two differ in the fields a view might
  // be tempted to print, which is what makes the equality below mean something.
  const expired = describeTicketLink({
    ok: false,
    error: refusal({ detail: "the requested ticket does not exist" }),
  });
  const neverExisted = describeTicketLink({
    ok: false,
    error: refusal({
      type: "https://cinema.local/problems/not-found",
      title: "Not found",
      detail: "the requested ticket does not exist",
    }),
  });

  assert.equal(expired.kind, "closed");
  assert.deepEqual(expired, neverExisted);
  // Structurally closed: the refusal itself is not carried, so no surface can print the
  // one thing this system refuses to say.
  assert.equal("problem" in expired, false);
  assert.equal(expired.retryable, false);
});

test("a closed link says links expire on purpose and offers the way forward", () => {
  const view = describeTicketLink({ ok: false, error: refusal() });
  if (view.kind !== "closed") {
    assert.fail(`expected a closed view, got ${view.kind}`);
  }

  // The honest version names the rule rather than an incident: nothing here says the
  // reader's purchase is gone.
  assert.match(view.message, /caduc/i);
  assert.match(view.message, /a propósito/i);
  assert.match(view.nextStep, /Mis entradas/);
  assert.match(view.nextStep, /confirmación/);
  // A closed link is not a reason to buy again, and it must not read like one.
  assert.doesNotMatch(view.message, /comprar|compra otra vez|no encontrado/i);
});

// ── Our own failure ─────────────────────────────────────────────────────────

test("an unreadable API reaches the retry state, never a verdict on the link", () => {
  const view = describeTicketLink({ ok: false, error: failure() });

  assert.equal(view.kind, "unavailable");
  if (view.kind !== "unavailable") {
    return;
  }
  assert.equal(view.retryable, true);
  assert.equal(view.problem.code, "service_unavailable");
  // Ours, said as ours: a failure to ask is not an answer about the link.
  assert.match(view.message, /fallo nuestro/i);
  assert.match(view.nextStep, /otra vez/i);
  assert.doesNotMatch(view.message, /caduc|inválid|ya no abre/i);
});

test("a network failure and a broken answer are the same retry, not a closed link", () => {
  const offline = describeTicketLink({
    ok: false,
    error: failure({
      type: "https://cinema.local/problems/network",
      title: "Network error",
      status: 0,
      code: "network_error",
      detail: "the reservation service could not be reached",
    }),
  });
  const brokenBody = describeTicketLink({
    ok: false,
    error: failure({
      type: "https://cinema.local/problems/unexpected-response",
      title: "Unexpected response",
      status: 200,
      code: "unexpected_response",
      detail: "the link answer carries no ticket id",
    }),
  });

  assert.equal(offline.kind, "unavailable");
  assert.equal(brokenBody.kind, "unavailable");
  assert.notEqual(offline.kind, "closed");
  assert.notEqual(brokenBody.kind, "closed");
});

// ── What this tab did with the tickets ──────────────────────────────────────

test("what this tab kept is said in words, with no claim about an account", () => {
  const writing = describeLinkRetention("writing", 3);
  assert.match(writing, /3 entradas/);
  assert.match(writing, /…$/);

  const kept = describeLinkRetention("kept", 1);
  assert.match(kept, /Esa entrada ya está en Mis entradas/);
  assert.match(kept, /no es una cuenta/);
  // D2/S2: the address was never verified, and this page never saw one.
  assert.doesNotMatch(`${writing} ${kept}`, /verificad|enviado a tu|tu correo/i);
});

test("a refused storage says the tickets are still openable from their links", () => {
  const refused = describeLinkRetention("refused", 2);

  assert.match(refused, /negó/i);
  assert.match(refused, /2 entradas/);
  // The consequence of a refused write is the list, never the tickets (S8): each one is
  // still reachable from the link this page prints.
  assert.match(refused, /no se ven afectadas/i);
  assert.match(refused, /enlace/i);
});

// ── What the route itself promises ──────────────────────────────────────────
//
// The screens are covered by the typecheck and the production build, and this repository
// ships no DOM test runner (see components/components.test.ts). What neither sees is the
// promise that belongs to the *route*: a link is a credential in a URL, so the page must
// never be indexed, and it must never be prerendered as somebody else's tickets.

function routeSource(): string {
  return readFileSync(new URL("../app/t/[token]/page.tsx", import.meta.url), "utf8");
}

test("the link route is unindexed, per request, and keeps the token out of its logs", () => {
  const source = routeSource();

  assert.match(source, /robots:\s*\{\s*index:\s*false,\s*follow:\s*false\s*\}/);
  assert.match(source, /export const dynamic = "force-dynamic"/);
  // The token lives in the URL, where access logs and `Referer` headers already see it: it
  // is never written into a log line or a problem of this application's own.
  assert.doesNotMatch(source, /console\.(log|info|warn|error)/);
});
