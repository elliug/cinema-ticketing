// The time chip (Marquesina): one screening printed the way a foyer board prints it.
//
// The hour is the only thing set in the mono face, and what is left sits under it, so a
// person comparing sessions reads a column of hours rather than a paragraph per session.
//
// It is a link when it leads somewhere and a plain span when it does not: an element that
// looks pressable and answers nothing is worse than a label. Colour reinforces the state
// (few left, sold out) and never carries it — the words "quedan 4" and "agotada" are
// always there for a screen reader, in greyscale, and in a colour-blind eye.
import Link from "next/link";

/** How a session is going; the caller decides it from the seats the API counted. */
export type TimeChipState = "on-sale" | "few" | "sold-out";

export interface TimeChipProps {
  /** The hour as the venue prints it, already presented by the API. */
  time: string;
  /** What is left, in words: "quedan 4", "agotada". */
  meta: string;
  state?: TimeChipState;
  href?: string;
  /** The machine-readable instant, when the caller has one, for `<time dateTime>`. */
  dateTime?: string;
  /** The accessible name, when "hour, meta" is not the whole story. */
  label?: string;
}

const STATE_CLASS: Record<TimeChipState, string> = {
  "on-sale": "time-chip",
  few: "time-chip time-chip--few",
  "sold-out": "time-chip time-chip--out",
};

export function TimeChip({
  time,
  meta,
  state = "on-sale",
  href,
  dateTime,
  label,
}: TimeChipProps) {
  const className = STATE_CLASS[state];
  const accessibleName = label ?? `${time}, ${meta}`;

  const content = (
    <>
      <time className="time-chip__time" dateTime={dateTime}>
        {time}
      </time>
      <span className="time-chip__meta">{meta}</span>
    </>
  );

  if (href === undefined) {
    return (
      <span className={className} aria-label={accessibleName}>
        {content}
      </span>
    );
  }

  return (
    <Link className={className} href={href} aria-label={accessibleName}>
      {content}
    </Link>
  );
}
