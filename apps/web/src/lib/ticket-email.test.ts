// The ticket page's "send the link to another address" action, as its answers become words
// (T5 · S2, S3).
//
// `POST /api/v1/tickets/{ticketId}/email` answers five different things, and a person reads
// five different sentences:
//
//   1. `202 {"sent": true}` — the provider took the message, so this one case may say a
//      message was sent;
//   2. `400 invalid_input` — the address was refused, and nothing was mailed;
//   3. `429` with a `Retry-After` — the order is over its hourly allowance: three sends per
//      hour, because a button that emails any address on request is a spam relay;
//   4. `503` — this deployment does not send mail (no provider configured, or the provider
//      could not be reached), and the page must say so instead of claiming a send;
//   5. the opaque `404` an unknown ticket and an unpaid order share, which is deliberately
//      not a verdict about which one it was.
//
// Plus our own failure — a network the request never reached, a `5xx`, a `202` whose body is
// not the answer — which is retryable and says nothing about the address, the ticket or the
// order.
//
// The tests below are the reason the copy can be trusted to stay honest: a claim of a send
// appears in exactly one state, the address is never echoed back (the function is not even
// given one), and the limit's wait is the API's number rather than a guess.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { PublicProblem } from "./public-api.ts";
import { describeTicketEmail } from "./ticket-email.ts";

/** One refusal, as the API spells it. */
function problem(overrides: Partial<PublicProblem> = {}): PublicProblem {
  return {
    type: "https://cinema.local/problems/invalid-input",
    title: "Invalid input",
    status: 400,
    code: "invalid_input",
    detail: "email must contain a single @",
    ...overrides,
  };
}

const SENT = { ok: true as const, value: { sent: true as const } };

// ── The one answer that says a message was sent ────────────────────────────

test("only the answer a provider accepted says a message was sent", () => {
  const view = describeTicketEmail(SENT);

  assert.equal(view.kind, "sent");
  assert.match(view.headline, /enviado/i);
  // The two facts the brief requires: the link expires, and the link is the whole
  // credential.
  assert.match(view.message, /caduca/i);
  assert.match(view.message, /cualquiera que tenga ese enlace/i);
  // S2, said plainly: nothing verified the address, and nothing kept it.
  assert.match(view.message, /no se ha verificado/i);
  assert.match(view.message, /no se guarda/i);
  // The address is for this send only, and the function is handed no address to echo.
  assert.doesNotMatch(`${view.headline} ${view.message} ${view.nextStep}`, /@/);
});

// ── The address the API's own rule refuses ─────────────────────────────────

test("a refused address says nothing was mailed and offers the correction", () => {
  const view = describeTicketEmail({ ok: false, error: problem() });

  assert.equal(view.kind, "refused");
  assert.match(view.headline, /no se le ha enviado nada/i);
  assert.match(view.message, /rechazó la dirección/i);
  // Never "sent", even negatively phrased as a success: the state is a refusal.
  assert.match(view.message, /no se envió nada/i);
  assert.match(view.nextStep, /Corrige la dirección/i);
  // The rule named is the checkout's own, so the two forms cannot disagree.
  assert.match(view.message, /un dominio con un punto/i);
});

// ── The hourly allowance ───────────────────────────────────────────────────

test("a limit with a Retry-After says how long the wait is, in the API's own seconds", () => {
  const view = describeTicketEmail({
    ok: false,
    error: problem({
      type: "https://cinema.local/problems/rate-limited",
      title: "Too many requests",
      status: 429,
      code: "rate_limited",
      detail:
        "this reservation has been emailed the maximum of 3 times in the last 1h0m0s; wait 2520 seconds and try again",
      retryAfterSeconds: 2520,
    }),
  });

  assert.equal(view.kind, "limited");
  if (view.kind !== "limited") {
    return;
  }
  assert.equal(view.retryAfterSeconds, 2520);
  // The number is the header's, printed rather than rounded away, with a readable form
  // beside it so nobody has to divide by sixty in their head.
  assert.match(view.nextStep, /2520 segundos/);
  assert.match(view.nextStep, /42 minutos/);
  // Nothing was sent this time, and the page must not imply otherwise.
  assert.match(view.message, /no se ha enviado nada/i);
  // Why the limit exists, said in the repo's voice: this is an anti-relay bound.
  assert.match(view.message, /relé de spam/i);
  assert.match(view.message, /tres veces por hora/i);

  // A wait under a minute is printed once, as itself: a readable form that repeats the same
  // number (or rounds it to "0 minutes") says nothing a person can use.
  const underAMinute = describeTicketEmail({
    ok: false,
    error: problem({ status: 429, code: "rate_limited", retryAfterSeconds: 45 }),
  });
  if (underAMinute.kind === "limited") {
    assert.equal(underAMinute.nextStep.match(/45 segundos/g)?.length, 1);
    assert.doesNotMatch(underAMinute.nextStep, /0 minutos/);
  }
});

test("a limit without a Retry-After says in a while rather than inventing a time", () => {
  const view = describeTicketEmail({
    ok: false,
    error: problem({
      status: 429,
      code: "rate_limited",
      detail: "too many sends for this order",
    }),
  });

  assert.equal(view.kind, "limited");
  if (view.kind !== "limited") {
    return;
  }
  assert.equal(view.retryAfterSeconds, null);
  assert.match(view.nextStep, /dentro de un rato/i);
  // No number is manufactured where the API served none.
  assert.doesNotMatch(view.nextStep, /\d+ segundos|\d+ minutos/);
});

// ── A deployment that cannot send ──────────────────────────────────────────

test("a deployment without a mail provider says so and claims no send", () => {
  const view = describeTicketEmail({
    ok: false,
    error: problem({
      type: "https://cinema.local/problems/service-unavailable",
      title: "Service unavailable",
      status: 503,
      code: "service_unavailable",
      detail: "this deployment does not send mail: no mail provider is configured",
    }),
  });

  assert.equal(view.kind, "no_mail");
  assert.match(view.headline, /no envía correo/i);
  // The honest half of S5: a deployment with no provider must not look like a success, and
  // the way to keep a copy is still there.
  assert.match(view.message, /proveedor de correo/i);
  assert.match(view.message, /no se ha enviado nada/i);
  assert.match(view.nextStep, /PDF/);
});

// ── The opaque refusal an unknown ticket shares with an unpaid order ───────

test("the box office's opaque 404 is never read as a verdict about the ticket", () => {
  const view = describeTicketEmail({
    ok: false,
    error: problem({
      type: "https://cinema.local/problems/not-found",
      title: "Not found",
      status: 404,
      code: "not_found",
      detail: "the requested ticket does not exist",
    }),
  });

  assert.equal(view.kind, "unknown");
  // One answer on purpose, and the page says it has one answer rather than picking one.
  assert.match(view.message, /a propósito/i);
  assert.match(view.message, /cuál de las dos/i);
  assert.match(view.nextStep, /se sigue abriendo aquí/i);
  // It must not print the refusal's detail, which is the sentence the API reuses for both.
  assert.doesNotMatch(view.message, /requested ticket does not exist/i);
});

// ── Our own failure ────────────────────────────────────────────────────────

test("a network that never carried the request is retryable, never a refusal", () => {
  const view = describeTicketEmail({
    ok: false,
    error: problem({
      type: "https://cinema.local/problems/network",
      title: "Network error",
      status: 0,
      code: "network_error",
      detail: "the reservation service could not be reached",
    }),
  });

  assert.equal(view.kind, "unavailable");
  if (view.kind !== "unavailable") {
    return;
  }
  assert.equal(view.retryable, true);
  assert.equal(view.problem.code, "network_error");
  // Ours, said as ours, and not an answer about the address or the ticket.
  assert.match(view.message, /fallo nuestro/i);
  assert.match(view.nextStep, /otra vez/i);
});

test("a success whose body is not the answer is a retry, not a send", () => {
  const view = describeTicketEmail({
    ok: false,
    error: problem({
      type: "https://cinema.local/problems/unexpected-response",
      title: "Unexpected response",
      status: 202,
      code: "unexpected_response",
      detail: "the email answer does not say the message was sent",
    }),
  });

  assert.equal(view.kind, "unavailable");
  // A `202` that says nothing is not allowed to read as a send.
  assert.notEqual(view.kind, "sent");
  if (view.kind === "unavailable") {
    assert.equal(view.retryable, true);
  }
});

test("every state names itself in words, so colour never carries the state alone", () => {
  const views = [
    describeTicketEmail(SENT),
    describeTicketEmail({ ok: false, error: problem() }),
    describeTicketEmail({ ok: false, error: problem({ status: 429, code: "rate_limited" }) }),
    describeTicketEmail({
      ok: false,
      error: problem({ status: 503, code: "service_unavailable" }),
    }),
    describeTicketEmail({
      ok: false,
      error: problem({ status: 404, code: "not_found" }),
    }),
    describeTicketEmail({
      ok: false,
      error: problem({ status: 0, code: "network_error" }),
    }),
  ];

  for (const view of views) {
    assert.notEqual(view.headline.trim(), "");
    assert.notEqual(view.message.trim(), "");
    assert.notEqual(view.nextStep.trim(), "");
  }
  // Six answers, six headlines: no two states may read the same, because a person has to be
  // able to tell "nothing was mailed" from "a deployment that cannot mail".
  assert.equal(new Set(views.map((view) => view.headline)).size, 6);
});
