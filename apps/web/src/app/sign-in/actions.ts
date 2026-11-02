"use server";

// Server actions for signing in and out (identity-login T6 · S1, S2, S7; T3 for the sign-out).
//
// The browser never talks to the Go API: both actions below reach it from this process, with the
// base URL resolved from server-side environment variables (lib/public-api.ts). That is the same
// rule `/t/[token]` follows, and it is not cosmetic — the API's address means something only to
// this machine, and a browser that learned it would be asked to make requests it cannot make.
//
// [requestSignInLinkAction] is a thin adapter: the address typed on the screen goes to
// `POST /identity/links`, and the typed answer goes back unchanged. It does not decide whether
// the address is acceptable (the API's rule does, and its own sentence is what the screen
// prints), it does not log it (an address is a person), and it does not read the answer (one
// `202` covers an issued link, a spent allowance and an unreadable counter alike).
//
// [signOutAction] does the two things a sign-out is, in the order that matters: it presents the
// session in this browser's cookie to the box office so the row behind it stops working now, and
// then it clears the cookie and sends the visitor home — **whatever the box office answered**. The
// rule and its reason live in lib/identity.ts ([decideSignOut]); this file is the shell that
// performs it. What does *not* happen here is a redirect kept waiting on the API: a failed
// revocation must not leave somebody signed in in their own browser, and the row expires on its
// own anyway.
import { redirect } from "next/navigation";

import type {
  PublicResult,
  PublicSignInLinkRequested,
  PublicSignOutRevoked,
} from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";
import { decideSignOut } from "@/lib/identity";
import { clearSessionCookie, readSessionToken } from "@/lib/session";

/**
 * Asks the box office to mail a sign-in link to this address: `POST /api/v1/identity/links`.
 *
 * The answer is passed through untouched, because every reading of it belongs to
 * lib/identity.ts: one confirmation for the accepted `202`, the API's own sentence for an address
 * its rule refused, and a retryable failure for a deployment that could not answer.
 */
export async function requestSignInLinkAction(
  email: string,
): Promise<PublicResult<PublicSignInLinkRequested>> {
  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, error: config.error };
  }

  return createPublicClient(config.value).requestSignInLink(email);
}

/**
 * Signs this browser out, and asks the box office to close the session too.
 *
 * `null` from [revokeSession] means the browser held no cookie, so there was no credential to
 * present and nothing to revoke: the visitor is signed out already. Otherwise the answer is
 * whatever the box office said, and [decideSignOut] turns it into the one thing that happens
 * next — which is the same for all three answers, because the cookie and the row are two
 * different lives and only one of them is this application's to end.
 *
 * The cookie is cleared before the redirect and after the call: `redirect` ends the action, so a
 * cookie written after it would never leave.
 */
export async function signOutAction(): Promise<void> {
  const token = await readSessionToken();
  const decision = decideSignOut(token === null ? null : await revokeSession(token));

  await clearSessionCookie();
  redirect(decision.redirectTo);
}

/**
 * Presents this browser's session to the box office to be closed: `DELETE /identity/sessions`.
 *
 * The token is the cookie's own value and it goes into the request as the bearer it is. Nothing
 * of it is logged or returned: a problem that reaches a page is the API's sentence about the
 * request, never the credential that made it.
 */
async function revokeSession(token: string): Promise<PublicResult<PublicSignOutRevoked>> {
  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, error: config.error };
  }

  return createPublicClient(config.value).signOut(token);
}
