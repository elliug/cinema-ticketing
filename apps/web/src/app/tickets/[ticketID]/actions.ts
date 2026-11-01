"use server";

// The ticket page's one write: mail this ticket's link to an address typed there (T5 · S1,
// S2, S3).
//
// Same rule as the checkout's actions: the browser never talks to the Go API directly. The
// API is reached from this process, with the base URL resolved from server-side environment
// variables, so nothing in the client bundle needs to know where it lives — the page passes
// this function down as a prop, exactly as it passes `payReservationAction`.
//
// The address is a parameter and it is passed through untouched: the rule that reads it is the
// panel's, the authority on it is the API's, and nothing in this process logs it or decides it
// is valid. Nothing here stores it either — this route's whole promise is that the address is
// used for one send and kept nowhere.
import type { TicketEmailResponse } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";

/**
 * Mails one ticket's order to one address: `POST /api/v1/tickets/{id}/email`.
 *
 * It is a message, not an attempt, so it carries no Idempotency-Key and changes nothing about
 * the order or the ticket. Every answer the route can give arrives as the typed problem the
 * client already returns — a refused address, the opaque `404`, the limit with its
 * `Retry-After`, and a deployment that does not send mail — and the panel decides what each one
 * says. This function does not interpret them.
 */
export async function sendTicketEmailAction(
  ticketId: string,
  email: string,
): Promise<TicketEmailResponse> {
  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const client = createPublicClient(config.value);
  const result = await client.sendTicketEmail(ticketId, email);
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }

  return { ok: true, email: result.value };
}
