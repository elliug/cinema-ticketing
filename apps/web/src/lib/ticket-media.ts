// The two binary surfaces of a ticket — its QR image and its PDF — are served by this
// web application at same-origin paths, and forwarded to the API from the server.
//
// Why not link the API directly: the API base URL is an absolute address that only means
// something to the machine running the API. Opened from a phone or a second laptop on the
// same network, a link to `http://127.0.0.1:8080/api/v1/tickets/{id}/qr.svg` asks *that
// device* for the ticket — the image renders blank and the download answers "page not
// found", and neither of them says why. Same-origin paths keep working wherever the page
// is opened. They also keep the pre-signed object address out of the browser: the API's
// pre-signed S3 link names the bucket endpoint, which is just as local as the API itself.
//
// The PDF is forwarded as an attachment whose filename this side declares, matching the
// disposition the API would have set, so a download is named after its ticket whichever
// door the browser came through.
import { DEFAULT_PUBLIC_BASE_URL, PUBLIC_BASE_URL_ENV } from "./public-api.ts";

/** The path prefix this application serves ticket media under. */
export const TICKET_MEDIA_PREFIX = "/api/tickets";

/**
 * The API's public prefix. Mirrored rather than imported because the forwarded address is
 * built in one place and that place is here.
 */
const API_PUBLIC_PATH_PREFIX = "/api/v1";

/** The same-origin address of a ticket's QR image. */
export function ticketQRImageUrl(ticketID: string): string {
  return `${TICKET_MEDIA_PREFIX}/${encodeURIComponent(ticketID.trim())}/qr.svg`;
}

/**
 * The same-origin address of a ticket's PDF.
 *
 * It is a plain link target rather than a fetch: the browser follows it here, this side
 * asks the API for the bytes, and the page never handles them.
 *
 * The PDF is a record of the sale, not evidence that the ticket is valid: it was rendered
 * when the payment was captured, so a later void or a cancelled screening cannot appear in
 * it. `PublicClient.verifyTicket` is what answers whether the ticket still admits its
 * holder.
 */
export function ticketPDFUrl(ticketID: string): string {
  return `${TICKET_MEDIA_PREFIX}/${encodeURIComponent(ticketID.trim())}/pdf`;
}

export type TicketMediaKind = "pdf" | "qr.svg";

/** The media types this side declares, one per surface. */
const CONTENT_TYPE: Record<TicketMediaKind, string> = {
  pdf: "application/pdf",
  "qr.svg": "image/svg+xml",
};

/**
 * The attachment disposition for a ticket's PDF, byte for byte what `pdf.TicketAttachmentDisposition`
 * declares on the API side: the name says which ticket it is, and a browser never renders a
 * PDF inline that was meant to be kept.
 *
 * Control characters are stripped here, and `ticketMedia` refuses an identifier carrying them,
 * and both halves exist on purpose. The refusal is the rule: the identifier arrives from the
 * URL path, so `/api/tickets/%0d%0aX/pdf` decodes to a CR LF that would otherwise be copied
 * into a header value, and no real ticket identifier contains one. The strip is the reason the
 * rule cannot be bypassed later: this function is the one whose result becomes a header, and a
 * value written into a header must not depend on every caller having done its own check. It
 * strips rather than throws so a download stays named after its ticket instead of the whole
 * answer failing over a byte that cannot be printed anyway.
 */
export function ticketMediaAttachment(ticketID: string): string {
  const printable = stripControlCharacters(ticketID);
  const escaped = printable.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
  return `attachment; filename="ticket-${escaped}.pdf"`;
}

/**
 * How long the forwarded request may take before this side stops waiting.
 *
 * The forward is one API request — for the PDF it follows the API's redirect to the pre-signed
 * object address — and the bytes it brings back are one QR image or one PDF, some 27 KB and
 * some 3.4 KB as the M3 gate measured. Ten seconds is orders of magnitude above a healthy
 * transfer of that size even on a slow mobile link, and short enough that a buyer whose ticket
 * service is hanging sees "unavailable" rather than a page that never finishes. The deadline
 * also governs the body stream, because a fetch signal cannot be detached from an answer it
 * has already started; with payloads this small that cannot truncate a download that was going
 * to finish.
 */
export const TICKET_MEDIA_TIMEOUT_MS = 10_000;

export interface TicketMediaEnvironment {
  /** Defaults to the process environment. */
  readonly env?: Record<string, string | undefined>;
  /** Defaults to the platform fetch. */
  readonly fetchImpl?: typeof fetch;
  /** Defaults to `TICKET_MEDIA_TIMEOUT_MS`; kept overridable so a test need not wait it out. */
  readonly timeoutMs?: number;
}

/** The address the API serves this media at. */
function apiMediaUrl(
  kind: TicketMediaKind,
  ticketID: string,
  env: Record<string, string | undefined>,
): string {
  const configured = (env[PUBLIC_BASE_URL_ENV] ?? "").trim();
  const base = (configured === "" ? DEFAULT_PUBLIC_BASE_URL : configured).replace(/\/+$/, "");
  const id = encodeURIComponent(ticketID.trim());
  return `${base}${API_PUBLIC_PATH_PREFIX}/tickets/${id}/${kind}`;
}

function plainResponse(body: string, status: number): Response {
  return new Response(body, {
    status,
    headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store" },
  });
}

/** The bytes that cannot travel in a response header: the C0 controls and DEL. */
function isControlCharacter(character: string): boolean {
  const code = character.codePointAt(0) ?? 0;
  return code < 0x20 || code === 0x7f;
}

function hasControlCharacter(value: string): boolean {
  for (const character of value) {
    if (isControlCharacter(character)) return true;
  }
  return false;
}

function stripControlCharacters(value: string): string {
  return [...value].filter((character) => !isControlCharacter(character)).join("");
}

/**
 * Releases an upstream body this side will not read. A response that is dropped instead of
 * handed on keeps its stream — and therefore its connection — open until the upstream or the
 * garbage collector decides otherwise, so the body is cancelled rather than forgotten.
 *
 * It is not awaited: the answer must not wait on the upstream it has just given up on. A
 * cancel that refuses (a stream already locked, disturbed or errored) changes nothing here,
 * because these bytes will not be read either way.
 */
function releaseBody(upstream: Response): void {
  const body = upstream.body;
  if (body === null) return;
  body.cancel().catch(() => undefined);
}

/**
 * Answers one ticket media request: forward it to the API and return the bytes under this
 * origin.
 *
 * The failure shapes are kept apart on purpose, because a caller cannot otherwise tell them
 * apart. An empty identifier, and an identifier carrying a control character (which no real
 * ticket has, and which must not reach a header), are both `404`. An API that cannot be
 * reached, that does not answer within the deadline, or that answers with its own failure, is
 * `502`: the ticket is not missing, this side simply could not fetch it.
 */
export async function ticketMedia(
  kind: TicketMediaKind,
  ticketID: string,
  environment: TicketMediaEnvironment = {},
): Promise<Response> {
  const id = ticketID.trim();
  // A control character in the identifier is refused before the network, because it can only
  // come from a path somebody composed by hand: `/api/tickets/%0d%0aX/pdf` decodes to a CR LF
  // this side would otherwise carry into the `content-disposition` value it declares.
  if (id === "" || hasControlCharacter(id)) {
    return plainResponse("ticket not found\n", 404);
  }

  const env = environment.env ?? process.env;
  const fetchImpl = environment.fetchImpl ?? fetch;
  const timeoutMs = environment.timeoutMs ?? TICKET_MEDIA_TIMEOUT_MS;

  let upstream: Response;
  try {
    // `follow` matters: the API answers the PDF route with a redirect to a pre-signed
    // object address, and the browser must receive bytes rather than that address.
    // `signal` matters too: without a deadline, forwarding to an API that accepted the
    // connection and then never answers waits forever, and a buyer's GET never finishes.
    // `AbortSignal.timeout` is the platform's own bounded signal — available since Node
    // 17.3, and this application targets Node >= 20 — so no timer has to be cleared by hand.
    upstream = await fetchImpl(apiMediaUrl(kind, id, env), {
      cache: "no-store",
      redirect: "follow",
      signal: AbortSignal.timeout(timeoutMs),
    });
  } catch {
    // A deadline that expired lands here with the network failures, and for the same reason:
    // the ticket is not missing, this side could not fetch it.
    return plainResponse("the ticket service is unavailable\n", 502);
  }

  if (upstream.ok) {
    const headers: Record<string, string> = {
      "content-type": CONTENT_TYPE[kind],
      "cache-control": "no-store",
    };
    if (kind === "pdf") {
      headers["content-disposition"] = ticketMediaAttachment(id);
    }
    return new Response(upstream.body, { status: 200, headers });
  }

  // Neither failure keeps the upstream body: it is released before the error answer, and the
  // status mapping is untouched — a missing ticket stays `404`, everything else stays `502`.
  releaseBody(upstream);
  if (upstream.status === 404) {
    return plainResponse("ticket not found\n", 404);
  }
  return plainResponse("the ticket service is unavailable\n", 502);
}
