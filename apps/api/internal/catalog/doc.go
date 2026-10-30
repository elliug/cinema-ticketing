// Package catalog owns the read models the public web needs and did not have: the
// catalogue of screenings a buyer can buy a seat for, the read of one of those
// screenings that the seat page arrives with, and the confirmation's read of one
// reservation.
//
// # Why these reads exist
//
// Until this package, the customer surface could only be addressed by an identifier
// somebody already had: the seat map needs an event id, the ticket needs a ticket id.
// A stranger arriving at the site has neither, so the catalogue answers the screenings
// a buyer can actually buy and the web no longer has to know an identifier in advance.
// It answers one bounded page of them and says where the next page starts, because a
// catalogue is a list that changes under its readers: [ListQuery] records why that
// position is a keyset over `(starts_at, id)` and not an offset.
// The reservation read is the other half: without it, reloading the confirmation page
// loses the state the buyer just produced with a hold or a payment.
//
// The single screening read is the join between those two: the seat page is reached from
// a catalogue row, and everything the buyer was just shown — the film, the cinema, the
// room, the venue's hour, the price of one seat and how many are free — has to survive
// the navigation, or the checkout has no amount to multiply. It answers the same
// projection as the list, from the same statement, with one difference in scope: it
// answers a screening that exists even when the catalogue no longer offers it.
//
// All three reads are projections. Nothing here writes: the hold window, the price, the
// sale and the tickets are decided by the modules that own them, and this package only
// reads what they stored. A question that could only be answered by changing a write
// path is not answered here.
//
// # What decides each number
//
//   - Whether a screening is offered is decided by PostgreSQL, not by Go: `status =
//     'on_sale'` and `starts_at > now()`. Holds are governed by that same database clock,
//     so a screening the catalogue offers and a screening that accepts a hold can never
//     disagree about which side of "now" they are on. That predicate is written once, as a
//     column of the shared screening projection: the list filters on it and the single
//     screening read answers it, so the two cannot drift apart and no client has to
//     compare a clock of its own to know whether a seat can still be bought.
//   - A screening that exists but is not offered is still answered by the single read,
//     with its stored `status` verbatim and `onSale: false`. A buyer holding a stale link
//     is told the screening is no longer on sale rather than that it never existed, and
//     is told why: cancelled, still a draft, or simply already started.
//   - The seats available are the same thing the seat map means: a seat that is neither
//     sold nor held by a live hold. "Live" is decided by the same predicate the seat read
//     uses, so an expired hold nobody has swept yet already counts as free — the
//     database clock again, rather than a sweep having run.
//   - The money is the amount that was actually taken, read from the reservation's
//     captured payment attempt, and it is absent while there is none. It is never the
//     price multiplied by the number of seats: a price that moved after the sale would
//     silently rewrite what the buyer paid, and an unpaid order would be shown a charge
//     that never happened.
//   - The screening time is answered twice, as the stored instant and as the venue's
//     local presentation. The presentation is the ticket package's rule
//     ([tickets.EventDetail.LocalStartsAt]), applied to this package's stored instant and
//     venue zone, so the catalogue and the ticket cannot disagree about the hour a
//     screening starts and the browser never has to know the venue's rules. The raw
//     instant is answered in UTC, so a client that sorts or compares the string it is
//     handed compares the same thing in every deployment; the offset lives in the local
//     presentation, which is where it means something.
//   - Money is presented by one formatter ([tickets.FormatMoney]) for the same reason:
//     the catalogue price and the ticket's order amount are spelled by one function.
//
// # Access model in v1: the reservation id is the capability
//
// M3.5's demonstration session does not authenticate anybody (S2, L3): the browser picks
// one of two demo people and scopes what the interface shows, and it grants no access
// control. There is therefore no buyer identity behind a reservation — nothing to check
// an owner against and nothing to leak — so this read is **not protected**, exactly like
// the ticket route ([tickets.TicketDetail], whose package documents the same model). The
// reservation identifier is the capability that grants access: whoever presents it reads
// that order, which is what makes a reloaded confirmation page work without an account.
//
// What that means, plainly:
//
//   - The identifier must stay unguessable. It is a random version-4 UUID, never a
//     sequential number, because possession is the whole of the access check.
//   - This read must not carry more than the confirmation screen needs. It answers the
//     order's own facts — its status, its deadline, the money taken, the seats it holds
//     or sold and the tickets issued for it — and nothing else. There is no buyer
//     identity in the projection because v1 stores none for a guest order. In
//     particular no payment-attempt internals are exposed: not the attempt identifier,
//     the provider, the provider reference, the idempotency key, the request
//     fingerprint, the outcome or the attempt status. Those are the checkout's ledger,
//     and a leaked confirmation URL must not become a window into it.
//   - Saying "not protected" is the honest description, not a placeholder: real accounts
//     (users, hashed passwords, sessions, ownership checks) are a backend milestone. Until
//     then a ticket URL and a reservation URL are forwardable by design, and no surface
//     may present this read as proof of ownership.
package catalog
