"use client";

// Site header (M3.5 · T3 · S1, S2, S8).
//
// The frame every page inherits: the product name, the primary navigation, and the
// demonstration session switcher. It is a client component for exactly two reasons —
// the current page has to be marked with `aria-current`, which needs the pathname,
// and the switcher changes state — and for no other. Nothing here reads the API.
//
// The demonstration session is labelled here, permanently, in words (S2, S8). The
// sentence is `DEMO_SESSION_LABEL` from lib/demo-session.ts, imported rather than
// copied: a second copy of that promise in this file could drift from the documented
// one, and a drifted promise is a claim the system does not keep.
//
// What it must never look like: a login. There is no "sign in", no user menu, no
// avatar, no padlock. The switcher is a dashed box that says what it does, and the
// name it shows is a demonstration name, not an identity: nothing downstream trusts
// it, and tickets stay addressable by their identifier (milestone log, L3).
//
// Accessibility:
//
//   - the header is a `header` element with a `nav` landmark named "Primary";
//   - the active link carries `aria-current="page"` and a solid underline, so the
//     current page is visible and announced — and a section entry (the administration)
//     is current on every page of its section, not only on the one it links to;
//   - a route this milestone has not built is plain text with a "not built yet"
//     badge, never a link that 404s (S8);
//   - the switcher's `select` is named by a visible label that includes the active
//     name, so the name on screen is text as well as a form value;
//   - the whole row wraps instead of overflowing: at 360px the brand, the navigation
//     and the switcher become three stacked rows.
import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import type { DemoPerson } from "@/lib/demo-session";
import {
  DEMO_PEOPLE,
  DEMO_SESSION_LABEL,
  clearDemoSession,
  defaultDemoPerson,
  demoSessionStorage,
  readDemoSession,
  switchDemoSession,
} from "@/lib/demo-session";
import { Badge } from "./badge";

/** One primary navigation entry. */
export interface SiteNavItem {
  label: string;
  /**
   * Where it goes, or `null` while the screen does not exist yet.
   *
   * A `null` entry is honest: this milestone builds the shell, and T4 (catalogue)
   * and T7 (ticket list) fill in these two hrefs. Until then the item is text with
   * a "not built yet" badge rather than a link to a 404 (S8).
   */
  href: string | null;
  /**
   * The section prefix that makes this entry current, when it is wider than its href.
   *
   * An entry links to one *screen*, but a section of the product is several screens. The
   * administration links to its venue screen and is the administration on the venue, the
   * schedule and the layouts screens alike; without this field those two pages carry no
   * current entry at all, which the browser gate found. Only the entry that owns a wide
   * section declares one: a prefix on every entry would mark several entries current at
   * once, which is worse than marking none.
   */
  prefix?: string;
}

/** The primary navigation, in the order the product presents itself. */
export const SITE_NAV: readonly SiteNavItem[] = [
  { label: "Now showing", href: "/events" },
  { label: "My tickets", href: "/tickets" },
  { label: "Administration", href: "/admin/venues", prefix: "/admin" },
];

/** What a navigation entry that has no screen yet says instead of linking. */
export const SITE_NAV_PENDING_LABEL = "not built yet";

/**
 * Whether this path is the entry's page (or one of its children).
 *
 * `base` is a path, so a page below it is one of its children and a path that merely
 * starts with the same letters (`/administrator` against `/admin`) is not.
 */
function isCurrentPath(pathname: string, base: string): boolean {
  if (base === "/") {
    return pathname === "/";
  }
  return pathname === base || pathname.startsWith(`${base}/`);
}

/**
 * Whether the navigation marks this entry as the page the visitor is on.
 *
 * The entry's own href decides, and — only for an entry that declares one — its wider
 * section prefix, from the same rule. Exported because the rule is the whole behaviour of
 * `aria-current` and the only part of the header a test could reason about.
 */
export function isCurrentEntry(pathname: string, item: SiteNavItem): boolean {
  if (item.href === null) {
    return false;
  }
  return (
    isCurrentPath(pathname, item.href) ||
    (item.prefix !== undefined && isCurrentPath(pathname, item.prefix))
  );
}

export function SiteHeader() {
  const pathname = usePathname();

  return (
    <header className="site-header">
      <div className="site-header__inner">
        <Link className="brand" href="/">
          <span className="brand__name">Cinema Ticketing</span>
          <span className="brand__meta">
            M3.5 · public purchase and local administration
          </span>
        </Link>

        <nav className="site-nav" aria-label="Primary">
          <ul className="site-nav__list">
            {SITE_NAV.map((item) => (
              <li key={item.label}>
                {item.href === null ? (
                  <span className="site-nav__pending">
                    {item.label}
                    <Badge tone="neutral">{SITE_NAV_PENDING_LABEL}</Badge>
                  </span>
                ) : (
                  <Link
                    className="site-nav__link"
                    href={item.href}
                    aria-current={
                      isCurrentEntry(pathname, item) ? "page" : undefined
                    }
                  >
                    {item.label}
                  </Link>
                )}
              </li>
            ))}
          </ul>
        </nav>

        <DemoSessionSwitcher />
      </div>
    </header>
  );
}

/**
 * The demonstration session, and the sentence that says what it is.
 *
 * The server render and the first client render both show the default person; the
 * effect then replaces it with this browser's stored choice. Reading storage during
 * render would make the two renders disagree and React would throw the server
 * markup away, so the name can change once after hydration — a page that shows the
 * default name for a moment is still honest, a hydration mismatch is a bug.
 */
export function DemoSessionSwitcher() {
  const [active, setActive] = useState<DemoPerson>(defaultDemoPerson);

  useEffect(() => {
    setActive(readDemoSession(demoSessionStorage(window)));
  }, []);

  const choose = (personId: string) => {
    setActive(switchDemoSession(demoSessionStorage(window), personId));
  };

  const reset = () => {
    setActive(clearDemoSession(demoSessionStorage(window)));
  };

  return (
    <div className="demo-switcher">
      <p className="demo-switcher__label">
        <Badge tone="neutral" pill>
          Demo
        </Badge>{" "}
        {DEMO_SESSION_LABEL}
      </p>
      <div className="demo-switcher__form">
        <label htmlFor="demo-session-person">
          Viewing as <strong>{active.displayName}</strong>
        </label>
        <select
          id="demo-session-person"
          value={active.id}
          onChange={(event) => choose(event.target.value)}
        >
          {DEMO_PEOPLE.map((person) => (
            <option key={person.id} value={person.id}>
              {person.displayName}
            </option>
          ))}
        </select>
        <button type="button" className="demo-switcher__reset" onClick={reset}>
          Reset
        </button>
      </div>
    </div>
  );
}
