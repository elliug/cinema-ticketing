"use server";

// The door's one action (M4.1 · T8 · S4, S5).
//
// It resolves the **public** client and posts the presented code to `POST /tickets/redeem`.
// Nothing administrative travels with it: the admission route is public, it needs no token, and
// this file must never reach for one — the door asks the same question a scanner would.
//
// The typed result is returned flattened, because a server action hands plain data back to the
// client component. The verdict itself is decided in ./door-outcome.ts, not here: this adapter
// only carries the API's answer across the boundary.
import type { RedemptionResponse } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";

/** Presents one code at the door and returns the API's answer, uninterpreted. */
export async function redeemCodeAction(token: string): Promise<RedemptionResponse> {
  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const result = await createPublicClient(config.value).redeemTicket({ token });
  return result.ok
    ? { ok: true, redemption: result.value }
    : { ok: false, problem: result.error };
}
