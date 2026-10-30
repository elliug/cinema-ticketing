// Presentation of one ticket (M3 · T7 · S1, S3, S4).
//
// Presentational only, and deliberately not a client component: nothing on the
// ticket page is interactive, so the whole surface is server-rendered HTML that
// works with JavaScript disabled — which is also the state a phone in a cinema
// lobby is often in.
//
// What it prints, and what it refuses to say:
//
//   - the screening time is the served `startsAtLocal` string, rendered as text
//     inside a `<time>` element with the same value in its `dateTime` attribute.
//     It is never parsed, re-zoned or re-formatted, because the API already
//     rendered it in the venue's zone;
//   - the amount is labelled **order total**: it is the money taken for the whole
//     reservation, not this seat's price (the API names its keys `orderAmount*`
//     for exactly that reason);
//   - validity is the API's answer to `POST /api/v1/tickets/verify`, printed with
//     its reason when there is one. The page never derives validity from the
//     ticket status, and never from the PDF;
//   - the PDF is offered as a link to the API's short-lived redirect and is
//     described as a record of the sale. It was rendered when the payment was
//     captured, so a later void or a cancelled screening cannot be in it, and no
//     page may present it as proof that a ticket is good.
import type {
  PublicProblem,
  PublicTicket,
  PublicVerification,
} from "@/lib/public-api";

/** What the code check answered. */
export type Checked =
  | { kind: "verified"; verification: PublicVerification }
  | { kind: "refused"; problem: PublicProblem };

export interface TicketViewProps {
  ticket: PublicTicket;
  checked: Checked;
  /** `GET /api/v1/tickets/{id}/qr.svg` — the signed token, rendered by the API. */
  qrImageUrl: string;
  /** `GET /api/v1/tickets/{id}/pdf` — a short-lived pre-signed redirect. */
  pdfUrl: string;
}

export function TicketView({ ticket, checked, qrImageUrl, pdfUrl }: TicketViewProps) {
  const seatLabels = ticket.seats.map(formatSeat);

  return (
    <>
      <section className="card ticket" aria-labelledby="ticket-title">
        <p className="eyebrow">Ticket · M3 checkout and tickets</p>
        <h1 id="ticket-title">{ticket.event.filmTitle}</h1>

        <p className="meta">
          <time dateTime={ticket.event.startsAtLocal}>{ticket.event.startsAtLocal}</time>
          {ticket.event.timezone === "" ? null : (
            <>
              {" · "}
              {ticket.event.timezone}
            </>
          )}
        </p>
        <p className="meta">
          {ticket.event.cinemaName}
          {ticket.event.roomName === "" ? null : ` · ${ticket.event.roomName}`}
        </p>

        <dl className="ticket-facts">
          <div>
            <dt>Seat</dt>
            <dd>{seatLabels.length === 0 ? "not served" : seatLabels.join(", ")}</dd>
          </div>
          <div>
            <dt>Order total</dt>
            <dd>
              {ticket.orderAmountDisplay === ""
                ? "not served"
                : ticket.orderAmountDisplay}
            </dd>
          </div>
          <div>
            <dt>Order</dt>
            <dd>
              <code>{ticket.reservationId}</code>
            </dd>
          </div>
          <div>
            <dt>Ticket</dt>
            <dd>
              <code>{ticket.ticketId}</code>
            </dd>
          </div>
        </dl>

        <p className="notice">
          The order total is what the whole reservation cost, not this seat&apos;s
          price: one order sells every seat of the reservation and is charged once.
        </p>
      </section>

      <ValidityCard checked={checked} />

      <section className="card qr" aria-labelledby="ticket-qr">
        <h2 id="ticket-qr">Entry code</h2>
        {/* eslint-disable-next-line @next/next/no-img-element */}
        <img
          className="qr-image"
          src={qrImageUrl}
          alt={`QR code for ticket ${ticket.ticketId}`}
          width={240}
          height={240}
        />
        <p className="notice">
          This code carries the ticket, the screening and its seats, signed by the
          box office. Staff check it against the box office system; a photograph of
          it is not a ticket in anyone else&apos;s hands.
        </p>
      </section>

      <section className="card" aria-labelledby="ticket-pdf">
        <h2 id="ticket-pdf">Print or keep a copy</h2>
        {/* A plain link: the browser follows the API's short-lived redirect and
            downloads the file, so this page never handles the bytes. */}
        <p>
          <a className="download" href={pdfUrl} rel="noreferrer">
            Download the ticket PDF
          </a>
        </p>
        <p className="notice">
          The PDF is a record of the sale, printed when the payment was captured —
          it shows the same detail and the same code as this page. It is{" "}
          <strong>not</strong> proof that the ticket is valid: a withdrawn ticket or
          a cancelled screening cannot appear in a document that was already
          printed. The check above is what says whether this ticket admits anyone.
        </p>
      </section>
    </>
  );
}

/** The seat as the door paints it: row label plus seat number. */
function formatSeat(seat: { rowLabel: string; seatNumber: string }): string {
  if (seat.rowLabel === "" && seat.seatNumber === "") {
    return "unknown seat";
  }
  if (seat.rowLabel === "") {
    return seat.seatNumber;
  }
  if (seat.seatNumber === "") {
    return `row ${seat.rowLabel}`;
  }
  return `${seat.rowLabel}${seat.seatNumber}`;
}

function ValidityCard({ checked }: { checked: Checked }) {
  if (checked.kind === "refused") {
    const unrecognised = checked.problem.code === "invalid_token";
    return (
      <section
        className="card problems feedback error"
        aria-labelledby="ticket-validity"
      >
        <h2 id="ticket-validity">
          {unrecognised
            ? "This code is not recognised as a ticket"
            : "The ticket could not be checked"}
        </h2>
        <p>
          <code>{checked.problem.code}</code> · HTTP {checked.problem.status}
        </p>
        <p>{checked.problem.detail}</p>
        <p className="notice">
          {unrecognised
            ? "The box office did not sign this code, so it will not admit anyone. If you paid for seats, open the ticket from the link your payment returned."
            : "Nothing about your ticket has changed; the check itself failed. Reload the page, and show this page to staff if it keeps failing."}
        </p>
      </section>
    );
  }

  const { verification } = checked;
  if (verification.valid) {
    return (
      <section className="card feedback" aria-labelledby="ticket-validity">
        <h2 id="ticket-validity">This ticket is valid</h2>
        <p>
          Checked against the box office just now. It admits its holder to the
          screening and seats above.
        </p>
      </section>
    );
  }

  return (
    <section
      className="card problems feedback error"
      aria-labelledby="ticket-validity"
    >
      <h2 id="ticket-validity">This ticket does not admit anyone</h2>
      <p>{describeReason(verification.reason)}</p>
      <p className="notice">
        Reason reported by the box office: <code>{verification.reason ?? "unknown"}</code>
      </p>
    </section>
  );
}

/** The two reasons this milestone can report, and the honest fallback. */
function describeReason(reason: string | undefined): string {
  switch (reason) {
    case "voided":
      return "The ticket itself was withdrawn (voided), so it no longer admits its holder to the screening.";
    case "event_cancelled":
      return "The screening was cancelled, so the ticket cannot admit anyone even though it was issued.";
    default:
      return "The box office reports that this ticket is not valid, and did not name a reason this page understands.";
  }
}
