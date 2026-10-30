// /tickets/[ticketID] — the ticket a buyer shows at the door (M3.5 · T7 · S3, S8).
//
// The page re-reads the ticket on every load. It does not trust navigation state, a
// cached payment answer or anything this browser kept: `GET /api/v1/tickets/{id}` is the
// one projection the ticket and its PDF are rendered from, so a reload can only answer
// the same thing, and a link opened on another device shows the same ticket.
//
// It asks the box office one question too — `POST /api/v1/tickets/verify` with the code
// the API itself served — because whether a ticket admits its holder is the door's
// question and only the server answers it (S8). The page never assembles a token, never
// reads validity from the stored status or the PDF, and never smooths an unreadable check
// into "invalid": a failed check is its own sentence.
//
// The states this page can render, each one a different answer:
//
//   1. loading                    a real region with a sentence, not a blank page;
//   2. the address names no ticket — nothing to read, and where to start again;
//   3. the box office does not know it (404)
//                                 final: asking again cannot answer it;
//   4. the ticket could not be read (503, network, 5xx)
//                                 retryable, and never drawn as a ticket;
//   5. the ticket                 the screening, the venue's own hour, the seat, the
//                                 order total, the entry code, the PDF, and the
//                                 verification answer in words.
//
// Guest access is by the ticket id, which is a capability: a forwarded link opens the
// ticket for whoever holds it, and no route lists tickets (milestone log, L3). The page
// says so rather than implying that it checked who you are.
//
// The route is server-rendered per request: validity is answered per request, so nothing
// here may be cached or prerendered.
import Link from "next/link";
import { Suspense } from "react";

import { PageHeader } from "@/components";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { TicketCheck } from "@/lib/issued-tickets";
import { describeKeptTicket } from "@/lib/issued-tickets";
import {
  createPublicClient,
  resolvePublicConfig,
  ticketPDFUrl,
  ticketQRImageUrl,
} from "@/lib/public-api";
import type { PublicProblem } from "@/lib/public-api";
import { TicketView } from "./ticket-view";

export const dynamic = "force-dynamic";

interface TicketPageProps {
  params: Promise<{ ticketID: string }>;
}

export default async function TicketPage({ params }: TicketPageProps) {
  const { ticketID } = await params;
  const ticketId = ticketID.trim();

  // The skip link and the `#main-content` target belong to the root layout: this page
  // used to render a second one, which is one skip link too many for a keyboard.
  return (
    <main id="main-content" className="foundation">
      <Suspense fallback={<TicketLoading />}>
        <TicketSurface ticketId={ticketId} />
      </Suspense>
    </main>
  );
}

/** Reads the ticket, asks for the code check, and decides which state is the honest one. */
async function TicketSurface({ ticketId }: { ticketId: string }) {
  if (ticketId === "") {
    return <MissingTicket />;
  }

  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    // Not reachable today (the configuration always resolves), but a page may not
    // pretend to have read something it could not configure.
    return <TicketReadProblem problem={config.error} ticketId={ticketId} />;
  }

  const client = createPublicClient(config.value);
  const read = await client.getTicket(ticketId);
  if (!read.ok) {
    // One rule turns a failed read into words, and the list screen uses the same one, so
    // a stale id reads identically in both places.
    return <TicketReadProblem problem={read.error} ticketId={ticketId} />;
  }

  const ticket = read.value;
  // The code the API served is the only thing presented to the check: what this page
  // says is what a door would say.
  const verification = await client.verifyTicket(ticket.qrToken);
  const checked: TicketCheck = verification.ok
    ? { kind: "verified", verification: verification.value }
    : { kind: "refused", problem: verification.error };

  const title =
    ticket.event.filmTitle.trim() === "" ? "Your ticket" : ticket.event.filmTitle;
  const venue = [ticket.event.cinemaName, ticket.event.roomName]
    .map((part) => part.trim())
    .filter((part) => part !== "")
    .join(" · ");

  return (
    <>
      <PageHeader
        eyebrow="Ticket · M3.5"
        title={title}
        description={`${venue === "" ? "Venue not served." : `${venue}.`} Times are the venue's own clock. This page is opened by the ticket's own link: nothing here checks who you are, and the link is what opens it.`}
      />
      <TicketView
        ticket={ticket}
        checked={checked}
        qrImageUrl={ticketQRImageUrl(config.value.baseUrl, ticket.ticketId)}
        pdfUrl={ticketPDFUrl(config.value.baseUrl, ticket.ticketId)}
      />
    </>
  );
}

/** The address names no ticket: there is nothing to read and nothing to check. */
function MissingTicket() {
  return (
    <>
      <PageHeader
        eyebrow="Ticket · M3.5"
        title="Your ticket"
        description="This address does not name a ticket, so there is nothing to read."
      />
      <Alert variant="destructive" role="alert" aria-labelledby="ticket-missing">
        <h2 id="ticket-missing" className="m-0 text-base font-semibold">
          This address names no ticket
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">
            A ticket is opened by its own identifier. Open it from the link your payment
            returned, or from the list of tickets this browser kept.
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            <Button asChild size="sm">
              <Link href="/tickets">My tickets</Link>
            </Button>
            <Button asChild variant="outline" size="sm">
              <Link href="/events">See what is on sale</Link>
            </Button>
          </div>
        </AlertDescription>
      </Alert>
    </>
  );
}

/**
 * The ticket could not be read, and the reasons stay apart.
 *
 * A `404` is final — no retry answers it, and the way forward is the payment's link — while
 * a `503` or an unreachable API is worth asking again. Neither is ever drawn as a ticket
 * with no seat or no amount: an unreadable ticket and an empty one are different facts.
 */
function TicketReadProblem({
  problem,
  ticketId,
}: {
  problem: PublicProblem;
  ticketId: string;
}) {
  // The same mapping the list screen uses, so a stale id reads the same in both places.
  const row = describeKeptTicket({ ok: false, problem });

  return (
    <>
      <PageHeader
        eyebrow="Ticket · M3.5"
        title="Your ticket"
        description="The box office was asked for this ticket, and the answer did not describe one."
      />
      <Alert
        variant="destructive"
        role="alert"
        aria-labelledby="ticket-problem"
        className="flex flex-col gap-2"
      >
        <h2 id="ticket-problem" className="m-0 text-base font-semibold">
          {row.kind === "unknown" ? row.headline : "The ticket could not be loaded"}
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">
            <code>{problem.code}</code> · HTTP {problem.status}
          </p>
          <p className="m-0">{problem.detail}</p>
          <p className="m-0">
            {row.kind === "unknown" || row.kind === "unavailable"
              ? row.nextStep
              : "This page does not interpret the refusal into a verdict about the ticket."}
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            {row.kind === "unavailable" ? (
              <Button asChild size="sm">
                <Link href={`/tickets/${encodeURIComponent(ticketId)}`}>Ask again</Link>
              </Button>
            ) : null}
            <Button asChild variant="outline" size="sm">
              <Link href="/tickets">My tickets</Link>
            </Button>
          </div>
        </AlertDescription>
      </Alert>
    </>
  );
}

/**
 * The ticket while it is being read.
 *
 * A real region with a heading and a sentence, so a slow read is announced instead of
 * looking like a broken page; the skeleton is decorative and hidden from assistive
 * technology by the primitive itself.
 */
function TicketLoading() {
  return (
    <>
      <PageHeader
        eyebrow="Ticket · M3.5"
        title="Your ticket"
        description="Reading the ticket, its screening and its code from the box office."
      />
      <section aria-labelledby="ticket-loading" className="flex flex-col gap-3">
        <h2 id="ticket-loading">Your ticket</h2>
        <p role="status" className="m-0 max-w-prose">
          <span className="text-muted-foreground">Loading the ticket…</span>
        </p>
        <Card aria-hidden="true" className="flex flex-col gap-3 p-5">
          {[0, 1, 2, 3].map((row) => (
            <Skeleton key={row} className="h-4 w-2/3" />
          ))}
        </Card>
      </section>
    </>
  );
}
