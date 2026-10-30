// Badge and StatusPill (M3.5 · T3 · S1, S8).
//
// A short state, spelled out. The tone is decoration on top of the word, never the
// word itself: "Sold out" is the information, the red is a hint that helps someone
// who can see it, and the glyph in front is a third cue for anyone who cannot tell
// the tones apart. Remove the colour and the component still works.
//
// No `"use client"`: a pill has no behaviour.
import type { ReactNode } from "react";

/** The states a surface can report, matching globals.css' tone tokens. */
export type StatusTone = "neutral" | "info" | "success" | "warning" | "danger";

/**
 * The glyph in front of each tone.
 *
 * Decorative (`aria-hidden`): a screen reader hears the label, and a glyph read
 * aloud as "heavy multiplication x" would only add noise. It exists for the
 * reader who sees shapes but not colours.
 */
const TONE_GLYPH: Record<StatusTone, string> = {
  neutral: "•",
  info: "i",
  success: "✓",
  warning: "!",
  danger: "✕",
};

export interface BadgeProps {
  children: ReactNode;
  tone?: StatusTone;
  /** Rounded ends instead of a small rectangle; see [StatusPill]. */
  pill?: boolean;
  className?: string;
}

export function Badge({ children, tone = "neutral", pill = false, className }: BadgeProps) {
  const classes = [
    "badge",
    `badge--${tone}`,
    pill ? "badge--pill" : "",
    className ?? "",
  ]
    .filter((part) => part !== "")
    .join(" ");

  return (
    <span className={classes}>
      <span className="badge__glyph" aria-hidden="true">
        {TONE_GLYPH[tone]}
      </span>
      {children}
    </span>
  );
}

/**
 * The pill shape of the same component: a seat that is held, a ticket that is
 * void, a screening that is cancelled. Identical semantics to [Badge].
 */
export function StatusPill(props: Omit<BadgeProps, "pill">) {
  return <Badge {...props} pill />;
}
