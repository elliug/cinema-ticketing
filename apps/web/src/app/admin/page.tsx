// /admin — the index of the local administration (issue #92; Marquesina since
// visual-redesign).
//
// The section had no page here: `/admin` answered 404, and the header linked straight to
// the venue screen. An operator who wanted to create a film landed on cinemas and rooms and
// concluded the feature did not exist, which is exactly how this issue was reported.
//
// The index exists to say two things a list of links cannot: what each screen is for, and
// why they come in this order. That order is the API's, not a preference — a screening needs
// a film, a room and a published layout version, and a layout version needs a room, which
// needs a cinema with a time zone. An alphabetical list would be easier to write and would
// teach the wrong thing.
//
// It reads nothing and calls nothing: the four screens own their data, and this page is a
// map. That is also why it can be static — there is no configuration to fail here.
//
// The map is drawn as the section's own ledger: a mono numeral per step, the screen's name,
// and the two sentences that say what it is for and what has to come before it. The numeral
// is decoration next to an `<ol>`, which is what actually states the order to a screen reader.
import Link from "next/link";

import { PageHeader } from "@/components";

import { ADMIN_SECTIONS } from "@/lib/admin-sections";

export const dynamic = "force-dynamic";

export default function AdminIndexPage() {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Administración local · M3.5"
        title="Administración"
        description={
          <>
            Cuatro pantallas, en el orden en que hay que usarlas: un cine con su propia zona
            horaria, las salas que tiene dentro y la versión del plano de la que salen sus
            butacas, después la película y por último la sesión que ata la película a una sala
            con un precio. Sólo funciona en desarrollo local.
          </>
        }
      />

      <ol className="m-0 flex list-none flex-col gap-0 border-t border-border p-0">
        {ADMIN_SECTIONS.map((section, index) => (
          <li
            key={section.href}
            className="flex flex-wrap items-baseline gap-x-4 gap-y-1 border-b border-border py-4"
          >
            <span className="ledger__num text-sm text-muted-foreground">
              {index + 1}
            </span>
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <h2 className="m-0 text-lg leading-tight font-semibold">
                <Link href={section.href} className="hover:underline">
                  {section.label}
                </Link>
              </h2>
              <p className="m-0 max-w-prose text-sm">{section.purpose}</p>
              <p className="m-0 max-w-prose text-sm text-muted-foreground">
                {section.prerequisite}
              </p>
            </div>
          </li>
        ))}
      </ol>
    </main>
  );
}
