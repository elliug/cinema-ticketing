// /tickets/[ticketID] — the guest ticket page (M3 · T7 · S1, S3, S4).
//
// The page a buyer shows at the door and prints. It renders one thing: the
// `TicketDetail` projection the API serves (`GET /api/v1/tickets/{id}`), which is
// the same value the PDF is rendered from, so the two cannot describe different
// tickets. Nothing here computes a fact of its own:
//
//   - the screening time is `startsAtLocal`, printed exactly as served, because it
//     was rendered in the venue's zone by the API and re-formatting it in the
//     browser would print the viewer's zone instead;
//   - the seat is the row label and number the venue paints at the door;
//   - `orderAmountDisplay` is labelled as the order total, because the money was
//     taken once for the whole reservation and this ticket is one seat of it;
//   - validity is not this page's judgement. It asks the verification endpoint
//     (`POST /api/v1/tickets/verify`) with the served code and prints the answer,
//     including the machine-readable `reason` when the ticket does not admit its
//     holder (`voided`, `event_cancelled`).
//
// The QR image and the PDF are the API's routes, addressed as an image source and
// a link: the browser fetches both itself, so this page needs no QR dependency and
// never holds the PDF bytes. The PDF is a record of the sale, not proof of
// validity — see ./ticket-view.tsx, which says so on the page.
//
// Guest access is by the ticket id, which is a capability (see the tickets package
// docs): a forwarded link opens the ticket for whoever holds it, and nothing lists
// tickets for an order.
import {
  createPublicClient,
  resolvePublicConfig,
  ticketPDFUrl,
  ticketQRImageUrl,
} from "@/lib/public-api";
import type { PublicProblem } from "@/lib/public-api";
import type { Checked } from "./ticket-view";
import { TicketView } from "./ticket-view";

export const dynamic = "force-dynamic";

interface TicketPageProps {
  params: Promise<{ ticketID: string }>;
}

/** What the page decided about the presented code. */

export default async function TicketPage({ params }: TicketPageProps) {
  const { ticketID } = await params;
  const ticketId = ticketID.trim();

  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return (
      <main id="main-content" className="foundation">
        <a className="skip-link" href="#main-content">
          Skip to content
        </a>
        <ProblemCard
          title="The ticket page is not configured"
          detail={config.error.detail}
        />
      </main>
    );
  }

  const client = createPublicClient(config.value);
  const result = await client.getTicket(ticketId);

  if (!result.ok) {
    return (
      <main id="main-content" className="foundation">
        <a className="skip-link" href="#main-content">
          Skip to content
        </a>
        <ProblemCard
          title={
            result.error.code === "not_found"
              ? "This ticket does not exist"
              : "The ticket could not be loaded"
          }
          detail={result.error.detail}
          problem={result.error}
        />
      </main>
    );
  }

  const ticket = result.value;
  // The code the API served is the only thing presented to the check: the page
  // never assembles a token of its own, so what it says is what a door would say.
  const verification = await client.verifyTicket(ticket.qrToken);
  const checked: Checked = verification.ok
    ? { kind: "verified", verification: verification.value }
    : { kind: "refused", problem: verification.error };

  return (
    <main id="main-content" className="foundation">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>
      <TicketView
        ticket={ticket}
        checked={checked}
        qrImageUrl={ticketQRImageUrl(config.value.baseUrl, ticket.ticketId)}
        pdfUrl={ticketPDFUrl(config.value.baseUrl, ticket.ticketId)}
      />
    </main>
  );
}

function ProblemCard({
  title,
  detail,
  problem,
}: {
  title: string;
  detail: string;
  problem?: PublicProblem;
}) {
  return (
    <section className="card problems" aria-labelledby="ticket-problem">
      <h2 id="ticket-problem">{title}</h2>
      {problem === undefined ? null : (
        <p>
          <code>{problem.code}</code> · HTTP {problem.status}
        </p>
      )}
      <p>{detail}</p>
      <p className="notice">
        {problem?.code === "not_found"
          ? "A ticket is opened by its own link: check the address you were given."
          : "This is temporary. Reload the page to try again; nothing about your ticket has changed."}
      </p>
    </section>
  );
}
