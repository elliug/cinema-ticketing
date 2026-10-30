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
 */
export function ticketMediaAttachment(ticketID: string): string {
  const escaped = ticketID.replace(/\\/g, "\\\\").replace(/"/g, '\\"');
  return `attachment; filename="ticket-${escaped}.pdf"`;
}

export interface TicketMediaEnvironment {
  /** Defaults to the process environment. */
  readonly env?: Record<string, string | undefined>;
  /** Defaults to the platform fetch. */
  readonly fetchImpl?: typeof fetch;
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

/**
 * Answers one ticket media request: forward it to the API and return the bytes under this
 * origin.
 *
 * The three failure shapes are kept apart on purpose, because a caller cannot otherwise
 * tell them apart. An empty identifier and an unknown ticket are both `404`. An API that
 * cannot be reached, or that answers with its own failure, is `502`: the ticket is not
 * missing, this side simply could not fetch it.
 */
export async function ticketMedia(
  kind: TicketMediaKind,
  ticketID: string,
  environment: TicketMediaEnvironment = {},
): Promise<Response> {
  const id = ticketID.trim();
  if (id === "") {
    return plainResponse("ticket not found\n", 404);
  }

  const env = environment.env ?? process.env;
  const fetchImpl = environment.fetchImpl ?? fetch;

  let upstream: Response;
  try {
    // `follow` matters: the API answers the PDF route with a redirect to a pre-signed
    // object address, and the browser must receive bytes rather than that address.
    upstream = await fetchImpl(apiMediaUrl(kind, id, env), {
      cache: "no-store",
      redirect: "follow",
    });
  } catch {
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

  if (upstream.status === 404) {
    return plainResponse("ticket not found\n", 404);
  }
  return plainResponse("the ticket service is unavailable\n", 502);
}
