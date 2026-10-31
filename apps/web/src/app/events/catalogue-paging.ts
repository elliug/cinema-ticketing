// The catalogue's pages, as the screen decides them (M3.5 · T5b · S3, S8).
//
// The API answers one page of screenings and says where the next one starts. Two decisions
// follow, and both are pure so they can be read without a browser:
//
//   - where the next page lives. A cursor in the URL means the page a buyer reaches is a URL
//     they can bookmark, share and reload; a control that appends to the list in memory loses
//     all three, so the next page is an address and the screen offers it as a link;
//   - whether a problem is the catalogue refusing the cursor this screen arrived with. A
//     cursor is where one page ended, so it is only as durable as the screenings it counted.
//     A stale bookmark must not be a dead end, and it must not be answered with page one
//     either, which would look like the page it asked for.
//
// No `"use client"`: nothing here renders.
import type { PublicCataloguePage, PublicProblem } from "../../lib/public-api.ts";

/** The catalogue's own address. Its first page is the one that names no cursor. */
export const CATALOGUE_PATH = "/events";

/** The query parameter that carries a page cursor. */
export const CURSOR_PARAM = "cursor";

/**
 * The cursor this screen was reached with, or null when it is the first page.
 *
 * The value is returned exactly as the URL carried it, because only the catalogue can read
 * it. A repeated parameter is the URL's problem rather than a cursor, so the first one wins —
 * that is what a browser sent — and a blank one is no cursor at all: the API refuses an empty
 * `cursor` by name, so sending it would earn a refusal nothing was wrong with.
 */
export function cursorFromParams(
  params: Record<string, string | string[] | undefined>,
): string | null {
  const value = params[CURSOR_PARAM];
  const cursor = Array.isArray(value) ? value[0] : value;
  if (cursor === undefined || cursor.trim() === "") {
    return null;
  }
  return cursor;
}

/**
 * The address of the page after this one, or null when this was the last.
 *
 * Both halves of the API's answer are needed and neither is assumed: `hasMore` is the read's
 * own verdict, and `nextCursor` is the only thing that can reach the page it names, so a
 * cursor without the flag (or a flag without a cursor) offers nothing.
 */
export function nextPageHref(page: PublicCataloguePage): string | null {
  if (!page.hasMore || page.nextCursor === null) {
    return null;
  }
  return `${CATALOGUE_PATH}?${CURSOR_PARAM}=${encodeURIComponent(page.nextCursor)}`;
}

/**
 * Whether the API refused the cursor this screen arrived with.
 *
 * This is exactly the 400 the page request earns when the cursor it carried is one the
 * catalogue cannot mean, and it is only believed when a cursor was sent at all: a 400 about
 * another field, or a catalogue that could not be read, keeps its own state and its own
 * sentence.
 */
export function isRefusedCursor(problem: PublicProblem, cursor: string | null): boolean {
  return (
    cursor !== null &&
    problem.status === 400 &&
    problem.code === "invalid_input" &&
    /\bcursor\b/.test(problem.detail)
  );
}
