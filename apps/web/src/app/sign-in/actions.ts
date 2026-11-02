"use server";

// Server actions for signing in and out (identity-login T6 · S1, S2, S7).
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
// [signOutAction] clears this browser's cookie and nothing else. **It does not call the API yet.**
// Revocation is the box office's `SessionService.Close`, and wiring a `DELETE` for it is the next
// task; until then, signing out removes the cookie from this browser while the row behind it
// stays live at the box office until it expires. The copy on the page says exactly that, and the
// button says "sign out of this browser" — because that is the whole of what it does.
import { redirect } from "next/navigation";

import type { PublicResult, PublicSignInLinkRequested } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";
import { clearSessionCookie } from "@/lib/session";

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
 * Signs this browser out: the cookie is cleared, and the visitor lands on the front page.
 *
 * No API call, deliberately — see the file comment. A session that is still live at the box
 * office and gone from this browser is the honest state today, and the page's copy says so
 * instead of claiming a revocation this action does not perform.
 */
export async function signOutAction(): Promise<void> {
  await clearSessionCookie();
  redirect("/");
}
