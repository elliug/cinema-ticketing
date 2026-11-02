// /tickets — "my tickets", in whichever of its two lists this browser gets
// (M3.5 · T7 · S3, S8; identity-login T7 · S5, S7).
//
// Until accounts existed, this screen could only be one thing: a note of the ticket ids this
// browser tab kept, because no route listed tickets and nothing on the server knew whose browser
// was asking (milestone log, L3 and L4). That note is still here, and a guest still needs it (D4):
// a ticket is bought and opened without an account, and nothing about that changed.
//
// What changed is that it is no longer the only list at this address. One fact decides which list
// a person gets — does this request carry a session cookie? — and lib/account-tickets.ts owns both
// the decision and every sentence about it, tested there:
//
//   - **with a session**, the screen is the account's list, read from the box office. The ids are
//     no longer this tab's to keep, so the screen stops saying they are (S5, D3). A `401` on that
//     read means the session died between this page and its call: the screen says so plainly and
//     offers the way back, and the dead cookie goes with the button that offers it — a cookie that
//     no longer works is worse than none;
//   - **without one**, the screen is exactly what it was: this tab's own note, with the states
//     drawn by [KeptTicketList], and one honest sentence about what an account would add and that
//     signing in is optional.
//
// The browser never learns where the API lives. The account read is a server action
// (./actions.ts) awaited by this page, the per-ticket read is the server action below, and both
// resolve the base URL from server-side environment variables
// (`apps/web/src/app/reservations/[reservationID]/actions.ts` explains the same rule for the
// checkout). The session token is read only on the server, from the cookie this application
// issued, and is never rendered, logged or put in a URL.
//
// The route stays server-rendered per request: the cookie, the API's answers and the base URL are
// all per request, and nothing here may be cached — least of all a list that belongs to somebody.
import Link from "next/link";

import { PageHeader } from "@/components";
import { KeptTicketList, TicketSummary } from "@/app/tickets/[ticketID]/ticket-view";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { describeAccountTickets, describeTicketsScreen } from "@/lib/account-tickets";
import { SIGN_IN_PATH } from "@/lib/identity";
import type { TicketResponse } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";
import { readSessionToken } from "@/lib/session";
import { clearDeadSessionAction, readAccountTicketsAction } from "./actions";

export const dynamic = "force-dynamic";

/**
 * Reads one ticket: `GET /api/v1/tickets/{id}`.
 *
 * A server action, because the read must happen in this process: the API's base URL comes
 * from server-side environment variables, and the browser is never told where it is. A
 * read changes nothing, which is why the list can call it on every load and a reload can
 * never charge, hold or release anything.
 */
async function readTicketAction(ticketId: string): Promise<TicketResponse> {
  "use server";

  if (ticketId.trim() === "") {
    return {
      ok: false,
      problem: {
        type: "https://cinema.local/problems/invalid-input",
        title: "Invalid input",
        status: 0,
        code: "invalid_input",
        detail: "this page needs a ticket id",
      },
    };
  }

  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const result = await createPublicClient(config.value).getTicket(ticketId);
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }
  return { ok: true, ticket: result.value };
}

export default async function TicketsPage() {
  // The one fact that decides which list this is. Read while the page renders, which Next
  // allows; nothing here writes it.
  const hasSession = (await readSessionToken()) !== null;
  const screen = describeTicketsScreen(hasSession);

  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow={screen.eyebrow}
        title={screen.headline}
        description={screen.description}
      />
      {hasSession ? <AccountTickets /> : <GuestTickets />}
    </main>
  );
}

/**
 * The account's list, read from the box office through the server action beside this file.
 *
 * Each state is its own answer, and none of them is drawn as another one: an empty list is an
 * account that bought nothing, a refused session is a session that ended, and a failure to ask is
 * ours, retryable, and never an empty list.
 */
async function AccountTickets() {
  const view = describeAccountTickets(await readAccountTicketsAction());

  if (view.kind === "loaded") {
    return (
      <section aria-labelledby="account-tickets" className="flex flex-col gap-3">
        <h2 id="account-tickets">{view.headline}</h2>
        <p className="m-0 max-w-prose">{view.message}</p>
        <ul role="list" className="m-0 flex list-none flex-col gap-4 p-0">
          {view.tickets.map((ticket) => (
            <li key={ticket.ticketId} className="min-w-0">
              {/* The same row the tab's own list draws for a ticket it could read: one rendering
                  of what a ticket is, so the two lists cannot disagree about it. */}
              <TicketSummary ticket={ticket} />
            </li>
          ))}
        </ul>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          This list is the box office&apos;s answer for this account, so another browser and another
          device show the same tickets. A ticket is still opened by its own link, which admits
          whoever holds it.
        </p>
      </section>
    );
  }

  if (view.kind === "empty") {
    return (
      <Card aria-labelledby="account-empty" className="flex flex-col gap-2 p-5">
        <h2 id="account-empty">{view.headline}</h2>
        <p className="m-0 max-w-prose">{view.message}</p>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">{view.nextStep}</p>
        <div className="flex flex-wrap gap-2 pt-1">
          <Button asChild size="sm">
            <Link href="/events">See what is on sale</Link>
          </Button>
        </div>
      </Card>
    );
  }

  if (view.kind === "ended") {
    return (
      <Alert variant="warning" aria-labelledby="account-ended" className="flex flex-col gap-2">
        <h2 id="account-ended" className="m-0 text-lg leading-tight font-semibold">
          {view.headline}
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">{view.message}</p>
          <p className="m-0">{view.nextStep}</p>
          {/* A form, not a link: clearing a cookie is only allowed from an action, and this button
              is the one place this screen can do it. It removes this browser's dead cookie and
              then takes the person to the screen that replaces it. */}
          <form action={clearDeadSessionAction} className="flex flex-wrap gap-2 pt-1">
            <Button type="submit" size="sm">
              Sign in again
            </Button>
          </form>
        </AlertDescription>
      </Alert>
    );
  }

  return (
    <Alert variant="destructive" role="alert" aria-labelledby="account-problem" className="flex flex-col gap-2">
      <h2 id="account-problem" className="m-0 text-lg leading-tight font-semibold">
        {view.headline}
      </h2>
      <AlertDescription className="flex flex-col gap-2">
        {/* The failure itself, so an operator reading the page can act on a code instead of a
            generic apology. Nothing about the account is inferred from it. */}
        <p className="m-0">
          <code>{view.problem.code}</code> · HTTP {view.problem.status}
        </p>
        <p className="m-0">{view.message}</p>
        <p className="m-0">{view.nextStep}</p>
        <div className="flex flex-wrap gap-2 pt-1">
          <Button asChild size="sm">
            <Link href="/tickets">Ask again</Link>
          </Button>
        </div>
      </AlertDescription>
    </Alert>
  );
}

/**
 * The screen a browser with no session gets: this tab's own note, and what an account would add.
 *
 * The list below is unchanged, with its own states — records still being read, a storage that will
 * not answer, nothing kept, and one row per ticket that could not be read. The card above it says
 * the one thing that changed about this screen: an account is an offer, not a requirement.
 */
function GuestTickets() {
  return (
    <>
      <Card aria-labelledby="guest-list" className="flex flex-col gap-2 p-5">
        <h2 id="guest-list" className="m-0 text-lg leading-tight font-semibold">
          This list belongs to this tab
        </h2>
        <p className="m-0 max-w-prose text-sm text-muted-foreground">
          An account would keep this list at the box office instead, so it would follow you to
          another browser and another device, and survive clearing this site&apos;s data. That is
          the whole of what signing in adds: nobody needs an account to buy a ticket or to open
          one, and a ticket is opened by its own link either way.
        </p>
        <div className="flex flex-wrap gap-2 pt-1">
          <Button asChild size="sm">
            <Link href={SIGN_IN_PATH}>Sign in</Link>
          </Button>
          <Button asChild variant="outline" size="sm">
            <Link href="/events">See what is on sale</Link>
          </Button>
        </div>
      </Card>

      <KeptTicketList readTicket={readTicketAction} />
    </>
  );
}
