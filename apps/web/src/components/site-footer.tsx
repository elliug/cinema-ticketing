// Site footer (M3.5 · T3 · S2, S8).
//
// What this build is, said once, in the one place a reader looks when they want to
// know whether they are using something real. Three facts and no marketing:
//
//   1. this is a local development build of the M3.5 milestone, not a service;
//   2. tickets and reservations are opened by their identifier — a forwarded link
//      works for whoever holds it, by design (milestone log, L3);
//   3. signing in is real and optional: a session is what makes "these are your tickets"
//      something the box office answers instead of something this browser remembers, and
//      nothing in the purchase path requires one (identity-login T6 · D3, D4, S7).
//
// The demonstration session this paragraph used to describe is gone: a fake session labelled
// beside a real one is a claim that the fake one authenticates somebody, and S7 retired it.
//
// No `"use client"`: the footer has no behaviour.

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
          Tickets and reservations are opened by their identifier, so anyone holding a
          link sees them — by design, and recorded in the milestone log. There is no
          demonstration session in this build any more: signing in is real, it is
          optional, and nothing in the purchase path asks for it.
        </p>
      </div>
    </footer>
  );
}
