"use client";

// The presentation of the two ticket surfaces (M3 · T7, M3.5 · T7 · S1, S3, S8):
// one ticket, and the list of the tickets this browser tab kept.
//
// Both live in one module because they print the same things — a screening, a seat, an
// order total, the entry code and the PDF — and because a second copy of that shape is
// how two screens start disagreeing about what a ticket is. Neither of them decides a
// fact of its own:
//
//   - the screening time is the served `startsAtLocal` string, printed verbatim inside a
//     `<time>` whose machine value is the UTC instant. Re-formatting it here would print
//     the viewer's zone on a page whose point is the venue's;
//   - the amount is labelled **order total**: it is the money taken for the whole
//     reservation, not this seat's price (which is why the API names its keys
//     `orderAmount*`);
//   - validity is the answer to `POST /api/v1/tickets/verify`, in words, from
//     lib/issued-tickets.ts — never the stored ticket status, never the PDF (S8);
//   - the PDF is a **link** the browser follows to the API's short-lived redirect, and it
//     is described as a record of the sale printed when the payment was captured. It
//     cannot contain a later void or a cancelled screening, so no page may present it as
//     proof that a ticket is good;
//   - the QR image is what a door checks, and its `alt` names the ticket rather than the
//     object, because "QR code" repeated on every ticket names nothing.
//
// Why a client module at all: the *list* is browser state. There is no identity in this
// application (milestone log, L3), so the "my tickets" screen cannot be a server-side
// query — it can only show what this tab kept, which means reading storage after mount.
// The ticket view shares the module because it is where a ticket is opened, and opening
// one is what makes this tab remember it. Everything either surface prints is still
// server-rendered HTML, so both read with JavaScript disabled; only the list's own
// storage work needs it.
//
// Accessibility: one `h1` per surface (the page header's, the screening or the page's
// own name — never a heading in here), exactly one polite live region per surface, every
// action a focus-ringed button or link, and no state carried by colour alone: each state
// prints its word, and its tone only reinforces it.
import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

// The checkout's own link shape, imported rather than copied: one order's tickets are
// printed by the confirmation and by this list, and two copies of that markup would let
// the two disagree about what a ticket link is.
import { IssuedTickets } from "@/app/reservations/[reservationID]/payment-panel";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { KeptTicketRow, KeptTickets, TicketCheck, ValidityView } from "@/lib/issued-tickets";
import {
  describeKeptTicket,
  describeTicketValidity,
  forgetIssuedTicket,
  keptTicketStorage,
  readKeptTickets,
  rememberIssuedTicket,
  ticketSeatLabel,
} from "@/lib/issued-tickets";
import type { PublicTicket, TicketResponse } from "@/lib/public-api";

export interface TicketViewProps {
  ticket: PublicTicket;
  /** The answer to the code check the page asked for, not a verdict of this module. */
  checked: TicketCheck;
  /** `GET /api/v1/tickets/{id}/qr.svg` — the signed token, rendered by the API. */
  qrImageUrl: string;
  /** `GET /api/v1/tickets/{id}/pdf` — a short-lived pre-signed redirect. */
  pdfUrl: string;
}

/** One ticket, as the page a buyer shows at the door renders it. */
export function TicketView({ ticket, checked, qrImageUrl, pdfUrl }: TicketViewProps) {
  const validity = describeTicketValidity(checked);
  const retention = useKeptInThisTab(ticket.ticketId);
  const seats = ticket.seats.map(ticketSeatLabel);
  const startsAt = ticket.event.startsAt === "" ? undefined : ticket.event.startsAt;

  return (
    <>
      <Card aria-labelledby="ticket-detail">
        <CardHeader>
          <div className="flex flex-wrap items-center gap-2">
            <h2 id="ticket-detail" className="m-0 text-lg leading-tight font-semibold">
              Your seat and your order
            </h2>
            {/* The served status is a stored fact, and it is printed as one. It is not
                the door's answer: the card below is. */}
            <Badge variant="outline">
              {ticket.status.trim() === "" ? "status not served" : ticket.status.trim()}
            </Badge>
          </div>
          <CardDescription>
            Read from the box office when this page loaded, so reloading it can only
            answer the same thing. The status beside this heading is the one the ticket is
            stored with, not a verdict about whether it admits you.
          </CardDescription>
        </CardHeader>

        <CardContent className="flex flex-col gap-3">
          <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
            <dt className="text-muted-foreground">Starts</dt>
            <dd className="m-0 font-medium break-words">
              <time dateTime={startsAt}>{ticket.event.startsAtLocal}</time>
              {ticket.event.timezone === "" ? null : (
                <span className="text-muted-foreground"> · {ticket.event.timezone}</span>
              )}
            </dd>

            <dt className="text-muted-foreground">Seat</dt>
            <dd className="m-0 font-medium break-words">
              {seats.length === 0 ? "not served" : seats.join(", ")}
            </dd>

            <dt className="text-muted-foreground">Order total</dt>
            <dd className="m-0 font-medium">
              {ticket.orderAmountDisplay === ""
                ? "not served"
                : ticket.orderAmountDisplay}
            </dd>

            <dt className="text-muted-foreground">Order</dt>
            <dd className="m-0 font-medium break-all">
              <code>{ticket.reservationId}</code>
            </dd>

            <dt className="text-muted-foreground">Ticket</dt>
            <dd className="m-0 font-medium break-all">
              <code>{ticket.ticketId}</code>
            </dd>

            <dt className="text-muted-foreground">Issued</dt>
            <dd className="m-0 font-medium">
              <time dateTime={ticket.issuedAt === "" ? undefined : ticket.issuedAt}>
                {ticket.issuedAt === "" ? "not served" : ticket.issuedAt}
              </time>
            </dd>
          </dl>

          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            The order total is what the whole reservation cost, not this seat&apos;s price:
            one order sells every seat of the reservation and is charged once.
          </p>
        </CardContent>
      </Card>

      <ValidityCard validity={validity} checked={checked} ticketId={ticket.ticketId} />

      <Card aria-labelledby="ticket-qr">
        <CardHeader>
          <h2 id="ticket-qr" className="m-0 text-lg leading-tight font-semibold">
            Entry code
          </h2>
          <CardDescription>
            This is what a door checks. Staff scan it, and the box office answers whether
            it admits you — the same answer printed above.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {/* A server-rendered image from the API's own route: no QR dependency, no
              client-side encoding, and the same code the PDF prints. The quiet zone
              stays white in both colour schemes, because a dark code is one a scanner
              may refuse. */}
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src={qrImageUrl}
            alt={`Entry code for ticket ${ticket.ticketId}`}
            width={240}
            height={240}
            className="max-w-full rounded-md border border-border bg-white p-3 [image-rendering:pixelated]"
          />
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            The code carries the ticket, the screening and its seats, signed by the box
            office. A photograph of it is the same code, so treat a screenshot like the
            ticket itself.
          </p>
        </CardContent>
      </Card>

      <Card aria-labelledby="ticket-pdf">
        <CardHeader>
          <h2 id="ticket-pdf" className="m-0 text-lg leading-tight font-semibold">
            Print or keep a copy
          </h2>
          <CardDescription>
            The PDF is a record of the sale, printed when the payment was captured — not
            proof that the ticket is valid.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          {/* A plain anchor, not a JS download: the browser follows the API's
              short-lived redirect and this page never handles the bytes. */}
          <Button asChild variant="outline" className="self-start">
            <a href={pdfUrl} rel="noreferrer">
              Open the ticket PDF
            </a>
          </Button>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            It shows the same detail and the same code as this page. It also cannot show a
            later withdrawal or a cancelled screening, because it was printed before them
            — which is why the check above, not the document, says whether this ticket
            admits anyone.
          </p>
        </CardContent>
      </Card>

      {/* The one polite live region of this surface, and the only thing on it that the
          page does not know at render time: whether this tab could keep the record. */}
      <p role="status" className="m-0 max-w-prose text-sm text-muted-foreground">
        {retentionNotices[retention]}
      </p>
    </>
  );
}

/** Whether this tab kept a record of the ticket it just opened. */
type Retention = "reading" | "kept" | "refused";

const retentionNotices: Record<Retention, string> = {
  reading: "Keeping this ticket in this tab's list…",
  kept: "This tab now lists this ticket on My tickets. That list is this browser's own note of what it bought — not an account, and not a record the box office keeps.",
  refused:
    "This tab refused to keep a record of this ticket, so it will not appear on My tickets. The ticket itself is unaffected: this link opens it again at any time.",
};

/**
 * Keeps this ticket in the tab's list, once, and reports whether it could.
 *
 * Opening a ticket is the moment this browser learns about it — there is no identity to
 * ask the box office what "my" tickets are (L3), so the purchase link is the only source
 * of truth, and the page records it here. `rememberIssuedTicket` is idempotent, so a
 * double render (React's strict mode in development) costs nothing.
 */
function useKeptInThisTab(ticketId: string): Retention {
  const [retention, setRetention] = useState<Retention>("reading");
  const remembered = useRef<string | null>(null);

  useEffect(() => {
    if (remembered.current === ticketId) {
      return;
    }
    remembered.current = ticketId;
    const storage = keptTicketStorage(window);
    if (storage === null) {
      setRetention("refused");
      return;
    }
    const kept = rememberIssuedTicket(storage, ticketId);
    setRetention(kept.kind === "unavailable" ? "refused" : "kept");
  }, [ticketId]);

  return retention;
}

/**
 * The validity card: the box office's answer, its reason, and what to do about it.
 *
 * The tone is decoration; the heading is the state, and the badge repeats the word, so
 * greyscale and a screen reader both lose nothing. A failed check offers a link that
 * re-reads this page (the route is per-request), which is a retry without JavaScript.
 */
function ValidityCard({
  validity,
  checked,
  ticketId,
}: {
  validity: ValidityView;
  checked: TicketCheck;
  ticketId: string;
}) {
  return (
    <Alert
      variant={alertVariant(validity)}
      aria-labelledby="ticket-validity"
      className="flex flex-col gap-2"
    >
      <div className="flex flex-wrap items-center gap-2">
        <h2 id="ticket-validity" className="m-0 text-lg leading-tight font-semibold">
          {validity.headline}
        </h2>
        <Badge variant={badgeVariant(validity)}>{badgeWord(validity)}</Badge>
      </div>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">{validity.message}</p>
        {checked.kind === "refused" ? (
          <p className="m-0">
            <code>{checked.problem.code}</code> · HTTP {checked.problem.status}
          </p>
        ) : null}
        {validity.reason === null ? null : (
          <p className="m-0">
            Reason reported by the box office: <code>{validity.reason}</code>
          </p>
        )}
        <p className="m-0">{validity.nextStep}</p>
        {validity.retryable ? (
          <div className="flex flex-wrap gap-2 pt-1">
            <Button asChild size="sm">
              <Link href={`/tickets/${encodeURIComponent(ticketId)}`}>
                Check the code again
              </Link>
            </Button>
          </div>
        ) : null}
      </AlertDescription>
    </Alert>
  );
}

/** The `Alert` tone for one answer; the words inside carry the state. */
function alertVariant(validity: ValidityView) {
  switch (validity.tone) {
    case "success":
      return "success" as const;
    case "warning":
      return "warning" as const;
    case "info":
      return "info" as const;
    default:
      return "destructive" as const;
  }
}

/** The badge tone for one answer. */
function badgeVariant(validity: ValidityView): BadgeProps["variant"] {
  switch (validity.kind) {
    case "valid":
      return "success";
    case "voided":
    case "event_cancelled":
    case "reason_unrecognised":
    case "unavailable":
      return "warning";
    default:
      return "danger";
  }
}

/** The one word the badge prints: the state, spelled out. */
function badgeWord(validity: ValidityView): string {
  switch (validity.kind) {
    case "valid":
      return "Admits its holder";
    case "voided":
      return "Voided";
    case "event_cancelled":
      return "Screening cancelled";
    case "reason_unrecognised":
      return "Not valid";
    case "invalid_token":
      return "Not a ticket";
    case "unavailable":
      return "Not checked";
    default:
      return "Check refused";
  }
}

// ── The list of tickets this tab kept ───────────────────────────────────────

export interface KeptTicketListProps {
  /** Normalized public API base URL, resolved on the server; used for the PDF links. */
  apiBaseUrl: string;
  /**
   * Reads one ticket: `GET /api/v1/tickets/{id}`, through the page's server action.
   *
   * It is passed in rather than imported because the browser must not know where the
   * API lives: the read happens in this application's process, with the base URL taken
   * from server-side environment variables.
   */
  readTicket: (ticketId: string) => Promise<TicketResponse>;
}

/**
 * "My tickets": the ticket ids this browser tab kept, each one re-read.
 *
 * This screen admits what it is, in its own words: it is a note this tab keeps, not an
 * account, and every id on it is read from the box office again rather than trusted. Its
 * states stay apart, because each one means something different:
 *
 *   1. the tab's records are still being read (a real region, not a blank list);
 *   2. this browser will not let the page read what it keeps — which is not "no
 *      tickets", and says so;
 *   3. the tab kept nothing — with the count of records it could not read, if any;
 *   4. the list, where each entry is itself one of four states: still being read, read,
 *      unknown to the box office, or not readable right now. **One stale id costs one
 *      row**, never the list, and only a row the box office does not know offers to be
 *      forgotten — the page never deletes a record it could not read.
 */
export function KeptTicketList({ apiBaseUrl, readTicket }: KeptTicketListProps) {
  // `null` until the tab's own storage has been read on the client: the server has no
  // access to it, so rendering a list before then would be inventing one.
  const [kept, setKept] = useState<KeptTickets | null>(null);
  const [rows, setRows] = useState<Record<string, KeptTicketRow>>({});
  const [announcement, setAnnouncement] = useState<string | null>(null);

  const ids = useMemo(() => (kept?.kind === "kept" ? kept.ids : []), [kept]);
  // The ids this component has already asked about: forgetting one record re-renders the
  // list, and re-reading the tickets that were already answered would be a second read of
  // the same thing.
  const requested = useRef<Set<string>>(new Set());

  useEffect(() => {
    setKept(readKeptTickets(keptTicketStorage(window)));
  }, []);

  // Every kept id is read once, in one batch: the box office answers per ticket, and a
  // slow one must not hold up the others.
  useEffect(() => {
    const pending = ids.filter((id) => !requested.current.has(id));
    if (pending.length === 0) {
      return;
    }
    for (const id of pending) {
      requested.current.add(id);
    }
    let cancelled = false;
    setRows((previous) => {
      const next = { ...previous };
      for (const id of pending) {
        next[id] = { kind: "reading" };
      }
      return next;
    });

    void (async () => {
      const answers = await Promise.all(
        pending.map(
          async (id) => [id, describeKeptTicket(await readTicket(id))] as const,
        ),
      );
      if (cancelled) {
        return;
      }
      setRows((previous) => {
        const next = { ...previous };
        for (const [id, row] of answers) {
          next[id] = row;
        }
        return next;
      });
    })();

    return () => {
      cancelled = true;
    };
  }, [ids, readTicket]);

  const retry = useCallback(
    async (ticketId: string) => {
      setRows((previous) => ({ ...previous, [ticketId]: { kind: "reading" } }));
      const answer = describeKeptTicket(await readTicket(ticketId));
      setRows((previous) => ({ ...previous, [ticketId]: answer }));
    },
    [readTicket],
  );

  const forget = useCallback(
    (ticketId: string) => {
      const next = forgetIssuedTicket(keptTicketStorage(window), ticketId);
      setKept(next);
      setRows((previous) => {
        const remaining = { ...previous };
        delete remaining[ticketId];
        return remaining;
      });
      setAnnouncement(
        next.kind === "unavailable"
          ? "This tab could not forget the ticket, so the list is unchanged."
          : `Forgotten on this tab: ticket ${ticketId}. The ticket itself is untouched.`,
      );
    },
    [],
  );

  const notice = announcement ?? announceKept(kept, rows, ids);

  return (
    <>
      {/* The one polite live region of this surface. It announces the list once, and
          then only what changed; the rows themselves are not a region, or every fresh
          answer would be read out as a second announcement. */}
      <p role="status" className="m-0 max-w-prose text-sm font-medium">
        {notice}
      </p>

      {kept === null ? <ListLoading /> : null}

      {kept?.kind === "unavailable" ? (
        <Alert variant="warning" aria-labelledby="kept-unavailable" className="flex flex-col gap-2">
          <h2 id="kept-unavailable" className="m-0 text-lg leading-tight font-semibold">
            This browser will not show what it kept
          </h2>
          <AlertDescription className="flex flex-col gap-2">
            <p className="m-0">{kept.detail}</p>
            <p className="m-0">
              This is not the same as having no ticket: the page could not read this tab&apos;s
              own record at all, so it says nothing about what was bought. A ticket stays
              reachable from the link the payment returned, and from the box office.
            </p>
          </AlertDescription>
        </Alert>
      ) : null}

      {kept?.kind === "empty" ? <NothingKept unreadable={kept.unreadable} /> : null}

      {ids.length > 0 ? (
        <section aria-labelledby="kept-tickets" className="flex flex-col gap-3">
          <h2 id="kept-tickets">Tickets this tab kept</h2>
          <p className="m-0 max-w-prose">
            {ids.length === 1
              ? "1 ticket."
              : `${ids.length} tickets.`}{" "}
            <span className="text-muted-foreground">
              Each one was read from the box office when this page loaded, so nothing here
              is remembered detail. A record this tab can no longer use is one row, never
              the whole list.
            </span>
          </p>
          {kept?.kind === "kept" && kept.unreadable > 0 ? (
            <p className="m-0 max-w-prose text-sm text-muted-foreground">
              {kept.unreadable === 1
                ? "1 record this tab kept could not be read as a ticket id, so it is not listed."
                : `${kept.unreadable} records this tab kept could not be read as ticket ids, so they are not listed.`}
            </p>
          ) : null}

          <ul role="list" className="m-0 flex list-none flex-col gap-4 p-0">
            {ids.map((ticketId) => (
              <li key={ticketId} className="min-w-0">
                <KeptTicketCard
                  ticketId={ticketId}
                  row={rows[ticketId] ?? { kind: "reading" }}
                  apiBaseUrl={apiBaseUrl}
                  onRetry={() => void retry(ticketId)}
                  onForget={() => forget(ticketId)}
                />
              </li>
            ))}
          </ul>

          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            This list is what this browser tab kept, not an account: nothing on this page
            checks who you are, and a forwarded ticket link opens the same ticket for
            whoever holds it.
          </p>
        </section>
      ) : null}
    </>
  );
}

/** One line about the list as a whole, for the live region. */
function announceKept(
  kept: KeptTickets | null,
  rows: Record<string, KeptTicketRow>,
  ids: readonly string[],
): string {
  if (kept === null) {
    return "Reading the tickets this tab kept…";
  }
  if (kept.kind === "unavailable") {
    return "This browser would not say what it kept.";
  }
  if (kept.kind === "empty") {
    return "This tab has kept no ticket.";
  }
  const reading = ids.filter((id) => rows[id]?.kind === "reading").length;
  if (reading > 0) {
    return `Reading ${reading === 1 ? "1 ticket" : `${reading} tickets`} this tab kept…`;
  }
  return ids.length === 1
    ? "1 ticket this tab kept, read from the box office."
    : `${ids.length} tickets this tab kept, read from the box office.`;
}

/** One kept ticket, in whichever of its four states it is. */
function KeptTicketCard({
  ticketId,
  row,
  apiBaseUrl,
  onRetry,
  onForget,
}: {
  ticketId: string;
  row: KeptTicketRow;
  apiBaseUrl: string;
  onRetry: () => void;
  onForget: () => void;
}) {
  if (row.kind === "reading") {
    return (
      <Card className="flex flex-col gap-3 p-5">
        <p className="m-0 font-medium break-all">
          Reading ticket <code>{ticketId}</code>…
        </p>
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
      </Card>
    );
  }

  if (row.kind === "found") {
    const { ticket } = row;
    // The heading id is derived from the ticket id and stripped of whitespace, so the
    // `aria-labelledby` target is always a valid id and always unique per row.
    const headingId = `kept-ticket-${ticketId.replace(/\s+/g, "-")}`;
    const title =
      ticket.event.filmTitle.trim() === "" ? "A screening" : ticket.event.filmTitle;
    const venue = [ticket.event.cinemaName, ticket.event.roomName]
      .map((part) => part.trim())
      .filter((part) => part !== "")
      .join(" · ");
    const startsAt = ticket.event.startsAt === "" ? undefined : ticket.event.startsAt;

    return (
      <Card aria-labelledby={`kept-${ticketId}`} className="flex flex-col gap-3 p-5">
        <div className="flex flex-wrap items-baseline gap-2">
          <h3 id={headingId} className="m-0 text-base font-semibold break-words">
            {title}
          </h3>
          <Badge variant="outline">
            {ticket.status.trim() === "" ? "status not served" : ticket.status.trim()}
          </Badge>
        </div>
        <p className="m-0 break-words text-sm">
          <span className="text-muted-foreground">{venue === "" ? "Venue not served" : venue}</span>
          {venue === "" ? null : <span className="text-muted-foreground"> · </span>}
          <time dateTime={startsAt} className="font-medium">
            {ticket.event.startsAtLocal}
          </time>
          <span className="text-muted-foreground">
            {ticket.event.timezone === "" ? "" : ` (${ticket.event.timezone})`}
          </span>
        </p>
        <p className="m-0 break-words text-sm">
          <span className="text-muted-foreground">Seat </span>
          <span className="font-medium">
            {ticket.seats.length === 0
              ? "not served"
              : ticket.seats.map(ticketSeatLabel).join(", ")}
          </span>
        </p>
        {/* The checkout's own link shape: the ticket page and the PDF, printed the same
            way on the confirmation and here. */}
        <IssuedTickets ticketIds={[ticket.ticketId]} apiBaseUrl={apiBaseUrl} />
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Whether this ticket still admits you is answered by the box office when it is
          scanned, not by this list. Open it to see the answer for this moment.
        </p>
      </Card>
    );
  }

  // Unknown and unreadable are different facts, and only the first one offers to be
  // forgotten: a record the page could not read is not a record it may delete.
  const problemHeadingId = `kept-ticket-${ticketId.replace(/\s+/g, "-")}`;
  return (
    <Alert
      variant={row.kind === "unknown" ? "warning" : "destructive"}
      aria-labelledby={problemHeadingId}
      className="flex flex-col gap-2"
    >
      <div className="flex flex-wrap items-baseline gap-2">
        <h3 id={problemHeadingId} className="m-0 text-base font-semibold">
          {row.headline}
        </h3>
        <Badge variant={row.kind === "unknown" ? "warning" : "danger"}>
          {row.kind === "unknown" ? "Not known" : "Not read"}
        </Badge>
      </div>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">
          Ticket <code>{ticketId}</code>
        </p>
        <p className="m-0">
          <code>{row.problem.code}</code> · HTTP {row.problem.status}
        </p>
        <p className="m-0">{row.message}</p>
        <p className="m-0">{row.nextStep}</p>
        <div className="flex flex-wrap gap-2 pt-1">
          {row.kind === "unavailable" ? (
            <Button type="button" size="sm" onClick={onRetry}>
              Ask the box office again
            </Button>
          ) : null}
          <Button type="button" size="sm" variant="outline" onClick={onForget}>
            Forget this record on this device
          </Button>
        </div>
      </AlertDescription>
    </Alert>
  );
}

/**
 * The tab kept nothing.
 *
 * Not a failure, and not a teaser: it says what the screen is, so an empty list cannot be
 * mistaken for "your purchase is gone", and it names the two ways to a ticket — the
 * payment's own link, and a new purchase.
 */
function NothingKept({ unreadable }: { unreadable: number }) {
  return (
    <Card aria-labelledby="kept-empty" className="flex flex-col gap-2 p-5">
      <h2 id="kept-empty">This tab has kept no ticket</h2>
      <p className="m-0 max-w-prose">
        This screen shows the tickets this browser kept from the purchases it made in it,
        so it is empty until one is opened here. It is not an account, and the box office
        keeps no list this page can ask for.
      </p>
      <p className="m-0 max-w-prose text-sm text-muted-foreground">
        A ticket is opened by its own link: the one your payment returned, or the PDF you
        kept. Opening it here adds it to this list.
      </p>
      {unreadable > 0 ? (
        <p className="m-0 max-w-prose text-sm">
          {unreadable === 1
            ? "1 record the tab kept could not be read as a ticket id."
            : `${unreadable} records the tab kept could not be read as ticket ids.`}{" "}
          <span className="text-muted-foreground">
            Nothing was deleted: a record this page cannot read is left where it is.
          </span>
        </p>
      ) : null}
      <div className="flex flex-wrap gap-2 pt-1">
        <Button asChild size="sm">
          <Link href="/events">See what is on sale</Link>
        </Button>
      </div>
    </Card>
  );
}

/** The list while the tab's own records are still being read. */
function ListLoading() {
  return (
    <section aria-labelledby="kept-loading" className="flex flex-col gap-3">
      <h2 id="kept-loading">Tickets this tab kept</h2>
      <Card aria-hidden="true" className="flex flex-col gap-3 p-5">
        <Skeleton className="h-5 w-1/2" />
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-4 w-1/3" />
      </Card>
    </section>
  );
}
