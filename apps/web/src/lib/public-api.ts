// Public (guest) reservation API client.
//
// The customer facing routes live under /api/v1, are mounted outside the
// administrative guard, and require no credential: a guest selects seats without
// an account, pays for them and shows the ticket. This client therefore:
//
//   - sends one credential, and only where an account is the subject: the session
//     token this application keeps in its own HttpOnly cookie (lib/session.ts) is
//     presented as a bearer by [PublicClient.myTickets], which reads the account's list,
//     and by [PublicClient.signOut], which ends the session that list is read with. Those
//     are the only two calls here that carry an Authorization header. Every other call sends
//     none and reads no token; besides the usual JSON ones, the only header those add is the
//     Idempotency-Key of a hold or a payment attempt;
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
// route again), the M3 ticket verification a page uses to say plainly whether a
// ticket admits its holder, the M4.1 admission the door performs with the same code, the
// signed link a purchase email carries (so the web route behind it redeems the tokens
// in this process and the browser never learns the API's address), and the resend action a
// ticket page offers (so mailing that link to an address typed there is also a call this
// process makes, with the address stored nowhere and the hourly bound read from the API), and
// the two halves of signing in (so a link is asked for and spent in this process as well, and
// the browser only ever holds the session cookie this application issues), the account's
// own list of tickets (the one read that presents that session, and the reason an account is
// worth having at all), and the sign-out that ends the session that list is read with.
// The two ticket *downloads* are not reads this
// client makes: the page points its link and its image at this application's own
// same-origin ticket routes (lib/ticket-media.ts), which forward the request to
// the API from the server, so the browser fetches the bytes and the page never
// handles them.
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

import { randomUUID } from "./uuid.ts";

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
  // The bound on mailing one order: the API answers a refused send with `429` and the wait
  // it has left, so a page can say when to try again instead of guessing.
  | "rate_limited"
  | "voided"
  | "event_cancelled"
  // The one refusal the sign-in routes give for a link that was spent, expired or never signed.
  // It is a code of its own rather than an unreadable answer: without it a page could not tell
  // the box office's opaque "no" from a body this client failed to parse, and would print a
  // broken-answer message for a link that was simply used twice.
  | "unauthorized"
  | "network_error"
  | "unexpected_response";

/** RFC 9457 problem details, normalized into a typed value. */
export interface PublicProblem {
  type: string;
  title: string;
  status: number;
  code: PublicErrorCode;
  detail: string;
  /**
   * How long the API asked the caller to wait, in whole seconds, when it said so in a
   * `Retry-After` header — the machine-readable half of a `429`.
   *
   * It is absent for every refusal that carried no such header, including one whose wait the
   * body only mentions in a sentence: a page cannot parse prose for a number, and inventing
   * one would print a time the server never asked for.
   */
  retryAfterSeconds?: number;
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
  /**
   * The address the tickets are to be mailed to, when the buyer gave one.
   *
   * It is the only field this request can carry, and it is not money: the amount stays the
   * box office's number, so the address cannot change what is charged, only where a copy of
   * the ticket goes. The API stores it in the same transaction that captures the payment,
   * which is why the checkout asks for it **before** paying (D1): a tab closed right after
   * the charge has still left an address behind.
   *
   * Absent and blank mean the same thing, and it is not `{"email": ""}`: the request then
   * carries no body at all, exactly as it did before this field existed. The gates pay
   * without an address, and a body they never asked for would be a change to them.
   *
   * The value goes out unverified and is not logged here: nothing in this process knows
   * whether the address belongs to whoever typed it, and nothing claims it does.
   */
  email?: string;
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
 * The identifier is the capability (a reservation is opened by its link, and an account
 * protects the list of them and not the links themselves), so this read is exactly as public
 * as the ticket route and answers no buyer identity and no payment-attempt internals.
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

/**
 * GET /ticket-links/{token} — what the signed link a purchase email carries opens.
 *
 * The token is the whole request and the whole credential: it names a paid order and the
 * instant it stops verifying, signed with the link key. It lives in the path because that
 * is the shape of a URL, which is also why nothing here parses, prints or re-derives it.
 *
 * The three fields are the answer and nothing else is: the order the signature named, the
 * expiry the signature spelled, and the tickets the sale issued. There is no buyer in it,
 * no address and no payment detail, so a link that leaks into a shared mailbox leaks the
 * tickets it opens and not who bought them.
 */
export interface PublicTicketLink {
  /** The order inside the signature, as the read confirmed it. */
  reservationId: string;
  /** The instant the link stops opening, RFC 3339, read from the signature. */
  expiresAt: string;
  /** The order's tickets, ascending as the API answers them; never re-sorted here. */
  ticketIds: string[];
}

/**
 * POST /tickets/{ticketId}/email — the ticket page's "send the link to another address".
 *
 * The answer of a send is one field, and it is the API's own word: `sent: true` is written
 * only after the provider took the message, which is why the status alone does not have to
 * be interpreted by a caller. Nothing here names the message, the link or the address — the
 * address is stored nowhere (S2), and the link is a credential.
 */
export interface PublicTicketEmailSent {
  /**
   * Always `true`: a `202` whose body says anything else is refused by this client instead
   * of being read as a send, because "a message was mailed" is the one claim this feature
   * must never make on its own.
   */
  sent: true;
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

/**
 * POST /tickets/redeem — the door's one answer about one presented code.
 *
 * It is the verification above plus the act: the API verifies the code, admits the ticket it
 * names exactly once, and answers with what the door has to say. `alreadyRedeemed` is the
 * field that separates the two admissions, because both are `200`: the second presentation is
 * not an error, it is a ticket that was used at the *first* instant.
 */
export interface PublicRedemption {
  ticketId: string;
  /** Stored ticket status: `issued` or `void`. */
  ticketStatus: string;
  eventId: string;
  seats: PublicTicketSeat[];
  filmTitle: string;
  cinemaName: string;
  roomName: string;
  /** The instant the screening starts, in UTC, for a `<time>` element. */
  startsAt: string;
  /** The screening time in the venue's own zone, rendered by the API. */
  startsAtLocal: string;
  /**
   * When this ticket was admitted: the first admission's instant, on both answers.
   *
   * The API serves it in UTC and serves no venue-local form of it, so a surface prints the
   * instant it is rather than deriving an hour from its own clock.
   */
  redeemedAt: string;
  /** The same instant in the venue's own clock (RFC 3339 with its offset). */
  redeemedAtLocal: string;
  /** The opaque label the door gave itself when it admitted the ticket, or empty. */
  redeemedBy: string;
  /** True when this ticket had already been admitted, before this call asked. */
  alreadyRedeemed: boolean;
}

export interface RedeemTicketInput {
  /** The signed code the holder shows; whitespace is trimmed before it is sent. */
  token: string;
  /** The door's own label (a till, a gate, a shift), or absent when it names none. */
  label?: string;
}

/**
 * GET /me/tickets — the account's own list, for the session presented with it (T7 · S5, D3).
 *
 * The entries are the ticket page's own document, field for field, because the same row is
 * rendered on both screens: a second, thinner shape here would be a second rendering of a ticket
 * that could disagree with the page it links to. An account that bought nothing answers an empty
 * array, and the API orders the list itself — newest first, the identifier breaking ties — so a
 * caller renders it as served rather than sorting a second time.
 */
export interface PublicAccountTickets {
  /** The account's tickets, in the box office's order. Empty is an answer, not a failure. */
  tickets: PublicTicket[];
}

/** One screening of the public catalogue, as `GET /events` answers it. */
/**
 * POST /identity/links — the answer of "mail me a sign-in link".
 *
 * It carries one bit, because the route carries one: a link that was mailed, one the hourly
 * allowance refused and one whose counter could not be read all answer the same `202` with the
 * same body. Nothing here can distinguish them, which is what makes the request safe to send an
 * address to: the answer is not an oracle for "does this person have an account".
 */
export interface PublicSignInLinkRequested {
  /** Always `true`: the request was accepted, and nothing more was said about it. */
  accepted: true;
}

/**
 * POST /identity/sessions — the session a spent sign-in link opened.
 *
 * The `201` is the only moment this token exists in a response: the API sets no cookie, so it is
 * this application that puts it in one (lib/session.ts), `HttpOnly`, and never puts it anywhere
 * else — not in a URL, not in a page, not in a log line.
 */
export interface PublicSignInSession {
  /** The opaque session token. A credential: it goes into the cookie and stops there. */
  token: string;
  /** When the session stops working, as the row the API wrote keeps it. */
  expiresAt: string;
}

/**
 * DELETE /identity/sessions — the answer of "end this session".
 *
 * The route answers `204 No Content` in every case it answers at all: a live token, an expired
 * one, a revoked one, a forged one and a request with no credential. There is therefore no body
 * to read and no field to interpret, and this is what the status means here: whatever that token
 * was, it opens no session now. Nothing about the token comes back, in an answer or in a header.
 */
export interface PublicSignOutRevoked {
  /** Always `true`: the one answer the route gives is the one that closes what it was given. */
  revoked: true;
}

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

/**
 * Where one page of the catalogue ends, as `GET /events` reports it beside the list.
 *
 * It is a sibling of `events`, not a replacement: the list is unchanged and this says where
 * it stopped. `hasMore` is the read's own answer — it exists because a row exists beyond
 * this page, not because the page happens to be full — so a client never has to guess
 * whether to offer the next one.
 */
export interface PublicCataloguePage {
  /** Whether a screening exists beyond this page. */
  hasMore: boolean;
  /** The cursor that reaches the next page, or null on the last page. */
  nextCursor: string | null;
}

/** One page of the public catalogue: the screenings it answered, then where it ended. */
export interface PublicCatalogue {
  events: PublicScreening[];
  page: PublicCataloguePage;
}

export interface CataloguePageInput {
  /**
   * The cursor a link carried, sent back to the API exactly as it was received.
   *
   * Only the catalogue can read a cursor, so this client does not parse it, trim it or
   * re-encode it into one of its own: a bookmark is either the page the API issued or a
   * value the API refuses by name, and both are its answers to give. Absent or blank means
   * the first page, because a blank `cursor` is one the API refuses as empty.
   */
  cursor?: string;
}

export interface PublicClient {
  /**
   * The screenings of the catalogue's first page, in the API's screening order.
   *
   * The order is the API's (screening time, then whatever it breaks ties by), so
   * callers render the array as served: re-sorting it here would be a second
   * opinion about a sequence one read already decided. An empty catalogue is an
   * empty array, never an error.
   *
   * This is the page's entries alone, which is what the catalogue answered before it
   * paged: a caller that only wants screenings keeps reading the array it always read.
   * A screen that has to offer the next page reads [listCataloguePage], which answers the
   * same entries with the `page` sibling beside them.
   */
  listEvents(): Promise<PublicResult<PublicScreening[]>>;
  /**
   * One page of the catalogue, and the cursor that reaches the next one.
   *
   * The entries are exactly what [listEvents] answers; the sibling is where they stopped.
   * A caller that renders the next page as a link needs both, which is why this read
   * exists instead of widening the one above.
   */
  listCataloguePage(input?: CataloguePageInput): Promise<PublicResult<PublicCatalogue>>;
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
  /**
   * The tickets this account bought, read with the session token this browser holds.
   *
   * This is the **first** call in this application that sends a credential to the API, and the
   * token comes from exactly one place: [readSessionToken] in lib/session.ts, the `HttpOnly`
   * cookie this application issued when a mailed link was spent. It is never read from a client
   * component, never logged, never cached and never placed in a URL — it travels in the
   * `Authorization` header of this request and of [PublicClient.signOut], and nowhere else. A
   * caller therefore does not get to pass an arbitrary string in: it passes the cookie's own
   * value, on the server, from the session helper that owns it.
   *
   * The two refusals are different facts and arrive as different problems. A `401` is the opaque
   * refusal the sign-in route already gives — spent, expired, revoked and never issued are one
   * answer — and it needs the `unauthorized` code so a page can say that the session ended
   * instead of printing a broken-answer message. A `503` is our failure, retryable, and must never
   * read as a sign-out. An unreadable body is refused rather than half-rendered: an empty account
   * answers `{"tickets": []}`, so a missing list is a broken read and not a list of nothing.
   */
  myTickets(sessionToken: string): Promise<PublicResult<PublicAccountTickets>>;
  /**
   * Asks the box office to mail a sign-in link to this address.
   *
   * The single answer covers every outcome the route is allowed to talk about: a link was
   * mailed, the hourly allowance refused it, or the counter that keeps that allowance could not
   * be read. All three are `202 {"status":"accepted"}`, deliberately, so a page can only say the
   * one true thing — "if that address can receive mail, a link is on its way" — instead of
   * becoming a way to ask whether an account exists.
   *
   * The two refusals that are *not* about the address's mailbox:
   *
   *   - `400 invalid_input` is the API's own rule refusing the value (or a blank field), and its
   *     `detail` is the sentence a page prints verbatim;
   *   - `503 service_unavailable` is a store or a mail provider that could not answer. It is
   *     retryable, and it decides nothing about the address.
   *
   * Nothing is logged here, and the address travels in the body: it is a person, and a problem
   * that echoed it would put it in page text.
   */
  requestSignInLink(email: string): Promise<PublicResult<PublicSignInLinkRequested>>;
  /**
   * Spends the token a sign-in email carried, and answers the session it opened.
   *
   * A link expires in minutes and works once, so the API answers every way of not opening —
   * spent, expired, forged, tampered — with one opaque `401` whose body is fixed. It arrives
   * here as the `unauthorized` problem so a page can render the single honest state that
   * refusal means; the distinction is not available, and no surface may imply it is.
   *
   * A `503` is our failure and stands apart from that refusal: an outage must never read as a
   * link somebody already used. The token is sent in the body, never in the path, so it does not
   * spend its life in access logs and `Referer` headers.
   */
  redeemSignInLink(token: string): Promise<PublicResult<PublicSignInSession>>;
  /**
   * Ends the session this browser holds: `DELETE /identity/sessions`.
   *
   * The token is the one in this application's session cookie (lib/session.ts), and it travels in
   * the `Authorization` header and nowhere else: not in the path, not in a query string, not in a
   * body.
   *
   * The route is built so that a sign-out is never a question. It answers `204` for a live token,
   * an expired one, a revoked one, a forged one and a request with no credential at all, because
   * a client told its sign-out failed believes it is still signed in, and because a `401` here
   * would let a caller ask whether a credential ever existed. So this call has two readings and
   * no third one: the no-content answer, and a failure to ask — a `5xx`, an unreachable API, an
   * answer that is not the documented one — which is reported as the problem it is. A caller must
   * not let that failure keep somebody signed in **in their own browser**: that decision, and
   * why, is in lib/identity.ts.
   */
  signOut(sessionToken: string): Promise<PublicResult<PublicSignOutRevoked>>;
  /**
   * Redeems the signed link a purchase email carries: the order it opens, and when it
   * stops opening.
   *
   * The API answers every way of not opening — forged, tampered, expired, an order this
   * database never sold, an order that was never paid — with one `404`, deliberately, so
   * a probe cannot confirm that a reservation exists and an aged-out email is
   * indistinguishable from a guess. It arrives here as the `not_found` problem an unknown
   * ticket already reads as; a `503` is our failure and says nothing about the link.
   */
  redeemTicketLink(token: string): Promise<PublicResult<PublicTicketLink>>;
  /**
   * Mails this ticket's order to an address typed at that moment: the same message, the same
   * subject and the same signed, expiring link a purchase email carries, and an address that
   * is stored nowhere.
   *
   * The ticket id is the whole credential, exactly as it is for the read above (guest access
   * has no account to prove ownership with), so this route is bounded server side: three
   * sends per order per hour, and a `429` with `Retry-After` past that. Only a `202` whose
   * body says `sent: true` means the message left; a `400` is the checkout's own address rule
   * refusing the value, the opaque `404` is an unknown ticket or an unpaid order, and a `503`
   * is a deployment that does not send mail.
   */
  sendTicketEmail(
    ticketId: string,
    email: string,
  ): Promise<PublicResult<PublicTicketEmailSent>>;
  verifyTicket(token: string): Promise<PublicResult<PublicVerification>>;
  /**
   * Presents a code at the door: the server verifies it and admits the ticket it names once.
   *
   * There is no second endpoint and no second opinion: this is the same public route a
   * scanner would POST to, it carries no administrative credential, and the answer — admitted,
   * already admitted, refused, or unreadable — is the only verdict a screen may render.
   */
  redeemTicket(input: RedeemTicketInput): Promise<PublicResult<PublicRedemption>>;
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

/** The serializable answer of the account list read. */
export type AccountTicketsResponse =
  | { ok: true; tickets: PublicTicket[] }
  | { ok: false; problem: PublicProblem };

/** The serializable answer of a ticket-email send. */
export type TicketEmailResponse =
  | { ok: true; email: PublicTicketEmailSent }
  | { ok: false; problem: PublicProblem };

/** The serializable answer of a ticket verification. */
export type VerificationResponse =
  | { ok: true; verification: PublicVerification }
  | { ok: false; problem: PublicProblem };

/** The serializable answer of an admission at the door. */
export type RedemptionResponse =
  | { ok: true; redemption: PublicRedemption }
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

/**
 * The prefix every public (guest) route of the API hangs off.
 *
 * It is declared here, once, because two places build a public address: this client and
 * the ticket media forward in lib/ticket-media.ts. A second copy of the value would drift
 * silently — the forward would answer 502 for every ticket while both files looked right.
 */
export const PUBLIC_PATH_PREFIX = "/api/v1";

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
  "rate_limited",
  // The two refusals an admission can earn. They are problem codes, not reasons: a void
  // ticket and a cancelled screening are the API's own words for why nobody walks in.
  "voided",
  "event_cancelled",
  // The opaque refusal of a sign-in link that was spent, expired or never signed. Without it in
  // this vocabulary the refusal would arrive as an unreadable answer, and the page behind it
  // could not tell the box office's own "no" from a body that failed to parse.
  "unauthorized",
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
  return randomUUID();
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
      return { ok: false, error: unreachable() };
    }

    const parsed = await readJson(response);

    if (!response.ok) {
      return {
        ok: false,
        error: withRetryAfter(toProblem(response.status, parsed), response),
      };
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

  // One page request, used by both catalogue reads so the entries cannot disagree and the
  // parser below is the only place a response is interpreted.
  async function readCatalogue(
    input: CataloguePageInput = {},
  ): Promise<PublicResult<PublicCatalogue>> {
    const cursor = namedCursor(input.cursor);
    const result = await send<unknown>(
      cursor === null ? "/events" : `/events?cursor=${encodeURIComponent(cursor)}`,
      {
        method: "GET",
        headers: { Accept: "application/json" },
        cache: "no-store",
      },
    );
    if (!result.ok) {
      return result;
    }
    return parseCatalogue(result.value);
  }

  return {
    async listEvents() {
      const catalogue = await readCatalogue();
      if (!catalogue.ok) {
        return catalogue;
      }
      return { ok: true, value: catalogue.value.events };
    },

    listCataloguePage: readCatalogue,

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

      // The one field this request may carry is the address the tickets are mailed to. It is
      // trimmed here because the API trims it anyway, so what leaves is exactly what was
      // accepted. Money stays absent by design: the amount is `events.price_cents × seats
      // held`, computed server side, so there is nothing for a client to send about it and
      // nothing for the API to trust if it did.
      const email = (input.email ?? "").trim();
      // A blank address is absent, not empty: the request then keeps the bodiless shape
      // every caller had before this field existed, the gates included.
      const address = email === "" ? undefined : email;

      const headers: Record<string, string> = {
        Accept: "application/json",
        "Idempotency-Key": input.idempotencyKey,
      };
      if (address !== undefined) {
        // A JSON content type is declared only when there is JSON to declare, so a request
        // without an address is the request the API already answers today.
        headers["Content-Type"] = "application/json";
      }

      const result = await send<unknown>(
        `/reservations/${encodeURIComponent(input.reservationId)}/payment`,
        {
          method: "POST",
          headers,
          cache: "no-store",
          body: address === undefined ? undefined : JSON.stringify({ email: address }),
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

    async myTickets(sessionToken) {
      // The token this application's own session cookie holds. A blank one names no session, and
      // sending it would only ask the API for the refusal this client already knows the answer to
      // — while putting an empty credential on the wire.
      const token = sessionToken.trim();
      if (token === "") {
        return { ok: false, error: invalidInput("this read needs the session this browser holds") };
      }

      // The one credential-bearing request of this client. The header is the whole of where the
      // token goes: not the path, not a query string, not the body, and nothing here logs or
      // caches the request (a cached list would answer a second person with the first person's
      // tickets).
      const result = await send<unknown>("/me/tickets", {
        method: "GET",
        headers: {
          Accept: "application/json",
          Authorization: `Bearer ${token}`,
        },
        cache: "no-store",
      });
      if (!result.ok) {
        return { ok: false, error: accountProblem(result.error) };
      }
      return parseAccountTickets(result.value);
    },

    async sendTicketEmail(ticketId, email) {
      const id = ticketId.trim();
      if (id === "") {
        return { ok: false, error: invalidInput("this page needs a ticket id") };
      }
      // The address is the entire request, so a blank one is never sent: the API answers it
      // with the `400` it gives a value its rule refuses, and a page with an empty field has
      // nothing to ask. Everything else goes out trimmed and otherwise untouched — the API is
      // the authority on what an address is, and a second validator here could only refuse an
      // address the box office would have mailed.
      const address = email.trim();
      if (address === "") {
        return { ok: false, error: invalidInput("an address is needed to mail the ticket") };
      }

      // A message, not an attempt: no Idempotency-Key, because a send is not replayed and the
      // order's allowance is counted by the API rather than by a key this browser keeps.
      const result = await send<unknown>(`/tickets/${encodeURIComponent(id)}/email`, {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
        },
        cache: "no-store",
        body: JSON.stringify({ email: address }),
      });
      if (!result.ok) {
        return result;
      }
      return parseTicketEmail(result.value);
    },

    async requestSignInLink(email) {
      // A blank field is a question this client already answers, and the API's answer to it is
      // the `400` this refusal mirrors; asking would spend a round trip to learn nothing.
      const address = email.trim();
      if (address === "") {
        return {
          ok: false,
          error: invalidInput("an address is needed to send the sign-in link"),
        };
      }

      const result = await send<unknown>("/identity/links", {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
        },
        cache: "no-store",
        body: JSON.stringify({ email: address }),
      });
      if (!result.ok) {
        return result;
      }
      return parseSignInLinkRequest(result.value);
    },

    async redeemSignInLink(token) {
      const presented = token.trim();
      if (presented === "") {
        // A link with no token cannot be spent, and the API would only answer it with the
        // refusal it gives a forged one.
        return { ok: false, error: invalidInput("this page needs a sign-in link token") };
      }

      // A write, so no cache: this call spends the link, and a cached answer would be a second
      // session handed out for a token the store has already consumed.
      const result = await send<unknown>("/identity/sessions", {
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
      return parseSignInSession(result.value);
    },

    async signOut(sessionToken) {
      // The token this application's own session cookie holds. A blank one names no session, and
      // sending it would put an empty credential on the wire to ask for an answer already known.
      const token = sessionToken.trim();
      if (token === "") {
        return {
          ok: false,
          error: invalidInput("signing out needs the session this browser holds"),
        };
      }

      // This one request is made here instead of through `send` because its answer has no body
      // to read and `send` reads one: an empty body would arrive as an unreadable answer, which
      // is the opposite of what a `204` says.
      let response: Response;
      try {
        response = await doFetch(`${baseUrl}${PUBLIC_PATH_PREFIX}/identity/sessions`, {
          method: "DELETE",
          headers: {
            Accept: "application/json",
            // The credential, in the header and nowhere else: not in the path, not in a query
            // string, not in a body, and never logged here.
            Authorization: `Bearer ${token}`,
          },
          cache: "no-store",
        });
      } catch {
        return { ok: false, error: unreachable() };
      }

      if (response.status === 204) {
        // The only answer the route gives, and the whole of what it means: the row is closed, or
        // there was nothing to close — a distinction the box office withholds on purpose.
        return { ok: true, value: { revoked: true } };
      }

      // Anything else is not the documented answer — a proxy's `502`, an older API's `404`, a
      // `405` from a deployment that does not serve this method — and none of them may be read
      // as a revocation. A problem body is read as the problem it is; a body that is not one is
      // refused rather than guessed at.
      const parsed = await readJson(response);
      return {
        ok: false,
        error: withRetryAfter(toProblem(response.status, parsed), response),
      };
    },

    async redeemTicketLink(token) {
      const presented = token.trim();
      if (presented === "") {
        // A link with no token names nothing, and the API would only answer it with the
        // refusal it gives a forged one. Asking would tell the page nothing new.
        return { ok: false, error: invalidInput("this page needs a link token") };
      }

      // A read: no key, no body. It redeems the link, not the tickets, so a reload of the
      // page costs the same answer and never spends anything.
      const result = await send<unknown>(
        `/ticket-links/${encodeURIComponent(presented)}`,
        {
          method: "GET",
          headers: { Accept: "application/json" },
          cache: "no-store",
        },
      );
      if (!result.ok) {
        // The route's one refusal is already the problem an unknown ticket answers with,
        // so it is handed on unchanged rather than translated into a second vocabulary.
        return result;
      }
      return parseTicketLink(result.value);
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

    async redeemTicket(input) {
      const presented = input.token.trim();
      if (presented === "") {
        // A blank code is not a code: the API would answer it with the refusal it gives a
        // forged one, and asking would tell the door nothing it does not already know.
        return { ok: false, error: invalidInput("a code is needed to admit a ticket") };
      }

      // The label is the door's own name for itself and is omitted when the door names
      // nothing, so an admission never records a name nobody gave it.
      const label = (input.label ?? "").trim();
      const result = await send<unknown>("/tickets/redeem", {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
        },
        cache: "no-store",
        body: JSON.stringify(
          label === "" ? { token: presented } : { token: presented, label },
        ),
      });
      if (!result.ok) {
        return result;
      }
      return parseRedemption(result.value);
    },
  };
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

/**
 * The failure of a request that never reached the API at all.
 *
 * It is one function because two calls now build it — the shared `send` and the sign-out, which
 * cannot go through `send` — and two spellings of "the service could not be reached" would be
 * two different answers to the same situation. Nothing of the underlying error survives into it:
 * that error can echo request material, and the UI only needs to know that a retry is the way
 * forward.
 */
function unreachable(): PublicProblem {
  return {
    type: "https://cinema.local/problems/network",
    title: "Network error",
    status: 0,
    code: "network_error",
    detail: "the reservation service could not be reached",
  };
}

/**
 * The account read's two refusals, in the vocabulary the screens read.
 *
 * A `401` is mapped by status as well as by code: the route answers the login route's own fixed
 * body, but a refusal that arrives without one must still read as the refusal rather than as a
 * body this client failed to parse — the difference is a page that says the session ended and a
 * page that says the box office answered something unreadable. A `503` is mapped the same way,
 * for the opposite reason: it must stay retryable, never a sign-out, even with no body at all.
 */
function accountProblem(problem: PublicProblem): PublicProblem {
  if (problem.code === "unauthorized") {
    return problem;
  }
  if (problem.status === 401) {
    return {
      type: "https://cinema.local/problems/unauthorized",
      title: "Unauthorized",
      status: 401,
      code: "unauthorized",
      detail: problem.detail,
    };
  }
  if (problem.code === "service_unavailable") {
    return problem;
  }
  if (problem.status === 503) {
    return {
      type: "https://cinema.local/problems/service-unavailable",
      title: "Service unavailable",
      status: 503,
      code: "service_unavailable",
      detail: problem.detail,
    };
  }
  return problem;
}

/**
 * Reads the account's list, refusing anything that is not the list.
 *
 * One unreadable entry refuses the whole read. Dropping it would hide a ticket the account owns,
 * and printing it would render half of one; an empty array is the only shape that means "nothing
 * was bought", and the route serves that shape rather than a missing key.
 */
function parseAccountTickets(value: unknown): PublicResult<PublicAccountTickets> {
  if (!isRecord(value) || !Array.isArray(value.tickets)) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        "the account list carries no ticket list, so what the account bought cannot be said",
      ),
    };
  }

  const tickets: PublicTicket[] = [];
  for (const item of value.tickets) {
    const parsed = parseTicket(item);
    if (!parsed.ok) {
      return {
        ok: false,
        error: unexpectedResponse(
          200,
          `the account list contains a ticket this client cannot read: ${parsed.error.detail}`,
        ),
      };
    }
    tickets.push(parsed.value);
  }

  // Served order, untouched: the API answers newest first and breaks ties by identifier, so a
  // client that sorted again would be a second opinion about somebody's history.
  return { ok: true, value: { tickets } };
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

function parseTicketLink(value: unknown): PublicResult<PublicTicketLink> {
  if (!isRecord(value)) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the link answer is not an order"),
    };
  }
  const reservationId = servedString(value.reservationId);
  if (reservationId === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(200, "the link answer names no order"),
    };
  }
  // The expiry is what the page prints to say when the email stops opening, and the
  // signature is the only thing that knows it: a missing one is refused rather than
  // filled in with a guess. The token itself is never echoed, here or anywhere below.
  const expiresAt = servedString(value.expiresAt);
  if (expiresAt === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the link to order ${reservationId} carries no expiresAt, so when the link stops opening cannot be said`,
      ),
    };
  }
  if (!Array.isArray(value.ticketIds)) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the link to order ${reservationId} carries no ticket list, so what it opens cannot be said`,
      ),
    };
  }
  // Only a paid sale with tickets opens a link (see the API's own route), so a `200` with
  // nothing in it is a broken answer. Reading it as an empty order would have the page
  // print that the link opens nothing, which is a claim no one made.
  const ticketIds = value.ticketIds.filter((id): id is string => typeof id === "string");
  if (ticketIds.length === 0) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the link to order ${reservationId} names no ticket, so it opens nothing to show`,
      ),
    };
  }

  return { ok: true, value: { reservationId, expiresAt, ticketIds } };
}

/** The one body the link route serves for every outcome it is willing to report. */
const SIGN_IN_LINK_ACCEPTED = "accepted";

/**
 * Reads the one acceptance, and refuses every other `202`.
 *
 * A `202` says the request was accepted, which is true even when nothing was mailed; only the
 * body's own word says the route took it in the shape the route documents. Anything else is not
 * a confirmation, and a client that read it as one would tell a person to wait for a message
 * nobody promised.
 */
function parseSignInLinkRequest(
  value: unknown,
): PublicResult<PublicSignInLinkRequested> {
  if (!isRecord(value) || value.status !== SIGN_IN_LINK_ACCEPTED) {
    return {
      ok: false,
      error: unexpectedResponse(
        202,
        "the sign-in link answer does not say the request was accepted",
      ),
    };
  }
  return { ok: true, value: { accepted: true } };
}

/**
 * Reads the session a spent link opened, and refuses a `201` that is not one.
 *
 * Both fields are required and neither is defaulted. An empty token would be a credential that
 * names nothing, and this application would write it into a cookie; an expiry it cannot read
 * would become a lifetime invented here, and both possible inventions (a session that never ends,
 * a session that died at once) are claims about a row nobody read. The token is not echoed in a
 * refusal: a problem reaches page text and logs.
 */
function parseSignInSession(value: unknown): PublicResult<PublicSignInSession> {
  if (!isRecord(value)) {
    return {
      ok: false,
      error: unexpectedResponse(201, "the sign-in answer is not a session"),
    };
  }
  const token = servedString(value.token);
  if (token === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(201, "the sign-in answer carries no session token"),
    };
  }
  const expiresAt = servedString(value.expiresAt);
  if (expiresAt === undefined || Number.isNaN(Date.parse(expiresAt))) {
    return {
      ok: false,
      error: unexpectedResponse(
        201,
        "the sign-in answer carries no readable expiresAt, so the session's lifetime is unknown",
      ),
    };
  }
  return { ok: true, value: { token, expiresAt } };
}

function parseTicketEmail(value: unknown): PublicResult<PublicTicketEmailSent> {
  // A `202` says the request was accepted; only `{"sent": true}` says a provider took the
  // message. An empty body or `sent: false` is therefore refused rather than read as a send,
  // and the status alone is never enough: the page prints "sent", and nothing else here may.
  if (!isRecord(value) || value.sent !== true) {
    return {
      ok: false,
      error: unexpectedResponse(
        202,
        "the email answer does not say the message was sent",
      ),
    };
  }
  return { ok: true, value: { sent: true } };
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

function parseRedemption(value: unknown): PublicResult<PublicRedemption> {
  if (!isRecord(value) || typeof value.ticketId !== "string") {
    return { ok: false, error: unexpectedResponse(200, "the admission names no ticket") };
  }

  // Both time forms are printed: the venue's own hour for the cashier, the instant for a
  // `<time>` element. Neither is derived here, so a missing one refuses the answer.
  const startsAtLocal = servedString(value.startsAtLocal);
  if (startsAtLocal === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the admission of ${value.ticketId} carries no startsAtLocal, so its screening time cannot be shown in the venue's zone`,
      ),
    };
  }
  // Without the stored instant the door cannot say when the ticket was used, and the one
  // thing a second presentation must never be is undated.
  const redeemedAt = servedString(value.redeemedAt);
  if (redeemedAt === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the admission of ${value.ticketId} carries no redeemedAt instant, so when it was used cannot be said`,
      ),
    };
  }
  // The venue's own hour is served beside the instant, and the door prints that one: the
  // browser's zone is not the venue's, and deriving an hour here would print a time the venue
  // never wrote down. Missing it is a refusal for the same reason a missing instant is.
  const redeemedAtLocal = servedString(value.redeemedAtLocal);
  if (redeemedAtLocal === undefined) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the admission of ${value.ticketId} carries no redeemedAtLocal, so the hour the venue wrote down cannot be shown`,
      ),
    };
  }
  // The flag is the whole difference between the two admissions, so it is served or the
  // answer is refused: defaulting it either way would admit a used ticket or refuse a fresh
  // one, and neither is the server's verdict.
  if (typeof value.alreadyRedeemed !== "boolean") {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the admission of ${value.ticketId} carries no alreadyRedeemed flag, so whether this ticket was used cannot be said`,
      ),
    };
  }
  if (!Array.isArray(value.seats)) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        `the admission of ${value.ticketId} carries no seat list, so where to go cannot be shown`,
      ),
    };
  }

  return {
    ok: true,
    value: {
      ticketId: value.ticketId,
      ticketStatus: stringOr(value.ticketStatus, "unknown"),
      eventId: stringOr(value.eventId, ""),
      seats: parseTicketSeats(value.seats),
      filmTitle: stringOr(value.filmTitle, ""),
      cinemaName: stringOr(value.cinemaName, ""),
      roomName: stringOr(value.roomName, ""),
      startsAt: stringOr(value.startsAt, ""),
      startsAtLocal,
      redeemedAt,
      redeemedAtLocal,
      redeemedBy: stringOr(value.redeemedBy, ""),
      alreadyRedeemed: value.alreadyRedeemed,
    },
  };
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

/**
 * The problem, with the wait the API stated in a `Retry-After` header when it served one.
 *
 * The header is the machine-readable half of a `429`; the body repeats the wait inside a
 * sentence, and a page may not parse prose for a number. Only whole seconds are read: the
 * header also allows an HTTP date, and turning a date into a countdown here would put a second
 * clock in a client whose rule is that the server owns the time.
 */
function withRetryAfter(problem: PublicProblem, response: Response): PublicProblem {
  const raw = (response.headers.get("Retry-After") ?? "").trim();
  if (!/^\d+$/.test(raw)) {
    return problem;
  }
  const seconds = Number(raw);
  return seconds > 0 ? { ...problem, retryAfterSeconds: seconds } : problem;
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

function parseCatalogue(value: unknown): PublicResult<PublicCatalogue> {
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

  const page = parseCataloguePage(value.page);
  if (!page.ok) {
    return page;
  }

  // Served order, untouched: the API answers the catalogue in screening order and
  // a client that sorted again could disagree with it.
  return { ok: true, value: { events, page: page.value } };
}

/**
 * The `page` sibling of the catalogue.
 *
 * It is optional because it is additive: an API from before the catalogue paged answers one
 * list and no page, and that list *was* the whole programme, so "nothing more" is the honest
 * reading rather than a broken contract. The `limit` the API also serves is deliberately not
 * carried: a screen cannot print a bound it never asked for, and carrying it here would
 * invite one to.
 *
 * When the sibling is served, it decides. A `hasMore` that is not a boolean is refused, and so
 * is a page that claims more without a cursor to reach it — the one thing a screen must never
 * do is tell a buyer there is more and then offer nothing to continue with.
 */
function parseCataloguePage(value: unknown): PublicResult<PublicCataloguePage> {
  if (value === undefined || value === null) {
    return { ok: true, value: { hasMore: false, nextCursor: null } };
  }
  if (!isRecord(value) || typeof value.hasMore !== "boolean") {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        "the catalogue page carries no hasMore flag, so whether another page exists is unknown",
      ),
    };
  }

  const nextCursor = optionalString(value.nextCursor) ?? null;
  if (value.hasMore && nextCursor === null) {
    return {
      ok: false,
      error: unexpectedResponse(
        200,
        "the catalogue says another page exists but carries no nextCursor to reach it",
      ),
    };
  }
  return { ok: true, value: { hasMore: value.hasMore, nextCursor } };
}

/** The cursor a page read continues from, or null when the caller named none. */
function namedCursor(cursor: string | undefined): string | null {
  if (cursor === undefined || cursor.trim() === "") {
    return null;
  }
  return cursor;
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
