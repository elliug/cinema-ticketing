// Materialized event seats view (M3.5 · T8 · S3; the ledger since visual-redesign).
//
// Presentation only: every row and the seat count come from the pure summary in
// lib/event-seats.ts, so what this component renders is exactly what the tested module
// produces. The list is ordered by event seat id, and each line states the layout seat the
// event seat was materialized from and its current state — a word, not a colour.
//
// Since the Marquesina redesign this card is the box office's ledger for one session: what is
// sold, what is held, what is still free, the share of the room that is taken, and how the
// sale reads in the board's own vocabulary (`En venta`, `Quedan 4`, `Agotada`). All six
// numbers arrive from `summarizeEventSeats`, so nothing here counts anything itself, and none
// of them is invented for a screening whose seats were never read.
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import type { EventSeatsSummary } from "@/lib/event-seats";
import { describeSaleStatus } from "@/lib/screening-display";

interface EventSeatsViewProps {
  summary: EventSeatsSummary;
}

/** The tone that agrees with the board's state; the word inside carries it. */
const SALE_TONE: Record<string, BadgeProps["variant"]> = {
  "on-sale": "success",
  few: "warning",
  "sold-out": "danger",
};

/** One ledger line: what the count is, and the count in the mono face. */
function CountLine({ label, value }: { label: string; value: number }) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b border-border py-1">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="ledger__num m-0 text-sm">{value}</dd>
    </div>
  );
}

export function EventSeatsView({ summary }: EventSeatsViewProps) {
  const sale = describeSaleStatus(summary.counts.available);

  return (
    <Card>
      <CardHeader>
        <h2 className="m-0 text-lg leading-tight font-semibold">Butacas materializadas</h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Sesión <code>{summary.eventId}</code> ·{" "}
          <strong className="text-foreground">{summary.countLabel}</strong>
        </p>
        {summary.states.length > 0 ? (
          <p className="m-0 flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">Estados:</span>
            {summary.states.map((entry) => (
              <Badge key={entry.state} variant={entry.state === "available" ? "success" : "secondary"}>
                {entry.count} {entry.word}
              </Badge>
            ))}
          </p>
        ) : null}
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        {summary.seats.length === 0 ? null : (
          <div className="flex flex-col gap-3">
            <dl className="m-0 flex flex-col">
              <CountLine label="Vendidas" value={summary.counts.sold} />
              <CountLine label="Reservadas" value={summary.counts.held} />
              <CountLine label="Libres" value={summary.counts.available} />
              <CountLine label="No disponibles" value={summary.counts.unavailable} />
            </dl>

            {/* The bar is the second reading, never the only one: the same share is printed
                right above it and the sale's state is a word, so the drawing itself is
                decoration and stays out of the accessibility tree. */}
            <div className="flex flex-col gap-2">
              <div className="flex flex-wrap items-baseline justify-between gap-2 text-sm">
                <span className="text-muted-foreground">Butacas ocupadas</span>
                <span className="ledger__num">
                  {summary.counts.taken} / {summary.counts.total} · {summary.counts.occupancyPercent}%
                </span>
              </div>
              <div className="meter" aria-hidden="true">
                <span
                  className="meter__fill"
                  style={{ width: `${summary.counts.occupancyPercent}%` }}
                />
              </div>
              <p className="m-0 flex flex-wrap items-center gap-2 text-sm">
                <span className="text-muted-foreground">Esta sesión se lee</span>
                <Badge variant={SALE_TONE[sale.state]}>{sale.word}</Badge>
              </p>
            </div>
          </div>
        )}

        {summary.seats.length === 0 ? (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Esta sesión no tiene butacas materializadas: la versión del plano con la que se
            programó no tenía ninguna butaca habilitada.
          </p>
        ) : (
          <ul
            role="list"
            aria-label="Butacas materializadas"
            className="m-0 flex list-none flex-col gap-1 p-0 text-sm"
          >
            {summary.seats.map((seat) => (
              <li key={seat.id} className="flex flex-wrap items-baseline gap-2">
                <code>{seat.layoutSeatId}</code>
                <Badge variant={seat.state === "available" ? "success" : "secondary"}>
                  {seat.stateLabel}
                </Badge>
                <span className="text-muted-foreground">
                  (butaca de la sesión {seat.id})
                </span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
