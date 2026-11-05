"use client";

// The tickets one signed link opened, and what this tab did with them (T4 · S3).
//
// The page above redeems the token and answers with the state; this module adds the one
// thing a server cannot know: whether this browser agreed to keep the tickets. There is no
// identity in this application (milestone log, L3), so "my tickets" is only ever what a tab
// kept from the purchases it made in it — which means the ids the link just opened are
// written here, in an effect, and never during render: a write belongs to an event, not to
// a paint, and rendering twice must not mean storing twice.
//
// Everything else on the surface is still server-rendered HTML, so the tickets, their
// counts and this link's expiry read with JavaScript disabled. Only the storage notice
// needs it, and it is the surface's one polite live region.
//
// Two promises are kept in the display rather than in a comment:
//
//   - **every ticket is directly openable**, through the same link shape the checkout and
//     the list already print. A browser that refused the storage must not leave the buyer
//     with nothing, so the links are not a convenience here; they are the fallback;
//   - **the list is described as what it is** — this tab's own note of what it bought, not
//     an account and not a record the box office keeps (lib/ticket-link.ts owns the words).
import { useEffect, useRef, useState } from "react";

import { IssuedTickets } from "@/app/reservations/[reservationID]/payment-panel";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader } from "@/components/ui/card";
import type { KeptTickets } from "@/lib/issued-tickets";
import { keptTicketStorage, rememberIssuedTicket } from "@/lib/issued-tickets";
import type { LinkRetention, OpenedTicketLink } from "@/lib/ticket-link";
import { describeLinkRetention } from "@/lib/ticket-link";

export interface OpenedTicketsProps {
  /**
   * The opened state the page derived, not a second copy of the API's answer: this
   * component renders the view lib/ticket-link.ts produced and decides nothing about what
   * the link meant.
   */
  view: OpenedTicketLink;
}

export function OpenedTickets({ view }: OpenedTicketsProps) {
  const retention = useKeptTickets(view.ticketIds);
  const count = view.ticketIds.length;

  return (
    <>
      <Card aria-labelledby="link-opened">
        <CardHeader>
          <div className="flex flex-wrap items-center gap-2">
            <h2 id="link-opened" className="m-0 text-xl leading-tight font-semibold">
              {view.headline}
            </h2>
            {/* The count as a word, so colour and shape carry nothing on their own. */}
            <Badge variant="success">
              {count === 1 ? "1 entrada" : `${count} entradas`}
            </Badge>
          </div>
          <CardDescription>{view.message}</CardDescription>
        </CardHeader>

        <CardContent className="flex flex-col gap-3">
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Este enlace deja de abrir el{" "}
            <time dateTime={view.expiresAt} className="font-mono font-medium">
              {view.expiresAt}
            </time>
            . Los enlaces de abajo no son lo mismo: no caducan, y son los que abren una entrada
            en el acceso.
          </p>

          {/* The checkout's own link shape, imported rather than copied: one ticket is
              printed the same way wherever it is offered, and a second copy of that markup
              would let two screens disagree about what a ticket link is. */}
          <div className="rounded-lg border border-dashed border-border bg-muted p-4">
            <IssuedTickets ticketIds={view.ticketIds} />
          </div>

          <p className="m-0 max-w-prose text-sm text-muted-foreground">{view.nextStep}</p>

          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            El pedido que nombra este enlace: <code className="break-all">{view.reservationId}</code>
            . La taquilla lo responde por ese identificador, y ninguna página de aquí comprueba
            quién lo pregunta.
          </p>
        </CardContent>
      </Card>

      {/* The one polite live region of this surface, and the only thing on it the page does
          not know when it is rendered: whether this tab could keep the tickets. */}
      <p role="status" className="m-0 max-w-prose text-sm font-medium">
        {describeLinkRetention(retention, count)}
      </p>
    </>
  );
}

/**
 * Keeps every ticket the link opened in this tab's list, and reports whether it could.
 *
 * Opening a ticket is the moment this browser learns about it — the box office has no list
 * to ask (L3) — so the link is the only source of truth and this is where its answer is
 * recorded. `rememberIssuedTicket` is idempotent, so a double render costs nothing; the ref
 * only avoids asking the storage the same question twice.
 */
function useKeptTickets(ticketIds: readonly string[]): LinkRetention {
  const [retention, setRetention] = useState<LinkRetention>("writing");
  const remembered = useRef<string | null>(null);
  const key = ticketIds.join(" ");

  useEffect(() => {
    if (remembered.current === key) {
      return;
    }
    remembered.current = key;

    const storage = keptTicketStorage(window);
    if (storage === null) {
      setRetention("refused");
      return;
    }
    // Every ticket of the link is kept, not just the first: one refused write would leave
    // the list disagreeing with the mail, and the page would have to say which of them it
    // dropped.
    let refused = false;
    for (const ticketId of ticketIds) {
      const kept: KeptTickets = rememberIssuedTicket(storage, ticketId);
      if (kept.kind === "unavailable") {
        refused = true;
      }
    }
    setRetention(refused ? "refused" : "kept");
  }, [key, ticketIds]);

  return retention;
}
