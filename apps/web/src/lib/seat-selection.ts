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
//
// The M3.5 additions (T5) keep that shape. The screening the buyer clicked is
// described here (`describeSaleState`, `describeScreeningFailure`) so the page can
// say why no seat is offered instead of drawing a dead map, and the running amount
// (`priceSummary`) is a summary of the screening's own price per seat, never the
// charge. The keyboard contract is a function too (`resolveSeatKey`), so what an
// arrow key does is a rule with a test rather than a handler with a hope. Finally,
// a seat is only selectable when the availability read says it is available: a
// `sold`, `unavailable` or unrecognised state is never offered (S8).
import type { PublicHold, PublicProblem } from "./public-api.ts";
import { MAX_HOLD_SEATS } from "./public-api.ts";

/** Seat kinds the domain knows, plus the honest "not projected" value. */
export type SeatKindLabel = "standard" | "accessible" | "companion" | "unknown";

/** One seat of the map: what the UI prints and which event seat it holds. */
export interface SeatMapSeat {
  eventSeatId: number;
  label: string;
  kind: SeatKindLabel;
  /** The layout row label, when the API served the projection (`A`). */
  rowLabel?: string;
  /** The seat number inside its own row, when the API served the projection. */
  seatNumber?: string;
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

export type SeatMapStatus =
  | "available"
  | "selected"
  | "held"
  | "sold"
  /**
   * The API reports the seat, but not as sellable: `unavailable`, a disabled
   * seat, or a state this milestone does not interpret. Never selectable.
   */
  | "blocked"
  /** The availability read did not report this seat at all. Never selectable. */
  | "unknown";

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
  soldCount: number;
  blockedCount: number;
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
  const states = new Map<number, string>();
  for (const seat of availability) {
    known.add(seat.id);
    if (seat.held) {
      held.add(seat.id);
    }
    // A snapshot with no state at all is an older API's answer, and it keeps the
    // older rule below: the `held` flag is then the only statement it made.
    if (seat.state !== undefined) {
      states.set(seat.id, seat.state);
    }
  }
  const selected = new Set(selection);

  let seatCount = 0;
  let availableCount = 0;
  let heldCount = 0;
  let soldCount = 0;
  let blockedCount = 0;
  let unknownCount = 0;

  const rows = layout.rows.map((row): SeatMapRowView => {
    const cells = row.seats.map((seat): SeatMapCell => {
      const status = statusOf(seat.eventSeatId, known, held, states, selected);
      seatCount += 1;
      if (status === "available" || status === "selected") {
        availableCount += 1;
      } else if (status === "held") {
        heldCount += 1;
      } else if (status === "sold") {
        soldCount += 1;
      } else if (status === "blocked") {
        blockedCount += 1;
      } else {
        unknownCount += 1;
      }
      return {
        eventSeatId: seat.eventSeatId,
        label: seat.label,
        kind: seat.kind,
        status,
        selectable: status === "available" || status === "selected",
        description: describeSeat(seat, status),
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
    soldCount,
    blockedCount,
    unknownCount,
    summaryLabel: `${seatCount} seat(s) · ${availableCount} available · ${heldCount} held`,
  };
}

/**
 * The state of one seat, from the availability read alone.
 *
 * The live hold is checked first, because selling a held seat is the failure that
 * costs a buyer. After that, only a state the API calls `available` opens a seat: a
 * `sold` one is paid for, `held` belongs to someone, and `unavailable` — or a state
 * this milestone does not know — is not sold here (S8). A response with no state at
 * all keeps the rule the map had before states were served, where the `held` flag
 * was the only statement the API made.
 */
function statusOf(
  eventSeatId: number,
  known: Set<number>,
  held: Set<number>,
  states: Map<number, string>,
  selected: Set<number>,
): SeatMapStatus {
  if (!known.has(eventSeatId)) {
    return "unknown";
  }
  if (held.has(eventSeatId)) {
    return "held";
  }
  const state = states.get(eventSeatId);
  if (state === undefined) {
    return selected.has(eventSeatId) ? "selected" : "available";
  }
  switch (state.trim().toLowerCase()) {
    case "available":
      return selected.has(eventSeatId) ? "selected" : "available";
    case "held":
      return "held";
    case "sold":
      return "sold";
    default:
      return "blocked";
  }
}

/**
 * The seat's own name plus its state: "Row A seat 3, available".
 *
 * The row and the number are the layout's, so the label a screen reader hears is
 * the one painted on the seat; the fallback for a response with no projection uses
 * the physical layout seat it does know rather than inventing a row.
 */
function describeSeat(seat: SeatMapSeat, status: SeatMapStatus): string {
  const spoken = seatStateEntry(status).spoken;
  const rowLabel = (seat.rowLabel ?? "").trim();
  const seatNumber = (seat.seatNumber ?? "").trim();
  if (rowLabel !== "" && seatNumber !== "") {
    return `Row ${rowLabel} seat ${seatNumber}, ${spoken}`;
  }
  return `${seat.label}, ${spoken}`;
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
      // The row and number travel with the seat so its accessible name can be the
      // one painted on it ("Row A seat 3") rather than a reconstruction.
      rowLabel,
      ...(number(seat) === "" ? {} : { seatNumber: number(seat) }),
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
  const seatNumber = number(seat);
  return seatNumber === "" ? `${rowLabel} seat ${seat.id}` : `${rowLabel}${seatNumber}`;
}

/** The served seat number, trimmed, or an empty string when the API sent none. */
function number(seat: AvailabilitySnapshotSeat): string {
  return (seat.seatNumber ?? "").trim();
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

// ── Seat states, named (M3.5 · T5 · S6, S8) ──────────────────────────────────
//
// Every state the map can draw has three names, and none of them is a colour: the
// legend's word, the sentence that explains it, and the short word a cell prints under
// its label. The legend and the map read from this one list, so a state cannot be
// explained in the legend with words the map does not use.

export interface SeatStateEntry {
  status: SeatMapStatus;
  /** The legend's own word for the state. */
  legend: string;
  /** What the legend says the state means. */
  legendDescription: string;
  /** The short word the cell prints under its label; empty for a free seat. */
  cellWord: string;
  /** What a screen reader hears as the state, inside the seat's own name. */
  spoken: string;
}

export const SEAT_STATE_LEGEND: readonly SeatStateEntry[] = [
  {
    status: "available",
    legend: "Available",
    legendDescription: "The API reports it as free. It can be selected.",
    cellWord: "",
    spoken: "available",
  },
  {
    status: "selected",
    legend: "Selected",
    legendDescription: "In your selection right now, and held for nobody yet.",
    cellWord: "selected",
    spoken: "selected",
  },
  {
    status: "held",
    legend: "Held",
    legendDescription: "Reserved by another checkout's live hold.",
    cellWord: "held",
    spoken: "held by another guest",
  },
  {
    status: "sold",
    legend: "Sold",
    legendDescription: "Already paid for. It is not for sale.",
    cellWord: "sold",
    spoken: "sold",
  },
  {
    status: "blocked",
    legend: "Not for sale",
    legendDescription:
      "The API reports it as unavailable, or in a state this map does not sell.",
    cellWord: "n/a",
    spoken: "not for sale",
  },
  {
    status: "unknown",
    legend: "Unknown",
    legendDescription:
      "The availability read did not report this seat, so it is never offered.",
    cellWord: "?",
    spoken: "unknown to the API",
  },
];

const SEAT_STATES_BY_STATUS = new Map<SeatMapStatus, SeatStateEntry>(
  SEAT_STATE_LEGEND.map((entry) => [entry.status, entry]),
);

/** One state's legend and wording. Every status has an entry is pinned by a test. */
export function seatStateEntry(status: SeatMapStatus): SeatStateEntry {
  const entry = SEAT_STATES_BY_STATUS.get(status);
  if (entry === undefined) {
    // Unreachable: the legend covers the union. Treated as the honest fallback
    // rather than thrown, so a future status cannot break a map that renders it.
    return {
      status: "unknown",
      legend: "Unknown",
      legendDescription: "This state has no legend entry.",
      cellWord: "?",
      spoken: "in an unexplained state",
    };
  }
  return entry;
}

/** The short word a seat cell prints for its state; empty for a free seat. */
export function seatStateWord(status: SeatMapStatus): string {
  return seatStateEntry(status).cellWord;
}

// ── The screening the buyer clicked (M3.5 · T5 · S3, S8) ─────────────────────
//
// The seat page is reached from the catalogue, so it owes the buyer the screening they
// chose. Two questions are answered here instead of in the component: the API is not
// selling (`onSale`), and the API could not be read. Neither re-derives anything from a
// clock, and the reason a sale stopped comes from the stored status rather than from
// the page's imagination.

/** Why a screening is not on sale. `closed` is the honest "and I do not know more". */
export type SaleStopReason = "cancelled" | "draft" | "started" | "closed";

export interface SaleState {
  onSale: boolean;
  /** Null exactly when the screening is on sale. */
  reason: SaleStopReason | null;
  /** The word the badge carries: the state, with the tone only reinforcing it. */
  badge: string;
  headline: string | null;
  message: string | null;
}

/**
 * What the page may say about the sale of one screening.
 *
 * `onSale` is the API's answer and is never recomputed: a screening that is on sale by
 * status but not by the flag has started, because that is the predicate the database
 * applied. Anything else is not guessed at — the stored status is printed verbatim, so
 * a status a later milestone adds reads as itself instead of as a silence.
 */
export function describeSaleState(screening: {
  status: string;
  onSale: boolean;
}): SaleState {
  if (screening.onSale) {
    return {
      onSale: true,
      reason: null,
      badge: "On sale",
      headline: null,
      message: null,
    };
  }

  const served = screening.status.trim();
  switch (served.toLowerCase()) {
    case "cancelled":
    case "canceled":
      return {
        onSale: false,
        reason: "cancelled",
        badge: "Cancelled",
        headline: "This screening was cancelled",
        message:
          "The cinema cancelled this screening, so its seats are no longer sold. Nothing on this page was charged: a seat is only paid for in the checkout.",
      };
    case "draft":
      return {
        onSale: false,
        reason: "draft",
        badge: "Not on sale yet",
        headline: "This screening is still a draft",
        message:
          "It has not been published for sale yet, so there are no seats to choose. The catalogue lists a screening once it is on sale.",
      };
    case "started":
    case "on_sale":
      // `status = 'on_sale'` and still not offered means the start time has passed:
      // the API decides `onSale` with the database clock, so this page does not.
      return {
        onSale: false,
        reason: "started",
        badge: "No longer on sale",
        headline: "This screening has already started",
        message:
          "Seats stop being sold once a screening starts. The venue's own clock decided that, not this page.",
      };
    default:
      return {
        onSale: false,
        reason: "closed",
        badge: "No longer on sale",
        headline: "This screening is not on sale",
        message: `The API reports this screening as ${served === "" ? "unknown" : served}, and only an on-sale screening can be bought. Nothing else about it is assumed here.`,
      };
  }
}

/** The three ways a screening read can fail, kept apart on purpose. */
export type ScreeningProblemKind = "missing" | "temporary" | "refused";

export interface ScreeningProblemView {
  kind: ScreeningProblemKind;
  headline: string;
  message: string;
  /** Whether asking the same question again could answer it. */
  retryable: boolean;
}

/**
 * Maps a failed screening read to the one honest thing the page can say.
 *
 * A 404 means the screening does not exist and no retry will change that; an outage is
 * retryable and is never rendered as an empty seat map; anything else is a refusal and
 * says so. The API's own code and detail are printed, because a failure the page cannot
 * explain is still better than one it disguises.
 */
export function describeScreeningFailure(
  problem: PublicProblem,
): ScreeningProblemView {
  if (problem.status === 404 || problem.code === "not_found") {
    return {
      kind: "missing",
      headline: "This screening does not exist",
      message:
        "No screening answers to this address, so there is nothing to sit in. Check the link you followed: the catalogue lists every screening that is on sale.",
      retryable: false,
    };
  }
  if (
    problem.code === "network_error" ||
    problem.code === "service_unavailable" ||
    problem.code === "coordination_unavailable" ||
    problem.status >= 500
  ) {
    return {
      kind: "temporary",
      headline: "The screening could not be loaded",
      message: `The reservation service could not be reached (${problem.code}). Nothing is shown in its place: ask for it again in a moment.`,
      retryable: true,
    };
  }
  return {
    kind: "refused",
    headline: "The API refused this screening",
    message: `${problem.code}: ${problem.detail}`,
    retryable: false,
  };
}

// ── The running amount (M3.5 · T5 · S3, S8) ──────────────────────────────────
//
// The page shows what the selected seats cost at the price the screening was read with.
// Money is never derived from a count of availability: the amount is the screening's own
// price per seat times the seats in the selection, and the copy says plainly that the
// order, not this sentence, is what gets paid.

/** A screening's price, as the API serves it: minor units and its display text. */
export interface SeatPrice {
  priceCents: number;
  priceDisplay: string;
}

export interface PriceSummary {
  count: number;
  amountCents: number;
  /** The amount in the API's display shape, for example `37.50`. */
  amountLabel: string;
  lineLabel: string;
  /** What the page says this number is — and is not. */
  note: string;
  /** Whether the selection is beyond what one hold accepts. */
  overLimit: boolean;
  limitNote: string | null;
}

/** What a summary is, said once: the charge is the order's, never this sentence's. */
const PRICE_SUMMARY_NOTE =
  "A summary, not a charge: this is the price per seat the screening was read with, times the seats you picked. The order the API creates is what gets paid, and the amount its payment answer reports is the amount taken.";

/**
 * Minor units as the API renders a price — `12.50`, and `0.00` for anything that is not
 * a whole, non-negative number of cents. Failing closed keeps a malformed amount out of
 * a sentence about money, which is the one place a wrong number is expensive.
 */
export function formatCents(amountCents: number): string {
  if (!Number.isInteger(amountCents) || amountCents < 0) {
    return "0.00";
  }
  const units = Math.floor(amountCents / 100);
  const cents = amountCents % 100;
  return `${units}.${String(cents).padStart(2, "0")}`;
}

/** The running total of a selection at the screening's price per seat. */
export function priceSummary(
  summary: SelectionSummary,
  price: SeatPrice,
): PriceSummary {
  const amountCents = summary.count * Math.max(0, Math.trunc(price.priceCents));
  const amountLabel = formatCents(amountCents);
  const perSeat = price.priceDisplay.trim();
  const lineLabel =
    summary.count === 0
      ? `No seats selected yet · ${perSeat} per seat`
      : `${summary.totalLabel.replace(" selected", "")} × ${perSeat} = ${amountLabel}`;
  const overLimit = summary.count > MAX_HOLD_SEATS;

  return {
    count: summary.count,
    amountCents,
    amountLabel,
    lineLabel,
    note: PRICE_SUMMARY_NOTE,
    overLimit,
    limitNote: overLimit
      ? `One hold takes at most ${MAX_HOLD_SEATS} seats. Deselect ${summary.count - MAX_HOLD_SEATS} to continue.`
      : null,
  };
}

// ── Which number the page prints (M3.5 · T5 · S8) ────────────────────────────
//
// A screening read carries how many seats are available; the seat read carries the map.
// The map is the later answer and the only one this page sells, so the page prints the
// map's numbers, and when the two disagree it says so instead of pretending the count
// was still true.

export interface SeatMapAvailability {
  seatCount: number;
  availableSeats: number;
  label: string;
  matchesScreening: boolean;
  /** Set only when the two reads disagree, naming both. */
  note: string | null;
}

/** The availability the page prints, taken from the map and reconciled with the read. */
export function mapAvailability(
  view: SeatMapView,
  screening: { seatCount: number; availableSeats: number },
): SeatMapAvailability {
  const label =
    view.seatCount === 0
      ? // An empty room is not a sold-out one: nothing was sold, and calling it
        // "sold out" would be a claim about demand the API never made.
        "No seats are sold for this screening"
      : view.availableCount === 0
        ? `0 of ${view.seatCount} seats available — sold out`
        : `${view.availableCount} of ${view.seatCount} seats available`;
  const matchesScreening =
    screening.seatCount === view.seatCount &&
    screening.availableSeats === view.availableCount;

  return {
    seatCount: view.seatCount,
    availableSeats: view.availableCount,
    label,
    matchesScreening,
    note: matchesScreening
      ? null
      : `The screening read counted ${screening.availableSeats} of ${screening.seatCount} seats available; the seat map was read afterwards and says ${view.availableCount} of ${view.seatCount}. The map is the later answer, and it is what this page sells.`,
  };
}

// ── The keyboard contract (M3.5 · T5 · S6) ───────────────────────────────────
//
// The map is movable without a mouse. The focus walks the seats as the room is drawn —
// arrows inside a row and between rows, Home and End inside one row — and Space or Enter
// toggles the seat it is on. Every rule below is a function so it can be tested without
// a browser, and the component only supplies the DOM.

/** The keys the map answers. */
export type SeatMapKey =
  | "ArrowLeft"
  | "ArrowRight"
  | "ArrowUp"
  | "ArrowDown"
  | "Home"
  | "End"
  | " "
  | "Enter";

export interface SeatKeyResult {
  /** The seat that should hold the focus after the key. */
  focusSeatId: number | null;
  /** Whether the key asks for a toggle; the selection rule decides if it happens. */
  toggle: boolean;
}

/**
 * The first seat the focus should land on: the first one that can be selected, so the
 * tab stop is a seat the buyer can use. With nothing selectable the focus still enters
 * the map, because a map of sold seats is a thing a person needs to be told about.
 */
export function initialSeatFocus(view: SeatMapView): number | null {
  for (const row of view.rows) {
    for (const cell of row.cells) {
      if (cell.selectable) {
        return cell.eventSeatId;
      }
    }
  }
  for (const row of view.rows) {
    if (row.cells.length > 0) {
      return row.cells[0].eventSeatId;
    }
  }
  return null;
}

/**
 * What one key does to the map's focus.
 *
 * Movement clamps instead of wrapping: a row ends where it ends, and the vertical walk
 * lands on the last seat of a shorter row rather than on a column that is not there. The
 * focus moves onto seats that cannot be selected on purpose — their state is the reason
 * they cannot be picked, and a focus that skipped them could never deliver it. Space and
 * Enter ask for a toggle and leave the focus where it is; the selection rule refuses the
 * toggle for any seat the API does not call available.
 */
export function resolveSeatKey(
  view: SeatMapView,
  focusedSeatId: number | null,
  key: string,
): SeatKeyResult {
  const position = seatPosition(view, focusedSeatId);

  if (key === " " || key === "Enter") {
    return position === null
      ? { focusSeatId: initialSeatFocus(view), toggle: false }
      : { focusSeatId: focusedSeatId, toggle: true };
  }
  if (!isMovementKey(key)) {
    return { focusSeatId: focusedSeatId, toggle: false };
  }
  if (position === null) {
    return { focusSeatId: initialSeatFocus(view), toggle: false };
  }

  const row = view.rows[position.rowIndex];
  let rowIndex = position.rowIndex;
  let cellIndex = position.cellIndex;

  switch (key) {
    case "ArrowLeft":
      cellIndex = Math.max(0, position.cellIndex - 1);
      break;
    case "ArrowRight":
      cellIndex = Math.min(row.cells.length - 1, position.cellIndex + 1);
      break;
    case "Home":
      cellIndex = 0;
      break;
    case "End":
      cellIndex = row.cells.length - 1;
      break;
    case "ArrowUp":
    case "ArrowDown": {
      const next = key === "ArrowUp" ? position.rowIndex - 1 : position.rowIndex + 1;
      if (next < 0 || next >= view.rows.length) {
        return { focusSeatId: focusedSeatId, toggle: false };
      }
      rowIndex = next;
      // A shorter row has no seat at this column, so the walk lands on its last one.
      const target = view.rows[rowIndex];
      cellIndex = Math.min(position.cellIndex, Math.max(0, target.cells.length - 1));
      break;
    }
  }

  const target = view.rows[rowIndex]?.cells[cellIndex];
  return {
    focusSeatId: target === undefined ? focusedSeatId : target.eventSeatId,
    toggle: false,
  };
}

function isMovementKey(key: string): boolean {
  return (
    key === "ArrowLeft" ||
    key === "ArrowRight" ||
    key === "ArrowUp" ||
    key === "ArrowDown" ||
    key === "Home" ||
    key === "End"
  );
}

/** Where a seat sits on the map, or null when the map does not draw it. */
function seatPosition(
  view: SeatMapView,
  eventSeatId: number | null,
): { rowIndex: number; cellIndex: number } | null {
  if (eventSeatId === null) {
    return null;
  }
  for (let rowIndex = 0; rowIndex < view.rows.length; rowIndex += 1) {
    const cells = view.rows[rowIndex].cells;
    for (let cellIndex = 0; cellIndex < cells.length; cellIndex += 1) {
      if (cells[cellIndex].eventSeatId === eventSeatId) {
        return { rowIndex, cellIndex };
      }
    }
  }
  return null;
}
