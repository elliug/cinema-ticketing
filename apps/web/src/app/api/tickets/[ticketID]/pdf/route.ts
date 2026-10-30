// GET /api/tickets/{ticketID}/pdf — this application's own door to a ticket's PDF.
//
// Same-origin on purpose: the page is opened from wherever the buyer is, and the API's
// absolute address is meaningless there. The forwarding, the declared attachment filename
// and the status mapping are owned by lib/ticket-media.ts and pinned by its tests.
import { ticketMedia } from "@/lib/ticket-media";

// Nothing here may be cached: a void can arrive between two downloads.
export const dynamic = "force-dynamic";

interface TicketPDFRouteContext {
  params: Promise<{ ticketID: string }>;
}

export async function GET(_request: Request, { params }: TicketPDFRouteContext): Promise<Response> {
  const { ticketID } = await params;
  return ticketMedia("pdf", ticketID);
}
