// /admin/door — the door screen a cashier uses to admit one person (M4.1 · T8 · S4, S5).
//
// It takes the code a buyer shows, offers one action, and prints one answer. The route
// authenticates nobody new: it sits under the local administration exactly as the venue, layout
// and schedule screens do, and it asks the **public** admission route (`POST /tickets/redeem`)
// with no administrative token, because that route is public and a second endpoint for this
// screen would be a second place for the same rule to live.
//
// Two things it deliberately does not do:
//
//   - it does not re-verify anything on the client. The server verifies the code and admits the
//     ticket it names, in one answer, and that answer is the verdict (S3);
//   - it does not trust the printed page. The PDF is a record of the sale and never changes; the
//     door asks the box office (S5).
//
// The route is server-rendered per request — an admission is never prerendered and never cached —
// and it is a local-development surface, like the rest of `/admin`.
import Link from "next/link";

import { PageHeader } from "@/components";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { redeemCodeAction } from "./actions";
import { DoorForm } from "./door-form";

export const dynamic = "force-dynamic";

export default function DoorPage() {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Local administration · M4.1"
        title="Door"
        description="One code, one answer. A ticket admits exactly one person, once: a code that has already been used, a ticket that was voided — or whose screening was cancelled — and a code that is not a ticket of ours are four different answers, and each one says so in words. Nothing is stored in this browser."
      />

      <DoorForm redeem={redeemCodeAction} />

      <Card>
        <CardHeader>
          <h2 className="m-0 text-lg leading-tight font-semibold">Where the code comes from</h2>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Every ticket a payment issued carries its own code on its page and in its PDF. The
            door reads neither of them as a verdict: the same public route a scanner would call
            answers what the code admits, and two doors presenting one code at the same moment is
            the ordinary case rather than an accident.
          </p>
        </CardHeader>
        <CardContent>
          <p className="m-0 max-w-prose text-sm text-muted-foreground">
            Buy one from <Link href="/events">the catalogue</Link>, then open it from{" "}
            <Link href="/tickets">the tickets this browser bought</Link>.
          </p>
        </CardContent>
      </Card>
    </main>
  );
}
