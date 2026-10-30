// Materialized event seats view (M3.5 · T8 · S3).
//
// Presentation only: every row and the seat count come from the pure summary in
// lib/event-seats.ts, so what this component renders is exactly what the tested module
// produces. The list is ordered by event seat id, and each line states the layout seat the
// event seat was materialized from and its current state — a word, not a colour.
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import type { EventSeatsSummary } from "@/lib/event-seats";

interface EventSeatsViewProps {
  summary: EventSeatsSummary;
}

export function EventSeatsView({ summary }: EventSeatsViewProps) {
  return (
    <Card>
      <CardHeader>
        <h2 className="m-0 text-lg leading-tight font-semibold">Materialized seats</h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          Screening <code>{summary.eventId}</code> · <strong>{summary.countLabel}</strong>
        </p>
        {summary.states.length > 0 ? (
          <p className="m-0 flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">States:</span>
            {summary.states.map((entry) => (
              <Badge key={entry.state} variant={entry.state === "available" ? "success" : "secondary"}>
                {entry.count} {entry.state}
              </Badge>
            ))}
          </p>
        ) : null}
      </CardHeader>
      <CardContent>
        {summary.seats.length === 0 ? (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            This screening has no materialized seats. The layout version it was scheduled from
            had no enabled seats.
          </p>
        ) : (
          <ul
            role="list"
            aria-label="Materialized seats"
            className="m-0 flex list-none flex-col gap-1 p-0 text-sm"
          >
            {summary.seats.map((seat) => (
              <li key={seat.id} className="flex flex-wrap items-baseline gap-2">
                <code>{seat.layoutSeatId}</code>
                <Badge variant={seat.state === "available" ? "success" : "secondary"}>
                  {seat.state}
                </Badge>
                <span className="text-muted-foreground">(event seat {seat.id})</span>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
