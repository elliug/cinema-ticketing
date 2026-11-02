// /in/[token] — the page a sign-in email opens (identity-login T6 · S1, S2, S3).
//
// The link is built by the box office from its own `API_PUBLIC_WEB_URL`, so this address is the
// whole of what the message carries: no account name, no order, no identifier of a person. It is
// a credential in a URL, which is why the route is unindexed and unfollowed (`robots` below), why
// it is rendered per request (a cached copy of somebody else's sign-in is the one thing this page
// must never be), and why the token is spent in this process and never reprinted.
//
// Where the work happens:
//
//   - this file reads the request's cookie jar and binds the token to the server action beside it
//     (./actions.ts). Next allows a cookie to be **read** while a server component renders and
//     only allows it to be **written** from a server action, and the token is a credential that
//     cannot be handed to the browser to carry back in a form field. So the page renders the form
//     whose action spends the link, writes the cookie and redirects to `/tickets`;
//   - ./link-surface.tsx draws the outcome, and lib/identity.ts decides it, tested there: the one
//     opaque refusal is a single page for a link that was spent, one that expired and one that
//     was never real, and an outage is a different, retryable one.
//
// A browser that already holds a session is sent straight on: reloading this page after signing
// in would otherwise read as a broken link, and a link that has not been spent yet has no reason
// to be spent twice.
import type { Metadata } from "next";
import { redirect } from "next/navigation";

import { PageHeader } from "@/components";
import { TICKETS_PATH } from "@/lib/identity";
import { readSessionToken } from "@/lib/session";
import { redeemSignInLinkAction } from "./actions";
import { SignInLinkSurface } from "./link-surface";

export const dynamic = "force-dynamic";

/**
 * The token is a credential in a URL, so this page is neither an entry nor a path through the
 * site for a search engine. Both directives are false, for the same reason `/t/[token]` says so
 * about a ticket link.
 */
export const metadata: Metadata = {
  title: "Signing in",
  robots: { index: false, follow: false },
};

interface SignInLinkPageProps {
  params: Promise<{ token: string }>;
}

export default async function SignInLinkPage({ params }: SignInLinkPageProps) {
  const { token } = await params;
  const presented = token.trim();

  // Already signed in on this browser: there is nothing here to do, and the link is better left
  // unspent than spent on a session somebody already has.
  if (presented !== "" && (await readSessionToken()) !== null) {
    redirect(TICKETS_PATH);
  }

  return (
    <main id="main-content" className="foundation">
      <PageHeader
        eyebrow="Identity · T6"
        title="Signing you in"
        description="This page spends the link from your message, once, and then writes a session into this browser and takes you to your tickets. The link expires in minutes, and nobody here learns anything about the address it was sent to beyond the fact that it was spent."
      />
      <SignInLinkSurface
        redeem={redeemSignInLinkAction.bind(null, presented)}
        hasToken={presented !== ""}
      />
    </main>
  );
}
