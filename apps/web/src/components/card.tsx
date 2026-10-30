// Card (M3.5 · T3 · S1).
//
// The unit every screen groups things with: one surface, one border, one heading.
// It owns the heading level rather than letting each page pick one, because a page
// that renders `<h3>` inside a card under an `<h1>` and no `<h2>` has a broken
// outline and nobody notices — the level is a prop, so it stays visible in review.
//
// No `"use client"`: a card has no behaviour.
//
// Accessibility: when the caller supplies an `id`, the card is labelled by its own
// heading (`aria-labelledby`), so a screen reader announces "Your order, region"
// instead of an anonymous section. Without an `id` the heading is still rendered and
// the section keeps its place in the outline.
import type { ReactNode } from "react";

/** How loud the surface is: a normal card, a quiet one, or a problem. */
export type CardTone = "default" | "muted" | "danger";

export interface CardProps {
  children: ReactNode;
  /** The card's heading. Omit only for a card that is one sentence long. */
  title?: ReactNode;
  /** Heading level; keep the page outline in order. */
  titleLevel?: 2 | 3;
  /** Element id, used to label the region by its own heading. */
  id?: string;
  /** Small uppercase label above the heading. */
  eyebrow?: string;
  /** One line under the heading, for a card whose body needs context. */
  description?: ReactNode;
  tone?: CardTone;
  className?: string;
}

export function Card({
  children,
  title,
  titleLevel = 2,
  id,
  eyebrow,
  description,
  tone = "default",
  className,
}: CardProps) {
  const classes = [
    "card",
    tone === "muted" ? "card--muted" : "",
    tone === "danger" ? "card--danger" : "",
    className ?? "",
  ]
    .filter((part) => part !== "")
    .join(" ");

  const titleId = id === undefined ? undefined : `${id}-title`;
  const Heading = titleLevel === 3 ? "h3" : "h2";

  return (
    <section
      className={classes}
      id={id}
      aria-labelledby={title === undefined ? undefined : titleId}
    >
      {eyebrow === undefined ? null : <p className="card__eyebrow">{eyebrow}</p>}
      {title === undefined ? null : (
        <Heading className="card__title" id={titleId}>
          {title}
        </Heading>
      )}
      {description === undefined ? null : (
        <p className="card__description">{description}</p>
      )}
      {children}
    </section>
  );
}
