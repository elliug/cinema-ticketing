"use client";

// The presentation of the two ticket surfaces (M3 · T7, M3.5 · T7 · S1, S3, S8;
// Marquesina · T6): one ticket, and the guest list of the tickets this browser tab kept.
//
// Both live in one module because they print the same things — a screening, a seat, an
// order total, the entry code and the PDF — and because a second copy of that shape is
// how two screens start disagreeing about what a ticket is. Neither of them decides a
// fact of its own:
//
//   - the screening time is the served `startsAtLocal` string, printed verbatim inside a
//     `<time>` whose machine value is the UTC instant. Re-formatting it here would print
//     the viewer's zone on a page whose point is the venue's;
//   - the amount is labelled **total del pedido**: it is the money taken for the whole
//     reservation, not this seat's price (which is why the API names its keys
//     `orderAmount*`);
//   - validity is the answer to `POST /api/v1/tickets/verify`, in words, from
//     lib/issued-tickets.ts — never the stored ticket status, never the PDF (S8);
//   - the PDF is a **link** the browser follows to the API's short-lived redirect, and it
//     is described as a record of the sale printed when the payment was captured. It
//     cannot contain a later void or a cancelled screening, so no page may present it as
//     proof that a ticket is good;
//   - the QR image is what a door checks, and its `alt` names the ticket rather than the
//     object, because "código QR" repeated on every ticket names nothing. It is served by
//     the API through `lib/ticket-media.ts`, never drawn: a drawn square would look the
//     same and scan as nothing.
//
// The ticket itself is the one [TicketStub] every surface prints (components/ticket-stub.tsx),
// so the paper a buyer shows at the door and the paper a list offers are the same component
// with the same facts. What a surface owns — the links, the retry, the buttons — is handed
// to the stub as its children.
//
// Why a client module at all: the *guest* list is browser state. A browser with no session has no
// account to ask, so the "my tickets" screen can only show what this tab kept, which means reading
// storage after mount. (A browser that holds a session gets the account's list instead, read from
// the box office by the page — `apps/web/src/app/tickets/page.tsx` — and drawn with the same
// [TicketSummary] this module renders.) The ticket view shares the module because it is where a
// ticket is opened, and opening one is what makes this tab remember it. Everything either surface
// prints is still server-rendered HTML, so both read with JavaScript disabled; only the guest
// list's own storage work needs it.
//
// Accessibility: one `h1` per surface (the page header's, the screening or the page's
// own name — never a heading in here), one polite live region per widget (the retention note
// below, and the send panel's own, because a send is a different event), every action a
// focus-ringed button or link, and no state carried by colour alone: each state prints its
// word, and its tone only reinforces it.
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
import { TicketStub } from "@/components/ticket-stub";
import type { KeptTicketRow, KeptTickets, TicketCheck, ValidityView } from "@/lib/issued-tickets";
import {
  TICKET_PDF_LINK_LABEL,
  describeKeptTicket,
  describeTicketValidity,
  forgetIssuedTicket,
  keptTicketStorage,
  readKeptTickets,
  rememberIssuedTicket,
  ticketSeatLabel,
} from "@/lib/issued-tickets";
import { presentAmount } from "@/lib/receipt";
import type { PublicTicket, TicketEmailResponse, TicketResponse } from "@/lib/public-api";
import { ticketQRImageUrl } from "@/lib/ticket-media";
import { TicketEmailPanel } from "./ticket-email-panel";

export interface TicketViewProps {
  ticket: PublicTicket;
  /** The answer to the code check the page asked for, not a verdict of this module. */
  checked: TicketCheck;
  /** `GET /api/v1/tickets/{id}/qr.svg` — the signed token, rendered by the API. */
  qrImageUrl: string;
  /** `GET /api/v1/tickets/{id}/pdf` — a short-lived pre-signed redirect. */
  pdfUrl: string;
  /**
   * Server action: mail this ticket's order to an address typed on this page.
   *
   * A prop, not an import, for the reason the list's `readTicket` is one: the call happens in
   * this application's process, so the browser never learns where the API lives.
   */
  sendTicketEmail: (ticketId: string, email: string) => Promise<TicketEmailResponse>;
}

/** One ticket, as the page a buyer shows at the door renders it. */
export function TicketView({
  ticket,
  checked,
  qrImageUrl,
  pdfUrl,
  sendTicketEmail,
}: TicketViewProps) {
  const validity = describeTicketValidity(checked);
  const retention = useKeptInThisTab(ticket.ticketId);
  const seats = ticket.seats.map(ticketSeatLabel);
  const startsAt = ticket.event.startsAt === "" ? undefined : ticket.event.startsAt;
  const title = ticket.event.filmTitle.trim() === "" ? "Una sesión" : ticket.event.filmTitle;
  const seatLabel = seats.length === 0 ? "no servida" : seats.join(", ");
  // The API's own rendering of the money, re-punctuated for this page: a read that served no
  // amount says so instead of printing a zero the API never said.
  const orderTotal =
    ticket.orderAmountDisplay === "" ? "no servido" : presentAmount(ticket.orderAmountDisplay);

  return (
    <>
      {/* The paper itself. The PDF is the one action this stub owns: the route is a plain
          link the browser follows, so the page never handles the bytes. */}
      <TicketStub
        filmTitle={title}
        cinemaName={ticket.event.cinemaName}
        roomName={ticket.event.roomName}
        startsAtLocal={ticket.event.startsAtLocal}
        startsAt={startsAt}
        seatLabel={seatLabel}
        orderTotal={orderTotal}
        statusWord={ticket.status.trim() === "" ? "estado no servido" : ticket.status.trim()}
        ticketId={ticket.ticketId}
        qrImageUrl={qrImageUrl}
      >
        {/* A plain anchor, not a JS download: the browser follows the API's
            short-lived redirect, and the URL it lands on declares
            `Content-Disposition: attachment`, so the file is saved rather than
            rendered. The words are the exported sentence both surfaces print. */}
        <Button asChild variant="outline" size="sm">
          <a href={pdfUrl} rel="noreferrer">
            {TICKET_PDF_LINK_LABEL}
          </a>
        </Button>
      </TicketStub>

      <ValidityCard validity={validity} checked={checked} ticketId={ticket.ticketId} />

      <Card aria-labelledby="ticket-entry">
        <CardHeader>
          <h2 id="ticket-entry" className="m-0 text-lg leading-tight font-semibold">
            En el acceso
          </h2>
          <CardDescription>
            El cuadrado de arriba es lo que mira el acceso. Es la imagen que sirve la propia
            taquilla, firmada con la entrada, la sesión y sus butacas, y un escaneo del
            personal decide si admite a alguien.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <ol className="m-0 flex max-w-prose list-decimal flex-col gap-2 pl-5 text-sm text-muted-foreground">
            <li>Enseña el código en el acceso. El personal lo escanea y la butaca se admite una sola vez.</li>
            <li>
              La respuesta de la taquilla es la de arriba: si la entrada se anuló, si la sesión
              se canceló o si no se pudo comprobar, lo dice con esas palabras.
            </li>
            <li>
              Una foto del código es el mismo código, así que una captura de pantalla se trata
              como la entrada misma.
            </li>
            <li>
              El enlace de la entrada funciona sin cuenta: quien lo tenga abre las mismas
              entradas, y una entrada usada o anulada se rechaza igualmente en el acceso.
            </li>
          </ol>
        </CardContent>
      </Card>

      {/* The caveat about the PDF lives beside the order, not beside the link: the link is on
          the paper above, and what a person needs to know about it is what it cannot show. */}
      <Card aria-labelledby="ticket-pdf">
        <CardHeader>
          <h2 id="ticket-pdf" className="m-0 text-lg leading-tight font-semibold">
            Lo que el PDF no prueba
          </h2>
          <CardDescription>
            Se descarga desde la entrada de arriba y es un registro de la venta, impreso cuando
            se cobró el pago, no una prueba de que la entrada valga: no puede mostrar una
            anulación posterior ni una sesión cancelada después. Por eso quien dice si esta
            entrada admite a alguien es la comprobación de arriba, no el documento.
          </CardDescription>
        </CardHeader>
      </Card>

      <Card aria-labelledby="ticket-order">
        <CardHeader>
          <h2 id="ticket-order" className="m-0 text-lg leading-tight font-semibold">
            Este pedido
          </h2>
          <CardDescription>
            Leído de la taquilla al cargar esta página, así que recargarla solo puede responder
            lo mismo.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
            <dt className="text-muted-foreground">Pedido</dt>
            <dd className="m-0 font-medium break-all">
              <code>{ticket.reservationId}</code>
            </dd>

            <dt className="text-muted-foreground">Entrada</dt>
            <dd className="m-0 font-medium break-all">
              <code>{ticket.ticketId}</code>
            </dd>

            <dt className="text-muted-foreground">La hora</dt>
            <dd className="m-0 font-medium break-words">
              <time dateTime={startsAt}>{ticket.event.startsAtLocal}</time>
              {ticket.event.timezone === "" ? null : (
                <span className="text-muted-foreground"> · {ticket.event.timezone}</span>
              )}
            </dd>

            <dt className="text-muted-foreground">Total del pedido</dt>
            <dd className="m-0 font-mono font-medium">{orderTotal}</dd>

            <dt className="text-muted-foreground">Emitida</dt>
            <dd className="m-0 font-medium">
              <time dateTime={ticket.issuedAt === "" ? undefined : ticket.issuedAt}>
                {ticket.issuedAt === "" ? "no servida" : ticket.issuedAt}
              </time>
            </dd>

            {/* The served status is a stored fact, and it is printed as one. It is not
                the door's answer: the card above is. */}
            <dt className="text-muted-foreground">Estado guardado</dt>
            <dd className="m-0 font-medium">
              {ticket.status.trim() === "" ? "no servido" : ticket.status.trim()}
            </dd>
          </dl>

          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            El total es lo que costó toda la reserva, no el precio de esta butaca: un pedido
            vende todas las butacas de la reserva y se cobra una sola vez. El estado guardado
            es con el que se emitió la entrada, no un veredicto sobre si admite a alguien.
          </p>
        </CardContent>
      </Card>

      {/* The one write of this surface: the mailed link, to an address typed here. It sits
          after the PDF because the PDF is what a person keeps, and before the retention note
          because that note closes the page. */}
      <TicketEmailPanel ticketId={ticket.ticketId} sendTicketEmail={sendTicketEmail} />

      {/* This surface's retention note, and its own polite live region: the send panel above
          has one too, because a send announcement is about a different thing and saying both
          through one region would make each of them unreadable. */}
      <p role="status" className="m-0 max-w-prose text-sm text-muted-foreground">
        {retentionNotices[retention]}
      </p>
    </>
  );
}

/** Whether this tab kept a record of the ticket it just opened. */
type Retention = "reading" | "kept" | "refused";

const retentionNotices: Record<Retention, string> = {
  reading: "Guardando esta entrada en la lista de esta pestaña…",
  kept: "Esta pestaña ya lista esta entrada en Mis entradas. Esa lista es la nota de este navegador sobre lo que abrió aquí: la de una cuenta es otra, y la responde la taquilla al abrir sesión.",
  refused:
    "Esta pestaña se negó a guardar un registro de esta entrada, así que no aparecerá en Mis entradas. La entrada no se ve afectada: este enlace la abre otra vez cuando quieras.",
};

/**
 * Keeps this ticket in the tab's list, once, and reports whether it could.
 *
 * Opening a ticket is the moment this browser learns about it: the purchase link is the source
 * of truth for a guest, and this note is what makes the next visit find it. An account's list is
 * the box office's answer instead (`GET /me/tickets`), so the two are different lists and neither
 * claims to be the other. `rememberIssuedTicket` is idempotent, so a double render (React's
 * strict mode in development) costs nothing.
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
            Motivo informado por la taquilla: <code>{validity.reason}</code>
          </p>
        )}
        <p className="m-0">{validity.nextStep}</p>
        {validity.retryable ? (
          <div className="flex flex-wrap gap-2 pt-1">
            <Button asChild size="sm">
              <Link href={`/tickets/${encodeURIComponent(ticketId)}`}>
                Comprobar el código otra vez
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
      return "Admite a su titular";
    case "voided":
      return "Anulada";
    case "event_cancelled":
      return "Sesión cancelada";
    case "reason_unrecognised":
      return "No válida";
    case "invalid_token":
      return "No es una entrada";
    case "unavailable":
      return "Sin comprobar";
    default:
      return "Comprobación rechazada";
  }
}

// ── The list of tickets this tab kept ───────────────────────────────────────

export interface KeptTicketListProps {
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
 * "Mis entradas" for a browser with no session: the ticket ids this tab kept, each one re-read.
 *
 * This is the guest half of the screen, and it admits what it is in its own words: it is a note
 * this tab keeps, not an account's list, and every id on it is read from the box office again
 * rather than trusted. (A browser that holds a session gets the account's list instead — see
 * `apps/web/src/app/tickets/page.tsx`.) Its states stay apart, because each one means something
 * different:
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
export function KeptTicketList({ readTicket }: KeptTicketListProps) {
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
          ? "Esta pestaña no pudo olvidar la entrada, así que la lista no ha cambiado."
          : `Olvidada en esta pestaña: la entrada ${ticketId}. La entrada misma no se ha tocado.`,
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
            Este navegador no muestra lo que guarda
          </h2>
          <AlertDescription className="flex flex-col gap-2">
            <p className="m-0">{kept.detail}</p>
            <p className="m-0">
              No es lo mismo que no tener ninguna entrada: la página no ha podido leer el
              registro de esta pestaña, así que no dice nada sobre lo que se compró. Una
              entrada sigue siendo alcanzable desde el enlace que devolvió el pago y desde la
              taquilla.
            </p>
          </AlertDescription>
        </Alert>
      ) : null}

      {kept?.kind === "empty" ? <NothingKept unreadable={kept.unreadable} /> : null}

      {ids.length > 0 ? (
        <section aria-labelledby="kept-tickets" className="flex flex-col gap-3">
          <h2 id="kept-tickets">Las entradas que guarda esta pestaña</h2>
          <p className="m-0 max-w-prose">
            {ids.length === 1 ? "1 entrada." : `${ids.length} entradas.`}{" "}
            <span className="text-muted-foreground">
              Cada una se leyó de la taquilla al cargar esta página, así que nada de aquí es un
              dato recordado. Un registro que esta pestaña ya no pueda usar es una fila, nunca
              la lista entera.
            </span>
          </p>
          {kept?.kind === "kept" && kept.unreadable > 0 ? (
            <p className="m-0 max-w-prose text-sm text-muted-foreground">
              {kept.unreadable === 1
                ? "1 registro que guardó esta pestaña no se pudo leer como identificador de entrada, así que no aparece en la lista."
                : `${kept.unreadable} registros que guardó esta pestaña no se pudieron leer como identificadores de entrada, así que no aparecen en la lista.`}
            </p>
          ) : null}

          <ul role="list" className="m-0 flex list-none flex-col gap-4 p-0">
            {ids.map((ticketId) => (
              <li key={ticketId} className="min-w-0">
                <KeptTicketCard
                  ticketId={ticketId}
                  row={rows[ticketId] ?? { kind: "reading" }}
                  onRetry={() => void retry(ticketId)}
                  onForget={() => forget(ticketId)}
                />
              </li>
            ))}
          </ul>

          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Esta lista es lo que guardó esta pestaña, no la lista de una cuenta: no hay ninguna
            sesión abierta en este navegador, así que nada de aquí se lee por nombre — y un
            enlace reenviado abre la misma entrada a quien lo tenga.
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
    return "Leyendo las entradas que guarda esta pestaña…";
  }
  if (kept.kind === "unavailable") {
    return "Este navegador no ha querido decir qué guarda.";
  }
  if (kept.kind === "empty") {
    return "Esta pestaña no guarda ninguna entrada.";
  }
  const reading = ids.filter((id) => rows[id]?.kind === "reading").length;
  if (reading > 0) {
    return `Leyendo ${reading === 1 ? "1 entrada" : `${reading} entradas`} que guarda esta pestaña…`;
  }
  return ids.length === 1
    ? "1 entrada que guarda esta pestaña, leída de la taquilla."
    : `${ids.length} entradas que guarda esta pestaña, leídas de la taquilla.`;
}

/**
 * One ticket of a list, in the shape both lists print.
 *
 * The guest list draws this for a ticket it could read, and the account's list draws it too
 * (`apps/web/src/app/tickets/page.tsx`), because a second rendering of a ticket is how two
 * screens start disagreeing about the film, the hour, the seat or the money. It is a client
 * component only because the module it lives in is one; it takes nothing but the ticket, so a
 * server component can render it.
 */
export function TicketSummary({ ticket }: { ticket: PublicTicket }) {
  // The heading id is derived from the ticket id and stripped of whitespace, so the
  // `aria-labelledby` target is always a valid id and always unique per row.
  const headingId = `kept-ticket-${ticket.ticketId.replace(/\s+/g, "-")}`;
  const title =
    ticket.event.filmTitle.trim() === "" ? "Una sesión" : ticket.event.filmTitle;
  const startsAt = ticket.event.startsAt === "" ? undefined : ticket.event.startsAt;

  return (
    <TicketStub
      headingId={headingId}
      filmTitle={title}
      cinemaName={ticket.event.cinemaName}
      roomName={ticket.event.roomName}
      startsAtLocal={ticket.event.startsAtLocal}
      startsAt={startsAt}
      seatLabel={ticket.seats.length === 0 ? "no servida" : ticket.seats.map(ticketSeatLabel).join(", ")}
      orderTotal={
        ticket.orderAmountDisplay === ""
          ? "no servido"
          : presentAmount(ticket.orderAmountDisplay)
      }
      statusWord={ticket.status.trim() === "" ? "estado no servido" : ticket.status.trim()}
      ticketId={ticket.ticketId}
      qrImageUrl={ticketQRImageUrl(ticket.ticketId)}
      note={`Si esta entrada sigue admitiendo a alguien lo responde la taquilla cuando se escanea, no esta lista: ábrela para ver la respuesta de este momento.${
        ticket.event.timezone === "" ? "" : ` La hora es la del propio local (${ticket.event.timezone}).`
      }`}
    >
      {/* The checkout's own link shape: the ticket page and the PDF, printed the same
          way on the confirmation and here. */}
      <IssuedTickets ticketIds={[ticket.ticketId]} />
    </TicketStub>
  );
}

/** One kept ticket, in whichever of its four states it is. */
function KeptTicketCard({
  ticketId,
  row,
  onRetry,
  onForget,
}: {
  ticketId: string;
  row: KeptTicketRow;
  onRetry: () => void;
  onForget: () => void;
}) {
  if (row.kind === "reading") {
    return (
      <Card className="flex flex-col gap-3 p-5">
        <p className="m-0 font-medium break-all">
          Leyendo la entrada <code>{ticketId}</code>…
        </p>
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
      </Card>
    );
  }

  if (row.kind === "found") {
    return <TicketSummary ticket={row.ticket} />;
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
          {row.kind === "unknown" ? "No conocida" : "Sin leer"}
        </Badge>
      </div>
      <AlertDescription className="flex flex-col gap-2">
        <p className="m-0">
          Entrada <code>{ticketId}</code>
        </p>
        <p className="m-0">
          <code>{row.problem.code}</code> · HTTP {row.problem.status}
        </p>
        <p className="m-0">{row.message}</p>
        <p className="m-0">{row.nextStep}</p>
        <div className="flex flex-wrap gap-2 pt-1">
          {row.kind === "unavailable" ? (
            <Button type="button" size="sm" onClick={onRetry}>
              Preguntar a la taquilla otra vez
            </Button>
          ) : null}
          <Button type="button" size="sm" variant="outline" onClick={onForget}>
            Olvidar este registro en este dispositivo
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
      <h2 id="kept-empty">Esta pestaña no guarda ninguna entrada</h2>
      <p className="m-0 max-w-prose">
        Esta pantalla enseña las entradas que este navegador guardó de las compras que hizo en
        él, así que está vacía hasta que se abra una aquí. No es la lista de una cuenta: no hay
        ninguna sesión abierta en este navegador, así que la taquilla no tiene ninguna lista que
        responderle.
      </p>
      <p className="m-0 max-w-prose text-sm text-muted-foreground">
        Una entrada se abre por su propio enlace: el que devolvió tu pago, o el PDF que
        guardaste. Abrirla aquí la añade a esta lista.
      </p>
      {unreadable > 0 ? (
        <p className="m-0 max-w-prose text-sm">
          {unreadable === 1
            ? "1 registro que guardó la pestaña no se pudo leer como identificador de entrada."
            : `${unreadable} registros que guardó la pestaña no se pudieron leer como identificadores de entrada.`}{" "}
          <span className="text-muted-foreground">
            No se ha borrado nada: un registro que esta página no puede leer se queda donde está.
          </span>
        </p>
      ) : null}
      <div className="flex flex-wrap gap-2 pt-1">
        <Button asChild size="sm">
          <Link href="/events">Ver qué hay a la venta</Link>
        </Button>
      </div>
    </Card>
  );
}

/** The list while the tab's own records are still being read. */
function ListLoading() {
  return (
    <section aria-labelledby="kept-loading" className="flex flex-col gap-3">
      <h2 id="kept-loading">Las entradas que guarda esta pestaña</h2>
      <Card aria-hidden="true" className="flex flex-col gap-3 p-5">
        <Skeleton className="h-5 w-1/2" />
        <Skeleton className="h-4 w-2/3" />
        <Skeleton className="h-4 w-1/3" />
      </Card>
    </section>
  );
}
