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
// ticket for whoever holds it. An account lists the tickets bought with a proved address, but
// it does not protect them — this page is as public as the link it is opened from, and it says
// so rather than implying that it checked who you are.
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
} from "@/lib/public-api";
import type { PublicProblem } from "@/lib/public-api";
import { TicketView } from "./ticket-view";
import { sendTicketEmailAction } from "./actions";
import { ticketPDFUrl, ticketQRImageUrl } from "@/lib/ticket-media";

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

  const venue = [ticket.event.cinemaName, ticket.event.roomName]
    .map((part) => part.trim())
    .filter((part) => part !== "")
    .join(" · ");

  return (
    <>
      <PageHeader
        eyebrow="Entrada"
        title="Tu entrada"
        description={`${venue === "" ? "La taquilla no sirvió ni el cine ni la sala." : `${venue}.`} La hora es la del propio local. Esta página se abre con el enlace de la propia entrada: nada aquí comprueba quién eres, y el enlace es lo que la abre. Una compra que dejó una dirección recibió también un enlace a esta entrada, y ese enlace caduca, así que el PDF se sigue descargando aquí.`}
      />
      {/* The film is the stub's own heading, so the page's one `h1` names the screen and the
          paper below names what it is for. */}
      <TicketView
        ticket={ticket}
        checked={checked}
        qrImageUrl={ticketQRImageUrl(ticket.ticketId)}
        pdfUrl={ticketPDFUrl(ticket.ticketId)}
        sendTicketEmail={sendTicketEmailAction}
      />
    </>
  );
}

/** The address names no ticket: there is nothing to read and nothing to check. */
function MissingTicket() {
  return (
    <>
      <PageHeader
        eyebrow="Entrada"
        title="Tu entrada"
        description="Esta dirección no nombra ninguna entrada, así que no hay nada que leer."
      />
      <Alert variant="destructive" role="alert" aria-labelledby="ticket-missing">
        <h2 id="ticket-missing" className="m-0 text-base font-semibold">
          Esta dirección no nombra ninguna entrada
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">
            Una entrada se abre por su propio identificador. Ábrela desde el enlace que devolvió
            tu pago, o desde la lista de entradas que guardó este navegador.
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            <Button asChild size="sm">
              <Link href="/tickets">Mis entradas</Link>
            </Button>
            <Button asChild variant="outline" size="sm">
              <Link href="/events">Ver qué hay a la venta</Link>
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
        eyebrow="Entrada"
        title="Tu entrada"
        description="La taquilla fue preguntada por esta entrada, y la respuesta no describió ninguna."
      />
      <Alert
        variant="destructive"
        role="alert"
        aria-labelledby="ticket-problem"
        className="flex flex-col gap-2"
      >
        <h2 id="ticket-problem" className="m-0 text-base font-semibold">
          {row.kind === "unknown" ? row.headline : "No se pudo cargar la entrada"}
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">
            <code>{problem.code}</code> · HTTP {problem.status}
          </p>
          <p className="m-0">{problem.detail}</p>
          <p className="m-0">
            {row.kind === "unknown" || row.kind === "unavailable"
              ? row.nextStep
              : "Esta página no interpreta el rechazo como un veredicto sobre la entrada."}
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            {row.kind === "unavailable" ? (
              <Button asChild size="sm">
                <Link href={`/tickets/${encodeURIComponent(ticketId)}`}>Preguntar otra vez</Link>
              </Button>
            ) : null}
            <Button asChild variant="outline" size="sm">
              <Link href="/tickets">Mis entradas</Link>
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
        eyebrow="Entrada"
        title="Tu entrada"
        description="Leyendo de la taquilla la entrada, su sesión y su código."
      />
      <section aria-labelledby="ticket-loading" className="flex flex-col gap-3">
        <h2 id="ticket-loading">Tu entrada</h2>
        <p role="status" className="m-0 max-w-prose">
          <span className="text-muted-foreground">Cargando la entrada…</span>
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
