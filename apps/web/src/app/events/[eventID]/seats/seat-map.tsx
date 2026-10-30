"use client";

// Seat map for one event: the guest selection surface (M2 · S9, S10, S11).
//
// This is the only stateful part of the page. It owns the availability
// snapshot, the selection, and the current hold attempt, and it renders rows
// exactly as lib/seat-selection.ts models them: every row is an independent
// block with its own seat count, so a 3-seat row and a 5-seat row need no shared
// column count and no padding cell. The rows themselves come from the layout
// projection the availability read serves, so what renders is the event's real
// geometry; only a response without that projection falls back to a flat list.
//
// Rules that live in the pure module and are only applied here:
//
//   - a seat is clickable only when the snapshot says it is free (or already
//     selected), so a held or unknown seat is inert;
//   - every attempt mints a fresh Idempotency-Key (`beginHoldAttempt`);
//   - the countdown is `expiresAt` from the server minus the current clock, and
//     it ends in an explicit expired state;
//   - a `seat_unavailable` conflict refetches availability, drops the lost seats
//     from the selection and names them, while an expired window and a temporary
//     outage are reported as themselves.
import { useCallback, useEffect, useMemo, useState } from "react";
import type {
  AvailabilityResponse,
  HoldResponse,
  PublicEventSeat,
} from "@/lib/public-api";
import { MAX_HOLD_SEATS, newIdempotencyKey } from "@/lib/public-api";import type {
  AvailabilitySnapshotSeat,
  SeatMapCell,
  SeatMapLayout,
} from "@/lib/seat-selection";
import {
  applyHoldFailure,
  applyHoldSuccess,
  beginHoldAttempt,
  buildSeatMap,
  COUNTDOWN_TICK_MS,
  describeCountdown,
  describeHoldFailure,
  dropUnavailableSeats,
  idleAttempt,
  selectionSummary,
  toggleSeat,
} from "@/lib/seat-selection";
import { HoldForm } from "./hold-form";

export interface SeatSelectionPanelProps {
  eventId: string;
  /** The availability snapshot the server read for this request. */
  initialSeats: PublicEventSeat[];
  /** The map geometry, prepared by the page. */
  layout: SeatMapLayout;
  /** Server action: refetch availability without a page reload. */
  loadAvailability: (eventId: string) => Promise<AvailabilityResponse>;
  /** Server action: POST /reservations/holds with this attempt's key. */
  requestHold: (input: {
    eventId: string;
    seatIds: number[];
    idempotencyKey: string;
  }) => Promise<HoldResponse>;
}

/** Reduces a seat to the fields the pure model reads. */
function toSnapshot(
  seats: readonly PublicEventSeat[],
): AvailabilitySnapshotSeat[] {
  return seats.map((seat) => ({
    id: seat.id,
    held: seat.held,
    layoutSeatId: seat.layoutSeatId,
    state: seat.state,
  }));
}

export function SeatSelectionPanel({
  eventId,
  initialSeats,
  layout,
  loadAvailability,
  requestHold,
}: SeatSelectionPanelProps) {
  const [seats, setSeats] = useState<PublicEventSeat[]>(initialSeats);
  const [selection, setSelection] = useState<readonly number[]>([]);
  const [attempt, setAttempt] = useState(idleAttempt);
  const [lostLabels, setLostLabels] = useState<string[]>([]);
  const [nowMs, setNowMs] = useState(() => Date.now());

  const snapshot = useMemo(() => toSnapshot(seats), [seats]);
  const view = useMemo(
    () => buildSeatMap(layout, snapshot, selection),
    [layout, snapshot, selection],
  );
  const summary = useMemo(
    () => selectionSummary(selection, layout),
    [selection, layout],
  );
  const countdown = useMemo(
    () =>
      attempt.hold === null
        ? null
        : describeCountdown(attempt.hold.expiresAt, nowMs),
    [attempt.hold, nowMs],
  );
  const expired = countdown?.expired === true;

  // The clock is the only timer in the surface: it re-reads the wall clock while
  // a hold is live, and stops once the server deadline has passed. No value is
  // ever subtracted from a client-side guess.
  useEffect(() => {
    if (attempt.status !== "held" || expired) {
      return;
    }
    setNowMs(Date.now());
    const timer = setInterval(() => setNowMs(Date.now()), COUNTDOWN_TICK_MS);
    return () => clearInterval(timer);
  }, [attempt.status, expired]);

  const handleToggle = useCallback((cell: SeatMapCell) => {
    setSelection((previous) =>
      toggleSeat(previous, cell.eventSeatId, cell.status),
    );
    // A new pick is a new intent: the previous outcome no longer describes what
    // the guest is looking at.
    setLostLabels([]);
  }, []);

  const handleReset = useCallback(() => {
    setSelection([]);
    setAttempt(idleAttempt());
    setLostLabels([]);
  }, []);

  const handleSubmit = useCallback(async () => {
    // One attempt, one key: `beginHoldAttempt` mints it, and the request below
    // sends exactly that key so a retry can never replay an older outcome.
    const pending = beginHoldAttempt(attempt, newIdempotencyKey);
    const idempotencyKey = pending.idempotencyKey;
    if (idempotencyKey === null) {
      return;
    }
    setAttempt(pending);
    setLostLabels([]);

    const response = await requestHold({
      eventId,
      seatIds: [...summary.seatIds],
      idempotencyKey,
    });

    if (response.ok) {
      setAttempt((previous) => applyHoldSuccess(previous, response.hold));
      setNowMs(Date.now());
      return;
    }

    const failure = describeHoldFailure(response.problem);
    setAttempt((previous) => applyHoldFailure(previous, response.problem));
    if (!failure.refetchAvailability) {
      return;
    }

    // S10: a taken seat is normal traffic. Refetch availability, drop exactly
    // the seats that are gone, and let the form say which ones those were.
    const refreshed = await loadAvailability(eventId);
    if (!refreshed.ok) {
      return;
    }
    setSeats(refreshed.seats);
    const dropped = dropUnavailableSeats(
      selection,
      toSnapshot(refreshed.seats),
      layout,
    );
    setSelection(dropped.selection);
    setLostLabels(dropped.lostLabels);
  }, [
    attempt,
    eventId,
    layout,
    loadAvailability,
    requestHold,
    selection,
    summary.seatIds,
  ]);

  const canSubmit =
    summary.count > 0 &&
    summary.count <= MAX_HOLD_SEATS &&
    attempt.status !== "submitting" &&
    !expired;

  return (
    <>
      <section className="card" aria-labelledby="seat-map">
        <h2 id="seat-map">Seat map</h2>
        <p className="meta">{view.summaryLabel}</p>

        {layout.geometryKnown ? null : (
          <p className="notice">
            This API response carries no layout projection, so the map lists the
            event&apos;s seats in order with the physical seat id the API reports.
            Rows and seat kinds render as soon as the projection is served, which
            the current API does; nothing here is guessed.
          </p>
        )}

        {view.rows.length === 0 ? (
          <p className="notice">This event has no seats to select.</p>
        ) : (
          <ol className="rows" aria-label="Seat rows">
            {view.rows.map((row) => (
              <li className="row" key={row.label}>
                <div className="row-header">
                  <h3>Row {row.label}</h3>
                  <p className="meta">{row.seatCount} seat(s)</p>
                </div>
                <ul className="actions" aria-label={`Row ${row.label} seats`}>
                  {row.cells.map((cell) => (
                    <li key={cell.eventSeatId}>
                      <button
                        type="button"
                        aria-pressed={cell.status === "selected"}
                        aria-label={cell.description}
                        disabled={!cell.selectable}
                        onClick={() => handleToggle(cell)}
                      >
                        {cell.label}
                        {cell.kind === "unknown" ? null : ` · ${cell.kind}`}
                        {cell.status === "held" ? " · held" : ""}
                      </button>
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ol>
        )}

        <p className="path">
          Seat ids are event seat ids: the hold request refers to them directly.
        </p>
      </section>

      <HoldForm
        summary={summary}
        countdown={countdown}
        attempt={attempt}
        lostLabels={lostLabels}
        canSubmit={canSubmit}
        onSubmit={handleSubmit}
        onReset={handleReset}
      />

      {attempt.status === "held" && attempt.hold !== null && !expired ? (
        <ContinueToPayment eventId={eventId} reservationId={attempt.hold.id} />
      ) : null}
    </>
  );
}

/**
 * The way from a successful hold to the confirmation page (M3 · T7).
 *
 * A hold is not a purchase: the seats are only sold once they are paid for, so the
 * page that just acquired them points at the checkout. The event id travels as a
 * query parameter because there is no `GET /api/v1/reservations/{id}` route to ask
 * which event a reservation belongs to; the confirmation page uses it only to offer
 * the way back to this seat map.
 */
function ContinueToPayment({
  eventId,
  reservationId,
}: {
  eventId: string;
  reservationId: string;
}) {
  const href = `/reservations/${encodeURIComponent(reservationId)}?eventId=${encodeURIComponent(
    eventId,
  )}`;
  return (
    <section className="card" aria-labelledby="continue-to-payment">
      <h2 id="continue-to-payment">Seats held — one step left</h2>
      <p>
        Reservation <code>{reservationId}</code> holds your seats until its window
        closes. Tickets are issued when the payment is approved.
      </p>
      <div className="actions">
        <a className="download" href={href}>
          Continue to payment
        </a>
      </div>
    </section>
  );
}
