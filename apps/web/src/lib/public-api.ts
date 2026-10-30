// Public (guest) reservation API client.
//
// The customer facing routes live under /api/v1, are mounted outside the
// administrative guard, and require no credential: a guest selects seats without
// an account, pays for them and shows the ticket. This client therefore:
//
//   - sends no Authorization header and reads no token; the only header it adds
//     besides the usual JSON ones is the Idempotency-Key of a hold or a payment
//     attempt;
//   - targets a configurable base URL resolved from server-side environment
//     variables, defaulting to http://127.0.0.1:8080;
//   - converts failures into typed results instead of throwing, so callers must
//     handle RFC 9457 problem details explicitly;
//   - rejects a request the API would reject anyway (no seats, too many seats, no
//     idempotency key, no ticket id) before anything leaves the process.
//
// It covers the M2 hold surface, the M3 checkout and ticket reads, the M3.5
// public catalogue a stranger reads before owning any identifier, the M3.5 read of
// one screening (so the seat page keeps the film, the venue, the hour and the price
// the buyer clicked on), the M3.5 read of one order (so a reloaded confirmation keeps
// its screening, its seats, its deadline and its tickets without asking the payment
// route again), and the M3 ticket verification a page uses to say plainly whether a
// ticket admits its holder. The two ticket *downloads* are not reads this
// client makes: they are built as link and image targets by [ticketPDFUrl] and
// [ticketQRImageUrl], so the browser fetches the bytes and the page never handles
// them.
//
// The client depends only on the standard fetch API, so it is usable from a
// server component, a server action, or a test with an injected fetch.
//
// Per ARCHITECTURE.md the field names below are the M2 and M3 shapes the API
// actually serves (see apps/api/internal/publicapi). The availability snapshot
// carries the layout projection (`rowLabel`, `seatNumber`, `kind`) because the
// public read joins the layout, so the seat map can render the event's real
// rows. Those three fields stay optional: an older API serves availability only,
// and the map then degrades to its honest flat fallback instead of inventing
// geometry.

/** Error codes the reservation API and this client can report. */
export type PublicErrorCode =
  | "invalid_input"
  | "invalid_request"
  | "invalid_token"
  | "seat_unavailable"
  | "seat_already_sold"
  | "hold_window_expired"
  | "hold_expired"
  | "idempotency_key_reused"
  | "duplicate_seat"
  | "not_found"
  | "not_sellable"
  | "already_paid"
  | "reservation_not_payable"
  | "payment_in_flight"
  | "payment_unavailable"
  | "amount_mismatch"
  | "provider_reference_taken"
  | "coordination_unavailable"
  | "service_unavailable"
  | "network_error"
  | "unexpected_response";

/** RFC 9457 problem details, normalized into a typed value. */
export interface PublicProblem {
  type: string;
  title: string;
  status: number;
  code: PublicErrorCode;
  detail: string;
}

/** Every client call returns a typed result and never throws. */
export type PublicResult<T> =
  | { ok: true; value: T }
  | { ok: false; error: PublicProblem };

/** One event seat as the public availability snapshot reports it. */
export interface PublicEventSeat {
  /** Event seat id; the hold request refers to seats by this value. */
  id: number;
  /** The physical layout seat this event seat materializes. */
  layoutSeatId: string;
  /** Stored event seat state, for example `available`. */
  state: string;
  /** Whether a live hold currently owns the seat (the authoritative flag). */
  held: boolean;
  /**
   * Label of the layout row the seat belongs to, for example `A`, when the API
   * serves the layout projection. Absent from an older API.
   */
  rowLabel?: string;
  /** Seat number inside its own row, when the API serves the projection. */
  seatNumber?: string;
  /** Physical seat kind (`standard`, `accessible`, `companion`), when served. */
  kind?: string;
}

/** GET /events/{eventID}/seats. */
export interface PublicEventSeats {
  eventId: string;
  seats: PublicEventSeat[];
}

export interface CreateHoldInput {
  eventId: string;
  seatIds: readonly number[];
  /** A fresh key per attempt; a retry must never reuse the failed one. */
  idempotencyKey: string;
}

/** POST /reservations/holds. */
export interface PublicHold {
  id: string;
  eventId: string;
  seatIds: number[];
  /** Server deadline of the hold; the countdown is derived from this value. */
  expiresAt: string;
}

export interface PayReservationInput {
  reservationId: string;
  /**
   * The key of this payment attempt, kept per reservation by
   * lib/payment-key.ts. A retry after a temporary failure resends the same key;
   * only a declined attempt discards it and mints a new one.
   */
  idempotencyKey: string;
}

/**
 * POST /reservations/{reservationId}/payment.
 *
 * A declined charge is a **successful answer** whose `paymentStatus` says so:
 * the request was processed and the provider refused it. Only `paymentStatus`
 * tells the two apart — never the HTTP status.
 */
export interface PublicPayment {
  reservationId: string;
  /** The payment attempt the answer belongs to. */
  attemptId: string;
  /** `approved` or `declined`, as the checkout recorded it. */
  paymentStatus: string;
  /** What the whole order cost, in minor units. */
  amountCents: number;
  /** One ticket per seat of an approved payment; empty for a decline. */
  ticketIds: string[];
}

/** One seat of an order, as the order read paints it at the door. */
export interface PublicOrderSeat {
  /** Event seat id; the hold request addresses seats by this value. */
  eventSeatId: number;
  rowLabel: string;
  seatNumber: string;
}

/**
 * GET /reservations/{reservationId} — one order, as the confirmation page reads it.
 *
 * This is what makes the checkout survive a reload. The hold and the payment are
 * one-shot answers; a browser that refreshes has nothing left to show without this
 * read, and the page would have to either invent the order or forget it.
 *
 * Two fields carry the S8 rules:
 *
 *   - `amountCents`/`amountDisplay` are **null together** while nothing has been
 *     captured. `0` would claim a charge that never happened, and `price × seats`
 *     would claim one nobody made: the read carries no price at all, so this client
 *     cannot compute an amount even by accident;
 *   - `startsAtLocal` is the venue's own presentation of `startsAt`, computed by the
 *     API, so a reloaded page prints the cinema's clock rather than the browser's.
 *
 * The identifier is the capability (the demonstration session authenticates nobody),
 * so this read is exactly as public as the ticket route and answers no buyer identity
 * and no payment-attempt internals.
 */
export interface PublicReservation {
  reservationId: string;
  /**
   * Stored order status, carried verbatim: `held` while the seats are held,
   * `payment_pending` while a payment is under way, `paid` once the money was taken,
   * and `expired` or `cancelled` once it ended without one. A surface prints it
   * instead of interpreting it into a second vocabulary the API never used.
   */
  status: string;
  /** The durable hold deadline in UTC; the countdown is derived from this value. */
  expiresAt: string;
  /** The money actually captured, or null while nothing was captured. */
  amountCents: number | null;
  /** The same amount as the API renders it, for example `19.98`; null with the above. */
  amountDisplay: string | null;
  /**
   * What paying costs right now, as the API quotes it, or null when nothing is due —
   * paid, ended, or holding no seats. It is a quote: the payment answer reports the money
   * actually taken.
   */
  amountDueCents: number | null;
  /** The same quote as the API renders it; null with the above. */
  amountDueDisplay: string | null;
  /** The seats the order holds or sold, in the order they are painted at the door. */
  seats: PublicOrderSeat[];
  /** The tickets issued for this order; an empty array while it is unpaid. */
  ticketIds: string[];
  /** The screening, so a reloaded confirmation says what is being bought and when. */
  eventId: string;
  filmTitle: string;
  cinemaName: string;
  roomName: string;
  /** The instant the screening starts, in UTC, for a `<time>` element. */
  startsAt: string;
  /** The screening time in the venue's own zone; what the page prints. */
  startsAtLocal: string;
}

/** One seat of a ticket, as it is painted at the door. */
export interface PublicTicketSeat {
  eventSeatId: number;
  rowLabel: string;
  seatNumber: string;
}

/** The screening a ticket admits its holder to. */
export interface PublicTicketEvent {
  id: string;
  filmTitle: string;
  /** The instant the screening starts, in UTC. */
  startsAt: string;
  /**
   * The screening time **in the venue's own zone**, rendered by the API.
   *
   * This is what a surface prints. Re-formatting `startsAt` in the browser would
   * render the viewer's zone instead of the venue's, so a missing value is a
   * refused response rather than something this client derives.
   */
  startsAtLocal: string;
  roomName: string;
  cinemaName: string;
  /** The venue's IANA zone, for labelling the local time. */
  timezone: string;
  /** Stored event status; `cancelled` is what makes a ticket unusable. */
  status: string;
}

/** GET /tickets/{ticketId} — the one projection the page and the PDF render. */
export interface PublicTicket {
  ticketId: string;
  /** Stored ticket status: `issued` or `void`. */
  status: string;
  /** The API's validity rule: issued and the screening was not cancelled. */
  valid: boolean;
  issuedAt: string;
  /** The order (one reservation) the ticket was sold in. */
  reservationId: string;
  event: PublicTicketEvent;
  /** This ticket's own seat; one ticket is one seat. */
  seats: PublicTicketSeat[];
  /** What the whole reservation cost, never this seat's price. */
  orderAmountCents: number;
  /** The same amount as the API renders it, for example `19.98`. */
  orderAmountDisplay: string;
  /** The signed code the page shows and the PDF prints. */
  qrToken: string;
}

/** POST /tickets/verify — what a presented code is, checked server side. */
export interface PublicVerification {
  ticketId: string;
  eventId: string;
  seats: PublicTicketSeat[];
  ticketStatus: string;
  eventStatus: string;
  valid: boolean;
  /**
   * Why the ticket does not admit its holder. Absent for a valid ticket, and
   * only these two values exist in this milestone: `voided` and
   * `event_cancelled`. Every code that is not a ticket of ours is instead a
   * refusal (`400 invalid_token`), never a reason.
   */
  reason?: string;
}

/** One screening of the public catalogue, as `GET /events` answers it. */
export interface PublicScreening {
  /** Event id; the seat page is reached at `/events/{id}/seats`. */
  id: string;
  /**
   * The stored status of the screening, carried verbatim: `draft`, `published`,
   * `on_sale`, `started`, `completed` or `cancelled`.
   *
   * It is what a surface prints when it has to explain why a screening is not on
   * sale, so it is read as served rather than interpreted into a second vocabulary
   * the API never used.
   */
  status: string;
  /**
   * Whether the API still offers this screening for sale.
   *
   * The decision is the database's (`status = 'on_sale'` and still ahead of its
   * clock) and is the same predicate that decides membership of the catalogue, so
   * no surface compares a clock of its own to know whether a seat can be bought.
   * It is the answer, not a hint: `false` means the page must not offer seats.
   */
  onSale: boolean;
  filmTitle: string;
  cinemaName: string;
  roomName: string;
  /**
   * The instant the screening starts, in UTC. It is the machine-readable value a
   * `<time>` element carries; the surface prints `startsAtLocal` instead.
   */
  startsAt: string;
  /**
   * The screening time **in the venue's own zone**, rendered by the API.
   *
   * Same rule as the ticket: this is what the catalogue prints. Re-formatting
   * `startsAt` would render the viewer's zone, so a missing value is a refused
   * response rather than something this client derives.
   */
  startsAtLocal: string;
  /** The price of one seat, in minor units. */
  priceCents: number;
  /** The same amount as the API renders it, for example `12.50`. */
  priceDisplay: string;
  /** How many seats the screening sells. */
  seatCount: number;
  /** How many of them are neither sold nor held right now. */
  availableSeats: number;
}

export interface PublicClient {
  /**
   * The screenings a buyer can buy right now, in the API's screening order.
   *
   * The order is the API's (screening time, then whatever it breaks ties by), so
   * callers render the array as served: re-sorting it here would be a second
   * opinion about a sequence one read already decided. An empty catalogue is an
   * empty array, never an error.
   */
  listEvents(): Promise<PublicResult<PublicScreening[]>>;
  /**
   * One screening, whatever its status, so a buyer who followed a link to a
   * screening that is no longer offered is told so instead of being answered with a
   * 404 that would claim it never existed.
   *
   * The entry shape is the catalogue's own, projected once by the API, so the
   * catalogue row and the seat page cannot disagree about the film, the venue, the
   * hour or the price. Only `not_found` means the screening does not exist.
   */
  getEvent(eventId: string): Promise<PublicResult<PublicScreening>>;
  listEventSeats(eventId: string): Promise<PublicResult<PublicEventSeats>>;
  createHold(input: CreateHoldInput): Promise<PublicResult<PublicHold>>;
  /**
   * One order, whatever its status, so the confirmation page can be reloaded: the
   * screening it is for, the seats it holds, the deadline it runs against, the money
   * taken and the tickets issued.
   *
   * Only `not_found` means the order does not exist; an unreadable store is a
   * retryable `503`, never an empty order a page would render as a purchase of
   * nothing.
   */
  getReservation(reservationId: string): Promise<PublicResult<PublicReservation>>;
  payReservation(input: PayReservationInput): Promise<PublicResult<PublicPayment>>;
  getTicket(ticketId: string): Promise<PublicResult<PublicTicket>>;
  verifyTicket(token: string): Promise<PublicResult<PublicVerification>>;
}

/**
 * The serializable answer a server action returns to the seat page.
 *
 * A server action cannot hand back a thrown error or a Response, so the typed
 * result is flattened into plain data: the page and its client components then
 * decide what to render from the same values the client returns.
 */
export type AvailabilityResponse =
  | { ok: true; seats: PublicEventSeat[] }
  | { ok: false; problem: PublicProblem };

/** The serializable answer of a screening read. */
export type ScreeningResponse =
  | { ok: true; screening: PublicScreening }
  | { ok: false; problem: PublicProblem };

/** The serializable answer of a hold attempt. */
export type HoldResponse =
  | { ok: true; hold: PublicHold }
  | { ok: false; problem: PublicProblem };

/** The serializable answer of an order read. */
export type ReservationResponse =
  | { ok: true; reservation: PublicReservation }
  | { ok: false; problem: PublicProblem };

/** The serializable answer of a payment attempt. */
export type PaymentResponse =
  | { ok: true; payment: PublicPayment }
  | { ok: false; problem: PublicProblem };

/** The serializable answer of a ticket read. */
export type TicketResponse =
  | { ok: true; ticket: PublicTicket }
  | { ok: false; problem: PublicProblem };

/** The serializable answer of a ticket verification. */
export type VerificationResponse =
  | { ok: true; verification: PublicVerification }
  | { ok: false; problem: PublicProblem };

export const DEFAULT_PUBLIC_BASE_URL = "http://127.0.0.1:8080";

/** Server-side environment variable holding the API base URL. */
export const PUBLIC_BASE_URL_ENV = "API_BASE_URL";

/**
 * The largest selection the API accepts, mirrored here so an oversized
 * selection is rejected before it is sent. apps/api/internal/reservation owns
 * the authoritative limit.
 */
export const MAX_HOLD_SEATS = 20;

const PUBLIC_PATH_PREFIX = "/api/v1";

const PROBLEM_CODES: readonly PublicErrorCode[] = [
  "invalid_input",
  "invalid_request",
  "invalid_token",
  "seat_unavailable",
  "seat_already_sold",
  "hold_window_expired",
  "hold_expired",
  "idempotency_key_reused",
  "duplicate_seat",
  "not_found",
  "not_sellable",
  "already_paid",
  "reservation_not_payable",
  "payment_in_flight",
  "payment_unavailable",
  "amount_mismatch",
  "provider_reference_taken",
  "coordination_unavailable",
  "service_unavailable",
  "unexpected_response",
];

/** A fetch-compatible function; injected in tests. */
export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

/** The environment shape this module reads; kept narrow for testability. */
export type PublicEnvironment = Record<string, string | undefined>;

export interface PublicConfig {
  baseUrl: string;
}

export interface PublicClientConfig extends PublicConfig {
  fetch?: FetchLike;
}

/**
 * Resolves the public client configuration from server-side environment
 * variables. There is no credential to resolve: reaching the reservation
 * surface never requires one.
 */
export function resolvePublicConfig(env: PublicEnvironment): PublicResult<PublicConfig> {
  const configured = (env[PUBLIC_BASE_URL_ENV] ?? "").trim();
  return {
    ok: true,
    value: {
      baseUrl: normalizeBaseUrl(
        configured === "" ? DEFAULT_PUBLIC_BASE_URL : configured,
      ),
    },
  };
}

/**
 * Mints an Idempotency-Key for one hold attempt.
 *
 * The API replays a stored hold for a repeated key, so every attempt the user
 * makes gets its own key; reusing the key of a failed attempt would replay the
 * failure instead of starting a new hold.
 */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

/** Builds a client; callers normally obtain it from resolvePublicConfig. */
export function createPublicClient(config: PublicClientConfig): PublicClient {
  const baseUrl = normalizeBaseUrl(config.baseUrl);
  const doFetch: FetchLike = config.fetch ?? ((input, init) => fetch(input, init));

  async function send<T>(
    path: string,
    init: RequestInit,
  ): Promise<PublicResult<T>> {
    let response: Response;
    try {
      response = await doFetch(`${baseUrl}${PUBLIC_PATH_PREFIX}${path}`, init);
    } catch {
      // The underlying error is discarded: it can echo request material, and the
      // UI only needs to know that the service is temporarily unreachable.
      return {
        ok: false,
        error: {
          type: "https://cinema.local/problems/network",
          title: "Network error",
          status: 0,
          code: "network_error",
          detail: "the reservation service could not be reached",
        },
      };
    }

    const parsed = await readJson(response);

    if (!response.ok) {
      return { ok: false, error: toProblem(response.status, parsed) };
    }
    if (!parsed.ok) {
      return {
        ok: false,
        error: unexpectedResponse(
          response.status,
          "the reservation API returned a body that is not valid JSON",
        ),
      };
    }
    return { ok: true, value: parsed.value as T };
  }

  return {
    async listEvents() {
      const result = await send<unknown>("/events", {
        method: "GET",
        headers: { Accept: "application/json" },
        cache: "no-store",
      });
      if (!result.ok) {
        return result;
      }
      return parseCatalog(result.value);
    },

    async getEvent(eventId) {
      const id = eventId.trim();
      if (id === "") {
        return { ok: false, error: invalidInput("this page needs an event id") };
      }

      const result = await send<unknown>(`/events/${encodeURIComponent(id)}`, {
        method: "GET",
        headers: { Accept: "application/json" },
        cache: "no-store",
      });
      if (!result.ok) {
        return result;
      }
      // The catalogue and this read answer one entry shape, so they share one
      // parser: neither can interpret the sale state differently from the other.
      return parseScreening(result.value);
    },

    async listEventSeats(eventId) {
      const result = await send<unknown>(
        `/events/${encodeURIComponent(eventId)}/seats`,
        {
          method: "GET",
          headers: { Accept: "application/json" },
          cache: "no-store",
        },
      );
      if (!result.ok) {
        return result;
      }
      return parseAvailability(result.value);
    },

    async createHold(input) {
      const invalid = validateHoldInput(input);
      if (invalid !== null) {
        return { ok: false, error: invalid };
      }

      const result = await send<unknown>("/reservations/holds", {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
          "Idempotency-Key": input.idempotencyKey,
        },
        cache: "no-store",
        body: JSON.stringify({
          eventId: input.eventId,
          seatIds: [...input.seatIds],
        }),
      });
      if (!result.ok) {
        return result;
      }
      return parseHold(result.value);
    },

    async getReservation(reservationId) {
      const id = reservationId.trim();
      if (id === "") {
        return { ok: false, error: invalidInput("this page needs a reservation id") };
      }

      // A read, so no Idempotency-Key and no body: nothing about this request changes
      // the order, which is why reloading can never charge twice.
      const result = await send<unknown>(
        `/reservations/${encodeURIComponent(id)}`,
        {
          method: "GET",
          headers: { Accept: "application/json" },
          cache: "no-store",
        },
      );
      if (!result.ok) {
        return result;
      }
      return parseReservation(result.value);
    },

    async payReservation(input) {
      const invalid = validatePayInput(input);
      if (invalid !== null) {
        return { ok: false, error: invalid };
      }

      // No body, by design: the amount is `events.price_cents × seats held`,
      // computed server side, so there is nothing for a client to send and
      // nothing for the API to trust if it did.
      const result = await send<unknown>(
        `/reservations/${encodeURIComponent(input.reservationId)}/payment`,
        {
          method: "POST",
          headers: {
            Accept: "application/json",
            "Idempotency-Key": input.idempotencyKey,
          },
          cache: "no-store",
        },
      );
      if (!result.ok) {
        return result;
      }
      return parsePayment(result.value);
    },

    async getTicket(ticketId) {
      const id = ticketId.trim();
      if (id === "") {
        return { ok: false, error: invalidInput("this page needs a ticket id") };
      }

      const result = await send<unknown>(`/tickets/${encodeURIComponent(id)}`, {
        method: "GET",
        headers: { Accept: "application/json" },
        cache: "no-store",
      });
      if (!result.ok) {
        return result;
      }
      return parseTicket(result.value);
    },

    async verifyTicket(token) {
      const presented = token.trim();
      if (presented === "") {
        // A blank code cannot verify, and sending it would only ask the API for a
        // refusal this client already knows the answer to.
        return {
          ok: false,
          error: invalidInput("a QR code is needed to check a ticket"),
        };
      }

      const result = await send<unknown>("/tickets/verify", {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
        },
        cache: "no-store",
        body: JSON.stringify({ token: presented }),
      });
      if (!result.ok) {
        return result;
      }
      return parseVerification(result.value);
    },
  };
}

/**
 * The URL of a ticket's PDF: `GET /api/v1/tickets/{ticketId}/pdf`, which answers
 * with a short-lived pre-signed redirect.
 *
 * It is built as a plain link target rather than fetched: the browser follows the
 * redirect and downloads the bytes, so this page never handles them.
 *
 * The PDF is a record of the sale, not evidence that the ticket is valid: it was
 * rendered when the payment was captured, so a later void or a cancelled
 * screening cannot appear in it. [PublicClient.verifyTicket] is what answers
 * whether the ticket still admits its holder.
 */
export function ticketPDFUrl(baseUrl: string, ticketId: string): string {
  const id = encodeURIComponent(ticketId.trim());
  return `${normalizeBaseUrl(baseUrl)}${PUBLIC_PATH_PREFIX}/tickets/${id}/pdf`;
}

/**
 * The URL of a ticket's QR image: `GET /api/v1/tickets/{ticketId}/qr.svg`, the
 * code rendered from the same signed token the PDF prints.
 *
 * It is an image source, not a client-side encoding step, so the page needs no QR
 * library and both media show one code minted by the API.
 */
export function ticketQRImageUrl(baseUrl: string, ticketId: string): string {
  const id = encodeURIComponent(ticketId.trim());
  return `${normalizeBaseUrl(baseUrl)}${PUBLIC_PATH_PREFIX}/tickets/${id}/qr.svg`;
}

/** Rejects a payment that must never reach the network. */
function validatePayInput(input: PayReservationInput): PublicProblem | null {
  // The key is what makes a retry a replay rather than a second charge, so a
  // request without one is never sent.
  if (input.idempotencyKey.trim() === "") {
    return invalidInput("an Idempotency-Key is required for every payment attempt");
  }
  if (input.reservationId.trim() === "") {
    return invalidInput("the payment request needs a reservation id");
  }
  return null;
}

/** Rejects a selection that must never reach the network. */
function validateHoldInput(input: CreateHoldInput): PublicProblem | null {
  const key = input.idempotencyKey.trim();
  if (key === "") {
    return invalidInput("an Idempotency-Key is required for every hold attempt");
  }
  if (input.eventId.trim() === "") {
    return invalidInput("the hold request needs an event id");
  }
  if (input.seatIds.length === 0) {
    return invalidInput("select at least one seat before holding");
  }
  if (input.seatIds.length > MAX_HOLD_SEATS) {
    return invalidInput(
      `a single hold can reserve at most ${MAX_HOLD_SEATS} seats`,
    );
  }
  return null;
}

function invalidInput(detail: string): PublicProblem {
  return {
    type: "https://cinema.local/problems/invalid-input",
    title: "Invalid input",
    status: 0,
    code: "invalid_input",
    detail,
  };
}

function parsePayment(value: unknown): PublicResult<PublicPayment> {
  if (!isRecord(value) || typeof value.reservationId !== "string") {
    return {
      ok: false,
      error: unexpectedResponse(200, "the payment answer names no reservation"),
    };
  }
  const paymentStatus = servedString(value.paymentStatus);
  if (paymentStatus === undefined) {
    // Without a verdict the page could only guess, and guessing "declined" or
    // "approved" from the HTTP status is exactly what the API refuses to do.
    return {
      ok: false,
      error: unexpectedResponse(200, "the payment answer carries no payment status"),
    };
  }
  return {
    ok: true,
    value: {
      reservationId: value.reservationId,
      attemptId: stringOr(value.attemptId, ""),
      paymentStatus,
      amountCents: integerOr(value.amountCents),
      // A decline answers with no tickets, so an absent list is empty rather than
      // an error; a client that iterates it never needs a nil check.
      ticketIds: Array.isArray(value.ticketIds)
        ? value.ticketIds.filter((id): id is string => typeof id === "string")
        : [],
    },
  };
}

function parseReservation(value: unknown): PublicResult<PublicReservation> {
  if (!isRecord(value)) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the order read is not an order"),
    };
  }
  const reservationId = servedString(value.reservationId);
  if (reservationId === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the order read names no reservation"),
    };
  }

  // The status is what the confirmation prints and what decides whether a payment may
  // be offered, so it is served or the read is refused — never defaulted to something
  // that would make an ended order look payable.
  const status = servedString(value.status);
  if (status === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the order ${reservationId} carries no status, so its state cannot be shown`,
      ),
    };
  }
  // Without the server deadline there is no countdown, and a client-side guess would
  // promise a window the server never granted.
  const expiresAt = servedString(value.expiresAt);
  if (expiresAt === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the order ${reservationId} carries no expiresAt deadline, so its hold window cannot be counted`,
      ),
    };
  }

  // The amount is nullable, and its two forms are null together. A number without the
  // text the API renders is half an answer: the page would have to format money
  // itself, which is the rule the catalogue and the ticket already avoid.
  let amountCents: number | null = null;
  let amountDisplay: string | null = null;
  if (value.amountCents !== null && value.amountCents !== undefined) {
    const served = wholeOrUndefined(value.amountCents);
    const text = servedString(value.amountDisplay);
    if (served === undefined || text === undefined) {
      return {
        ok: false,
        error: unexpectedResponse(
          200,
          `the order ${reservationId} carries an amount without the display text for it`,
        ),
      };
    }
    amountCents = served;
    amountDisplay = text;
  }

  // The quote follows the same rule: two forms that are null together, and half an
  // answer is a broken read rather than a reason to format money in the browser.
  let amountDueCents: number | null = null;
  let amountDueDisplay: string | null = null;
  if (value.amountDueCents !== null && value.amountDueCents !== undefined) {
    const due = wholeOrUndefined(value.amountDueCents);
    const dueText = servedString(value.amountDueDisplay);
    if (due === undefined || dueText === undefined) {
      return {
        ok: false,
        error: unexpectedResponse(
          200,
          `the order ${reservationId} carries an amount due without the display text for it`,
        ),
      };
    }
    amountDueCents = due;
    amountDueDisplay = dueText;
  }

  // The seats and the tickets are always arrays on the wire, so a client iterates them
  // without a nil check. Something else is not an empty order: it is a broken read.
  if (!Array.isArray(value.seats)) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the order ${reservationId} carries no seat list, so the seats it holds cannot be shown`,
      ),
    };
  }
  // A seat the read cannot name is not a seat the page can print, and dropping it would
  // show an order holding fewer seats than it does. One broken seat refuses the read.
  const seats: PublicOrderSeat[] = [];
  for (const item of value.seats) {
    if (!isRecord(item) || typeof item.eventSeatId !== "number") {
      return {
        ok: false,
        error: unexpectedResponse(
          200,
          `the order ${reservationId} contains a seat without a numeric event seat id`,
        ),
      };
    }
    seats.push({
      eventSeatId: item.eventSeatId,
      rowLabel: stringOr(item.rowLabel, ""),
      seatNumber: stringOr(item.seatNumber, ""),
    });
  }
  if (!Array.isArray(value.ticketIds)) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the order ${reservationId} carries no ticket list, so a paid order could not show its tickets`,
      ),
    };
  }

  // The screening is what a reloaded confirmation keeps. Every field of it is printed,
  // so a read that lost one is refused rather than rendered as a screening that is only
  // partly known.
  const filmTitle = servedString(value.filmTitle);
  if (filmTitle === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the order ${reservationId} carries no film title, so a reload cannot say what it buys`,
      ),
    };
  }
  const cinemaName = servedString(value.cinemaName);
  if (cinemaName === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the order ${reservationId} carries no cinema name, so a reload cannot say where it is`,
      ),
    };
  }
  const roomName = servedString(value.roomName);
  if (roomName === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the order ${reservationId} carries no room name, so a reload cannot say where it is`,
      ),
    };
  }
  const startsAtLocal = servedString(value.startsAtLocal);
  if (startsAtLocal === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the order ${reservationId} carries no startsAtLocal, so its screening time cannot be shown in the venue's zone`,
      ),
    };
  }

  return {
    ok: true,
    value: {
      reservationId,
      status,
      expiresAt,
      amountCents,
      amountDisplay,
      amountDueCents,
      amountDueDisplay,
      seats,
      ticketIds: value.ticketIds.filter(
        (id): id is string => typeof id === "string",
      ),
      eventId: stringOr(value.eventId, ""),
      filmTitle,
      cinemaName,
      roomName,
      startsAt: stringOr(value.startsAt, ""),
      startsAtLocal,
    },
  };
}

function parseTicket(value: unknown): PublicResult<PublicTicket> {
  if (!isRecord(value) || typeof value.ticketId !== "string") {
    return {
      ok: false,
      error: unexpectedResponse(200, "the ticket detail names no ticket"),
    };
  }
  if (!isRecord(value.event)) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the ticket detail carries no screening"),
    };
  }

  const event = value.event;
  // The venue's local time is served, never derived: falling back to `startsAt`
  // here would print the ticket in the browser's zone, which is the one thing the
  // API computes it to prevent. A response without it is refused.
  const startsAtLocal = servedString(event.startsAtLocal);
  if (startsAtLocal === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        "the ticket detail carries no startsAtLocal, so its screening time cannot be shown in the venue's zone",
      ),
    };
  }
  const qrToken = servedString(value.qrToken);
  if (qrToken === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the ticket detail carries no QR code"),
    };
  }
  if (!Array.isArray(value.seats)) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the ticket detail carries no seat"),
    };
  }

  return {
    ok: true,
    value: {
      ticketId: value.ticketId,
      status: stringOr(value.status, "unknown"),
      // Validity is the API's rule (issued and the screening was not
      // cancelled), so only an explicit `true` reads as valid.
      valid: value.valid === true,
      issuedAt: stringOr(value.issuedAt, ""),
      reservationId: stringOr(value.reservationId, ""),
      event: {
        id: stringOr(event.id, ""),
        filmTitle: stringOr(event.filmTitle, ""),
        startsAt: stringOr(event.startsAt, ""),
        startsAtLocal,
        roomName: stringOr(event.roomName, ""),
        cinemaName: stringOr(event.cinemaName, ""),
        timezone: stringOr(event.timezone, ""),
        status: stringOr(event.status, "unknown"),
      },
      seats: parseTicketSeats(value.seats),
      orderAmountCents: integerOr(value.orderAmountCents),
      orderAmountDisplay: stringOr(value.orderAmountDisplay, ""),
      qrToken,
    },
  };
}

function parseVerification(value: unknown): PublicResult<PublicVerification> {
  if (!isRecord(value) || typeof value.ticketId !== "string") {
    return {
      ok: false,
      error: unexpectedResponse(200, "the ticket check names no ticket"),
    };
  }

  const verification: PublicVerification = {
    ticketId: value.ticketId,
    eventId: stringOr(value.eventId, ""),
    seats: Array.isArray(value.seats) ? parseTicketSeats(value.seats) : [],
    ticketStatus: stringOr(value.ticketStatus, "unknown"),
    eventStatus: stringOr(value.eventStatus, "unknown"),
    valid: value.valid === true,
  };
  // The reason is present only for a ticket that does not admit its holder, so
  // an absent key stays absent instead of becoming an empty string a surface
  // would have to special-case.
  const reason = servedString(value.reason);
  if (reason !== undefined) {
    verification.reason = reason;
  }
  return { ok: true, value: verification };
}

function parseTicketSeats(items: unknown[]): PublicTicketSeat[] {
  const seats: PublicTicketSeat[] = [];
  for (const item of items) {
    if (!isRecord(item) || typeof item.eventSeatId !== "number") {
      continue;
    }
    seats.push({
      eventSeatId: item.eventSeatId,
      rowLabel: stringOr(item.rowLabel, ""),
      seatNumber: stringOr(item.seatNumber, ""),
    });
  }
  return seats;
}

function parseAvailability(value: unknown): PublicResult<PublicEventSeats> {
  if (!isRecord(value) || !Array.isArray(value.seats)) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the availability snapshot has no seat list"),
    };
  }

  const seats: PublicEventSeat[] = [];
  for (const item of value.seats) {
    if (!isRecord(item) || typeof item.id !== "number") {
      return {
        ok: false,
        error: unexpectedResponse(
          200,
          "the availability snapshot contains a seat without a numeric id",
        ),
      };
    }
    const seat: PublicEventSeat = {
      id: item.id,
      layoutSeatId: stringOr(item.layoutSeatId, ""),
      state: stringOr(item.state, "unknown"),
      // Occupancy is fail-closed: only an explicit `false` reads as free, and a
      // missing flag is reported as not held because the API always sends it.
      held: item.held === true,
    };
    // The layout projection is additive and optional: a field the API did not
    // send stays absent, so the map can tell "no projection" from "empty label"
    // and never invents geometry.
    const rowLabel = optionalString(item.rowLabel);
    if (rowLabel !== undefined) {
      seat.rowLabel = rowLabel;
    }
    const seatNumber = optionalString(item.seatNumber);
    if (seatNumber !== undefined) {
      seat.seatNumber = seatNumber;
    }
    const kind = optionalString(item.kind);
    if (kind !== undefined) {
      seat.kind = kind;
    }
    seats.push(seat);
  }

  return {
    ok: true,
    value: { eventId: stringOr(value.eventId, ""), seats },
  };
}

function parseHold(value: unknown): PublicResult<PublicHold> {
  if (!isRecord(value) || typeof value.id !== "string") {
    return {
      ok: false,
      error: unexpectedResponse(201, "the hold response has no reservation id"),
    };
  }
  const expiresAt = value.expiresAt;
  if (typeof expiresAt !== "string" || expiresAt.trim() === "") {
    // Without the server deadline there is no countdown, and a client-side guess
    // would promise a window the server never granted.
    return {
      ok: false,
      error: unexpectedResponse(
        201,
        "the hold response has no expiresAt deadline",
      ),
    };
  }
  return {
    ok: true,
    value: {
      id: value.id,
      eventId: stringOr(value.eventId, ""),
      seatIds: Array.isArray(value.seatIds)
        ? value.seatIds.filter((id): id is number => typeof id === "number")
        : [],
      expiresAt,
    },
  };
}

/** Strips trailing slashes so path joins never double up. */
function normalizeBaseUrl(value: string): string {
  return value.trim().replace(/\/+$/, "");
}

type JsonRead = { ok: true; value: unknown } | { ok: false };

async function readJson(response: Response): Promise<JsonRead> {
  try {
    const text = await response.text();
    return { ok: true, value: JSON.parse(text) as unknown };
  } catch {
    return { ok: false };
  }
}

function toProblem(status: number, parsed: JsonRead): PublicProblem {
  if (parsed.ok && isRecord(parsed.value)) {
    const body = parsed.value;
    const code = body.code;
    if (typeof code === "string" && isProblemCode(code)) {
      return {
        type: stringOr(body.type, "about:blank"),
        title: stringOr(body.title, "Request failed"),
        status: typeof body.status === "number" ? body.status : status,
        code,
        detail: stringOr(body.detail, "the request could not be completed"),
      };
    }
  }
  return unexpectedResponse(
    status,
    "the reservation API returned an unrecognised error response",
  );
}

function unexpectedResponse(status: number, detail: string): PublicProblem {
  return {
    type: "https://cinema.local/problems/unexpected-response",
    title: "Unexpected response",
    status,
    code: "unexpected_response",
    detail,
  };
}

function isProblemCode(value: string): value is PublicErrorCode {
  return (PROBLEM_CODES as readonly string[]).includes(value);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function stringOr(value: unknown, fallback: string): string {
  return typeof value === "string" ? value : fallback;
}

/** A value the API must serve for the page to be honest, or undefined when absent. */
function servedString(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  return value.trim() === "" ? undefined : value;
}

function parseCatalog(value: unknown): PublicResult<PublicScreening[]> {
  if (!isRecord(value) || !Array.isArray(value.events)) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the public catalogue has no screening list"),
    };
  }

  const events: PublicScreening[] = [];
  for (const item of value.events) {
    const event = parseScreening(item);
    if (!event.ok) {
      // One unreadable entry refuses the whole catalogue. Dropping it would hide a
      // screening the API offered, and printing a screening whose numbers are
      // missing would show an availability nobody counted.
      return event;
    }
    events.push(event.value);
  }

  // Served order, untouched: the API answers the catalogue in screening order and
  // a client that sorted again could disagree with it.
  return { ok: true, value: events };
}

function parseScreening(value: unknown): PublicResult<PublicScreening> {
  if (!isRecord(value)) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the catalogue contains a screening without an id"),
    };
  }
  const id = servedString(value.id);
  if (id === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the catalogue contains a screening without an id"),
    };
  }

  // The sale state is the API's decision, not a client's: `onSale` is the predicate
  // the database applied, so a missing flag would leave the page with no honest way
  // to know whether a seat can be bought. It is a boolean or the response is refused —
  // never a default, because both defaults would be a claim about the sale.
  const status = servedString(value.status);
  if (status === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the catalogue entry ${id} carries no status, so why it is not on sale cannot be explained`,
      ),
    };
  }
  if (typeof value.onSale !== "boolean") {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the catalogue entry ${id} carries no onSale flag, so whether its seats can be bought is unknown`,
      ),
    };
  }

  const filmTitle = servedString(value.filmTitle);
  if (filmTitle === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(200, `the catalogue entry ${id} carries no film title`),
    };
  }
  const cinemaName = servedString(value.cinemaName);
  if (cinemaName === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(200, `the catalogue entry ${id} carries no cinema name`),
    };
  }
  const roomName = servedString(value.roomName);
  if (roomName === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(200, `the catalogue entry ${id} carries no room name`),
    };
  }
  // The venue's local hour is served by the API's own rule, never derived here:
  // falling back to `startsAt` would print the browser's zone on a page whose
  // whole point is to print the venue's. A response without it is refused.
  const startsAtLocal = servedString(value.startsAtLocal);
  if (startsAtLocal === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the catalogue entry ${id} carries no startsAtLocal, so its screening time cannot be shown in the venue's zone`,
      ),
    };
  }
  const priceDisplay = servedString(value.priceDisplay);
  if (priceDisplay === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the catalogue entry ${id} carries no priceDisplay, so its price cannot be shown`,
      ),
    };
  }
  // Availability is the read model's numbers, so both of them have to be there:
  // "12 of 40 seats available" with either number missing is a claim nobody made.
  const seatCount = wholeOrUndefined(value.seatCount);
  const availableSeats = wholeOrUndefined(value.availableSeats);
  if (seatCount === undefined || availableSeats === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the catalogue entry ${id} carries no seat count, so its availability cannot be shown`,
      ),
    };
  }

  return {
    ok: true,
    value: {
      id,
      status,
      onSale: value.onSale,
      filmTitle,
      cinemaName,
      roomName,
      startsAt: stringOr(value.startsAt, ""),
      startsAtLocal,
      priceCents: integerOr(value.priceCents),
      priceDisplay,
      seatCount,
      availableSeats,
    },
  };
}

/** A minor-units amount; anything that is not a finite number counts as zero. */
function integerOr(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

/** A served string, or undefined when the field is absent or blank. */
function optionalString(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  return value.trim() === "" ? undefined : value;
}

/** A served whole number (a seat count), or undefined when it is not one. */
function wholeOrUndefined(value: unknown): number | undefined {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    return undefined;
  }
  return value;
}
