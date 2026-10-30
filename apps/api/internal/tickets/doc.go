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
// # The signed code, and the one place it is minted
//
// The detail carries a QR code ([TicketDetail.QRToken]), because a ticket that could
// not be scanned would not be a ticket. That code is a signed opaque token: two
// base64url halves — the payload and an HMAC-SHA256 signature over those exact
// payload bytes — and the payload is the canonical, versioned identity of the ticket,
//
//	v1|<ticketID>|<eventID>|<seatID>,<seatID>,...
//
// with the seats ascending. Three facts, all identifiers, and nothing else: no buyer
// name, no email, no film, no screening time, no price. A QR code is a photograph
// waiting to happen, so the code carries what the server can look up and every
// readable fact is fetched server side after the code verifies.
//
// The version is inside the signed payload rather than beside the signature, because a
// version label that is not signed is a label anybody can rewrite: an old payload
// relabelled as a new format would be parsed under rules nobody signed it for. Signed,
// the version is an authenticated statement, and a token minted after an upgrade is
// distinguishable from one this build understands instead of being guessed at.
//
// The code is minted in exactly one place — [Service.Detail] — so the web page (T7)
// and the PDF (T6) print the same code for the same ticket instead of each renderer
// signing its own version. A detail that cannot be signed is [ErrUnavailable], never a
// ticket served without its code, because a renderer would print it.
//
// [Service.Verify] is the other half, and the only thing a door scanner is asked to
// believe. It checks the signature, reads the ticket the code names, and requires the
// signed identity to agree with the stored one: the same ticket, the same event, the
// same seats. Anything else is [ErrInvalidToken] — the same answer a forged code gets,
// so probing identifiers cannot confirm that one exists — and an unreadable database is
// [ErrUnavailable] rather than a verdict on the ticket. What the token deliberately
// does not do is mark anything as used: a code is copyable by nature, and redemption is
// a state change M4's cashier workflow owns. What M3 promises is that a photographed
// code carries no personal data — not that it hides anything. The payload is signed,
// not encrypted: it is base64url text, so anyone who decodes the code reads the ticket,
// event and seat identifiers, and the public ticket route resolves them to the whole
// screening. A photograph is therefore both a readable reference and a usable
// credential, and that is why a door must honour `valid` rather than the 200, and why
// single use is the next milestone's work. What a forged, tampered or unknown code gets
// is a refusal, server side.
//
// # What this package decides, and what it only copies
//
// The readers here copy what the schema already decided; the rules that are more than
// copying — validity, the invalid reason, the amount presentation, the local rendering
// — live in this package next to the fields they describe, so the web page, the PDF and
// the door inherit them instead of reimplementing them and disagreeing.
package tickets
