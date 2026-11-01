// The section navigation of the administration (issue #92).
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
"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { ADMIN_SECTIONS, isCurrentSection } from "@/lib/admin-sections";

/** The name a screen reader announces for this navigation. */
export const ADMIN_NAV_LABEL = "Administration sections";

export function AdminNav() {
  const pathname = usePathname() ?? "";

  return (
    <nav
      aria-label={ADMIN_NAV_LABEL}
      className="mx-auto mb-6 w-full max-w-5xl px-4 sm:px-6"
    >
      <ul className="flex flex-wrap items-center gap-x-1 gap-y-2 border-b pb-2 text-sm">
        {ADMIN_SECTIONS.map((section) => {
          const current = isCurrentSection(pathname, section.href);
          return (
            <li key={section.href}>
              <Link
                href={section.href}
                aria-current={current ? "page" : undefined}
                className={
                  current
                    ? "rounded-md bg-muted px-3 py-1.5 font-medium text-foreground"
                    : "rounded-md px-3 py-1.5 text-muted-foreground hover:text-foreground"
                }
              >
                {section.label}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
