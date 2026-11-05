// Materialized event seats, prepared for display.
//
// Creating an event materializes exactly one event seat per enabled layout seat.
// The administrative API returns those seats as a flat list holding only their
// own id, the layout seat they came from, and their state, so no row structure
// is available here: the view is a stable list sorted by event seat id, plus a
// clear count of what was materialized.
//
// Since the Marquesina redesign this summary is also the box office's ledger for one session:
// it states the counts by name (libres, reservadas, vendidas, no disponibles) and the share of
// the room that is taken, so the screen prints numbers it was given instead of working them
// out in JSX. Two rules are load-bearing:
//
//   - **a state is a word.** The column stores `available`, `held`, `sold` and `unavailable`;
//     a cashier reads "libre", "reservada", "vendida" and "no disponible". A state this build
//     has not learned is printed verbatim, because a blank would hide a real seat;
//   - **the occupancy is held plus sold over the materialized seats.** Those are the two ways
//     a seat leaves the sale; `available` and `unavailable` are not taken by anybody. A
//     session with nothing materialized has no share to paint, and the answer is 0 — never a
//     division by zero and never a full bar.
import type { AdminEventSeat, AdminEventSeats } from "./admin-api.ts";

/** One materialized event seat, ready to render. */
export interface EventSeatRow {
  /** Event seat id; the list is ordered by this value. */
  id: number;
  /** The layout seat this event seat was materialized from. */
  layoutSeatId: string;
  /** Seat state as the API reports it, for example `available` or `held`. */
  state: string;
  /** The state in a word a cashier reads: `libre`, `reservada`, `vendida`. */
  stateLabel: string;
  /** The single line the view renders for this seat. */
  label: string;
}

/** How many seats share one state. */
export interface EventSeatStateCount {
  state: string;
  /** The state in the same word [eventSeatStateWord] prints for one seat. */
  word: string;
  count: number;
}

/** The ledger's counts: the four stored states by name, and the share of the room taken. */
export interface EventSeatCounts {
  /** Every materialized seat, which is also what a layout's enabled seats produced. */
  total: number;
  available: number;
  held: number;
  sold: number;
  unavailable: number;
  /** Held or sold: the seats that are no longer for sale to somebody else. */
  taken: number;
  /** [taken] of [total] as a whole percent, 0 when nothing is materialized. */
  occupancyPercent: number;
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
  /** The named counts and the occupancy share. */
  counts: EventSeatCounts;
}

/**
 * The word this screen prints for one stored seat state.
 *
 * The four states the column accepts (`db/migrations/0002_venue_layout.up.sql`), in Castilian
 * since the Marquesina redesign. A state the column grew later is handed back as served
 * rather than filtered out: an unknown word beside a real seat is honest, a blank is not.
 */
export function eventSeatStateWord(state: string): string {
  switch (state) {
    case "available":
      return "libre";
    case "held":
      return "reservada";
    case "sold":
      return "vendida";
    case "unavailable":
      return "no disponible";
    default:
      return state;
  }
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
    const created: EventSeatStateCount = {
      state: row.state,
      word: row.stateLabel,
      count: 1,
    };
    seen.set(row.state, created);
    states.push(created);
  }

  const total = rows.length;
  const counts: EventSeatCounts = {
    total,
    available: countOf(states, "available"),
    held: countOf(states, "held"),
    sold: countOf(states, "sold"),
    unavailable: countOf(states, "unavailable"),
    taken: countOf(states, "held") + countOf(states, "sold"),
    occupancyPercent: 0,
  };
  counts.occupancyPercent =
    total === 0 ? 0 : Math.round((counts.taken / total) * 100);

  return {
    eventId: value.eventId,
    total,
    countLabel:
      total === 1 ? "1 butaca materializada" : `${total} butacas materializadas`,
    seats: rows,
    states,
    counts,
  };
}

function countOf(states: readonly EventSeatStateCount[], state: string): number {
  return states.find((entry) => entry.state === state)?.count ?? 0;
}

function toRow(seat: AdminEventSeat): EventSeatRow {
  const stateLabel = eventSeatStateWord(seat.state);
  return {
    id: seat.id,
    layoutSeatId: seat.layoutSeatId,
    state: seat.state,
    stateLabel,
    label: `${seat.layoutSeatId} · ${stateLabel}`,
  };
}
