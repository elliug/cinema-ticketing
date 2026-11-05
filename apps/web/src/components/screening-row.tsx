// One row of the listings board (Marquesina).
//
// The catalogue prints a screening, not a film: the API serves sessions, one page at a
// time, in its own order, and this row refuses to group or re-sort them. What it adds is
// the shape of a foyer board — the drawing, the title, where and how much, and the hour
// on the right — without inventing a single value: every word here comes from the
// screening it was handed.
import Link from "next/link";

import { describeAvailability } from "@/lib/screening-display";
import { PosterArt } from "./poster-art";
import { TimeChip } from "./time-chip";

export interface ScreeningRowProps {
  filmTitle: string;
  cinemaName: string;
  roomName: string;
  /** The venue's own presentation of the start, as served. */
  startsAtLocal: string;
  /** The machine-readable instant, when the caller has one. */
  startsAt?: string;
  priceDisplay: string;
  availableSeats: number;
  seatCount: number;
  href: string;
  /** What the price is for: "por butaca" unless a screen says otherwise. */
  priceLabel?: string;
}

export function ScreeningRow({
  filmTitle,
  cinemaName,
  roomName,
  startsAtLocal,
  startsAt,
  priceDisplay,
  availableSeats,
  seatCount,
  href,
  priceLabel = "por butaca",
}: ScreeningRowProps) {
  const { state, left } = describeAvailability(availableSeats);

  return (
    <li className="listing__row">
      <PosterArt title={filmTitle} className="listing__poster" />

      <div className="listing__main">
        <h3 className="listing__title">
          <Link href={href}>{filmTitle}</Link>
        </h3>
        <p className="listing__meta">
          <span>
            {cinemaName} · {roomName}
          </span>
          <span aria-hidden="true">·</span>
          <span>
            {priceLabel} {priceDisplay}
          </span>
          <span aria-hidden="true">·</span>
          <span>
            {availableSeats} de {seatCount} butacas libres
          </span>
        </p>
      </div>

      <div className="listing__times">
        {/* The chip repeats the link the title already offers. That is deliberate: the
            hour is what a person scans for, and hiding it behind the title would make the
            board read like a paragraph. The accessible name says which session it opens,
            so the two links are not "the same link" twice to a screen reader. */}
        <TimeChip
          href={href}
          time={startsAtLocal}
          dateTime={startsAt}
          state={state}
          meta={left}
          label={`${startsAtLocal}, ${filmTitle}: ${left}`}
        />
      </div>
    </li>
  );
}
