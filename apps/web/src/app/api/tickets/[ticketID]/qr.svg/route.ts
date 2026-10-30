// GET /api/tickets/{ticketID}/qr.svg — this application's own door to a ticket's QR image.
//
// Same-origin for the reason its PDF sibling is: the code is an image source, and an
// absolute API address renders blank when the page is opened from another device.
import { ticketMedia } from "@/lib/ticket-media";

export const dynamic = "force-dynamic";

interface TicketQRRouteContext {
  params: Promise<{ ticketID: string }>;
}

export async function GET(_request: Request, { params }: TicketQRRouteContext): Promise<Response> {
  const { ticketID } = await params;
  return ticketMedia("qr.svg", ticketID);
}
