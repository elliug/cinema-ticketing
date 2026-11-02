// Home page (M3.5 · T3 · S1, S2, S8).
//
// A stranger's first screen, and it tells the truth about the state of the product:
// the buyer's path in three steps, what exists today with a link to each of those
// surfaces, and a note about signing in — which is real, and optional (identity-login
// T6 · D3, D4, S7).
//
// What it deliberately does not do is pretend a catalogue exists. There is no list
// of screenings here, no film title, no price, no seat count: T4 builds the catalogue
// over an endpoint that must answer for it, and inventing a row of plausible films
// would be exactly the claim S8 forbids. Where a screen is missing, the step says so.
import Link from "next/link";
import {
  Alert,
  Card,
  PageHeader,
  StatusPill,
} from "@/components";
import { SIGN_IN_PATH } from "@/lib/identity";

export default function HomePage() {
  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Cinema ticketing · M3.5"
        title="Buy a ticket, or run the box office"
        description="One screening, one hold, one payment, one ticket per seat. The same data read from the other side is the administration surface: cinemas, rooms, layout versions and screenings. This page is the frame those screens are being built into."
      />

      <Card
        id="buyer-path"
        title="The buyer's path, in three steps"
        description="What a stranger does, in order, and how much of it this build already answers."
      >
        <ol className="list">
          <li>
            <h3>1. Find a screening</h3>
            <p className="notice">
              The catalogue screen that lists what is on sale does not exist yet; it
              arrives with the next slice of this milestone. Until then a screening is
              reached by its own id, which the administration surface lists. No price
              is shown anywhere until the API serves one.
            </p>
            <p>
              <StatusPill tone="warning">screen not built yet</StatusPill>
            </p>
          </li>
          <li>
            <h3>2. Choose the seats</h3>
            <p className="notice">
              Seats are drawn from the venue&apos;s real layout: rows with their own
              seat count, every seat labelled, and each state spelled out in words as
              well as colour. The map answers to a keyboard as well as to a mouse.
              Open it at <code>/events/&#123;eventId&#125;/seats</code>, with an id
              taken from administration.
            </p>
            <p>
              <StatusPill tone="success">works today</StatusPill>
            </p>
          </li>
          <li>
            <h3>3. Pay, and keep the ticket</h3>
            <p className="notice">
              Holding seats starts a countdown. Paying inside that window issues one
              ticket per seat, with a code and a PDF. The PDF is a record of the sale,
              not proof that the ticket still admits anyone: the ticket page asks the
              API that question each time it is opened.
            </p>
            <p>
              <StatusPill tone="success">works today</StatusPill>
            </p>
          </li>
        </ol>
      </Card>

      <Card
        id="exists-today"
        title="What you can open right now"
        description="The administration surfaces are local development only: the API must run with local administration enabled and the web application needs the same token."
      >
        <ul className="list">
          <li>
            <Link href="/admin/venues">Cinemas, rooms and layout versions</Link> —
            create a cinema, add rooms, and open the layouts of one room.
          </li>
          <li>
            <Link href="/admin/layouts">The layout editor</Link> — rows and seats of
            one layout version, saved as a draft and published when it is right.
          </li>
          <li>
            <Link href="/admin/schedule">Films and screenings</Link> — schedule a
            film in a room and read the seats materialised from the layout.
          </li>
          <li>
            <Link href="/api/health">This web application&apos;s health contract</Link>{" "}
            — the deterministic payload the repository checks use.
          </li>
        </ul>
        <p className="notice">
          The seat map, the confirmation page and the ticket page are openable by
          their own identifier today. The screens that will list them for a visitor —
          the catalogue and the ticket list — are named in the navigation as not built
          yet rather than linked to pages that do not exist.
        </p>
      </Card>

      <Alert tone="info" title="About signing in">
        <p>
          Signing in is real, and it is optional: a link mailed to your address signs
          you in for a while, and that session is what lets the box office answer
          &quot;these are your tickets&quot; instead of this browser keeping its own
          note. Nothing in the purchase path asks for an account, and nothing here
          checks who you are before it sells you a seat.
        </p>
        <p>
          The header offers a plain <Link href={SIGN_IN_PATH}>Sign in</Link> link and says
          nothing about whether this browser is signed in — it would have to read the
          session cookie to know that, and a guess rendered in the browser would
          disagree with the page the server sent. The link in your email lands on
          <code> /in/&#123;token&#125;</code>, and that page is the one that knows.
        </p>
        <p>
          The demonstration session this page used to describe is gone. Having a
          pretend session beside a real one is exactly the claim that the pretend one
          authenticates somebody, so it was retired the day real sign-in arrived.
        </p>
      </Alert>
    </main>
  );
}
