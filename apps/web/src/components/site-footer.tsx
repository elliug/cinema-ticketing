// Site footer (M3.5 · T3 · S2, S8).
//
// What this build is, said once, in the one place a reader looks when they want to
// know whether they are using something real. Three facts and no marketing:
//
//   1. this is a local development build of the M3.5 milestone, not a service;
//   2. tickets and reservations are opened by their identifier — a forwarded link
//      works for whoever holds it, by design (milestone log, L3);
//   3. the session switcher is a demonstration. The sentence is the same exported
//      constant the header renders, so the two can never say different things.
//
// No `"use client"`: the footer has no behaviour.
import { DEMO_SESSION_LABEL } from "@/lib/demo-session";

export function SiteFooter() {
  return (
    <footer className="site-footer">
      <div className="site-footer__inner">
        <p className="site-footer__heading">
          M3.5 build — public purchase and local administration.
        </p>
        <p>
          A local development build of the cinema ticketing platform: this web
          application in front of the Go API in this repository, running on this
          machine. It is not a production service, and nothing here is sold to
          anyone.
        </p>
        <p>
          Tickets and reservations are opened by their identifier, so anyone holding
          a link sees them — by design, and recorded in the milestone log. The name
          in the header is the demonstration session: {DEMO_SESSION_LABEL}
        </p>
      </div>
    </footer>
  );
}
