"use server";

// Server actions for the checkout (M3 · T7, M3.5 · T6 · S1, S3).
//
// The browser never talks to the Go API directly: the API is reached from this process,
// with the base URL resolved from server-side environment variables, so nothing in the
// client bundle needs to know where the API lives. Each action is a thin adapter over
// lib/public-api.ts and returns the typed result unchanged.
//
// Two actions, two jobs. [loadReservation] reads the order, and it is what makes the page
// survive a reload: the screening, the seats, the deadline, the money taken and the
// tickets all come from it, so a returning buyer loses no context and pays nothing twice.
// [payReservationAction] is the one write this surface makes.
//
// The Idempotency-Key is a parameter, never something minted here. It belongs to one
// payment attempt and it is the browser that keeps it across reloads (see
// lib/payment-key.ts): an action that invented a key would turn a retry into a second
// attempt, which the schema refuses with `409 payment_in_flight` and only the original
// key can settle.
import type { PaymentResponse, ReservationResponse } from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";

/**
 * Reads one order: `GET /api/v1/reservations/{id}`.
 *
 * A read, so it changes nothing — which is why the page can call it on every load and a
 * reload can never charge, hold or release a seat. Only `not_found` means the order does
 * not exist; an unreadable store is the retryable `503` the client already types.
 */
export async function loadReservation(
  reservationId: string,
): Promise<ReservationResponse> {
  if (reservationId.trim() === "") {
    return {
      ok: false,
      problem: {
        type: "https://cinema.local/problems/invalid-input",
        title: "Invalid input",
        status: 0,
        code: "invalid_input",
        detail: "this page needs a reservation id",
      },
    };
  }

  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const client = createPublicClient(config.value);
  const result = await client.getReservation(reservationId);
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }

  return { ok: true, reservation: result.value };
}

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
