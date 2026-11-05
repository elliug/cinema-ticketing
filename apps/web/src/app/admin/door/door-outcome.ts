// What the door says about one code, decided once and without a browser (M4.1 · T8 · S1, S3, S4).
//
// The screen has one job: a cashier pastes the code a buyer shows, presses one button, and
// reads one answer that cannot be misread. Every verdict is decided here, as a pure function
// of the API's answer, for two reasons:
//
//   - the API's answer is the only verdict (S3, S5). This module never re-verifies a code, never
//     reads validity from a ticket's printed page, and never softens a refusal into a friendlier
//     sentence. Where the API gave words — "the presented code is not a valid ticket", or why a
//     screening was cancelled — those words are the ones the cashier reads;
//   - four answers exist and each has to look different at a glance *and* read differently to a
//     screen reader, so the difference lives in a `kind`, a `headline` and the value of `admits`
//     rather than in a colour.
//
// The fifth answer is not a verdict: a door that could not reach the box office must not be
// told the code is bad, so it says it does not know and offers to ask again.
//
// The sentences are Castilian since the Marquesina redesign (the logic did not move with
// them): the headline is what a cashier reads aloud, and the seat is named the way the rest
// of the product names it — "Fila A, butaca 1".
//
// No `"use client"`: nothing here renders, and the same module is used by the screen and by
// its tests.
import type { PublicProblem, PublicRedemption, PublicTicketSeat } from "../../../lib/public-api.ts";
import type { RedemptionResponse } from "../../../lib/public-api.ts";

/** The five answers, in the order a cashier meets them. */
export type DoorOutcomeKind =
  | "admitted"
  | "already_used"
  | "not_ours"
  | "not_admissible"
  | "unavailable";

/** The tone a surface may reinforce the words with; the words stand alone without it. */
export type DoorTone = "success" | "warning" | "error" | "info";

/** Where the holder is going, as the API renders it. */
export interface DoorScreening {
  filmTitle: string;
  /** Cinema and room, already joined for reading: `Cinema Centro · Room 1`. */
  venue: string;
  /** The instant, for a `<time>` element. */
  startsAt: string;
  /** The venue's own hour, printed verbatim; never reformatted in the browser. */
  startsAtLocal: string;
}

/**
 * One answer, as the screen renders it.
 *
 * `admits` is the machine-readable half of the same fact the headline states, so nothing
 * downstream has to parse a sentence to know whether somebody walks in.
 */
export interface DoorAnswer {
  kind: DoorOutcomeKind;
  tone: DoorTone;
  headline: string;
  message: string;
  /**
   * The verdict as a stamp: one or two words, printed as the block a doorman would carry.
   *
   * It is the same fact as `headline`, shortened for the stamp-like block on the screen, and
   * it exists here so the screen does not invent a second vocabulary for the same verdict.
   */
  stamp: string;
  /** Whether this answer lets the person in. True for exactly one kind. */
  admits: boolean;
  /** Whether asking again could answer it: true only when the door could not ask at all. */
  retryable: boolean;
  /** The seats admitted, as sentences; empty for every answer that admits nobody. */
  seatLabels: string[];
  screening: DoorScreening | null;
  /** When the ticket was first used, in words, or null when it never was. */
  usedAt: string | null;
}

/** Turns the API's answer into the one sentence and the one shape a cashier reads. */
export function describeRedemption(result: RedemptionResponse): DoorAnswer {
  if (result.ok) {
    return result.redemption.alreadyRedeemed
      ? describeAlreadyUsed(result.redemption)
      : describeAdmitted(result.redemption);
  }
  return describeProblem(result.problem);
}

function describeAdmitted(redemption: PublicRedemption): DoorAnswer {
  return {
    kind: "admitted",
    tone: "success",
    headline: "Admitida: una persona, esta vez",
    message: "La entrada no se había usado. Admite a su titular ahora, y nunca más.",
    stamp: "Admitida",
    admits: true,
    retryable: false,
    seatLabels: redemption.seats.map(seatLabel),
    screening: serving(redemption),
    usedAt: null,
  };
}

function describeAlreadyUsed(redemption: PublicRedemption): DoorAnswer {
  // The venue's own hour, as served: the API answers `redeemedAtLocal` beside the instant,
  // so the door prints the hour the venue wrote down rather than this browser's zone. Before
  // that field existed the answer said UTC and a cashier in Madrid read a time two hours early.
  const usedAt = venueHour(redemption.redeemedAtLocal);
  return {
    kind: "already_used",
    tone: "warning",
    headline: "Esta entrada ya se usó: no se admite",
    message: `Se usó por primera vez a las ${usedAt}. Esto no es una admisión: una entrada admite a una persona, una sola vez.`,
    stamp: "Ya usada",
    admits: false,
    retryable: false,
    seatLabels: redemption.seats.map(seatLabel),
    screening: serving(redemption),
    usedAt,
  };
}

function describeProblem(problem: PublicProblem): DoorAnswer {
  switch (problem.code) {
    case "invalid_token":
      // The API's own sentence, unreworded: every code that is not a ticket of ours gets this
      // one answer, whether it was forged, tampered with, or names a ticket nobody sold.
      return verdict("not_ours", "error", "No es una entrada nuestra: no se admite", "No es nuestra", problem);
    case "voided":
      return verdict(
        "not_admissible",
        "error",
        "No se puede admitir: la entrada está anulada",
        "Anulada",
        problem,
      );
    case "event_cancelled":
      return verdict(
        "not_admissible",
        "error",
        "No se puede admitir: la sesión está cancelada",
        "Sesión cancelada",
        problem,
      );
    default:
      // Our failure, never a verdict on the code in the cashier's hand: the door could not
      // ask, so it says so and offers to ask again.
      return {
        kind: "unavailable",
        tone: "warning",
        headline: "El acceso no pudo comprobar este código",
        message:
          problem.detail.trim() === ""
            ? "La taquilla no respondió, así que este código no tiene veredicto todavía. Vuelve a preguntar."
            : `Todavía no se sabe nada de este código: ${problem.detail}`,
        stamp: "Sin respuesta",
        admits: false,
        retryable: true,
        seatLabels: [],
        screening: null,
        usedAt: null,
      };
  }
}

/** A refusal that admits nobody, in the API's words. */
function verdict(
  kind: DoorOutcomeKind,
  tone: DoorTone,
  headline: string,
  stamp: string,
  problem: PublicProblem,
): DoorAnswer {
  return {
    kind,
    tone,
    headline,
    message: problem.detail,
    stamp,
    admits: false,
    retryable: false,
    seatLabels: [],
    screening: null,
    usedAt: null,
  };
}

function serving(redemption: PublicRedemption): DoorScreening {
  return {
    filmTitle: redemption.filmTitle,
    venue: [redemption.cinemaName, redemption.roomName]
      .map((part) => part.trim())
      .filter((part) => part !== "")
      .join(" · "),
    startsAt: redemption.startsAt,
    startsAtLocal: redemption.startsAtLocal,
  };
}

/** `Fila A, butaca 1`, the way the rest of the product names a seat to a person. */
function seatLabel(seat: PublicTicketSeat): string {
  const row = seat.rowLabel.trim();
  const number = seat.seatNumber.trim();
  if (row === "") {
    return number === "" ? `Butaca ${seat.eventSeatId}` : `Butaca ${number}`;
  }
  return number === "" ? `Fila ${row}` : `Fila ${row}, butaca ${number}`;
}

/**
 * The venue's own hour, as the API served it: `2026-05-04 22:14:00+02:00`.
 *
 * Only the date/time separator is changed, so every digit and the offset are exactly what the
 * venue wrote down. A value this cannot read is printed as sent — a door that invented a
 * plausible hour would be worse than one showing the raw value.
 */
function venueHour(served: string): string {
  return served.replace("T", " ");
}

