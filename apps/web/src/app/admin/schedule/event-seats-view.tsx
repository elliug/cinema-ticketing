// Materialized event seats view.
//
// Presentation only: every row and the seat count come from the pure summary in
// lib/event-seats.ts, so what this component renders is exactly what the tested
// module produces. The list is ordered by event seat id, and each line states
// the layout seat the event seat was materialized from and its current state.
import type { EventSeatsSummary } from "@/lib/event-seats";

interface EventSeatsViewProps {
  summary: EventSeatsSummary;
}

export function EventSeatsView({ summary }: EventSeatsViewProps) {
  return (
    <section className="card" aria-labelledby="event-seats">
      <h2 id="event-seats">Materialized seats</h2>
      <p className="meta">
        Event <code>{summary.eventId}</code> · <strong>{summary.countLabel}</strong>
      </p>

      {summary.states.length > 0 ? (
        <p className="meta">
          {summary.states
            .map((entry) => `${entry.count} ${entry.state}`)
            .join(" · ")}
        </p>
      ) : null}

      {summary.seats.length === 0 ? (
        <p className="notice">
          This event has no materialized seats. The layout version it was
          scheduled from had no enabled seats.
        </p>
      ) : (
        <ul className="list" aria-label="Materialized seats">
          {summary.seats.map((seat) => (
            <li key={seat.id}>
              <code>{seat.label}</code>{" "}
              <span className="path">(event seat {seat.id})</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
