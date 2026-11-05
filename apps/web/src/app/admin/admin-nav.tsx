// The section bar of the administration (issue #92; Marquesina since visual-redesign).
//
// The header's primary navigation speaks to a visitor and offers two entries: the section
// as a whole (`/admin/venues`) and the door, because a cashier works there all evening.
// The other three screens had no way in at all, so this component gives the section its
// own navigation instead of widening the site header — the header answers "where can I
// go in this product", and this answers "where can I go in this section".
//
// It renders on every `/admin` page from the section's layout, so a screen added later
// inherits it by existing.
//
// `aria-current` is set here, and only here inside this element. The browser gate counts
// the primary navigation's current entry and expects exactly one; this nav is outside that
// element on purpose, so the two counts cannot interfere. It is a client component because
// the current section is the current address, which only the browser knows.
//
// The bar is the artboard's "Pantalla 5" strip: the section in spaced capitals, the four
// screens as tabs with the current one raised, and the station on the right. It is rendered
// here, from [ADMIN_SECTIONS], rather than per screen: one list, one row of tabs, and a new
// screen appears in it by existing.
"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { ADMIN_SECTIONS, isCurrentSection } from "@/lib/admin-sections";

/** The name a screen reader announces for this navigation. */
export const ADMIN_NAV_LABEL = "Secciones de administración";

/** The section's own name, set in caps the way the artboard sets it. */
export const ADMIN_NAV_TAG = "Administración";

/**
 * Who the tabs belong to, printed at the end of the bar.
 *
 * It names the station and not a person, deliberately: this application's session cookie
 * carries a token and never a name (`lib/session.ts`), and the header claims nothing about
 * who is signed in (identity-login T6 · S7). A name here would have to be invented, so what
 * the bar states is the one thing it can prove — where these screens work at all.
 */
export const ADMIN_NAV_STATION = "Personal · desarrollo local";

export function AdminNav() {
  const pathname = usePathname() ?? "";

  return (
    <nav aria-label={ADMIN_NAV_LABEL} className="admin-bar">
      <div className="admin-bar__inner">
        <span className="admin-bar__tag">{ADMIN_NAV_TAG}</span>

        <ul className="admin-bar__tabs">
          {ADMIN_SECTIONS.map((section) => {
            const current = isCurrentSection(pathname, section.href);
            return (
              <li key={section.href}>
                <Link
                  href={section.href}
                  aria-current={current ? "page" : undefined}
                  className="admin-bar__tab"
                >
                  {section.label}
                </Link>
              </li>
            );
          })}
        </ul>

        <span className="admin-bar__who">{ADMIN_NAV_STATION}</span>
      </div>
    </nav>
  );
}
