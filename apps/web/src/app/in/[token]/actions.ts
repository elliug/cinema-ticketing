"use server";

// Spending the link a sign-in email carries (identity-login T6 · S1, S2, S3).
//
// One write, and it happens here rather than in the page's render for a reason that is a
// property of the framework and not a preference: Next's cookie jar is read-only while a server
// component renders, and can only be written from a server action or a route handler. The token
// this action holds is a credential, so it cannot be handed to the browser to carry back in a
// form field either. The page therefore renders a form whose action is this function with the
// token already bound to it (Next encrypts bound arguments in the reference the client receives),
// and the flow the brief describes stays intact: follow the link, a session is written, and the
// browser lands on the tickets.
//
// What happens on each answer is decided in lib/identity.ts and rendered by the surface beside
// this file; this function only does the three things that need the server:
//
//   - `201`: write the cookie with the API's own `expiresAt`, then redirect to `/tickets`. In that
//     order — a redirect ends the action, so a cookie written after it would never leave;
//   - the opaque `401`: hand the problem back, so one honest page can be rendered for spent,
//     expired and never-valid alike;
//   - anything else: hand the problem back too, and the page renders our own retryable failure.
//
// The token is never logged, never echoed in an error and never placed in a URL. The address it
// proves is not printed anywhere either: this side learns that *somebody* signed in, and nothing
// about who.
import { redirect } from "next/navigation";

import type { PublicResult, PublicSignInSession } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";
import { TICKETS_PATH } from "@/lib/identity";
import { setSessionCookie } from "@/lib/session";

/**
 * Spends one presented sign-in link: `POST /api/v1/identity/sessions`.
 *
 * `token` is bound on the server before this action reaches the client; it is the link's own
 * variable half, and the only thing that makes the request a credential.
 */
export async function redeemSignInLinkAction(
  token: string,
): Promise<PublicResult<PublicSignInSession>> {
  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, error: config.error };
  }

  const result = await createPublicClient(config.value).redeemSignInLink(token);
  if (!result.ok) {
    return result;
  }

  // The lifetime is the instant the API answered and the session row keeps. This is the only
  // place in the application where the session token is written down, and it is written into an
  // `HttpOnly` cookie that no script can read.
  await setSessionCookie(result.value.token, result.value.expiresAt);
  redirect(TICKETS_PATH);
}
