// Skeleton (M3.5 · T3 · S1).
//
// A placeholder with the shape of the thing that is loading. Hidden from assistive
// technology on purpose: a skeleton that announced itself would double the loading
// message the region already gives (`aria-busy` on the region, or a status text next
// to it), and a screen reader has nothing to do with a grey rectangle.
//
// No `"use client"`: it holds no state. The pulse is CSS and is turned off under
// `prefers-reduced-motion`.
import type { CSSProperties } from "react";

export interface SkeletonProps {
  /** How many bars to draw; one by default. */
  lines?: number;
  /** Any CSS length, for example `"12rem"` or `"60%"`. */
  width?: string;
  /** Any CSS length; `"1rem"` reads as a line of text. */
  height?: string;
  className?: string;
}

export function Skeleton({
  lines = 1,
  width,
  height = "1rem",
  className,
}: SkeletonProps) {
  const style: CSSProperties = { height, width: width ?? "100%" };
  const classes = ["skeleton", className ?? ""]
    .filter((part) => part !== "")
    .join(" ");

  return (
    <div aria-hidden="true">
      {Array.from({ length: Math.max(1, lines) }, (_, index) => (
        <span
          key={index}
          className={classes}
          style={{
            ...style,
            marginBottom: index === lines - 1 ? 0 : "0.5rem",
          }}
        />
      ))}
    </div>
  );
}
