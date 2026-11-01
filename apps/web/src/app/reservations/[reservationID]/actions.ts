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
//
// The address the tickets are mailed to is a parameter too, and it is passed through
// untouched: the rule that reads it is the panel's (lib/payment-key.ts), the authority on
// it is the API's, and nothing in this process logs it or decides it is valid.
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
 * Pays one held reservation: `POST /api/v1/reservations/{id}/payment` with the caller's key
 * and, when the buyer gave one, the address the tickets are to be mailed to.
 *
 * The amount is still computed server side and is never sent. The address is the only field
 * this request can carry and it cannot change the charge, only where a copy of the ticket
 * goes; an empty one is passed as it is, and the client then sends no body at all.
 *
 * An approved and a declined charge are both successes here; `paymentStatus` in
 * the returned value says which. A refused address is not a special case: the API answers it
 * with the `400 invalid_input` this route already answers for bad input, and it reaches the
 * panel through the same problem type every other refusal does.
 */
export async function payReservationAction(input: {
  reservationId: string;
  idempotencyKey: string;
  /** The address the tickets are to be mailed to, when the buyer gave one. */
  email?: string;
}): Promise<PaymentResponse> {
  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const client = createPublicClient(config.value);
  const result = await client.payReservation({
    reservationId: input.reservationId,
    idempotencyKey: input.idempotencyKey,
    email: input.email,
  });
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }

  return { ok: true, payment: result.value };
}
