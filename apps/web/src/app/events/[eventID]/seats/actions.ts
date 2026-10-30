"use server";

// The seat page's server actions (M3.5 · T5 · S1, S3).
//
// The Go API is same-origin only for the web server, so the browser never talks to it
// directly: every read and the hold request below run on the server and hand plain
// data back to a client component. The three functions are the whole surface, and
// each one returns the typed result the client returns — never a thrown error, because
// a server action cannot hand one back and a page that has to guess what went wrong
// cannot be honest about it.
//
// Nothing here decides anything. The screening read, the seat read and the hold are
// passed through unchanged from lib/public-api.ts, so the rules stay in one place: what
// a failure means is decided by lib/seat-selection.ts, and what to draw is decided by
// the components.
import type {
  AvailabilityResponse,
  HoldResponse,
  ScreeningResponse,
} from "@/lib/public-api";
import { createPublicClient, resolvePublicConfig } from "@/lib/public-api";

/**
 * Reads one screening: the film, the venue, the room, the venue's own hour, the price
 * per seat, and whether the API still offers it (T5).
 *
 * A screening that exists but is no longer on sale is a success carrying
 * `onSale: false`; only `not_found` means it does not exist. The two are kept apart so
 * the page can tell a buyer which of them happened.
 */
export async function loadScreening(eventId: string): Promise<ScreeningResponse> {
  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const client = createPublicClient(config.value);
  const result = await client.getEvent(eventId);
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }

  return { ok: true, screening: result.value };
}

/** Reads the public availability snapshot of one event. */
export async function loadAvailability(
  eventId: string,
): Promise<AvailabilityResponse> {
  if (eventId === "") {
    return {
      ok: false,
      problem: {
        type: "https://cinema.local/problems/invalid-input",
        title: "Invalid input",
        status: 0,
        code: "invalid_input",
        detail: "this page needs an event id",
      },
    };
  }

  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const client = createPublicClient(config.value);
  const result = await client.listEventSeats(eventId);
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }

  return { ok: true, seats: result.value.seats };
}

/**
 * Asks the reservation API to hold a selection.
 *
 * The key comes from the caller because it belongs to one attempt: the panel mints a
 * fresh one per attempt, and this action must never invent or reuse one.
 */
export async function requestHold(input: {
  eventId: string;
  seatIds: number[];
  idempotencyKey: string;
}): Promise<HoldResponse> {
  const config = resolvePublicConfig(process.env);
  if (!config.ok) {
    return { ok: false, problem: config.error };
  }

  const client = createPublicClient(config.value);
  const result = await client.createHold(input);
  if (!result.ok) {
    return { ok: false, problem: result.error };
  }

  return { ok: true, hold: result.value };
}
