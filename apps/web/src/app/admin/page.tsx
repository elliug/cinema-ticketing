// /admin — the index of the local administration (issue #92).
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
import Link from "next/link";

import { PageHeader } from "@/components";
import { Card, CardContent, CardHeader } from "@/components/ui/card";

import { ADMIN_SECTIONS } from "@/lib/admin-sections";

export const dynamic = "force-dynamic";

export default function AdminIndexPage() {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Local administration · M3.5"
        title="Administration"
        description={
          <>
            Four screens, in the order they have to be used: a cinema with its own time zone,
            the rooms inside it and the layout version their seats come from, then the film,
            then the screening that binds the film to a room at a price. Works in local
            development only.
          </>
        }
      />

      <ol className="m-0 grid list-none gap-4 p-0 sm:grid-cols-2">
        {ADMIN_SECTIONS.map((section) => (
          <li key={section.href} className="m-0">
            <Card className="h-full">
              <CardHeader>
                <h2 className="m-0 text-lg leading-tight font-semibold">
                  <Link href={section.href} className="hover:underline">
                    {section.label}
                  </Link>
                </h2>
                <p className="m-0 max-w-prose text-sm text-muted-foreground">
                  {section.purpose}
                </p>
              </CardHeader>
              <CardContent>
                <p className="m-0 max-w-prose text-sm text-muted-foreground">
                  {section.prerequisite}
                </p>
              </CardContent>
            </Card>
          </li>
        ))}
      </ol>
    </main>
  );
}
