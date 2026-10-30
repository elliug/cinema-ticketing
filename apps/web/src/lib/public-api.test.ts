// Public reservation API client contract (guest seat selection).
//
// The client is exercised through its public interface only: the HTTP request it
// builds and the typed result it returns. An injected fetch keeps the tests
// offline and deterministic, and the real customer runs are left to the browser
// level check (see apps/web/README.md).
//
// Two rules matter most here:
//
//   - nothing administrative leaks into a guest request: no bearer token, no
//     /admin prefix, and no credential is ever read;
//   - every attempt carries the Idempotency-Key it was given, and a caller that
//     retries must mint a new one.
import assert from "node:assert/strict";
import { test } from "node:test";
import type {
  PublicClient,
  PublicProblem,
  PublicResult,
} from "./public-api.ts";
import {
  createPublicClient,
  DEFAULT_PUBLIC_BASE_URL,
  MAX_HOLD_SEATS,
  newIdempotencyKey,
  PUBLIC_BASE_URL_ENV,
  resolvePublicConfig,
  ticketPDFUrl,
  ticketQRImageUrl,
} from "./public-api.ts";

const BASE_URL = "http://127.0.0.1:8080";

interface RecordedCall {
  url: string;
  init: RequestInit;
}

interface RecordingFetch {
  fetch: (input: string, init?: RequestInit) => Promise<Response>;
  calls: RecordedCall[];
}

function recordingFetch(respond: (call: RecordedCall) => Response): RecordingFetch {
  const calls: RecordedCall[] = [];
  return {
    calls,
    fetch: (input, init) => {
      const call: RecordedCall = { url: input, init: init ?? {} };
      calls.push(call);
      return Promise.resolve(respond(call));
    },
  };
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function makeClient(recorder: RecordingFetch): PublicClient {
  return createPublicClient({ baseUrl: BASE_URL, fetch: recorder.fetch });
}

function unwrap<T>(result: PublicResult<T>): T {
  assert.equal(result.ok, true, `expected success, got ${JSON.stringify(result)}`);
  if (!result.ok) {
    throw new Error("unreachable");
  }
  return result.value;
}

function unwrapError<T>(result: PublicResult<T>): PublicProblem {
  assert.equal(result.ok, false, `expected a typed failure, got ${JSON.stringify(result)}`);
  if (result.ok) {
    throw new Error("unreachable");
  }
  return result.error;
}

function headerValue(init: RequestInit, name: string): string | undefined {
  const headers = init.headers as Record<string, string> | undefined;
  return headers?.[name];
}

test("resolvePublicConfig needs no credential and defaults the base URL", () => {
  const fallback = unwrap(resolvePublicConfig({}));
  assert.equal(fallback.baseUrl, DEFAULT_PUBLIC_BASE_URL);
  assert.equal(fallback.baseUrl, "http://127.0.0.1:8080");

  const configured = unwrap(
    resolvePublicConfig({ [PUBLIC_BASE_URL_ENV]: "http://127.0.0.1:9999/" }),
  );
  assert.equal(configured.baseUrl, "http://127.0.0.1:9999");
  assert.equal(configured.baseUrl, DEFAULT_PUBLIC_BASE_URL.replace("8080", "9999"));

  // A guest request never needs an administrative token, so an operator token in
  // the environment is ignored rather than required.
  const withToken = unwrap(
    resolvePublicConfig({ API_LOCAL_ADMIN_TOKEN: "should-not-be-used" }),
  );
  assert.equal("token" in withToken, false);
});

test("newIdempotencyKey mints a distinct key every call", () => {
  const first = newIdempotencyKey();
  const second = newIdempotencyKey();

  assert.match(first, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  assert.notEqual(first, second);
  assert.equal(new Set([first, second, newIdempotencyKey()]).size, 3);
});

test("listEventSeats reads the public availability snapshot", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      eventId: "event-a",
      seats: [
        { id: 11, layoutSeatId: "layout-seat-a1", state: "available", held: false },
        { id: 12, layoutSeatId: "layout-seat-a2", state: "held", held: true },
      ],
    }),
  );

  const seats = unwrap(await makeClient(recorder).listEventSeats("event-a"));

  assert.equal(recorder.calls.length, 1);
  assert.equal(
    recorder.calls[0].url,
    `${BASE_URL}/api/v1/events/event-a/seats`,
  );
  assert.equal(recorder.calls[0].init.method, "GET");
  assert.equal(recorder.calls[0].init.cache, "no-store");
  assert.equal(headerValue(recorder.calls[0].init, "Authorization"), undefined);
  assert.equal(headerValue(recorder.calls[0].init, "Idempotency-Key"), undefined);

  assert.equal(seats.eventId, "event-a");
  assert.deepEqual(seats.seats, [
    { id: 11, layoutSeatId: "layout-seat-a1", state: "available", held: false },
    { id: 12, layoutSeatId: "layout-seat-a2", state: "held", held: true },
  ]);
});

test("listEventSeats escapes the event id and treats a missing flag as free", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      eventId: "event a/b",
      seats: [{ id: 11, layoutSeatId: "layout-seat-a1", state: "available" }],
    }),
  );

  const seats = unwrap(await makeClient(recorder).listEventSeats("event a/b"));

  assert.equal(
    recorder.calls[0].url,
    `${BASE_URL}/api/v1/events/event%20a%2Fb/seats`,
  );
  assert.equal(seats.seats[0].held, false);
});

test("listEventSeats reads the layout projection when the API serves it", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      eventId: "event-a",
      seats: [
        {
          id: 11,
          layoutSeatId: "layout-seat-a1",
          state: "available",
          held: false,
          rowLabel: "A",
          seatNumber: "1",
          kind: "standard",
        },
        {
          id: 12,
          layoutSeatId: "layout-seat-a2",
          state: "available",
          held: true,
          rowLabel: "B",
          seatNumber: "2",
          kind: "accessible",
        },
      ],
    }),
  );

  const seats = unwrap(await makeClient(recorder).listEventSeats("event-a"));

  // The projection is carried through as served: the map builds the real rows
  // from it, so nothing may be dropped or renamed here.
  assert.deepEqual(seats.seats, [
    {
      id: 11,
      layoutSeatId: "layout-seat-a1",
      state: "available",
      held: false,
      rowLabel: "A",
      seatNumber: "1",
      kind: "standard",
    },
    {
      id: 12,
      layoutSeatId: "layout-seat-a2",
      state: "available",
      held: true,
      rowLabel: "B",
      seatNumber: "2",
      kind: "accessible",
    },
  ]);
});

test("a response without the layout projection keeps the older shape", async () => {
  // An older API serves availability only; the client must not invent the
  // missing projection, so the seat reads exactly as it did before.
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      eventId: "event-a",
      seats: [{ id: 11, layoutSeatId: "layout-seat-a1", state: "available", held: false }],
    }),
  );

  const seats = unwrap(await makeClient(recorder).listEventSeats("event-a"));

  assert.deepEqual(seats.seats, [
    { id: 11, layoutSeatId: "layout-seat-a1", state: "available", held: false },
  ]);
  assert.equal("rowLabel" in seats.seats[0], false);
  assert.equal("seatNumber" in seats.seats[0], false);
  assert.equal("kind" in seats.seats[0], false);
});

test("createHold posts the selection with the Idempotency-Key it was given", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(201, {
      id: "hold-1",
      eventId: "event-a",
      seatIds: [11, 21],
      expiresAt: "2026-03-01T10:10:00.000Z",
    }),
  );

  const hold = unwrap(
    await makeClient(recorder).createHold({
      eventId: "event-a",
      seatIds: [11, 21],
      idempotencyKey: "key-1",
    }),
  );

  const call = recorder.calls[0];
  assert.equal(call.url, `${BASE_URL}/api/v1/reservations/holds`);
  assert.equal(call.init.method, "POST");
  assert.equal(headerValue(call.init, "Idempotency-Key"), "key-1");
  assert.equal(headerValue(call.init, "Content-Type"), "application/json");
  assert.equal(headerValue(call.init, "Authorization"), undefined);
  assert.deepEqual(JSON.parse(call.init.body as string), {
    eventId: "event-a",
    seatIds: [11, 21],
  });

  assert.deepEqual(hold, {
    id: "hold-1",
    eventId: "event-a",
    seatIds: [11, 21],
    expiresAt: "2026-03-01T10:10:00.000Z",
  });
});

test("each hold attempt sends its own key and never reuses the previous one", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(201, {
      id: "hold-1",
      eventId: "event-a",
      seatIds: [11],
      expiresAt: "2026-03-01T10:10:00.000Z",
    }),
  );
  const client = makeClient(recorder);

  await client.createHold({ eventId: "event-a", seatIds: [11], idempotencyKey: newIdempotencyKey() });
  await client.createHold({ eventId: "event-a", seatIds: [11], idempotencyKey: newIdempotencyKey() });

  const keys = recorder.calls.map((call) => headerValue(call.init, "Idempotency-Key"));
  assert.equal(keys.length, 2);
  assert.notEqual(keys[0], keys[1]);
  for (const key of keys) {
    assert.equal(typeof key, "string");
    assert.ok((key as string).length >= 8);
  }
});

test("createHold rejects an empty selection before any request", async () => {
  const recorder = recordingFetch(() => jsonResponse(201, {}));
  const problem = unwrapError(
    await makeClient(recorder).createHold({
      eventId: "event-a",
      seatIds: [],
      idempotencyKey: "key-1",
    }),
  );

  assert.equal(recorder.calls.length, 0);
  assert.equal(problem.code, "invalid_input");
  assert.equal(problem.status, 0);
  assert.match(problem.detail, /at least one seat/i);
});

test("createHold rejects more seats than the API accepts before any request", async () => {
  assert.equal(MAX_HOLD_SEATS, 20);
  const recorder = recordingFetch(() => jsonResponse(201, {}));
  const seatIds = Array.from({ length: MAX_HOLD_SEATS + 1 }, (_, index) => index + 1);

  const problem = unwrapError(
    await makeClient(recorder).createHold({
      eventId: "event-a",
      seatIds,
      idempotencyKey: "key-1",
    }),
  );

  assert.equal(recorder.calls.length, 0);
  assert.equal(problem.code, "invalid_input");
  assert.match(problem.detail, /20/);
});

test("createHold also requires an Idempotency-Key", async () => {
  const recorder = recordingFetch(() => jsonResponse(201, {}));
  const problem = unwrapError(
    await makeClient(recorder).createHold({
      eventId: "event-a",
      seatIds: [11],
      idempotencyKey: "   ",
    }),
  );

  assert.equal(recorder.calls.length, 0);
  assert.equal(problem.code, "invalid_input");
  assert.match(problem.detail, /Idempotency-Key/);
});

test("a 409 seat_unavailable keeps the API code", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(409, {
      type: "https://cinema.local/problems/seat-unavailable",
      title: "Seat unavailable",
      status: 409,
      code: "seat_unavailable",
      detail: "at least one selected seat is already held by another reservation",
    }),
  );

  const problem = unwrapError(
    await makeClient(recorder).createHold({
      eventId: "event-a",
      seatIds: [11],
      idempotencyKey: "key-1",
    }),
  );

  assert.equal(problem.status, 409);
  assert.equal(problem.code, "seat_unavailable");
  assert.match(problem.detail, /already held/);
});

test("a 400 hold_window_expired is distinct from a taken seat", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(400, {
      type: "https://cinema.local/problems/hold-window-expired",
      title: "Hold window expired",
      status: 400,
      code: "hold_window_expired",
      detail: "the hold window closed before the seats could be claimed",
    }),
  );

  const problem = unwrapError(
    await makeClient(recorder).createHold({
      eventId: "event-a",
      seatIds: [11],
      idempotencyKey: "key-1",
    }),
  );

  assert.equal(problem.status, 400);
  assert.equal(problem.code, "hold_window_expired");
});

test("a 503 coordination failure stays a retryable status", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(503, {
      type: "https://cinema.local/problems/coordination-unavailable",
      title: "Service unavailable",
      status: 503,
      code: "coordination_unavailable",
      detail: "the reservation service is temporarily unavailable; retry the request",
    }),
  );

  const problem = unwrapError(
    await makeClient(recorder).createHold({
      eventId: "event-a",
      seatIds: [11],
      idempotencyKey: "key-1",
    }),
  );

  assert.equal(problem.status, 503);
  assert.equal(problem.code, "coordination_unavailable");
});

test("an unreachable API becomes a typed network failure", async () => {
  const client = createPublicClient({
    baseUrl: BASE_URL,
    fetch: () => Promise.reject(new Error("connect ECONNREFUSED 127.0.0.1:8080")),
  });

  const problem = unwrapError(await client.listEventSeats("event-a"));
  assert.equal(problem.code, "network_error");
  assert.equal(problem.status, 0);
  assert.doesNotMatch(problem.detail, /ECONNREFUSED/, "internals stay out of the UI");
});

// M3 · T7 — the checkout and ticket reads the confirmation and ticket pages use.
//
// The checkout route takes no body at all (the amount is computed server side)
// and is idempotent by header, so what matters here is the URL, the key, and that
// a refusal keeps the code the API used: `payment_in_flight`, `hold_expired` and
// `payment_unavailable` each tell the buyer to do something different.

const TICKET_BODY = {
  ticketId: "ticket-1",
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

test("payReservation posts with the Idempotency-Key and no body", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      reservationId: "reservation-a",
      attemptId: "attempt-1",
      paymentStatus: "approved",
      amountCents: 1998,
      ticketIds: ["ticket-1"],
    }),
  );

  const payment = unwrap(
    await makeClient(recorder).payReservation({
      reservationId: "reservation-a",
      idempotencyKey: "key-1",
    }),
  );

  const call = recorder.calls[0];
  assert.equal(call.url, `${BASE_URL}/api/v1/reservations/reservation-a/payment`);
  assert.equal(call.init.method, "POST");
  assert.equal(headerValue(call.init, "Idempotency-Key"), "key-1");
  assert.equal(headerValue(call.init, "Authorization"), undefined);
  assert.equal(call.init.body, undefined, "the amount is computed server side");

  assert.deepEqual(payment, {
    reservationId: "reservation-a",
    attemptId: "attempt-1",
    paymentStatus: "approved",
    amountCents: 1998,
    ticketIds: ["ticket-1"],
  });
});

test("payReservation escapes the reservation id in its path", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      reservationId: "reservation a/b",
      attemptId: "attempt-1",
      paymentStatus: "approved",
      amountCents: 1,
      ticketIds: [],
    }),
  );

  await makeClient(recorder).payReservation({
    reservationId: "reservation a/b",
    idempotencyKey: "key-1",
  });

  assert.equal(
    recorder.calls[0].url,
    `${BASE_URL}/api/v1/reservations/reservation%20a%2Fb/payment`,
  );
});

test("a declined payment is a success whose body says so, with no tickets", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      reservationId: "reservation-a",
      attemptId: "attempt-1",
      paymentStatus: "declined",
      amountCents: 1998,
      ticketIds: [],
    }),
  );

  const payment = unwrap(
    await makeClient(recorder).payReservation({
      reservationId: "reservation-a",
      idempotencyKey: "key-1",
    }),
  );

  assert.equal(payment.paymentStatus, "declined");
  assert.deepEqual(payment.ticketIds, []);
});

test("payReservation rejects a missing key or reservation before any request", async () => {
  const recorder = recordingFetch(() => jsonResponse(200, {}));
  const client = makeClient(recorder);

  const withoutKey = unwrapError(
    await client.payReservation({ reservationId: "reservation-a", idempotencyKey: "  " }),
  );
  assert.equal(withoutKey.code, "invalid_input");
  assert.match(withoutKey.detail, /Idempotency-Key/);

  const withoutReservation = unwrapError(
    await client.payReservation({ reservationId: "", idempotencyKey: "key-1" }),
  );
  assert.equal(withoutReservation.code, "invalid_input");

  assert.equal(recorder.calls.length, 0);
});

test("409 payment_in_flight keeps its own code, because it asks for the same key", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(409, {
      type: "https://cinema.local/problems/payment-in-flight",
      title: "Conflict",
      status: 409,
      code: "payment_in_flight",
      detail:
        "a payment for this reservation is already being processed; retry with the same Idempotency-Key",
    }),
  );

  const problem = unwrapError(
    await makeClient(recorder).payReservation({
      reservationId: "reservation-a",
      idempotencyKey: "key-1",
    }),
  );

  assert.equal(problem.status, 409);
  assert.equal(problem.code, "payment_in_flight");
  assert.match(problem.detail, /same Idempotency-Key/);
});

test("a closed hold and an already paid order stay distinguishable", async () => {
  const expired = recordingFetch(() =>
    jsonResponse(409, {
      type: "https://cinema.local/problems/hold-expired",
      title: "Hold expired",
      status: 409,
      code: "hold_expired",
      detail: "the hold window closed before the payment was completed",
    }),
  );
  const expiredProblem = unwrapError(
    await makeClient(expired).payReservation({
      reservationId: "reservation-a",
      idempotencyKey: "key-1",
    }),
  );
  assert.equal(expiredProblem.code, "hold_expired");

  const paid = recordingFetch(() =>
    jsonResponse(409, {
      type: "https://cinema.local/problems/already-paid",
      title: "Conflict",
      status: 409,
      code: "already_paid",
      detail: "the reservation already carries a captured payment",
    }),
  );
  const paidProblem = unwrapError(
    await makeClient(paid).payReservation({
      reservationId: "reservation-a",
      idempotencyKey: "key-1",
    }),
  );
  assert.equal(paidProblem.code, "already_paid");
  assert.notEqual(paidProblem.code, expiredProblem.code);
});

test("503 payment_unavailable stays retryable with the same key", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(503, {
      type: "https://cinema.local/problems/payment-unavailable",
      title: "Service unavailable",
      status: 503,
      code: "payment_unavailable",
      detail: "the payment provider is temporarily unavailable; retry the same Idempotency-Key",
    }),
  );

  const problem = unwrapError(
    await makeClient(recorder).payReservation({
      reservationId: "reservation-a",
      idempotencyKey: "key-1",
    }),
  );

  assert.equal(problem.status, 503);
  assert.equal(problem.code, "payment_unavailable");
});

test("getTicket reads GET /tickets/{id} and keeps the served local time verbatim", async () => {
  const recorder = recordingFetch(() => jsonResponse(200, TICKET_BODY));

  const ticket = unwrap(await makeClient(recorder).getTicket("ticket-1"));

  const call = recorder.calls[0];
  assert.equal(call.url, `${BASE_URL}/api/v1/tickets/ticket-1`);
  assert.equal(call.init.method, "GET");
  assert.equal(call.init.cache, "no-store");
  assert.equal(headerValue(call.init, "Authorization"), undefined);

  // The venue's own clock is served, and it is what the page prints: re-deriving
  // it from `startsAt` in the browser would render the viewer's zone instead.
  assert.equal(ticket.event.startsAtLocal, "2026-05-04T20:30:00+02:00");
  assert.equal(ticket.event.timezone, "Europe/Madrid");
  assert.equal(ticket.event.filmTitle, "The Long Take");
  assert.equal(ticket.event.roomName, "Room 1");
  assert.equal(ticket.event.cinemaName, "Cinema Centro");

  assert.deepEqual(ticket.seats, [{ eventSeatId: 11, rowLabel: "A", seatNumber: "1" }]);
  assert.equal(ticket.orderAmountCents, 1998);
  assert.equal(ticket.orderAmountDisplay, "19.98");
  assert.equal(ticket.qrToken, "cGF5bG9hZA.c2ln");
  assert.equal(ticket.valid, true);
  assert.equal(ticket.status, "issued");
  assert.equal(ticket.reservationId, "reservation-a");
  assert.equal(ticket.issuedAt, "2026-05-04T18:30:00Z");
});

test("getTicket escapes the ticket id and reports an unknown ticket", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(404, {
      type: "https://cinema.local/problems/not-found",
      title: "Not found",
      status: 404,
      code: "not_found",
      detail: "the requested ticket does not exist",
    }),
  );

  const problem = unwrapError(await makeClient(recorder).getTicket("ticket/1 x"));

  assert.equal(recorder.calls[0].url, `${BASE_URL}/api/v1/tickets/ticket%2F1%20x`);
  assert.equal(problem.status, 404);
  assert.equal(problem.code, "not_found");
});

test("a ticket body without its served local time or its code is refused, never patched", async () => {
  const withoutLocalTime = recordingFetch(() => {
    const body = {
      ...TICKET_BODY,
      event: { ...TICKET_BODY.event, startsAtLocal: "" },
    };
    return jsonResponse(200, body);
  });
  const noTime = unwrapError(await makeClient(withoutLocalTime).getTicket("ticket-1"));
  assert.equal(noTime.code, "unexpected_response");
  assert.match(noTime.detail, /startsAtLocal/);

  const withoutCode = recordingFetch(() => jsonResponse(200, { ...TICKET_BODY, qrToken: "" }));
  const noCode = unwrapError(await makeClient(withoutCode).getTicket("ticket-1"));
  assert.equal(noCode.code, "unexpected_response");
  assert.match(noCode.detail, /QR/);
});

test("verifyTicket posts the served token and renders the reason it is refused", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      ticketId: "ticket-1",
      eventId: "event-a",
      seats: [{ eventSeatId: 11, rowLabel: "A", seatNumber: "1" }],
      ticketStatus: "void",
      eventStatus: "on_sale",
      valid: false,
      reason: "voided",
    }),
  );

  const verification = unwrap(
    await makeClient(recorder).verifyTicket("cGF5bG9hZA.c2ln"),
  );

  const call = recorder.calls[0];
  assert.equal(call.url, `${BASE_URL}/api/v1/tickets/verify`);
  assert.equal(call.init.method, "POST");
  assert.equal(headerValue(call.init, "Content-Type"), "application/json");
  assert.deepEqual(JSON.parse(call.init.body as string), { token: "cGF5bG9hZA.c2ln" });

  assert.equal(verification.valid, false);
  assert.equal(verification.reason, "voided");
  assert.equal(verification.ticketStatus, "void");
});

test("a cancelled screening is a different reason from a withdrawn ticket", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(200, {
      ticketId: "ticket-1",
      eventId: "event-a",
      seats: [],
      ticketStatus: "issued",
      eventStatus: "cancelled",
      valid: false,
      reason: "event_cancelled",
    }),
  );

  const verification = unwrap(await makeClient(recorder).verifyTicket("token"));
  assert.equal(verification.reason, "event_cancelled");
});

test("a valid verification carries no reason, and a refused one is invalid_token", async () => {
  const valid = recordingFetch(() =>
    jsonResponse(200, {
      ticketId: "ticket-1",
      eventId: "event-a",
      seats: [],
      ticketStatus: "issued",
      eventStatus: "on_sale",
      valid: true,
    }),
  );
  const verdict = unwrap(await makeClient(valid).verifyTicket("token"));
  assert.equal(verdict.valid, true);
  assert.equal(verdict.reason, undefined);

  const refused = recordingFetch(() =>
    jsonResponse(400, {
      type: "https://cinema.local/problems/invalid-token",
      title: "Invalid token",
      status: 400,
      code: "invalid_token",
      detail: "the presented code is not a valid ticket",
    }),
  );
  const problem = unwrapError(await makeClient(refused).verifyTicket("nope"));
  assert.equal(problem.status, 400);
  assert.equal(problem.code, "invalid_token");
});

test("verifyTicket refuses a blank token before any request", async () => {
  const recorder = recordingFetch(() => jsonResponse(200, {}));
  const problem = unwrapError(await makeClient(recorder).verifyTicket("   "));

  assert.equal(recorder.calls.length, 0);
  assert.equal(problem.code, "invalid_input");
});

test("the PDF and QR links point at the public ticket routes", () => {
  assert.equal(
    ticketPDFUrl(BASE_URL, "ticket-1"),
    `${BASE_URL}/api/v1/tickets/ticket-1/pdf`,
  );
  assert.equal(
    ticketQRImageUrl(BASE_URL, "ticket-1"),
    `${BASE_URL}/api/v1/tickets/ticket-1/qr.svg`,
  );
  // A configured base URL with a trailing slash must not double up, and an id is
  // escaped exactly as the client escapes it.
  assert.equal(
    ticketPDFUrl(`${BASE_URL}/`, "ticket/1"),
    `${BASE_URL}/api/v1/tickets/ticket%2F1/pdf`,
  );
  assert.equal(
    ticketQRImageUrl(`${BASE_URL}/`, "ticket 1"),
    `${BASE_URL}/api/v1/tickets/ticket%201/qr.svg`,
  );
});

// M3.5 · T4 — the public catalogue the "Now showing" screen reads.
//
// The catalogue is the first read a stranger makes: it names the screenings on sale
// without an identifier in hand. Two rules decide the parser below. The screening
// order is the API's, so nothing here sorts. And what the screen prints is served:
// the venue's own hour and the price text are carried verbatim, so a response that
// omits either is a broken contract rather than something this client fills in.

const CATALOGUE_BODY = {
  events: [
    {
      id: "event-early",
      status: "on_sale",
      onSale: true,
      filmTitle: "The Long Take",
      cinemaName: "Cinema Centro",
      roomName: "Room 1",
      startsAt: "2026-05-04T18:30:00Z",
      startsAtLocal: "2026-05-04T20:30:00+02:00",
      priceCents: 1250,
      priceDisplay: "12.50",
      seatCount: 40,
      availableSeats: 12,
    },
    {
      id: "event-late",
      status: "on_sale",
      onSale: true,
      filmTitle: "Second Feature",
      cinemaName: "Cinema Centro",
      roomName: "Room 2",
      startsAt: "2026-05-04T20:00:00Z",
      startsAtLocal: "2026-05-04T22:00:00+02:00",
      priceCents: 900,
      priceDisplay: "9.00",
      seatCount: 24,
      availableSeats: 0,
    },
  ],
};

test("listEvents reads GET /events and keeps the screening order it was served", async () => {
  const recorder = recordingFetch(() => jsonResponse(200, CATALOGUE_BODY));

  const events = unwrap(await makeClient(recorder).listEvents());

  const call = recorder.calls[0];
  assert.equal(call.url, `${BASE_URL}/api/v1/events`);
  assert.equal(call.init.method, "GET");
  assert.equal(call.init.cache, "no-store");
  assert.equal(headerValue(call.init, "Authorization"), undefined);
  assert.equal(headerValue(call.init, "Idempotency-Key"), undefined);

  // Everything the screen shows is carried through as served, in the order the
  // API answered: the page renders this array without sorting it again.
  assert.deepEqual(events, CATALOGUE_BODY.events);
  assert.deepEqual(
    events.map((event) => event.id),
    ["event-early", "event-late"],
  );
  assert.equal(events[0].startsAtLocal, "2026-05-04T20:30:00+02:00");
  assert.equal(events[1].availableSeats, 0);
});

test("an empty catalogue is an empty list, not a failure", async () => {
  const recorder = recordingFetch(() => jsonResponse(200, { events: [] }));

  const events = unwrap(await makeClient(recorder).listEvents());

  assert.deepEqual(events, []);
});

test("a catalogue entry without its served hour or price is refused, never defaulted", async () => {
  for (const [field, expected] of [
    ["startsAtLocal", /startsAtLocal/],
    ["priceDisplay", /priceDisplay/],
  ] as const) {
    const recorder = recordingFetch(() => {
      const [first] = CATALOGUE_BODY.events;
      return jsonResponse(200, { events: [{ ...first, [field]: "" }] });
    });

    const problem = unwrapError(await makeClient(recorder).listEvents());

    assert.equal(problem.code, "unexpected_response");
    assert.equal(problem.status, 200);
    assert.match(problem.detail, expected);
  }
});

test("a catalogue without a screening list or a seat count is unreadable", async () => {
  // The list itself: an answer that names no catalogue cannot be rendered as one.
  const noList = recordingFetch(() => jsonResponse(200, { screenings: [] }));
  const notACatalogue = unwrapError(await makeClient(noList).listEvents());
  assert.equal(notACatalogue.code, "unexpected_response");
  assert.match(notACatalogue.detail, /screening list/);

  // One broken entry refuses the whole catalogue: the screen would otherwise have
  // to drop a screening or print "0 of 0 seats available", and both are claims the
  // API never made.
  for (const field of ["seatCount", "availableSeats"] as const) {
    const recorder = recordingFetch(() => {
      const [first] = CATALOGUE_BODY.events;
      const broken: Record<string, unknown> = { ...first };
      delete broken[field];
      return jsonResponse(200, { events: [broken] });
    });

    const problem = unwrapError(await makeClient(recorder).listEvents());
    assert.equal(problem.code, "unexpected_response");
    assert.match(problem.detail, /seat count/);
  }

  // An entry that is not even an object, and one that names no id to link to.
  const notAnEntry = recordingFetch(() => jsonResponse(200, { events: [42] }));
  const unnamed = unwrapError(await makeClient(notAnEntry).listEvents());
  assert.equal(unnamed.code, "unexpected_response");
  assert.match(unnamed.detail, /without an id/);
});

test("an unavailable catalogue stays the retryable code the API used", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(503, {
      type: "https://cinema.local/problems/service-unavailable",
      title: "Service unavailable",
      status: 503,
      code: "service_unavailable",
      detail: "the catalogue is temporarily unavailable; retry the request",
    }),
  );

  const problem = unwrapError(await makeClient(recorder).listEvents());

  assert.equal(problem.status, 503);
  assert.equal(problem.code, "service_unavailable");
  assert.match(problem.detail, /temporarily unavailable/);
});

test("a body that is not JSON or not the expected shape is unexpected_response", async () => {
  const garbage = createPublicClient({
    baseUrl: BASE_URL,
    fetch: () => Promise.resolve(new Response("<html>oops</html>", { status: 200 })),
  });
  const notJson = unwrapError(await garbage.listEventSeats("event-a"));
  assert.equal(notJson.code, "unexpected_response");

  const wrongShape = recordingFetch(() => jsonResponse(200, { eventId: "event-a" }));
  const missingSeats = unwrapError(await makeClient(wrongShape).listEventSeats("event-a"));
  assert.equal(missingSeats.code, "unexpected_response");

  const missingDeadline = recordingFetch(() =>
    jsonResponse(201, { id: "hold-1", eventId: "event-a", seatIds: [11] }),
  );
  const noExpiry = unwrapError(
    await makeClient(missingDeadline).createHold({
      eventId: "event-a",
      seatIds: [11],
      idempotencyKey: "key-1",
    }),
  );
  assert.equal(noExpiry.code, "unexpected_response");
  assert.match(noExpiry.detail, /expiresAt/);
});

// ── One screening, read by the page the buyer clicked (M3.5 · T5 · S3, S8) ────
//
// The catalogue names screenings; the seat page opens one. It answers with the same
// entry shape as the catalogue row, plus the two fields that only matter once the
// buyer is looking at a single screening: the stored status and whether the API
// still offers it. A screening that exists but is no longer on sale is a 200 that
// says so, so the page can explain itself instead of showing a dead seat map or
// claiming the screening never existed.

const SCREENING_BODY = {
  ...CATALOGUE_BODY.events[0],
  status: "cancelled",
  onSale: false,
};

test("getEvent reads one screening from the same entry shape the catalogue uses", async () => {
  const recorder = recordingFetch(() => jsonResponse(200, SCREENING_BODY));

  const screening = unwrap(await makeClient(recorder).getEvent("event-early"));

  const call = recorder.calls[0];
  assert.equal(call.url, `${BASE_URL}/api/v1/events/event-early`);
  assert.equal(call.init.method, "GET");
  assert.equal(call.init.cache, "no-store");
  assert.equal(headerValue(call.init, "Authorization"), undefined);
  assert.equal(headerValue(call.init, "Idempotency-Key"), undefined);

  // The sale state is served, never inferred: the page must be able to say why a
  // screening is not on sale, and must not compare a clock of its own to find out.
  assert.equal(screening.status, "cancelled");
  assert.equal(screening.onSale, false);
  // The venue's own hour travels verbatim, exactly as the catalogue carries it.
  assert.equal(screening.startsAtLocal, "2026-05-04T20:30:00+02:00");
  assert.equal(screening.startsAt, "2026-05-04T18:30:00Z");
  assert.equal(screening.priceCents, 1250);
  assert.equal(screening.priceDisplay, "12.50");
  assert.equal(screening.filmTitle, "The Long Take");
  assert.equal(screening.cinemaName, "Cinema Centro");
  assert.equal(screening.roomName, "Room 1");
});

test("getEvent escapes the event id and keeps an unknown screening a 404", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(404, {
      type: "https://cinema.local/problems/not-found",
      title: "Not found",
      status: 404,
      code: "not_found",
      detail: "the requested screening does not exist",
    }),
  );

  const problem = unwrapError(await makeClient(recorder).getEvent("a/b c"));

  assert.equal(recorder.calls[0].url, `${BASE_URL}/api/v1/events/a%2Fb%20c`);
  assert.equal(problem.code, "not_found");
  assert.equal(problem.status, 404);
  assert.match(problem.detail, /does not exist/);
});

test("an unavailable screening stays the retryable code the API used", async () => {
  const recorder = recordingFetch(() =>
    jsonResponse(503, {
      type: "https://cinema.local/problems/service-unavailable",
      title: "Service unavailable",
      status: 503,
      code: "service_unavailable",
      detail: "the screening is temporarily unavailable; retry the request",
    }),
  );

  const problem = unwrapError(await makeClient(recorder).getEvent("event-early"));

  assert.equal(problem.code, "service_unavailable");
  assert.equal(problem.status, 503);
});

test("a screening without its sale state is refused, not guessed", async () => {
  for (const [field, expected] of [
    ["onSale", /onSale/],
    ["status", /status/],
  ] as const) {
    const recorder = recordingFetch(() => {
      const body: Record<string, unknown> = { ...SCREENING_BODY };
      delete body[field];
      return jsonResponse(200, body);
    });

    const problem = unwrapError(await makeClient(recorder).getEvent("event-early"));

    assert.equal(problem.code, "unexpected_response", field);
    assert.match(problem.detail, expected, `${field} cannot be assumed`);
  }
});

test("the catalogue carries the same sale state as the single read", async () => {
  // One entry shape serves both routes, so the catalogue row and the screening read
  // cannot drift: a screening is on sale or not in exactly the same words.
  const recorder = recordingFetch(() => jsonResponse(200, CATALOGUE_BODY));

  const events = unwrap(await makeClient(recorder).listEvents());

  assert.equal(events[0].onSale, true);
  assert.equal(events[0].status, "on_sale");
  assert.equal(events[1].availableSeats, 0);
  assert.equal(events[1].onSale, true, "sold out is still on sale");
});
