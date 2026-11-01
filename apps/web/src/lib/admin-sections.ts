// The administration's four screens, in the order an operator needs them (issue #92).
//
// The order is not alphabetical and not cosmetic: it is the order the API enforces. A
// screening cannot be scheduled without a film, a room and a *published* layout version,
// and a layout version cannot exist without a room, which needs a cinema with a time zone.
// Before this module existed, three of those four hops had no link at all, so the only way
// to follow the path was to know four URLs by heart — which is how "Create a film" came to
// look missing on a screen nobody could reach.
//
// The list lives here, rather than in the navigation component and again in the index page,
// because two copies of a path are a claim that can drift: the index would keep linking a
// screen the navigation had already renamed. Both render [ADMIN_SECTIONS].
//
// It lives in `lib` rather than beside the pages for two reasons: the repository keeps
// render-ready data and pure predicates here, and every module in this directory is free of
// the `@/` import alias so the `node --test` runner can load it without a bundler. The two
// path constants that already existed are imported relatively for the same reason.
import { ADMIN_SCHEDULE_PATH } from "./scheduling.ts";
import { ADMIN_VENUES_PATH } from "./venue-admin.ts";

// Re-exported so the four administration addresses can be read from one place: a caller that
// needs "where does the schedule screen live" should not have to know that this one constant
// historically belonged to the scheduling module and that one to the venue editor.
export { ADMIN_SCHEDULE_PATH, ADMIN_VENUES_PATH };

/** The layout editor's address. The editor predates a constant of its own. */
export const ADMIN_LAYOUTS_PATH = "/admin/layouts";

/** The door's address, written once so the section navigation cannot disagree with the header. */
export const ADMIN_DOOR_PATH = "/admin/door";

/** The index of the administration. It links to the four screens; it is not one of them. */
export const ADMIN_INDEX_PATH = "/admin";

/** One administration screen, as the section navigation and the index both describe it. */
export interface AdminSection {
  /** The address of the screen. */
  href: string;
  /** The name a person looks for, close enough to the screen's own `h1` to be honest. */
  label: string;
  /** One sentence: what this screen is for, in an operator's words rather than the schema's. */
  purpose: string;
  /**
   * Why this screen comes after the one before it. The index prints it; the navigation
   * does not, because a navigation is for moving and not for teaching.
   */
  prerequisite: string;
}

/**
 * The four screens, in working order.
 *
 * `purpose` and `prerequisite` are written for somebody setting a cinema up for the first
 * time, because that is the moment the order matters: a returning operator already knows it,
 * and a first-time operator is exactly who gets lost.
 */
export const ADMIN_SECTIONS: readonly AdminSection[] = [
  {
    href: ADMIN_VENUES_PATH,
    label: "Cinemas, rooms and layouts",
    purpose:
      "Create a cinema with its own time zone, the rooms inside it, and their layout versions.",
    prerequisite:
      "Nothing comes before this: every other screen needs a cinema and a room to point at.",
  },
  {
    href: ADMIN_LAYOUTS_PATH,
    label: "Layout editor",
    purpose:
      "Draw or amend a room's rows and seats, then publish the version the screenings will use.",
    prerequisite:
      "A room must exist first, and a published layout is immutable — a correction is a new version.",
  },
  {
    href: ADMIN_SCHEDULE_PATH,
    label: "Films and screenings",
    purpose:
      "Create a film, then bind it to a room and a published layout version with a price and a status.",
    prerequisite:
      "The room's layout version must already be published, and the film is created here.",
  },
  {
    href: ADMIN_DOOR_PATH,
    label: "Door",
    purpose: "Admit a ticket once, and read the four verdicts a cashier has to tell apart.",
    prerequisite:
      "Only after a screening has sold a ticket: the door admits what the checkout issued.",
  },
];

/**
 * Whether `pathname` is the given section's own screen.
 *
 * Exact match, deliberately. The administration's screens are four leaves, so a prefix match
 * would only introduce the trap this repository has already paid for once: `/admin` is a
 * prefix of `/administrator`, and a screen that does not belong to the section must never be
 * marked as if it did. The wider, section-level claim belongs to the header's primary
 * navigation, which declares the `/admin` prefix itself.
 *
 * A trailing slash is tolerated because a hand-typed address may carry one, and `/admin/` and
 * `/admin` are the same request to Next.
 */
export function isCurrentSection(pathname: string, href: string): boolean {
  if (pathname === "") {
    return false;
  }
  const normalized = pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
  return normalized === href;
}
