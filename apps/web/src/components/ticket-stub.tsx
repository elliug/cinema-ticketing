// The ticket stub (Marquesina, visual-redesign · T6 · S1, S2, S8).
//
// Every surface that shows one ticket shows the same piece of paper: the film and the
// session on the left, and a perforated side on the right holding the entry square, the
// code and which seat of the order it is. The ticket page, the list this tab keeps, the
// account's list and the page an emailed link opens all print it, because a second
// rendering of a ticket is how two screens start disagreeing about the film, the hour, the
// seat or the money.
//
// Four decisions are load-bearing:
//
//   - **the square is the real code.** `qrImageUrl` is the API's own image, forwarded by
//     this application (`lib/ticket-media.ts`). A drawn square would look identical to
//     this one and scan as nothing, which is the one thing a ticket may not do;
//   - **the white plate is deliberate.** The code is served on white and a scanner refuses
//     a dark one, so the image sits on `bg-white` in either colour scheme — the only place
//     in the redesign where a surface is not a token, and it is the scanner's requirement,
//     not a colour;
//   - **the props are the caller's, and none of them is an API type.** Every value here was
//     already decided somewhere a test can reach it: the hour is the venue's own
//     presentation, the seat label came from `ticketSeatLabel`, the amount from the one
//     money formatter, and the state word from the surface that knows what it means. This
//     component prints them and derives nothing;
//   - **what a screen owns stays with the screen.** The links and buttons that act on a
//     ticket (open it, download its PDF) arrive as `children`, so the stub never invents an
//     action and a surface keeps its own routes.
//
// No `"use client"`: no behaviour. The drawing is an `<img>` served by this origin, the
// title is a heading, and the seat line is prose, so greyscale and a screen reader lose
// nothing: the state of the ticket is a word beside the title, and the tilt of the paper
// carries nothing on its own.
import type { ReactNode } from "react";

import type { BadgeProps } from "./ui/badge";
import { Badge } from "./ui/badge";
import { localHour } from "@/lib/screening-display";

export interface TicketStubProps {
  /** The film, as served. It is the stub's heading. */
  filmTitle: string;
  cinemaName: string;
  roomName: string;
  /** The venue's own presentation of the start, printed as served. */
  startsAtLocal: string;
  /** The machine-readable instant, when the caller has one, for `<time dateTime>`. */
  startsAt?: string;
  /** The seat as the door paints it (`A3`), already read from the served labels. */
  seatLabel: string;
  /**
   * Which ticket of the order this stub is, and how many the order issued.
   *
   * Optional on purpose: the ticket read serves no order seat count, so a surface that only
   * has one ticket prints the seat alone rather than inventing a "1 de 1".
   */
  seatPosition?: number;
  seatCount?: number;
  /** The money taken for the whole order, already presented by the caller. */
  orderTotal: string;
  /** The served status in words (`issued`, `void`); the tone only reinforces it. */
  statusWord?: string;
  statusVariant?: BadgeProps["variant"];
  /** The ticket's identifier: the code printed on the stub, and whose code the image is. */
  ticketId: string;
  /** `GET /api/tickets/{id}/qr.svg` — the API's own image, at this origin. */
  qrImageUrl: string;
  /** The id the stub's heading is labelled by, when the caller needs to name the article. */
  headingId?: string;
  /** A sentence the caller owns, printed under the facts. */
  note?: string;
  /** The actions this screen owns: a link to the ticket, the PDF, a retry. */
  children?: ReactNode;
}

/** "Butaca A3", plus which ticket of the order when the caller knows. */
function describeSeat({
  seatLabel,
  seatPosition,
  seatCount,
}: Pick<TicketStubProps, "seatLabel" | "seatPosition" | "seatCount">): string {
  if (seatPosition !== undefined && seatCount !== undefined) {
    return `Entrada ${seatPosition} de ${seatCount} · butaca ${seatLabel}`;
  }
  if (seatCount !== undefined) {
    return `Butaca ${seatLabel} de ${seatCount}`;
  }
  return `Butaca ${seatLabel}`;
}

export function TicketStub({
  filmTitle,
  cinemaName,
  roomName,
  startsAtLocal,
  startsAt,
  seatLabel,
  seatPosition,
  seatCount,
  orderTotal,
  statusWord,
  statusVariant = "outline",
  ticketId,
  qrImageUrl,
  headingId,
  note,
  children,
}: TicketStubProps) {
  const status = (statusWord ?? "").trim();

  return (
    <article
      aria-labelledby={headingId}
      className="grid overflow-hidden rounded-lg border border-border bg-card sm:grid-cols-[minmax(0,1fr)_11rem]"
    >
      <div className="flex min-w-0 flex-col gap-4 p-5">
        <div className="flex flex-col gap-1">
          {/* Two facts, and no verdict: whether this ticket admits anybody is the box
              office's answer, printed by the caller beside it. */}
          <p className="m-0 text-xs font-semibold tracking-[0.14em] text-primary uppercase">
            Una persona
            {roomName.trim() === "" ? null : ` · ${roomName.trim()}`}
          </p>

          <div className="flex flex-wrap items-baseline gap-2">
            <h3
              id={headingId}
              className="m-0 text-2xl leading-tight font-semibold break-words"
            >
              {filmTitle}
            </h3>
            {/* The word is the state; the tone only reinforces it. */}
            {status === "" ? null : <Badge variant={statusVariant}>{status}</Badge>}
          </div>

          {/* The separators are decoration: a screen reader hears the facts, not the dots. */}
          <p className="m-0 flex flex-wrap items-center gap-x-2 text-sm text-muted-foreground">
            {cinemaName.trim() === "" ? null : (
              <span className="break-words">{cinemaName}</span>
            )}
            {cinemaName.trim() === "" ? null : <span aria-hidden="true">·</span>}
            <time dateTime={startsAt}>{localHour(startsAtLocal)}</time>
          </p>
        </div>

        {/* A hairline, as the paper is ruled. Decoration: it separates, it does not group. */}
        <div aria-hidden="true" className="h-px bg-border" />

        <dl className="m-0 grid grid-cols-2 gap-4">
          <div className="flex flex-col gap-1">
            <dt className="text-xs tracking-[0.1em] text-muted-foreground uppercase">
              Butaca
            </dt>
            <dd className="m-0 text-base font-semibold break-words">{seatLabel}</dd>
          </div>
          <div className="flex flex-col gap-1">
            <dt className="text-xs tracking-[0.1em] text-muted-foreground uppercase">
              Pagado
            </dt>
            <dd className="m-0 font-mono text-base font-semibold">{orderTotal}</dd>
          </div>
        </dl>

        {note === undefined ? null : (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">{note}</p>
        )}

        {children === undefined ? null : (
          <div className="flex flex-wrap items-center gap-2">{children}</div>
        )}
      </div>

      {/* The perforated side: a dashed edge, the code served by the box office, and which
          ticket of the order it is. */}
      <div className="flex flex-col items-center gap-3 border-t border-dashed border-border bg-muted p-4 text-center sm:border-t-0 sm:border-l">
        {/* The API's own image, at this origin: the same code the PDF prints. The plate is
            white because a scanner refuses a dark code, and the alt names the ticket rather
            than the object, because "código QR" repeated on every stub names nothing. */}
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img
          src={qrImageUrl}
          alt={`Código de acceso de la entrada ${ticketId}`}
          width={132}
          height={132}
          className="w-full max-w-[8.25rem] rounded-sm bg-white p-1.5 [image-rendering:pixelated]"
        />
        <span className="font-mono text-xs tracking-[0.08em] break-all">{ticketId}</span>
        <span className="text-xs text-muted-foreground">
          {describeSeat({ seatLabel, seatPosition, seatCount })}
        </span>
      </div>
    </article>
  );
}
