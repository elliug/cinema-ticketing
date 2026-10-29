// Pure seat selection model for the public event page (M2 · S9, S10, S11).
//
// Everything the seat page shows or decides is computed here first: the seat
// map geometry, which seats can be picked, the running total, the countdown
// derived from the server deadline, and the calm state each API failure maps
// to. The components stay presentational, so these rules are testable without a
// browser, a timer, or a network.
//
// Three invariants are load-bearing:
//
//   - Geometry is never assumed. A row owns its own seat list, so rows with
//     different seat counts render as they are and no padding seat is invented.
//     Every function iterates rows independently.
//   - The map is built from the layout projection the availability read serves
//     (`layoutFromAvailability`), so the browser renders the event's real rows;
//     a response without that projection degrades to one flat group and says so.
//   - The countdown never guesses. The remaining time is always
//     `serverExpiresAt - now`, and an unreadable deadline counts as elapsed, so
//     the UI can never promise a window the server did not grant.
import type { PublicHold, PublicProblem } from "./public-api.ts";

/** Seat kinds the domain knows, plus the honest "not projected" value. */
export type SeatKindLabel = "standard" | "accessible" | "companion" | "unknown";

/** One seat of the map: what the UI prints and which event seat it holds. */
export interface SeatMapSeat {
  eventSeatId: number;
  label: string;
  kind: SeatKindLabel;
}

/** One row of the map. Its seat count is its own; rows never share a width. */
export interface SeatMapRow {
  label: string;
  seats: SeatMapSeat[];
}

/**
 * The geometry the map renders.
 */
export interface SeatMapLayout {
  rows: SeatMapRow[];
  /**
   * Whether the rows come from a real layout projection. When false the map is
   * a flat list of the event's seats, which is all an older API serves.
   */
  geometryKnown: boolean;
}

/** The seat fields the availability snapshot contributes. */
export interface AvailabilitySnapshotSeat {
  id: number;
  held: boolean;
  layoutSeatId?: string;
  state?: string;
  /** Layout row label the seat belongs to, when the API serves the projection. */
  rowLabel?: string;
  /** Seat number inside its own row, when the API serves the projection. */
  seatNumber?: string;
  /** Seat kind as the API reports it, when the API serves the projection. */
  kind?: string;
}

export type SeatMapStatus = "available" | "held" | "unknown" | "selected";

/** One rendered seat cell. */
export interface SeatMapCell {
  eventSeatId: number;
  label: string;
  kind: SeatKindLabel;
  status: SeatMapStatus;
  /** Only an available or already selected seat accepts a click. */
  selectable: boolean;
  /** Accessible one-line description for the cell's control. */
  description: string;
}

export interface SeatMapRowView {
  label: string;
  cells: SeatMapCell[];
  seatCount: number;
}

/** Everything the seat map view renders. */
export interface SeatMapView {
  rows: SeatMapRowView[];
  geometryKnown: boolean;
  seatCount: number;
  availableCount: number;
  heldCount: number;
  unknownCount: number;
  summaryLabel: string;
}

/** The selected event seat ids, in the order the guest picked them. */
export type SeatSelection = readonly number[];

export const EMPTY_SELECTION: SeatSelection = [];

export interface SelectionSummary {
  count: number;
  seatIds: readonly number[];
  labels: string[];
  totalLabel: string;
}

/** How often the client re-reads the clock while a hold is live. */
export const COUNTDOWN_TICK_MS = 1000;

export interface HoldCountdown {
  remainingMs: number;
  /** `M:SS` of the remaining time. */
  label: string;
  /** What the countdown area prints: the time, or the expired state. */
  displayLabel: string;
  expired: boolean;
  notice: string | null;
}

/** The user-facing classification of a failed hold. */
export type HoldFailureKind =
  | "seat_unavailable"
  | "hold_window_expired"
  | "temporary"
  | "rejected"
  | "unknown";

export interface HoldFailureView {
  kind: HoldFailureKind;
  headline: string;
  message: string;
  /** Whether the page must refetch availability before it can retry. */
  refetchAvailability: boolean;
  /** Whether the same selection is worth trying again. */
  retryable: boolean;
  tone: "info" | "warning" | "error";
}

/** The state of one hold attempt. */
export interface HoldAttempt {
  status: "idle" | "submitting" | "held" | "failed";
  /** The key minted for this attempt, or null before the first one. */
  idempotencyKey: string | null;
  hold: PublicHold | null;
  failure: HoldFailureView | null;
}

/** Mints an Idempotency-Key for one attempt; injected so tests stay pure. */
export type KeyFactory = () => string;

/**
 * Builds the seat map for a layout and an availability snapshot.
 *
 * The snapshot decides availability; the layout decides geometry, labels and
 * kinds. Selection only decorates seats the snapshot reports as free, so a seat
 * taken by another guest never renders as selected.
 */
export function buildSeatMap(
  layout: SeatMapLayout,
  availability: readonly AvailabilitySnapshotSeat[],
  selection: SeatSelection = EMPTY_SELECTION,
): SeatMapView {
  const known = new Set<number>();
  const held = new Set<number>();
  for (const seat of availability) {
    known.add(seat.id);
    if (seat.held) {
      held.add(seat.id);
    }
  }
  const selected = new Set(selection);

  let seatCount = 0;
  let availableCount = 0;
  let heldCount = 0;
  let unknownCount = 0;

  const rows = layout.rows.map((row): SeatMapRowView => {
    const cells = row.seats.map((seat): SeatMapCell => {
      const status = statusOf(seat.eventSeatId, known, held, selected);
      seatCount += 1;
      if (status === "available" || status === "selected") {
        availableCount += 1;
      } else if (status === "held") {
        heldCount += 1;
      } else {
        unknownCount += 1;
      }
      return {
        eventSeatId: seat.eventSeatId,
        label: seat.label,
        kind: seat.kind,
        status,
        selectable: status === "available" || status === "selected",
        description: `${seat.label}, ${seat.kind} seat, ${status}`,
      };
    });
    return { label: row.label, cells, seatCount: cells.length };
  });

  return {
    rows,
    geometryKnown: layout.geometryKnown,
    seatCount,
    availableCount,
    heldCount,
    unknownCount,
    summaryLabel: `${seatCount} seat(s) · ${availableCount} available · ${heldCount} held`,
  };
}

function statusOf(
  eventSeatId: number,
  known: Set<number>,
  held: Set<number>,
  selected: Set<number>,
): SeatMapStatus {
  if (!known.has(eventSeatId)) {
    return "unknown";
  }
  if (held.has(eventSeatId)) {
    return "held";
  }
  return selected.has(eventSeatId) ? "selected" : "available";
}

/**
 * Adds or removes one seat from the selection.
 *
 * A seat that cannot be selected right now (held, unknown, or gone) leaves the
 * selection untouched, so the UI can never hold a seat the snapshot says is
 * taken.
 */
export function toggleSeat(
  selection: SeatSelection,
  eventSeatId: number,
  status: SeatMapStatus,
): SeatSelection {
  if (status !== "available" && status !== "selected") {
    return selection;
  }
  if (selection.includes(eventSeatId)) {
    return selection.filter((id) => id !== eventSeatId);
  }
  return [...selection, eventSeatId];
}

/**
 * The running total of a selection: how many seats, which labels, and the line
 * the UI prints. Money is not part of M2 (price bands arrive with checkout), so
 * the total is a seat count, never a fabricated amount.
 */
export function selectionSummary(
  selection: SeatSelection,
  layout: SeatMapLayout,
): SelectionSummary {
  const labelsBySeat = new Map<number, string>();
  for (const row of layout.rows) {
    for (const seat of row.seats) {
      if (!labelsBySeat.has(seat.eventSeatId)) {
        labelsBySeat.set(seat.eventSeatId, seat.label);
      }
    }
  }

  const seatIds: number[] = [];
  for (const id of selection) {
    if (!seatIds.includes(id)) {
      seatIds.push(id);
    }
  }

  const count = seatIds.length;
  return {
    count,
    seatIds,
    labels: seatIds.map((id) => labelsBySeat.get(id) ?? `seat ${id}`),
    totalLabel: count === 1 ? "1 seat selected" : `${count} seats selected`,
  };
}

/** `M:SS`, floored, and never negative. */
export function formatCountdown(remainingMs: number): string {
  const totalSeconds = Math.max(0, Math.floor(remainingMs / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

/**
 * Milliseconds left until the server deadline. An unreadable deadline is
 * reported as elapsed, which fails closed.
 */
export function remainingMilliseconds(expiresAt: string, nowMs: number): number {
  const deadline = Date.parse(expiresAt);
  if (!Number.isFinite(deadline) || !Number.isFinite(nowMs)) {
    return 0;
  }
  return Math.max(0, deadline - nowMs);
}

/** The countdown for one moment in time, ending in an explicit expired state. */
export function describeCountdown(
  expiresAt: string,
  nowMs: number,
): HoldCountdown {
  const remaining = remainingMilliseconds(expiresAt, nowMs);
  const expired = remaining <= 0;
  return {
    remainingMs: remaining,
    label: formatCountdown(remaining),
    displayLabel: expired ? "Hold expired" : formatCountdown(remaining),
    expired,
    notice: expired
      ? "This hold expired. The seats are no longer reserved for you."
      : null,
  };
}

/**
 * Maps a failed hold to one calm user-facing state.
 *
 * The cases stay distinct on purpose: a seat taken by another guest, a hold
 * window that closed on its own, and a temporary outage need three different
 * reactions, and only the first one requires refetching availability.
 */
export function describeHoldFailure(problem: PublicProblem): HoldFailureView {
  if (
    problem.code === "coordination_unavailable" ||
    problem.code === "network_error" ||
    problem.status === 503
  ) {
    return {
      kind: "temporary",
      headline: "Seats are temporarily unavailable",
      message:
        "We could not reach the reservation service. Your selection is unchanged, so try again in a moment.",
      refetchAvailability: false,
      retryable: true,
      tone: "warning",
    };
  }

  switch (problem.code) {
    case "seat_unavailable":
      return {
        kind: "seat_unavailable",
        headline: "Some of your seats were taken",
        message:
          "Another guest held some of your seats first, so we refreshed availability and removed those seats from your selection. Everything else is still selected.",
        refetchAvailability: true,
        retryable: true,
        tone: "warning",
      };
    case "hold_window_expired":
      return {
        kind: "hold_window_expired",
        headline: "The hold window closed",
        message:
          "The hold window closed before your seats could be claimed. This is about how long the claim took, not about anyone taking your seats. Select your seats again to start a new hold.",
        refetchAvailability: false,
        retryable: true,
        tone: "warning",
      };
    case "invalid_input":
    case "invalid_request":
    case "idempotency_key_reused":
    case "duplicate_seat":
    case "not_found":
      return {
        kind: "rejected",
        headline: "We could not start this hold",
        message: `${problem.code}: ${problem.detail}`,
        refetchAvailability: false,
        retryable: false,
        tone: "error",
      };
    default:
      return {
        kind: "unknown",
        headline: "Something unexpected happened",
        message: `${problem.detail} No seats were reserved. Reload the page and try again.`,
        refetchAvailability: false,
        retryable: false,
        tone: "error",
      };
  }
}

/** What a refetch removed from the selection. */
export interface DroppedSeats {
  selection: SeatSelection;
  lostEventSeatIds: number[];
  lostLabels: string[];
}

/**
 * Drops the seats a refetch reports as no longer free, and names them so the UI
 * can say which ones were lost instead of silently shrinking the selection.
 */
export function dropUnavailableSeats(
  selection: SeatSelection,
  availability: readonly AvailabilitySnapshotSeat[],
  layout: SeatMapLayout,
): DroppedSeats {
  const known = new Set<number>();
  const held = new Set<number>();
  for (const seat of availability) {
    known.add(seat.id);
    if (seat.held) {
      held.add(seat.id);
    }
  }

  const labels = new Map<number, string>();
  for (const row of layout.rows) {
    for (const seat of row.seats) {
      if (!labels.has(seat.eventSeatId)) {
        labels.set(seat.eventSeatId, seat.label);
      }
    }
  }

  const kept: number[] = [];
  const lostEventSeatIds: number[] = [];
  for (const id of selection) {
    if (held.has(id) || !known.has(id)) {
      lostEventSeatIds.push(id);
      continue;
    }
    kept.push(id);
  }

  return {
    selection: kept,
    lostEventSeatIds,
    lostLabels: lostEventSeatIds.map((id) => labels.get(id) ?? `seat ${id}`),
  };
}

/**
 * Builds the map from an availability snapshot.
 *
 * The public availability read joins the event's layout, so a seat normally
 * carries `rowLabel`, `seatNumber` and `kind`. When it does, the map is the real
 * geometry: one row per label, in the row-position order the server served (the
 * read orders rows by their position and seats by their position inside the
 * row), each row holding its own seats, with its own count and no padding. Seat
 * labels are the row label followed by the seat number, and a kind the domain
 * does not know reads as `unknown` rather than being echoed.
 *
 * A seat without a row label is not something this map can place, so a response
 * that carries no projection — or one that only labels some of its seats —
 * degrades to the honest minimum instead: one group of the event's seats ordered
 * by event seat id, each labelled with the physical layout seat the API reports
 * and with an explicitly unknown kind, plus `geometryKnown: false` so the page
 * can say so. Nothing is invented either way.
 */
export function layoutFromAvailability(
  seats: readonly AvailabilitySnapshotSeat[],
): SeatMapLayout {
  const projected = projectedRows(seats);
  if (projected !== null) {
    return { geometryKnown: true, rows: projected };
  }
  return fallbackLayout(seats);
}

/**
 * Groups the snapshot into its real rows, or null when it carries no usable
 * projection. Rows keep the order the server served, which is row-position
 * order; seats inside a row keep their served order, which is seat-position
 * order. Grouping is by label, so the same row is never split into two blocks.
 */
function projectedRows(
  seats: readonly AvailabilitySnapshotSeat[],
): SeatMapRow[] | null {
  if (seats.length === 0) {
    // An empty snapshot proves nothing about the geometry.
    return null;
  }
  const rows: SeatMapRow[] = [];
  const rowsByLabel = new Map<string, SeatMapRow>();
  for (const seat of seats) {
    const rowLabel = (seat.rowLabel ?? "").trim();
    if (rowLabel === "") {
      // Never place a seat in a row nobody labelled, and never drop it either.
      return null;
    }
    let row = rowsByLabel.get(rowLabel);
    if (row === undefined) {
      row = { label: rowLabel, seats: [] };
      rowsByLabel.set(rowLabel, row);
      rows.push(row);
    }
    row.seats.push({
      eventSeatId: seat.id,
      label: projectedSeatLabel(rowLabel, seat),
      kind: projectedSeatKind(seat.kind),
    });
  }
  return rows;
}

/** The flat, honest minimum for a server that serves no layout projection. */
function fallbackLayout(
  seats: readonly AvailabilitySnapshotSeat[],
): SeatMapLayout {
  const ordered = [...seats].sort((left, right) => left.id - right.id);
  return {
    geometryKnown: false,
    rows: [
      {
        label: "Seats",
        seats: ordered.map((seat) => ({
          eventSeatId: seat.id,
          label: layoutSeatLabel(seat),
          kind: "unknown",
        })),
      },
    ],
  };
}

/** `A1` for row A seat 1; the event seat id when the number is not served. */
function projectedSeatLabel(
  rowLabel: string,
  seat: AvailabilitySnapshotSeat,
): string {
  const number = (seat.seatNumber ?? "").trim();
  return number === "" ? `${rowLabel} seat ${seat.id}` : `${rowLabel}${number}`;
}

/** The seat kinds the domain knows; anything else is reported as unknown. */
const KNOWN_SEAT_KINDS: readonly SeatKindLabel[] = [
  "standard",
  "accessible",
  "companion",
];

function projectedSeatKind(kind: string | undefined): SeatKindLabel {
  const normalized = (kind ?? "").trim().toLowerCase();
  return (KNOWN_SEAT_KINDS as readonly string[]).includes(normalized)
    ? (normalized as SeatKindLabel)
    : "unknown";
}

function layoutSeatLabel(seat: AvailabilitySnapshotSeat): string {
  const id = (seat.layoutSeatId ?? "").trim();
  return id === "" ? `seat ${seat.id}` : id;
}

/** The state before the first hold attempt. */
export function idleAttempt(): HoldAttempt {
  return { status: "idle", idempotencyKey: null, hold: null, failure: null };
}

/**
 * Starts a hold attempt with a freshly minted Idempotency-Key.
 *
 * A key is never reused across attempts: the API answers a known key with the
 * stored outcome, so reusing the key of a failed or older attempt would replay
 * that result instead of asking for a new hold.
 */
export function beginHoldAttempt(
  previous: HoldAttempt,
  newKey: KeyFactory,
): HoldAttempt {
  return {
    status: "submitting",
    idempotencyKey: newKey(),
    hold: previous.hold === null ? null : previous.hold,
    failure: null,
  };
}

/** Records a successful hold; the countdown runs from the server deadline. */
export function applyHoldSuccess(
  attempt: HoldAttempt,
  hold: PublicHold,
): HoldAttempt {
  return { ...attempt, status: "held", hold, failure: null };
}

/** Records a failed hold together with its calm, user-facing view. */
export function applyHoldFailure(
  attempt: HoldAttempt,
  problem: PublicProblem,
): HoldAttempt {
  return {
    ...attempt,
    status: "failed",
    hold: null,
    failure: describeHoldFailure(problem),
  };
}
