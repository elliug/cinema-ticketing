// The receipt of the Marquesina (visual-redesign · T5 · S1, S2, S8).
//
// Two screens print the same money in the same shape: the seat map's running summary and
// the checkout's order summary. Both are a receipt — a title, rows of "label … amount", a
// total, and the deadline of the hold — so the rows and their wording are a rule here
// instead of a paragraph of JSX that exists twice and drifts once.
//
// Three invariants are load-bearing:
//
//   - **Nothing here prices anything.** The amount is brought in by the caller: the API's own
//     quote (`amountDueDisplay`), the amount it reports as taken (`amountDisplay`), or the
//     running summary of a price per seat that `priceSummary` already multiplied. This module
//     prints the number it is given, so `price × seats` is not a result it can reach on its
//     own (S8);
//   - **a served decimal is re-punctuated, never re-valued.** The API renders money as
//     `12.50`; the page's copy is Spanish, so the same number is printed `12,50 €`. A value
//     that is not a plain decimal is printed exactly as it arrived, because a repaired or
//     rounded amount would be a number the box office never said;
//   - **the deadline carries its state in words.** `describeHoldDeadline` names the end of
//     the window (`La reserva caducó`) instead of leaving a reader to infer it from a clock
//     that stopped, so colour is never the only carrier of the state.
//
// The fee line is a statement about this version of the product, not a calculation: it takes
// no management fee, so the line is printed as `0,00 €` and the total is exactly the amount
// of the seats. Keeping it here — beside the total it has to agree with — is what stops the
// two from disagreeing.

/** One printed row: what the row is, and the amount it carries. */
export interface ReceiptLine {
  label: string;
  value: string;
}

/** A receipt as a screen prints it: the rows, then the total they add up to. */
export interface Receipt {
  lines: ReceiptLine[];
  total: string;
}

/**
 * The management fee this version charges, in minor units.
 *
 * Zero, and said out loud: the sale takes the price of the seats and nothing else. The
 * constant exists so the receipt's rows and its total are derived from one number instead of
 * from two opinions about whether there is a fee.
 */
export const BOOKING_FEE_CENTS = 0;

/** The row that names the money taken for the seats. */
export const SEATS_LINE_LABEL = "Butacas";

/** The row that names the management fee. */
export const FEE_LINE_LABEL = "Gastos de gestión";

/** The row a receipt prints while nothing is selected. */
export const NO_SEATS_LINE_LABEL = "Ninguna butaca seleccionada";

/** What leads the deadline row while the hold is live. */
export const HOLD_DEADLINE_LABEL = "La reserva caduca en";

/** What leads it once the window has closed; the state, in words, in the lead. */
export const HOLD_DEADLINE_EXPIRED_LABEL = "La reserva caducó";

/** The countdown a receipt may print, as the seat and checkout rules already decided it. */
export interface HoldDeadline {
  label: string;
  value: string;
}

/**
 * An amount in minor units, as Spanish money: `25,00 €`.
 *
 * Fails closed like every other money rule in this application: a value that is not a whole,
 * non-negative number is not an amount, and printing `NaN €` beside a Pay button would be
 * worse than printing zero.
 */
export function formatAmount(cents: number): string {
  if (!Number.isInteger(cents) || cents < 0) {
    return "0,00 €";
  }
  const units = Math.floor(cents / 100);
  const rest = cents % 100;
  return `${units},${String(rest).padStart(2, "0")} €`;
}

/**
 * A decimal the API served (`12.50`, `25,00`) as this page prints it (`12,50 €`).
 *
 * Only the punctuation moves. Anything that is not a plain decimal — a sentence, a range, a
 * negative, a value that already carries a symbol — is returned exactly as served: this
 * function never invents, rounds or repairs an amount.
 */
export function presentAmount(served: string): string {
  const text = served.trim();
  if (text === "") {
    return "";
  }
  const match = /^(\d+)(?:[.,](\d{1,2}))?$/.exec(text);
  if (match === null) {
    return served;
  }
  const units = match[1];
  const cents = (match[2] ?? "").padEnd(2, "0");
  return `${units},${cents} €`;
}

/** What a receipt prints besides its own rows. */
export interface SeatReceiptInput {
  /** The seats as they are painted at the door (`A3`, `A4`); empty while nothing is picked. */
  seatLabels: readonly string[];
  /** How many seats the amount is for. */
  count: number;
  /** The amount in minor units: the caller's summary, or the API's own quote. */
  amountCents: number | null;
  /** The API's rendering of the same amount, when it served one. It wins. */
  amountDisplay: string | null;
}

/**
 * The receipt of a sale: the seats, what they cost, the fee, and the total.
 *
 * The amount is the caller's — the API's quote when there is one, otherwise the number the
 * caller brought — and the total is that same amount, because the fee is zero. A selection
 * with no seat is not a receipt with a lone total: it says, in words, that nothing is picked.
 */
export function seatReceipt({
  seatLabels,
  count,
  amountCents,
  amountDisplay,
}: SeatReceiptInput): Receipt {
  if (count <= 0) {
    return {
      lines: [{ label: NO_SEATS_LINE_LABEL, value: formatAmount(0) }],
      total: formatAmount(0),
    };
  }

  const served = (amountDisplay ?? "").trim();
  const amount =
    served === ""
      ? formatAmount(amountCents ?? 0)
      : presentAmount(served);

  const lines: ReceiptLine[] = [];
  const labels = seatLabels.map((label) => label.trim()).filter((label) => label !== "");
  if (labels.length > 0) {
    lines.push({ label: SEATS_LINE_LABEL, value: labels.join(", ") });
  }
  lines.push({ label: count === 1 ? "1 butaca" : `${count} butacas`, value: amount });
  lines.push({ label: FEE_LINE_LABEL, value: formatAmount(BOOKING_FEE_CENTS) });

  return { lines, total: amount };
}

/**
 * The deadline row of a receipt, or `null` while there is no window to print.
 *
 * The countdown itself is not re-derived here: it is the string the tested countdown rule
 * produced (`formatCountdown` over the server's deadline). What this adds is the wording of
 * the row, including the expired state, which is stated rather than implied by a stopped
 * clock.
 */
export function describeHoldDeadline(
  countdown: { displayLabel: string; expired: boolean } | null,
): HoldDeadline | null {
  if (countdown === null) {
    return null;
  }
  if (countdown.expired) {
    return { label: HOLD_DEADLINE_EXPIRED_LABEL, value: "0:00" };
  }
  return { label: HOLD_DEADLINE_LABEL, value: countdown.displayLabel };
}
