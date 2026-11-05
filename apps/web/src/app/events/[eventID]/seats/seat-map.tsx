"use client";

// The seat map of one screening (M3.5 · T5 · S6 · M2 · S9, S10, S11).
//
// This is the only stateful part of the page. It owns the availability snapshot, the
// selection, the keyboard focus and the current hold attempt, and it renders the rows
// exactly as lib/seat-selection.ts models them: every row is an independent block with
// its own seats, so a 3-seat row and a 5-seat row need no shared column count and no
// padding seat. The rows come from the layout projection the availability read serves,
// so what is drawn is the event's real geometry; a response without that projection
// degrades to one flat group and says so.
//
// Accessibility is the point of this component, not a coat of paint:
//
//   - every seat is a button whose accessible name is its own ("Fila A, butaca 3: libre"),
//     so a screen reader never reads "A1 button" and leaves the state to the eye;
//   - the state is carried by words as well as by tone: a cell prints its state word
//     under its seat number, the legend says what each one means, and the border style
//     differs per state, so the map survives greyscale (S6);
//   - the keyboard contract is the pure `resolveSeatKey` rule: arrows walk the seats,
//     Home and End jump inside a row, Space and Enter toggle the focused seat, and a
//     roving `tabIndex` means the map is one tab stop instead of two hundred;
//   - one polite live region announces the selection count and the running amount, and
//     says why a seat that cannot be selected refused the toggle. It is the only live
//     region here, so a change is announced once;
//   - a seat is selectable only when the availability read calls it available, and the
//     running amount is a summary of the screening's price per seat — never a promise
//     about the charge (S8).
//
// The Marquesina look (visual-redesign · T5) is the room itself: the plan sits in a dark
// auditorium, a curved amber band over it says "PANTALLA", and a seat is a small rounded
// square — amber when it is yours, a dim recessed square when it is taken. Every one of
// those cues is a token utility, so the palette stays in globals.css, and every one of them
// is redundant with the word the cell prints.
//
// Rules that live in the pure module and are only applied here:
//
//   - a seat accepts a toggle only when the snapshot says it is free (or already
//     selected), so a held, sold or unavailable seat is inert;
//   - every attempt mints a fresh Idempotency-Key (`beginHoldAttempt`);
//   - the countdown is `expiresAt` from the server minus the current clock, and it ends
//     in an explicit expired state;
//   - a `seat_unavailable` conflict refetches availability, drops the lost seats from
//     the selection and names them, while an expired window and a temporary outage are
//     reported as themselves.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type {
  AvailabilityResponse,
  HoldResponse,
  PublicEventSeat,
  PublicScreening,
} from "@/lib/public-api";
import { MAX_HOLD_SEATS, newIdempotencyKey } from "@/lib/public-api";
import type {
  AvailabilitySnapshotSeat,
  SeatMapCell,
  SeatMapLayout,
  SeatMapStatus,
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
  initialSeatFocus,
  mapAvailability,
  priceSummary,
  resolveSeatKey,
  seatStateWord,
  selectionSummary,
  SEAT_STATE_LEGEND,
  toggleSeat,
} from "@/lib/seat-selection";
import { HoldForm } from "./hold-form";

export interface SeatSelectionPanelProps {
  eventId: string;
  /** The screening this map belongs to: its price and the counts it was read with. */
  screening: PublicScreening;
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

/** Keys that move the focus, so a key that only clamps still stops the page scrolling. */
const MOVEMENT_KEYS = new Set([
  "ArrowLeft",
  "ArrowRight",
  "ArrowUp",
  "ArrowDown",
  "Home",
  "End",
]);

/**
 * How each seat state is drawn.
 *
 * The tone is one signal of several: the border style and weight differ per state, the
 * cell prints its own state word, and the accessible name spells the state out. Strip
 * the colour away and every state is still distinguishable — a free seat is a lit square, a
 * selected one is filled and bold, a taken one is recessed into the dark with its label
 * struck through.
 */
const SEAT_STATE_CLASSES: Record<SeatMapStatus, string> = {
  available:
    "border border-border bg-secondary text-foreground hover:border-primary hover:text-primary",
  selected:
    "border border-primary bg-primary text-primary-foreground font-bold hover:bg-primary/90",
  held: "border border-dashed border-border bg-background text-muted-foreground",
  sold: "border border-border bg-background text-muted-foreground line-through",
  blocked:
    "border border-dotted border-muted-foreground bg-card text-muted-foreground",
  unknown:
    "border border-dotted border-border bg-background text-muted-foreground",
};

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
  screening,
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
  const [notice, setNotice] = useState<string | null>(null);
  const [focusedSeatId, setFocusedSeatId] = useState<number | null>(null);
  const buttonRefs = useRef(new Map<number, HTMLButtonElement>());

  const snapshot = useMemo(() => toSnapshot(seats), [seats]);
  const view = useMemo(
    () => buildSeatMap(layout, snapshot, selection),
    [layout, snapshot, selection],
  );
  const summary = useMemo(
    () => selectionSummary(selection, layout),
    [selection, layout],
  );
  // The price is the screening's own. The amount is a summary of it — `priceSummary`
  // says so in the sentence it returns, and the order decides what is charged.
  const priced = useMemo(
    () =>
      priceSummary(summary, {
        priceCents: screening.priceCents,
        priceDisplay: screening.priceDisplay,
      }),
    [summary, screening.priceCents, screening.priceDisplay],
  );
  // What the page prints about availability: the map's numbers, reconciled against the
  // count the screening read carried. Never the count on its own (S8).
  const availability = useMemo(
    () => mapAvailability(view, screening),
    [view, screening],
  );
  const countdown = useMemo(
    () =>
      attempt.hold === null
        ? null
        : describeCountdown(attempt.hold.expiresAt, nowMs),
    [attempt.hold, nowMs],
  );
  const expired = countdown?.expired === true;

  // The roving tab stop: the seat the keyboard is on, unless the map has moved out from
  // under it, in which case the entry point is recomputed rather than left dangling.
  const seatIds = useMemo(
    () => new Set(view.rows.flatMap((row) => row.cells.map((cell) => cell.eventSeatId))),
    [view],
  );
  const tabStop =
    focusedSeatId !== null && seatIds.has(focusedSeatId)
      ? focusedSeatId
      : initialSeatFocus(view);

  // The clock is the only timer in the surface: it re-reads the wall clock while a hold
  // is live, and stops once the server deadline has passed. No value is ever subtracted
  // from a client-side guess.
  useEffect(() => {
    if (attempt.status !== "held" || expired) {
      return;
    }
    setNowMs(Date.now());
    const timer = setInterval(() => setNowMs(Date.now()), COUNTDOWN_TICK_MS);
    return () => clearInterval(timer);
  }, [attempt.status, expired]);

  const handleToggle = useCallback((cell: SeatMapCell) => {
    if (!cell.selectable) {
      // A seat that cannot be picked is not silent: the announcement is the reason,
      // which is the whole point of letting the focus reach it.
      setNotice(`${cell.description}. No se puede seleccionar.`);
      return;
    }
    setNotice(null);
    setSelection((previous) =>
      toggleSeat(previous, cell.eventSeatId, cell.status),
    );
    // A new pick is a new intent: the previous outcome no longer describes what the
    // guest is looking at.
    setLostLabels([]);
  }, []);

  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLButtonElement>, cell: SeatMapCell) => {
      const result = resolveSeatKey(view, cell.eventSeatId, event.key);
      if (result.toggle) {
        // A native button also fires a click for Enter and Space; the toggle is applied
        // once, from the tested rule, instead of twice.
        event.preventDefault();
        handleToggle(cell);
        return;
      }
      if (!MOVEMENT_KEYS.has(event.key)) {
        return;
      }
      // Stopped even when the focus cannot move (a row end, the last row), so the page
      // never scrolls out from under a keyboard user who is walking the map.
      event.preventDefault();
      if (result.focusSeatId === null) {
        return;
      }
      setFocusedSeatId(result.focusSeatId);
      buttonRefs.current.get(result.focusSeatId)?.focus();
    },
    [handleToggle, view],
  );

  const handleReset = useCallback(() => {
    setSelection([]);
    setAttempt(idleAttempt());
    setLostLabels([]);
    setNotice(null);
  }, []);

  const handleSubmit = useCallback(async () => {
    // One attempt, one key: `beginHoldAttempt` mints it, and the request below sends
    // exactly that key so a retry can never replay an older outcome.
    const pending = beginHoldAttempt(attempt, newIdempotencyKey);
    const idempotencyKey = pending.idempotencyKey;
    if (idempotencyKey === null) {
      return;
    }
    setAttempt(pending);
    setLostLabels([]);
    setNotice(null);

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

    // S10: a taken seat is normal traffic. Refetch availability, drop exactly the seats
    // that are gone, and let the form say which ones those were.
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

  // The one announcement of this surface: the count and the running amount, or the
  // reason a seat refused to be picked. Polite, so it waits its turn instead of
  // interrupting whatever the person is reading.
  const announcement = notice ?? `${summary.totalLabel}. ${priced.lineLabel}.`;

  return (
    // The boards' two columns: the plan on the left, the running receipt on the right. The
    // receipt is the second column only where there is room for one; on a narrow screen it
    // follows the map, which is the order a buyer reads it in anyway.
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_20rem] lg:items-start">
      <section aria-labelledby="seat-map" className="flex flex-col gap-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h2 id="seat-map">Plano de butacas</h2>
          <p className="m-0 text-sm text-muted-foreground">{availability.label}</p>
        </div>

        <p role="status" className="sr-only">
          {announcement}
        </p>

        {availability.note === null ? null : (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            {availability.note}
          </p>
        )}

        {layout.geometryKnown ? null : (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Esta respuesta de la API no trae la proyección del plano, así que el plano lista
            las butacas de la sesión en orden con el identificador físico que informa la API.
            Las filas y los tipos de butaca aparecerán en cuanto se sirva esa proyección;
            aquí no se da nada por supuesto.
          </p>
        )}

        {view.seatCount === 0 ? (
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Esta sesión no tiene butacas que elegir.
          </p>
        ) : (
          // The plan as the room draws it: a curved band over the seats that says where the
          // screen is, then the rows inside one scroll container so a 200-seat room stays
          // navigable on a 360px screen. The container is focusable and named, so a keyboard
          // user can scroll it without a mouse.
          <div className="flex flex-col gap-5 rounded-lg border border-border bg-card p-5">
            <div className="flex flex-col items-center gap-1">
              <svg
                viewBox="0 0 600 46"
                preserveAspectRatio="none"
                aria-hidden="true"
                focusable="false"
                className="h-10 w-full text-primary"
              >
                <path
                  d="M0 30Q300 -12 600 30V44Q300 2 0 44Z"
                  fill="currentColor"
                  fillOpacity="0.18"
                />
                <path
                  d="M0 30Q300 -12 600 30"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2"
                />
              </svg>
              {/* The word, not only the shape: the band names what is on the other side. */}
              <p className="m-0 text-[0.65rem] tracking-[0.3em] uppercase text-muted-foreground">
                Pantalla
              </p>
            </div>

            <div
              role="region"
              aria-label="Plano de butacas, se desplaza en horizontal para llegar a todas"
              tabIndex={0}
              className="overflow-x-auto rounded-sm focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background focus-visible:outline-none"
            >
              <ol className="m-0 flex w-max list-none flex-col gap-1.5 p-0">
                {view.rows.map((row) => (
                  <li key={row.label} className="flex items-center gap-1.5">
                    {/* The row label is visual: the list below is named "Fila A, 5 butacas"
                        and every seat spells its row out, so a heading per row would only
                        flood the heading list of a 200-seat room. */}
                    <span
                      aria-hidden="true"
                      className="w-5 shrink-0 text-center font-mono text-xs text-muted-foreground"
                    >
                      {row.label}
                    </span>
                    <ul
                      aria-label={`Fila ${row.label}, ${row.seatCount} butacas`}
                      className="m-0 flex list-none gap-1.5 p-0"
                    >
                      {row.cells.map((cell) => {
                        const stateWord = seatStateWord(cell.status);
                        return (
                          <li key={cell.eventSeatId} className="shrink-0">
                            <button
                              type="button"
                              ref={(node) => {
                                if (node === null) {
                                  buttonRefs.current.delete(cell.eventSeatId);
                                  return;
                                }
                                buttonRefs.current.set(cell.eventSeatId, node);
                              }}
                              // The roving tab stop: one seat is reachable with Tab, the
                              // rest with the arrow keys the contract above defines.
                              tabIndex={cell.eventSeatId === tabStop ? 0 : -1}
                              aria-label={cell.description}
                              aria-pressed={
                                cell.selectable
                                  ? cell.status === "selected"
                                  : undefined
                              }
                              aria-disabled={!cell.selectable || undefined}
                              onClick={() => handleToggle(cell)}
                              onKeyDown={(event) => handleKeyDown(event, cell)}
                              onFocus={() => setFocusedSeatId(cell.eventSeatId)}
                              className={`flex min-h-11 min-w-11 cursor-pointer flex-col items-center justify-center rounded-t-md rounded-b-sm px-1 py-0.5 text-xs leading-tight focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background focus-visible:outline-none ${SEAT_STATE_CLASSES[cell.status]}`}
                            >
                              <span className="max-w-16 truncate font-medium">
                                {cell.label}
                              </span>
                              {stateWord === "" ? null : (
                                <span className="text-[0.6rem]">{stateWord}</span>
                              )}
                            </button>
                          </li>
                        );
                      })}
                    </ul>
                  </li>
                ))}
              </ol>
            </div>

            <SeatLegend />
          </div>
        )}

        <p className="m-0 text-xs text-muted-foreground">
          Las flechas recorren las butacas, Inicio y Fin saltan dentro de una fila, y Espacio
          o Intro seleccionan la butaca en la que estás. Todas las butacas funcionan también
          con ratón o con un toque.
        </p>
      </section>

      {/* The receipt sticks while the plan scrolls, so the money and the deadline stay in
          sight on a 200-seat room. `top-4` and not `top-0`: the shell's header is not fixed,
          and a flush edge would look like a mistake. */}
      <div className="flex flex-col gap-3 lg:sticky lg:top-4">
        <HoldForm
          summary={summary}
          priced={priced}
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
      </div>
    </div>
  );
}

/**
 * The legend: every state the map can draw, in words.
 *
 * The swatch repeats the cell's tone and border so the eye can match them, and it is
 * hidden from assistive technology because the word beside it is the state. The list
 * walks `SEAT_STATE_LEGEND`, so a state cannot appear on the map without being
 * explained here.
 */
function SeatLegend() {
  return (
    <div className="flex flex-col gap-2">
      <h3 className="m-0 text-sm font-semibold">Qué significa cada butaca</h3>
      <ul
        role="list"
        aria-label="Estados de las butacas"
        className="m-0 flex list-none flex-wrap gap-x-4 gap-y-2 p-0 text-sm"
      >
        {SEAT_STATE_LEGEND.map((entry) => (
          <li key={entry.status} className="flex items-start gap-2">
            <span
              aria-hidden="true"
              className={`mt-0.5 inline-block size-4 shrink-0 rounded-sm ${SEAT_STATE_CLASSES[entry.status]}`}
            />
            <span>
              <span className="font-medium">{entry.legend}</span>
              <span className="text-muted-foreground">
                {" — "}
                {entry.legendDescription}
              </span>
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * The way from a successful hold to the confirmation page (M3 · T7).
 *
 * A hold is not a purchase: the seats are only sold once they are paid for, so the page
 * that just acquired them points at the checkout. The event id travels as a query
 * parameter because the reservation read does not answer which event a reservation
 * belongs to; the confirmation page uses it only to offer the way back to this seat map.
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
    <section
      aria-labelledby="continue-to-payment"
      className="flex flex-col gap-3 rounded-lg border border-border bg-card p-5"
    >
      <div className="flex flex-wrap items-center gap-2">
        <h2 id="continue-to-payment" className="m-0">
          Butacas reservadas: queda un paso
        </h2>
        <Badge variant="success">Reservada</Badge>
      </div>
      <p className="m-0 max-w-prose">
        La reserva <code>{reservationId}</code> guarda tus butacas hasta que se cierre su
        plazo. Las entradas se emiten cuando el pago se aprueba.
      </p>
      <div className="flex flex-wrap gap-2">
        <Button asChild>
          <a href={href}>Ir al pago</a>
        </Button>
      </div>
    </section>
  );
}
