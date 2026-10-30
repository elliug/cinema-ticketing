// The ticket's QR image and PDF must work at the address the page was opened from, which
// is why both are same-origin routes of this application rather than links to the API.
// These tests pin the addresses, the forwarded request and the declared answer — including
// the three failure shapes a page cannot otherwise tell apart.
import assert from "node:assert/strict";
import { test } from "node:test";

import { PUBLIC_PATH_PREFIX } from "./public-api.ts";
import {
  ticketMedia,
  ticketMediaAttachment,
  ticketPDFUrl,
  ticketQRImageUrl,
  TICKET_MEDIA_PREFIX,
  TICKET_MEDIA_TIMEOUT_MS,
} from "./ticket-media.ts";

const API = "http://api.test";
const ENV = { API_BASE_URL: API };

interface RecordedRequest {
  url: string;
  redirect?: RequestRedirect;
  signal?: AbortSignal | null;
}

/** A fetch stand-in that records what was asked and answers with one canned response. */
function recorder(response: Response | Error) {
  const requests: RecordedRequest[] = [];
  const fetchImpl = (async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), redirect: init?.redirect, signal: init?.signal });
    if (response instanceof Error) throw response;
    return response;
  }) as typeof fetch;
  return { requests, fetchImpl };
}

/**
 * A fetch stand-in for the API that accepts the connection and never answers, the shape a
 * hung upstream has: it settles only when the signal it was given aborts. A forward that
 * carries no signal therefore never settles either, and says so instead of hanging the test.
 */
function neverAnswering() {
  let forwarded: AbortSignal | null | undefined;
  const fetchImpl = ((_input: RequestInfo | URL, init?: RequestInit) => {
    forwarded = init?.signal;
    const signal = init?.signal;
    return new Promise<Response>((_resolve, reject) => {
      if (!signal) {
        reject(new Error("the forwarded request carried no abort signal"));
        return;
      }
      // An expired deadline is already aborted and will never fire an event: checking it first
      // is what lets a test drive this path without waiting on a clock.
      if (signal.aborted) {
        reject(signal.reason);
        return;
      }
      signal.addEventListener("abort", () => reject(signal.reason));
    });
  }) as typeof fetch;
  return { fetchImpl, forwardedSignal: () => forwarded };
}

/**
 * An upstream body that reports whether it was released. A failed forward that returns a
 * fresh error response without touching this body leaks the connection it was streaming on.
 */
function releasableBody() {
  let cancelled = false;
  const body = new ReadableStream<Uint8Array>({
    cancel() {
      cancelled = true;
    },
  });
  return { body, released: () => cancelled };
}

/** C0 control characters and DEL, the bytes that cannot appear in a response header. */
function hasControlCharacter(value: string): boolean {
  return [...value].some((character) => {
    const code = character.codePointAt(0) ?? 0;
    return code < 0x20 || code === 0x7f;
  });
}

test("the QR image and the PDF are addressed on this application's own origin", () => {
  assert.equal(ticketQRImageUrl("ticket-1"), `${TICKET_MEDIA_PREFIX}/ticket-1/qr.svg`);
  assert.equal(ticketPDFUrl("ticket-1"), `${TICKET_MEDIA_PREFIX}/ticket-1/pdf`);
  // Same-origin means no scheme and no host: a device that opened the page from anywhere
  // asks the same server back.
  assert.ok(!ticketPDFUrl("ticket-1").includes("://"));
  // An identifier is escaped and trimmed exactly as the API would receive it.
  assert.equal(ticketPDFUrl("  ticket/1  "), `${TICKET_MEDIA_PREFIX}/ticket%2F1/pdf`);
  assert.equal(ticketQRImageUrl("ticket 1"), `${TICKET_MEDIA_PREFIX}/ticket%201/qr.svg`);
});

// The forward's address is built on the public client's prefix, not on a second copy of it
// (review finding R3-001). A copy is invisible until the API's prefix changes and only one of
// the two follows, which is a defect nobody would look for in this file.
test("the forwarded address is built on the public client's own path prefix", async () => {
  const { requests, fetchImpl } = recorder(new Response("<svg/>", { status: 200 }));
  await ticketMedia("qr.svg", "ticket-1", { env: ENV, fetchImpl });

  assert.equal(PUBLIC_PATH_PREFIX, "/api/v1", "the prefix the API serves");
  assert.equal(requests[0]?.url, `${API}${PUBLIC_PATH_PREFIX}/tickets/ticket-1/qr.svg`);
});

test("the declared filename is the ticket's own, in the API's own words", () => {
  assert.equal(ticketMediaAttachment("ticket-1"), 'attachment; filename="ticket-ticket-1.pdf"');
  // A quote in the identifier cannot break out of the quoted filename.
  assert.equal(
    ticketMediaAttachment('t"1'),
    'attachment; filename="ticket-t\\"1.pdf"',
  );
});

test("the QR image is forwarded to the API and declared as an image", async () => {
  const { requests, fetchImpl } = recorder(new Response("<svg/>", { status: 200 }));
  const response = await ticketMedia("qr.svg", "ticket-1", { env: ENV, fetchImpl });

  assert.equal(response.status, 200);
  assert.equal(response.headers.get("content-type"), "image/svg+xml");
  assert.equal(response.headers.get("cache-control"), "no-store");
  assert.equal(response.headers.get("content-disposition"), null);
  assert.equal(requests.length, 1);
  assert.equal(requests[0]?.url, `${API}/api/v1/tickets/ticket-1/qr.svg`);
});

test("the PDF is forwarded following the API's pre-signed redirect and declared as an attachment", async () => {
  const { requests, fetchImpl } = recorder(new Response("%PDF-1.4", { status: 200 }));
  const response = await ticketMedia("pdf", "ticket-1", { env: ENV, fetchImpl });

  assert.equal(response.status, 200);
  assert.equal(response.headers.get("content-type"), "application/pdf");
  assert.equal(
    response.headers.get("content-disposition"),
    'attachment; filename="ticket-ticket-1.pdf"',
  );
  // `follow` is the point: the browser must receive the document, never the object address.
  assert.equal(requests[0]?.redirect, "follow");
  assert.equal(await response.text(), "%PDF-1.4");
});

test("a configured base with a trailing slash does not double up in the forwarded address", async () => {
  const { requests, fetchImpl } = recorder(new Response("<svg/>", { status: 200 }));
  await ticketMedia("qr.svg", "ticket-1", { env: { API_BASE_URL: `${API}/` }, fetchImpl });
  assert.equal(requests[0]?.url, `${API}/api/v1/tickets/ticket-1/qr.svg`);
});

test("an empty identifier is answered without reaching the network", async () => {
  const { requests, fetchImpl } = recorder(new Response("should not be reached", { status: 200 }));
  const response = await ticketMedia("pdf", "   ", { env: ENV, fetchImpl });

  assert.equal(response.status, 404);
  assert.equal(requests.length, 0);
});

test("an unknown ticket stays a 404, because the ticket is missing rather than unreachable", async () => {
  const { fetchImpl } = recorder(new Response("not found", { status: 404 }));
  const response = await ticketMedia("pdf", "ticket-1", { env: ENV, fetchImpl });
  assert.equal(response.status, 404);
});

test("an unreachable API is a 502, never a 404 that would read as a deleted ticket", async () => {
  const { fetchImpl } = recorder(new Error("connect ECONNREFUSED"));
  const response = await ticketMedia("qr.svg", "ticket-1", { env: ENV, fetchImpl });
  assert.equal(response.status, 502);
});

test("an API that fails its own way is a 502 rather than a status passed through", async () => {
  const { fetchImpl } = recorder(new Response("boom", { status: 500 }));
  const response = await ticketMedia("pdf", "ticket-1", { env: ENV, fetchImpl });
  assert.equal(response.status, 502);
});

// R4-001: a forward with no deadline waits forever when the API accepts the connection and
// never answers, and the buyer's page never finishes. The wait must be bounded, and its end
// is the same answer an unreachable API already gets: this side could not fetch the ticket.
test("an API that accepts the connection and never answers is a bounded 502", async () => {
  const { fetchImpl, forwardedSignal } = neverAnswering();
  // An expired deadline, injected rather than waited out: `AbortSignal.timeout` schedules its
  // expiry on an unref'd timer, so a test double that holds nothing else can have its event loop
  // drained before the deadline fires — which is exactly how this test passed locally and failed
  // in CI. The path exercised is the same one an expired deadline takes.
  const response = await ticketMedia("pdf", "ticket-1", {
    env: ENV,
    fetchImpl,
    signal: AbortSignal.abort(new DOMException("deadline expired", "TimeoutError")),
  });

  assert.ok(
    forwardedSignal() instanceof AbortSignal,
    "the forwarded request must carry an abort signal",
  );
  assert.equal(response.status, 502);
  assert.equal(await response.text(), "the ticket service is unavailable\n");
});

test("the forwarded deadline is bounded by default, not the platform's patience", async () => {
  const { requests, fetchImpl } = recorder(new Response("<svg/>", { status: 200 }));
  await ticketMedia("qr.svg", "ticket-1", { env: ENV, fetchImpl });

  // Every forward carries a deadline, and it is a wait a person would sit through rather
  // than one measured in minutes.
  assert.ok(requests[0]?.signal instanceof AbortSignal, "a default deadline is carried");
  assert.ok(Number.isFinite(TICKET_MEDIA_TIMEOUT_MS));
  assert.ok(TICKET_MEDIA_TIMEOUT_MS >= 5_000 && TICKET_MEDIA_TIMEOUT_MS <= 30_000);
});

// R1-header-control-chars: the identifier arrives from the URL path, so `/api/tickets/%0d%0aX/pdf`
// decodes to a CR LF this side would copy into `content-disposition`. No real ticket identifier
// contains a control character, so such a request names no ticket: refused before the network.
test("an identifier carrying control characters is a 404 before any network call", async () => {
  const { requests, fetchImpl } = recorder(new Response("should not be reached", { status: 200 }));

  for (const id of ["ticket\r\nX", "ticket\u0000X", "ticket\u001bX", "ticket\u007fX"]) {
    const response = await ticketMedia("pdf", id, { env: ENV, fetchImpl });
    assert.equal(response.status, 404, `identifier ${JSON.stringify(id)} must be a 404`);
    assert.equal(await response.text(), "ticket not found\n");
  }

  assert.equal(requests.length, 0);
});

// The refusal above only protects the route. The disposition builder must not be able to emit a
// control character on its own, because it is the value a header is written from.
test("the declared filename cannot carry a control character, even when called directly", () => {
  const attachment = ticketMediaAttachment("ticket\r\nX\u0000");

  assert.ok(
    !hasControlCharacter(attachment),
    `a control character survived into the disposition: ${JSON.stringify(attachment)}`,
  );
  assert.equal(attachment, 'attachment; filename="ticket-ticketX.pdf"');
});

// R4-002: returning a fresh error response without touching the upstream body leaves a streamed
// answer holding its connection open. The status mapping stays exactly as it was.
test("a 404 from the API releases the upstream body before answering", async () => {
  const upstream = releasableBody();
  const fetchImpl = (async () => new Response(upstream.body, { status: 404 })) as typeof fetch;

  const response = await ticketMedia("pdf", "ticket-1", { env: ENV, fetchImpl });

  assert.equal(response.status, 404);
  assert.equal(await response.text(), "ticket not found\n");
  assert.equal(upstream.released(), true);
});

test("any other non-ok answer releases the upstream body and stays a 502", async () => {
  const upstream = releasableBody();
  const fetchImpl = (async () => new Response(upstream.body, { status: 503 })) as typeof fetch;

  const response = await ticketMedia("qr.svg", "ticket-1", { env: ENV, fetchImpl });

  assert.equal(response.status, 502);
  assert.equal(await response.text(), "the ticket service is unavailable\n");
  assert.equal(upstream.released(), true);
});

// The bytes that are served must not be released: they are the answer.
test("a served body is handed to the caller rather than released", async () => {
  const upstream = releasableBody();
  const fetchImpl = (async () => new Response(upstream.body, { status: 200 })) as typeof fetch;

  const response = await ticketMedia("qr.svg", "ticket-1", { env: ENV, fetchImpl });

  assert.equal(response.status, 200);
  assert.equal(upstream.released(), false);
});
