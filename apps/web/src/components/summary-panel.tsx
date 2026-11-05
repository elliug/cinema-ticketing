// The receipt (Marquesina, visual-redesign · T5 · S1, S2).
//
// Both screens end in money, and both print it the way a till prints a receipt: a title, one
// row per thing that was bought, the total, the deadline of the hold and, at the bottom, the
// one action the screen exists for. The seat plan's running summary and the checkout's order
// summary are that same shape, so it is one component instead of two paragraphs of JSX that
// agree today and drift later.
//
// Three decisions are load-bearing:
//
//   - **the numbers are the caller's.** Nothing here prices anything: the rows arrive as
//     label/value pairs the tested rules in `lib/receipt.ts` (and the API behind them) already
//     decided. This component only prints them;
//   - **the leader is decoration.** The dotted line between a label and its amount is
//     `aria-hidden`, so a screen reader hears "Butacas, A3, A4" and not a run of dots; the
//     label is always prose, never a bare glyph;
//   - **the deadline keeps its words.** The row leads with what the countdown is about
//     ("La reserva caduca en"), and the value is set in mono with the accent. The accent never
//     carries the state by itself: an expired window arrives with its own wording and a
//     separate live region, so greyscale and a screen reader hear the same thing.
//
// No `"use client"`: no behaviour. The action is the caller's real button or link, so it
// keeps whatever focus ring and semantics that control already had.
import type { ReactNode } from "react";

/** One printed row: what the row is, and the amount it carries. */
export interface SummaryLine {
  label: string;
  value: string;
}

/** The deadline of a hold, already worded and formatted by the caller's rule. */
export interface SummaryDeadline {
  label: string;
  value: string;
}

export interface SummaryPanelProps {
  /** What this receipt is: "Tu selección", "Tu pedido". */
  title: string;
  /** The rows, in the order a till prints them. */
  lines: readonly SummaryLine[];
  /** The total, already formatted. */
  total: string;
  /** The hold deadline, when it is worth printing. */
  deadline?: SummaryDeadline;
  /** One sentence about what the numbers are — and are not. */
  note?: string;
  /** The action this receipt exists for: a button, or a link. */
  children?: ReactNode;
}

export function SummaryPanel({
  title,
  lines,
  total,
  deadline,
  note,
  children,
}: SummaryPanelProps) {
  return (
    <section
      aria-label={title}
      className="flex flex-col gap-4 rounded-lg border border-border bg-card p-5"
    >
      <h2 className="m-0 text-base font-semibold leading-tight">{title}</h2>

      {lines.length === 0 ? null : (
        <dl className="m-0 flex flex-col gap-2">
          {lines.map((line) => (
            <div
              key={`${line.label}-${line.value}`}
              className="flex items-baseline gap-2 text-sm"
            >
              <dt className="m-0 text-muted-foreground">{line.label}</dt>
              {/* The leader is decoration: the label and the amount are the sentence. */}
              <span
                aria-hidden="true"
                className="min-w-4 flex-1 self-center border-b border-dotted border-border"
              />
              <dd className="m-0 font-mono text-foreground">{line.value}</dd>
            </div>
          ))}
        </dl>
      )}

      <p className="m-0 flex items-baseline justify-between gap-3 border-t border-border pt-3">
        <span className="text-sm">Total</span>
        <strong className="font-mono text-xl font-semibold leading-none">
          {total}
        </strong>
      </p>

      {deadline === undefined ? null : (
        <p className="m-0 flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          <span aria-hidden="true">◷</span>
          <span>{deadline.label}</span>
          {/* Mono and accent, as the boards print a clock — never the only signal. */}
          <strong className="font-mono text-base text-primary">{deadline.value}</strong>
        </p>
      )}

      {children === undefined ? null : (
        <div className="flex flex-col gap-2">{children}</div>
      )}

      {note === undefined ? null : (
        <p className="m-0 max-w-prose text-xs leading-normal text-muted-foreground">
          {note}
        </p>
      )}
    </section>
  );
}
