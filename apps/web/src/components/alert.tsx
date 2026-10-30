// Alert (M3.5 · T3 · S1, S8).
//
// A message that arrived from something else: a confirmation, a warning about the
// hold window, a refusal from the API. Four tones, and the tone decides how loudly
// the message is announced — which is the part a plain `<div>` gets wrong.
//
// No `"use client"`: an alert holds no behaviour. It announces itself because the
// role is in the markup, so it works on a server-rendered page too.
//
// Accessibility:
//
//   - an error or a warning is `role="alert"`: assertive, interrupting, because
//     something the user just did was refused or something is about to be lost;
//   - info and success are `role="status"`: polite, announced when the reader is
//     between things, because a confirmation should not cut across a sentence;
//   - the tone is never the only signal: the glyph is decorative, but the title and
//     the body are words, and the border sits on the leading edge where a reader
//     expects it even in greyscale.
import type { ReactNode } from "react";

/** What the message is about. */
export type AlertTone = "info" | "success" | "warning" | "error";

/** How loudly each tone interrupts a screen reader. */
const TONE_ROLE: Record<AlertTone, "alert" | "status"> = {
  info: "status",
  success: "status",
  warning: "alert",
  error: "alert",
};

const TONE_GLYPH: Record<AlertTone, string> = {
  info: "i",
  success: "✓",
  warning: "!",
  error: "✕",
};

export interface AlertProps {
  tone?: AlertTone;
  /** A short heading; the message body alone is rarely enough for a scan. */
  title?: ReactNode;
  children?: ReactNode;
  className?: string;
}

export function Alert({ tone = "info", title, children, className }: AlertProps) {
  const classes = ["alert", `alert--${tone}`, className ?? ""]
    .filter((part) => part !== "")
    .join(" ");

  return (
    <div className={classes} role={TONE_ROLE[tone]}>
      {title === undefined ? null : (
        <p className="alert__title">
          <span className="alert__glyph" aria-hidden="true">
            {TONE_GLYPH[tone]}
          </span>
          {title}
        </p>
      )}
      {children}
    </div>
  );
}
