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
//     current page is visible and announced;
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
}

/** The primary navigation, in the order the product presents itself. */
export const SITE_NAV: readonly SiteNavItem[] = [
  { label: "Now showing", href: null },
  { label: "My tickets", href: null },
  { label: "Administration", href: "/admin/venues" },
];

/** What a navigation entry that has no screen yet says instead of linking. */
export const SITE_NAV_PENDING_LABEL = "not built yet";

/** Whether this path is the entry's page (or one of its children). */
function isCurrentPage(pathname: string, href: string): boolean {
  if (href === "/") {
    return pathname === "/";
  }
  return pathname === href || pathname.startsWith(`${href}/`);
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
                      isCurrentPage(pathname, item.href) ? "page" : undefined
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
