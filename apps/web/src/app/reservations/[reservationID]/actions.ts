"use server";

// Server actions for the guest confirmation page (M3 · T7 · S1, S3).
//
// The browser never talks to the Go API directly: the API is reached from this
// process, with the base URL resolved from server-side environment variables, so
// nothing in the client bundle needs to know where the API lives. Each action is a
// thin adapter over lib/public-api.ts and returns the typed result unchanged.
//
// The Idempotency-Key is a parameter, never something minted here. It belongs to
// one payment attempt and it is the browser that keeps it across reloads (see
// lib/payment-key.ts): an action that invented a key would turn a retry into a
// second attempt, which the schema refuses with `409 payment_in_flight` and only
// the original key can settle.
import type { PaymentResponse } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";

/**
 * Pays one held reservation: `POST /api/v1/reservations/{id}/payment` with the
 * caller's key and no body, because the amount is computed server side.
 *
 * An approved and a declined charge are both successes here; `paymentStatus` in
 * the returned value says which.
 */
export async function payReservationAction(input: {
  reservationId: string;
  idempotencyKey: string;
}): Promise<PaymentResponse> {
  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const client = createPublicClient(config.value);
  const result = await client.payReservation({
    reservationId: input.reservationId,
    idempotencyKey: input.idempotencyKey,
  });
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }

  return { ok: true, payment: result.value };
}
