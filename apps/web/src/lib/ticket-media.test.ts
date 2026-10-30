// The ticket's QR image and PDF must work at the address the page was opened from, which
// is why both are same-origin routes of this application rather than links to the API.
// These tests pin the addresses, the forwarded request and the declared answer — including
// the three failure shapes a page cannot otherwise tell apart.
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  ticketMedia,
  ticketMediaAttachment,
  ticketPDFUrl,
  ticketQRImageUrl,
  TICKET_MEDIA_PREFIX,
} from "./ticket-media.ts";

const API = "http://api.test";
const ENV = { API_BASE_URL: API };

interface RecordedRequest {
  url: string;
  redirect?: RequestRedirect;
}

/** A fetch stand-in that records what was asked and answers with one canned response. */
function recorder(response: Response | Error) {
  const requests: RecordedRequest[] = [];
  const fetchImpl = (async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), redirect: init?.redirect });
    if (response instanceof Error) throw response;
    return response;
  }) as typeof fetch;
  return { requests, fetchImpl };
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
