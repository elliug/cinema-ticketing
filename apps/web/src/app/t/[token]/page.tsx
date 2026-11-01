// /t/[token] — the page the link in a purchase email opens (T4 · D3, S3).
//
// D3 put a signed, dated link in the mail instead of the ticket's permanent addresses, so
// this route is where that link lands: it redeems the token, and the tickets it opens are
// kept by this browser's list. The route is deliberately the *only* thing the email needs:
// the token names a paid order and the instant it stops verifying, and nothing else about
// the buyer.
//
// Two rules shape the file:
//
//   1. **the token never leaves this process.** The read happens here, through
//      lib/public-api.ts with a base URL taken from server-side environment variables —
//      never from a client component and never from a URL the browser could learn. That is
//      why the page is a server component and why the only client module it renders is
//      [OpenedTickets], which needs browser storage and receives the answer as a value. The
//      token is never logged, never echoed in a problem and never placed in an external URL;
//      the only links this page prints are same-origin.
//   2. **the link is a credential in a URL**, so they spend their lives in access logs,
//      histories and `Referer` headers. The route is therefore unindexed and unfollowed
//      (`robots` below), and it is server-rendered per request: a cached copy of somebody
//      else's tickets is the one thing this page must never be.
//
// The states are three, and they are decided in lib/ticket-link.ts rather than here:
//
//   1. **opened** — the signature named a paid order, and its tickets are printed, each one
//      openable by its own link, with what this tab did with them said in words;
//   2. **closed** — the API's one opaque refusal, which an aged-out email and a token this
//      deployment never signed share on purpose (S3). One page, one sentence about links
//      expiring, and the way forward: the list this browser keeps, or the confirmation the
//      payment returned;
//   3. **unavailable** — a `503`, an unreachable API, a body that is not the answer: our
//      failure, retryable, and never a claim that the link is invalid.
//
// The page says nothing about an address having been verified (it never is, S2) and nothing
// about anything having been mailed to whoever is reading: the link arrived, and that is all
// this page knows.
import type { Metadata } from "next";
import Link from "next/link";
import { Suspense } from "react";

import { PageHeader } from "@/components";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import type { PublicResult, PublicTicketLink } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";
import type { OpenedTicketLink, TicketLinkView } from "@/lib/ticket-link";
import { describeTicketLink } from "@/lib/ticket-link";
import { OpenedTickets } from "./opened-tickets";

export const dynamic = "force-dynamic";

/**
 * The link is a credential in a URL: a crawler that indexed or followed it would file a
 * stranger's tickets under this application's name. Both directives are false, so the page
 * is neither an entry nor a path through the site for a search engine.
 */
export const metadata: Metadata = {
  title: "Your tickets",
  robots: { index: false, follow: false },
};

interface TicketLinkPageProps {
  params: Promise<{ token: string }>;
}

export default async function TicketLinkPage({ params }: TicketLinkPageProps) {
  const { token } = await params;

  // The skip link and the `#main-content` target belong to the root layout.
  return (
    <main id="main-content" className="foundation">
      <Suspense fallback={<LinkLoading />}>
        <LinkSurface token={token.trim()} />
      </Suspense>
    </main>
  );
}

/** Redeems the token and decides which of the three states is the honest one. */
async function LinkSurface({ token }: { token: string }) {
  if (token === "") {
    // An address with no token opens nothing, and there is no question to put to the box
    // office: this is the route's shape, not an answer about a link.
    return <MissingToken />;
  }

  const config = resolvePublicConfig(process.env);
  // The read happens in this process, with the API's address from server-side environment
  // variables, so the browser never learns where the box office is.
  const answer: PublicResult<PublicTicketLink> = config.ok
    ? await createPublicClient(config.value).redeemTicketLink(token)
    : { ok: false, error: config.error };

  // One rule turns an answer into a state and its words, and the same rule is tested on its
  // own: nothing about which page to render is decided in this JSX.
  const view = describeTicketLink(answer);
  switch (view.kind) {
    case "opened":
      return <OpenedLink view={view} />;
    case "closed":
      return <ClosedLink view={view} />;
    default:
      return <LinkFailure view={view} retryHref={`/t/${encodeURIComponent(token)}`} />;
  }
}

/** The link opened a paid order: its tickets, and what this tab did with them. */
function OpenedLink({ view }: { view: OpenedTicketLink }) {
  return (
    <>
      <PageHeader
        eyebrow="Tickets · M3.5"
        title="Your tickets"
        description="This page redeemed the link you followed and read the order it names from the box office. Nothing here checks who you are: the link is what opened it, and whoever holds it opens the same tickets."
      />
      <OpenedTickets view={view} />
    </>
  );
}

/**
 * The link does not open, and the page cannot say why — because the system cannot either.
 *
 * Expired, forged, tampered, unknown and never-paid are one answer at the box office, so
 * they are one page here: the way forward is the list this browser keeps and the
 * confirmation the payment returned, never a guess about which of them happened.
 */
function ClosedLink({ view }: { view: Extract<TicketLinkView, { kind: "closed" }> }) {
  return (
    <>
      <PageHeader
        eyebrow="Tickets · M3.5"
        title="Your tickets"
        description="The link you followed was checked, and it did not open an order."
      />
      <Alert
        variant="warning"
        role="alert"
        aria-labelledby="link-closed"
        className="flex flex-col gap-2"
      >
        <h2 id="link-closed" className="m-0 text-lg leading-tight font-semibold">
          {view.headline}
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">{view.message}</p>
          <p className="m-0">{view.nextStep}</p>
          <div className="flex flex-wrap gap-2 pt-1">
            <Button asChild size="sm">
              <Link href="/tickets">My tickets</Link>
            </Button>
            <Button asChild variant="outline" size="sm">
              <Link href="/events">See what is on sale</Link>
            </Button>
          </div>
        </AlertDescription>
      </Alert>
    </>
  );
}

/**
 * The link could not be checked: ours, retryable, and no verdict on the link.
 *
 * The refusal is printed with the API's own code, because a page that interpreted it would
 * be answering a question the box office did not answer. Nothing about it is drawn as a
 * closed link: an outage must not read like an expired mail.
 */
function LinkFailure({
  view,
  retryHref,
}: {
  view: Extract<TicketLinkView, { kind: "unavailable" }>;
  retryHref: string;
}) {
  return (
    <>
      <PageHeader
        eyebrow="Tickets · M3.5"
        title="Your tickets"
        description="The box office was asked what this link opens, and the answer did not arrive."
      />
      <Alert
        variant="destructive"
        role="alert"
        aria-labelledby="link-failure"
        className="flex flex-col gap-2"
      >
        <h2 id="link-failure" className="m-0 text-lg leading-tight font-semibold">
          {view.headline}
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">
            <code>{view.problem.code}</code> · HTTP {view.problem.status}
          </p>
          <p className="m-0">{view.message}</p>
          <p className="m-0">{view.nextStep}</p>
          <div className="flex flex-wrap gap-2 pt-1">
            {view.retryable ? (
              <Button asChild size="sm">
                <Link href={retryHref}>Ask again</Link>
              </Button>
            ) : null}
            <Button asChild variant="outline" size="sm">
              <Link href="/tickets">My tickets</Link>
            </Button>
          </div>
        </AlertDescription>
      </Alert>
    </>
  );
}

/** The address carries no link at all: there is nothing to redeem. */
function MissingToken() {
  return (
    <>
      <PageHeader
        eyebrow="Tickets · M3.5"
        title="Your tickets"
        description="This address carries no link, so there is nothing to open."
      />
      <Alert
        variant="destructive"
        role="alert"
        aria-labelledby="link-missing"
        className="flex flex-col gap-2"
      >
        <h2 id="link-missing" className="m-0 text-lg leading-tight font-semibold">
          This address carries no link
        </h2>
        <AlertDescription className="flex flex-col gap-2">
          <p className="m-0">
            A link from a purchase email carries its own token in the address. Open the
            address exactly as it arrived, or use one of these instead.
          </p>
          <div className="flex flex-wrap gap-2 pt-1">
            <Button asChild size="sm">
              <Link href="/tickets">My tickets</Link>
            </Button>
            <Button asChild variant="outline" size="sm">
              <Link href="/events">See what is on sale</Link>
            </Button>
          </div>
        </AlertDescription>
      </Alert>
    </>
  );
}

/** The page while the link is being redeemed: a real region, not a blank screen. */
function LinkLoading() {
  return (
    <>
      <PageHeader
        eyebrow="Tickets · M3.5"
        title="Your tickets"
        description="Redeeming the link and reading the tickets it opens from the box office."
      />
      <section aria-labelledby="link-loading" className="flex flex-col gap-3">
        <h2 id="link-loading">What this link opened</h2>
        <p role="status" className="m-0 max-w-prose">
          <span className="text-muted-foreground">Opening the link…</span>
        </p>
        <Card aria-hidden="true" className="flex flex-col gap-3 p-5">
          {[0, 1, 2, 3].map((row) => (
            <Skeleton key={row} className="h-4 w-2/3" />
          ))}
        </Card>
      </section>
    </>
  );
}
