// The session strip (Marquesina, visual-redesign · T5 · S1, S2).
//
// Both screens that follow one screening — the seat plan and the checkout — open with the
// same question answered: which film, where, at what hour, and at what price. On the boards
// that is a strip above the work: the drawn poster, the title, the facts of the session in
// one line, and the other sessions of the same film if the caller has them.
//
// Everything it prints is served by the caller and nothing is derived here: the hour is the
// venue's own presentation (`startsAtLocal`), the price is the API's own text, and a value
// the caller does not have is left out instead of being invented. That is also why the
// price and the sessions are optional: a reservation read answers what was sold and no
// per-seat price, so the checkout prints the same strip without the price rather than a
// number the read never carried.
//
// No `"use client"`: no behaviour. The poster is decoration (`aria-hidden` in its own
// component), the state of a sale is a word next to the title, and a session is a link only
// when the caller says where it leads, so nothing invites a click it cannot answer.
import { PosterArt } from "./poster-art";
import { TimeChip, type TimeChipState } from "./time-chip";
import { Badge, type BadgeProps } from "./ui/badge";

/** One of the other sessions of the same film, as the strip prints it. */
export interface ScreeningBannerSession {
  /** The hour as the venue prints it, already presented by the caller. */
  time: string;
  /** What is left, in words: "quedan 4", "agotada". */
  meta: string;
  state?: TimeChipState;
  /** Where the session leads, when the caller knows; a plain label otherwise. */
  href?: string;
  dateTime?: string;
  /** The accessible name, when "hour, meta" is not the whole story. */
  label?: string;
}

export interface ScreeningBannerProps {
  filmTitle: string;
  cinemaName: string;
  roomName: string;
  /** The venue's own presentation of the start, as served. */
  startsAtLocal: string;
  /** The machine-readable instant, when the caller has one, for `<time dateTime>`. */
  startsAt?: string;
  /** The price of one seat, as the API renders it. Omitted when the read carries none. */
  priceDisplay?: string;
  /** What the price is for: "por butaca" unless a screen says otherwise. */
  priceLabel?: string;
  /** A word about the sale ("A la venta", "Cancelada"); the tone only reinforces it. */
  statusWord?: string;
  statusTone?: BadgeProps["variant"];
  /** The other sessions of this film, when the caller has them to offer. */
  sessions?: readonly ScreeningBannerSession[];
}

export function ScreeningBanner({
  filmTitle,
  cinemaName,
  roomName,
  startsAtLocal,
  startsAt,
  priceDisplay,
  priceLabel = "por butaca",
  statusWord,
  statusTone = "success",
  sessions,
}: ScreeningBannerProps) {
  const price = (priceDisplay ?? "").trim();
  const sessionsOffered = sessions ?? [];

  return (
    <section
      aria-label="Sesión"
      className="flex flex-wrap items-center gap-4 rounded-lg border border-border bg-card p-4"
    >
      <PosterArt title={filmTitle} className="w-12 shrink-0" />

      <div className="flex min-w-0 flex-col gap-1">
        <p className="m-0 flex flex-wrap items-center gap-2 text-base font-semibold leading-tight">
          <span className="break-words">{filmTitle}</span>
          {/* The word is the state; the tone only reinforces it. */}
          {statusWord === undefined ? null : (
            <Badge variant={statusTone}>{statusWord}</Badge>
          )}
        </p>

        {/* The separators are decoration: a screen reader hears the facts, not the dots. */}
        <p className="m-0 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted-foreground">
          <span className="break-words">{cinemaName}</span>
          <span aria-hidden="true">·</span>
          <span className="break-words">{roomName}</span>
          <span aria-hidden="true">·</span>
          <time dateTime={startsAt}>{startsAtLocal}</time>
          {price === "" ? null : (
            <>
              <span aria-hidden="true">·</span>
              <span>
                {priceLabel} {price}
              </span>
            </>
          )}
        </p>
      </div>

      {sessionsOffered.length === 0 ? null : (
        <div className="ml-auto flex flex-wrap gap-2">
          {sessionsOffered.map((session) => (
            <TimeChip
              key={`${session.time}-${session.meta}`}
              time={session.time}
              meta={session.meta}
              state={session.state}
              href={session.href}
              dateTime={session.dateTime}
              label={session.label}
            />
          ))}
        </div>
      )}
    </section>
  );
}
