// EmptyState (M3.5 · T3 · S1, S8).
//
// What a list says when it has nothing to show. It exists so an empty screen never
// becomes an invented one: "no screenings are on sale yet" is honest, a placeholder
// row of fake films is not (S8). The copy belongs to the screen; the shape, and the
// heading level, belong here.
//
// No `"use client"`: nothing here has behaviour.
import type { ReactNode } from "react";

export interface EmptyStateProps {
  /** What is empty and why; a sentence, not "No data". */
  title: ReactNode;
  /** What the reader can do, or when the data will exist. */
  description?: ReactNode;
  /** An action, usually a [Button] or a link. */
  children?: ReactNode;
  /** Heading level, so the page outline stays in order. */
  titleLevel?: 2 | 3;
  className?: string;
}

export function EmptyState({
  title,
  description,
  children,
  titleLevel = 2,
  className,
}: EmptyStateProps) {
  const Heading = titleLevel === 3 ? "h3" : "h2";
  const classes = ["empty-state", className ?? ""]
    .filter((part) => part !== "")
    .join(" ");

  return (
    <div className={classes}>
      <Heading className="empty-state__title">{title}</Heading>
      {description === undefined ? null : (
        <p className="empty-state__description">{description}</p>
      )}
      {children}
    </div>
  );
}
