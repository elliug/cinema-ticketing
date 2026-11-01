// /tickets — "my tickets", the list this browser kept (M3.5 · T7 · S3, S8).
//
// The screen exists because of what this application does not have. The demonstration
// session authenticates nobody and no route lists tickets (milestone log, L3 and L4), so
// "my tickets" **cannot** be a server-side query: the server has no idea whose browser is
// asking, and a list it invented would be a claim about an account that does not exist.
// What it can be is exactly what this browser tab kept from the purchases it made in it,
// and that is what it says it is — in the page header, above the list, and again under it.
//
// That makes this page the one surface of the milestone with browser state, so it is a
// server component that frames the screen honestly (its `h1`, the sentence about what the
// list is, the API's base URL for the PDF links) and hands the storage work to one client
// component, [KeptTicketList]. The client component re-reads every kept id from the box
// office through the server action below, so the browser still never learns where the API
// lives (`apps/web/src/app/reservations/[reservationID]/actions.ts` explains the same
// rule for the checkout).
//
// The states — records being read, a storage that will not answer, an empty list, and a
// list whose entries are each read or not — are described in the component that renders
// them, because they are its decision, not this file's.
//
// The route is server-rendered per request: the base URL and the box office's answers are
// per request, and nothing here may be cached.
import { PageHeader } from "@/components";
import { KeptTicketList } from "@/app/tickets/[ticketID]/ticket-view";
import type { TicketResponse } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";

export const dynamic = "force-dynamic";

/**
 * Reads one ticket: `GET /api/v1/tickets/{id}`.
 *
 * A server action, because the read must happen in this process: the API's base URL comes
 * from server-side environment variables, and the browser is never told where it is. A
 * read changes nothing, which is why the list can call it on every load and a reload can
 * never charge, hold or release anything.
 */
async function readTicketAction(ticketId: string): Promise<TicketResponse> {
  "use server";

  if (ticketId.trim() === "") {
    return {
      ok: false,
      problem: {
        type: "https://cinema.local/problems/invalid-input",
        title: "Invalid input",
        status: 0,
        code: "invalid_input",
        detail: "this page needs a ticket id",
      },
    };
  }

  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const result = await createPublicClient(config.value).getTicket(ticketId);
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }
  return { ok: true, ticket: result.value };
}

export default function TicketsPage() {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Tickets · M3.5"
        title="My tickets"
        description="The ticket ids this browser tab kept from the purchases it made in it — not an account, and not a list the box office keeps. Each one is read from the box office when this screen loads, and a ticket is opened by its own link. If this deployment sends mail and the purchase left an address, a link to these tickets was mailed to it as well — that link expires, so the PDF is still downloaded from each ticket."
      />
      <KeptTicketList readTicket={readTicketAction} />
    </main>
  );
}
