// Materialized event seats, prepared for display.
//
// Creating an event materializes exactly one event seat per enabled layout seat.
// The administrative API returns those seats as a flat list holding only their
// own id, the layout seat they came from, and their state, so no row structure
// is available here: the view is a stable list sorted by event seat id, plus a
// clear count of what was materialized.
import type { AdminEventSeat, AdminEventSeats } from "./admin-api.ts";

/** One materialized event seat, ready to render. */
export interface EventSeatRow {
  /** Event seat id; the list is ordered by this value. */
  id: number;
  /** The layout seat this event seat was materialized from. */
  layoutSeatId: string;
  /** Seat state as the API reports it, for example `available` or `held`. */
  state: string;
  /** The single line the view renders for this seat. */
  label: string;
}

/** How many seats share one state. */
export interface EventSeatStateCount {
  state: string;
  count: number;
}

/** Everything the event seat view renders. */
export interface EventSeatsSummary {
  eventId: string;
  /** Number of materialized seats. */
  total: number;
  /** The count as the view states it. */
  countLabel: string;
  /** Materialized seats, ordered by event seat id. */
  seats: EventSeatRow[];
  /** State totals, in first-seen order. */
  states: EventSeatStateCount[];
}

/**
 * Prepares the materialized seats of one event for rendering.
 *
 * The input is never mutated: the returned list is a new array sorted by seat
 * id, so the same response always renders in the same order.
 */
export function summarizeEventSeats(value: AdminEventSeats): EventSeatsSummary {
  const seats = Array.isArray(value.seats) ? value.seats : [];
  const rows = seats
    .map(toRow)
    .sort((left, right) => left.id - right.id);

  const states: EventSeatStateCount[] = [];
  const seen = new Map<string, EventSeatStateCount>();
  for (const row of rows) {
    const existing = seen.get(row.state);
    if (existing) {
      existing.count += 1;
      continue;
    }
    const created: EventSeatStateCount = { state: row.state, count: 1 };
    seen.set(row.state, created);
    states.push(created);
  }

  const total = rows.length;
  return {
    eventId: value.eventId,
    total,
    countLabel: `${total} materialized seat(s)`,
    seats: rows,
    states,
  };
}

function toRow(seat: AdminEventSeat): EventSeatRow {
  return {
    id: seat.id,
    layoutSeatId: seat.layoutSeatId,
    state: seat.state,
    label: `${seat.layoutSeatId} · ${seat.state}`,
  };
}
