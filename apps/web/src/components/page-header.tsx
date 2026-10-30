// PageHeader (M3.5 · T3 · S1).
//
// The top of every page: an eyebrow, the one `h1`, a lead sentence and a row of
// actions. It exists so the `h1` count and the title-to-lead relationship are the
// shell's business rather than each screen's, and so a page cannot ship with two
// first-level headings.
//
// No `"use client"`: no behaviour.
import type { ReactNode } from "react";

export interface PageHeaderProps {
  /** The name of the page. Rendered as the document's only `h1`. */
  title: ReactNode;
  /** Small uppercase context line above the title, for example the milestone. */
  eyebrow?: string;
  /** One or two sentences under the title; constrained to a readable measure. */
  description?: ReactNode;
  /** Actions, usually [Button]s; they wrap instead of pushing the title off screen. */
  children?: ReactNode;
  /** An id, when a page points `aria-labelledby` at this heading. */
  titleId?: string;
  className?: string;
}

export function PageHeader({
  title,
  eyebrow,
  description,
  children,
  titleId,
  className,
}: PageHeaderProps) {
  const classes = ["page-header", className ?? ""]
    .filter((part) => part !== "")
    .join(" ");

  return (
    <header className={classes}>
      {eyebrow === undefined ? null : (
        <p className="page-header__eyebrow">{eyebrow}</p>
      )}
      <h1 className="page-header__title" id={titleId}>
        {title}
      </h1>
      {description === undefined ? null : (
        <p className="page-header__description">{description}</p>
      )}
      {children === undefined ? null : (
        <div className="page-header__actions">{children}</div>
      )}
    </header>
  );
}
