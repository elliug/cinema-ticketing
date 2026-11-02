"use server";

// /tickets' two server-side pieces (identity-login T7 · S5, D3).
//
// The browser never talks to the Go API. Both functions below run in this process, with the base
// URL resolved from server-side environment variables (lib/public-api.ts), and the session token
// is read from this application's own `HttpOnly` cookie — so the browser learns neither where the
// API lives nor what the cookie contains.
//
// [readAccountTicketsAction] is the account's list: `GET /api/v1/me/tickets`, presented with the
// session this browser holds. The page awaits it while it renders, which is what makes the list
// real HTML rather than something that arrives after a round trip through the client — and it is
// the reason this function **may not write a cookie**: Next's cookie jar is read-only while a
// server component renders, so a dead session cannot be cleared here. [clearDeadSessionAction] is
// where that happens, bound to the one button the dead-session screen offers, in the action phase
// where writing a cookie is allowed.
//
// The token is never logged, never echoed in a refusal and never put in a URL. A `401` from the
// API is handed back as the problem it is; what it means is decided in lib/account-tickets.ts,
// where it is one page for every way a session can end.
import { redirect } from "next/navigation";

import type { AccountTicketsResponse } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";
import { SIGN_IN_PATH } from "@/lib/identity";
import { clearSessionCookie, readSessionToken } from "@/lib/session";

/**
 * The tickets this account bought, read with the session cookie this browser holds.
 *
 * No session means no question to ask: the page renders the guest screen from the same cookie
 * check, so this branch is a caller's mistake — and it still answers the API's own refusal rather
 * than asking with an empty credential.
 */
export async function readAccountTicketsAction(): Promise<AccountTicketsResponse> {
  const token = await readSessionToken();
  if (token === null) {
    return {
      ok: false,
      problem: {
        type: "https://cinema.local/problems/unauthorized",
        title: "Unauthorized",
        status: 401,
        code: "unauthorized",
        detail: "this browser holds no session",
      },
    };
  }

  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const result = await createPublicClient(config.value).myTickets(token);
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }

  return { ok: true, tickets: result.value.tickets };
}

/**
 * Removes this browser's dead session cookie and takes the person to the sign-in screen.
 *
 * It is deliberately not automatic. A cookie can only be written from an action, so the page
 * cannot clear one while it renders, and an action fired behind the person's back would re-render
 * the page as the guest screen — which would erase the sentence that says what just happened. One
 * button does both things the dead-session state promises: the cookie goes, and the person is on
 * the screen that replaces it.
 *
 * Only this browser's copy is removed. The row behind it is still the box office's until it
 * expires; this application has no revocation call yet, and nothing here claims to have made one.
 */
export async function clearDeadSessionAction(): Promise<void> {
  await clearSessionCookie();
  redirect(SIGN_IN_PATH);
}
