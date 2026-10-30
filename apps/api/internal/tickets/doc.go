// Package tickets defines the one ticket detail projection and reads it from
// PostgreSQL.
//
// # One projection, two renderers
//
// The web confirmation page (T7) and the PDF (T6) show the same ticket, so they
// render the same value: [TicketDetail]. The PDF must never assemble its own
// version of the ticket, because two assemblies of the same ticket can disagree,
// and the printed ticket would then say something the web page does not. This
// package therefore owns the only shape and the only query; a renderer translates
// the projection into its own medium and adds nothing numeric or temporal of its
// own.
//
// # Where every field comes from
//
// Nothing here is recomputed from parts that could drift apart:
//
//   - The amount is the money that was actually taken, read from the reservation's
//     captured `cinema.payment_attempts` row. It is never price × seats. If the
//     event price changed after the sale, or a past checkout charged something
//     else, the recorded charge is the truth and the recomputation would be a lie.
//     Because the captured attempt is per reservation, that number is what the
//     **whole order** cost, which is why the field is named after the order and not
//     after the ticket: M3 stores no per-ticket amount, since an order is priced as
//     price × seats, and a renderer that read a field called "amount" as this
//     ticket's price would print a two-seat order as one very expensive seat.
//   - The seat is the ticket's own: the ticket row names the `event_seat_id` it was
//     sold for, read through its layout seat and row so the label and number are
//     the ones printed at the door. One ticket is one seat in this milestone, so the
//     projection always answers exactly one — the PDF prints one document per
//     ticket, and a document listing two seats admits two people at the door while
//     making ticket A1 indistinguishable from ticket A2. The field stays a list so
//     that a later milestone selling several seats on one ticket extends it there
//     instead of a renderer inventing a seat list of its own.
//   - The screening time is answered twice: the raw instant, and the local
//     presentation computed here from `cinema.cinemas.timezone`, with an explicit
//     offset. Neither consumer has to know the venue's zone rules, and a client
//     cannot render the ticket in its own zone by mistake.
//   - The statuses are carried as stored. A voided ticket and an issued ticket of a
//     cancelled screening both answer, so a surface can say what happened instead of
//     pretending the ticket is still good. [TicketDetail.Valid] is the single
//     derived answer to "does this ticket still admit its holder", so the web page
//     and the PDF cannot disagree about it.
//
// # Access model in v1: the ticket id is the capability
//
// v1 has guest checkout and no accounts, so nobody can prove ownership of a ticket
// by signing in. The ticket identifier is therefore the capability that grants
// access to its detail: the route that serves this projection is public, mounted
// outside the administrative guard, and whoever presents a ticket id receives that
// ticket's detail. There is deliberately no token and no session in this package —
// an authentication scheme with no identity to authenticate would be theatre, and
// inventing one here would suggest a protection the system does not have.
//
// What that means, plainly:
//
//   - The identifier must stay unguessable. It is a random version-4 UUID, never a
//     sequential number, because possession is the whole of the access check.
//   - The projection carries nothing about the buyer: no name, no email, no payment
//     instrument, no provider reference. A leaked ticket URL exposes a screening, a
//     seat and an amount, which is what a stolen cinema ticket already shows.
//   - It proves nothing at the door. A URL can be forwarded by anyone, so it is not
//     an admission token; the signed QR token of T5, verified server side against
//     the database, is what later proves a ticket to a cashier.
//   - There is no route that lists tickets. Without an identity there is nothing to
//     list them by, so T7 must keep the ticket ids the checkout response gave it.
//   - An identifier that names no ticket, including one that cannot be an
//     identifier at all, is the same answer: not found. Nothing distinguishes "you
//     guessed wrong" from "it never existed", because that distinction is exactly
//     the oracle a capability URL cannot afford.
//
// # Why a query and not a service
//
// This package reads; it decides nothing that the schema does not already decide.
// The projection rules that are more than copying — validity, the amount
// presentation, the local rendering — live in this package next to the fields they
// describe, so both renderers inherit them instead of reimplementing them.
package tickets
