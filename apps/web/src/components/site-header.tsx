"use client";

// Site header (M3.5 · T3 · S1, S8; identity-login T6 · S7).
//
// The frame every page inherits: the product name, the primary navigation, and the way in to
// signing in. It is a client component for exactly one reason — the current page has to be marked
// with `aria-current`, which needs the pathname — and for no other. Nothing here reads the API.
//
// The header used to carry a demonstration session: a switcher whose name was a demonstration
// name, labelled with a sentence promising that it granted no access. It is gone (S7), and the
// reason is worth keeping written down: this application has a real session now, and a real one
// standing beside a fake one is exactly the claim that the fake one is authentication — which
// `odd/tasks/m35-product-web.md` (S2, S8, L3) forbids.
//
// What replaced it is a link, and its label makes no claim: "Sign in". The header cannot know
// whether this browser holds a session — that would mean reading the cookie here, and a
// client-side guess would render differently from the server's HTML and hydrate into a
// disagreement. So it offers the way in and says nothing about who is already in, which is a
// sentence only a surface that can read the cookie is entitled to write.
//
// Accessibility:
//
//   - the header is a `header` element with a `nav` landmark named "Principal";
//   - the active link carries `aria-current="page"` and a solid underline, so the
//     current page is visible and announced — and a section entry (the administration)
//     is current on every page of its section, not only on the one it links to;
//   - a route this milestone has not built is plain text with a "not built yet"
//     badge, never a link that 404s (S8);
//   - the sign-in link is a link like any other: same styling, same focus treatment, and it is
//     `aria-current` on `/sign-in` because that is the page being read, never as a claim about a
//     session;
//   - the whole row wraps instead of overflowing: at 360px the brand, the navigation and the
//     sign-in link become three stacked rows.
import Link from "next/link";
import { usePathname } from "next/navigation";
import { SIGN_IN_PATH } from "@/lib/identity";
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
   * once, which is worse than marking none. A screen that has its own entry — the door —
   * still wins on its own address, because [currentEntry] prefers the entry whose href is
   * the page over the entry that only owns the section around it.
   */
  prefix?: string;
}

/** The primary navigation, in the order the product presents itself. */
export const SITE_NAV: readonly SiteNavItem[] = [
  { label: "Cartelera", href: "/events" },
  { label: "Mis entradas", href: "/tickets" },
  // The door used to sit here, beside the section, because a cashier works there all evening
  // and should not have to cross the venue editor to reach it. It is inside the administration
  // now: the primary navigation is for the buyer, and a screen only an operator uses belongs
  // where the operator already is, described with the other three screens and their order.
  { label: "Administración", href: "/admin/venues", prefix: "/admin" },
];

/** What a navigation entry that has no screen yet says instead of linking. */
export const SITE_NAV_PENDING_LABEL = "aún no construida";

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

/**
 * The one entry the navigation marks as the current page.
 *
 * A section entry (`Administration`, with its `/admin` prefix) claims every page below it, and
 * some of those pages have a screen of their own in the administration's index. Where two
 * entries could claim one page, two `aria-current` links at once is worse than none, so the
 * more specific one wins: an entry whose own href is this path beats an entry that only owns
 * the section around it.
 */
export function currentEntry(pathname: string): SiteNavItem | null {
  const claiming = SITE_NAV.filter((item) => isCurrentEntry(pathname, item));
  return (
    claiming.find((item) => item.href !== null && isCurrentPath(pathname, item.href)) ??
    claiming[0] ??
    null
  );
}

export function SiteHeader() {
  const pathname = usePathname();
  const current = currentEntry(pathname);

  return (
    <header className="site-header">
      <div className="site-header__inner">
        <Link className="brand" href="/">
          <span className="brand__name">Cinema Ticketing</span>
          <span className="brand__meta">Entradas con butaca numerada</span>
        </Link>

        <nav className="site-nav" aria-label="Principal">
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
                    aria-current={current === item ? "page" : undefined}
                  >
                    {item.label}
                  </Link>
                )}
              </li>
            ))}
          </ul>
        </nav>

        <div className="site-header__account">
          {/* The label is the whole affordance: no name, no avatar, no "signed in as", and no
              "not signed in" either — the header would have to read the cookie to know, and a
              client-side answer would disagree with the server's render (S7). */}
          <Link
            className="site-nav__link"
            href={SIGN_IN_PATH}
            aria-current={pathname === SIGN_IN_PATH ? "page" : undefined}
          >
            Iniciar sesión
          </Link>
        </div>
      </div>
      {/* The marquee strip: three pixels of lamps under the header, decoration only, so
          it is hidden from assistive technology and never carries a state. */}
      <div className="site-header__bulbs" aria-hidden="true" />
    </header>
  );
}
