// How a session is going, in words (Marquesina).
//
// The board prints the seats left as a sentence and only then reinforces it with colour, so
// the sentence has to be decided somewhere a test can reach it. Keeping it here — instead of
// inside the row — is what lets the catalogue and the box office's ledger say the same thing
// about the same number.

/**
 * The point below which a session is printed as "few left" instead of a plain number.
 *
 * A display threshold, not a rule of the house: the API still says how many seats are free,
 * and this only decides which shape the chip takes. Ten is low enough to be a warning and
 * high enough to be reachable in a 208-seat room.
 */
export const FEW_SEATS = 10;

/** The three shapes the chip can take. */
export type AvailabilityState = "on-sale" | "few" | "sold-out";

export interface Availability {
  state: AvailabilityState;
  /** The words under the hour: "quedan 4", "agotada". Never a colour. */
  left: string;
}

/**
 * Turns the counted seats into the board's two words.
 *
 * Zero and below are the same answer: nothing can be sold below zero, and a negative count is
 * a reading this screen will not render as "-3 libres".
 */
export function describeAvailability(availableSeats: number): Availability {
  if (availableSeats <= 0) {
    return { state: "sold-out", left: "agotada" };
  }
  return {
    state: availableSeats <= FEW_SEATS ? "few" : "on-sale",
    left: `quedan ${availableSeats}`,
  };
}

/**
 * The hour a session starts, taken from the venue's own presentation.
 *
 * The API serves `startsAtLocal` as a complete local timestamp ("2030-01-15T19:00:00+01:00")
 * because that is the value a machine can trust. A board prints an hour, so this reads the
 * `HH:MM` out of the string instead of parsing a date: no `Date`, no zone arithmetic, and a
 * value that is not that shape is handed back untouched rather than turned into midnight.
 */
export function localHour(startsAtLocal: string): string {
  const match = /T(\d{2}:\d{2})/.exec(startsAtLocal);
  return match === null ? startsAtLocal : match[1];
}
