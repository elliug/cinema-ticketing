// Package reservation implements the Redis coordination layer for M2 seat
// holds. PostgreSQL stays the source of truth for the durable reservation rows;
// Redis is the fast, expiring layer that decides which hold owns a seat right
// now, so two concurrent claimers cannot both proceed.
//
// # Key scheme
//
// One key per event seat, in the namespace "reservation:{hold}:seat:<eventSeatID>":
//
//	reservation:{hold}:seat:4711 -> "hold-9f3c..."
//
// The value is the identifier of the hold that currently owns the seat, and the
// key's TTL is that hold's remaining window. Event seat identifiers are the
// globally unique cinema.event_seats.id values, so the key needs no event scope;
// the event is implied by the seat. The braces around "hold" are a Redis Cluster
// hash tag: they place every hold key in one slot so a single script or MGET can
// address a multi-seat set on a clustered deployment too.
//
// # Atomicity
//
// Acquire runs one Lua script: it reads every requested key, fails without
// writing anything as soon as one of them is owned by a different hold, and
// otherwise writes all of them. Redis executes a script atomically on the server
// thread, so the read-then-write cannot interleave with another acquire, and the
// result is all-or-nothing for the whole seat set (S1, S4, S5).
//
// Release runs a compare-and-delete script per set: it deletes a key only when
// the stored owner is still the releasing hold, so a stale or duplicated release
// can never delete a seat that a different hold re-acquired in the meantime.
//
// # Expiry
//
// The hold window is enforced by the Redis TTL (S6). Expiry is implicit: the key
// disappears, the seat becomes acquirable again, and no sweep is needed here.
// Durable, restart-safe expiry of the PostgreSQL rows belongs to T5.
//
// The window is written as whole milliseconds: PX takes milliseconds, so a
// sub-millisecond remainder is truncated. The stored deadline is therefore at
// most one millisecond shorter than requested and never longer.
//
// Re-acquiring restarts the window of the keys in the request, which means a
// caller that renews a hold must send the hold's complete seat set. Renewing a
// subset leaves the remaining keys on their older deadline, so one hold can end
// up owning a mix of live and expired seats.
//
// Release of a hold whose window already elapsed is a documented no-op: nothing
// is owned by that hold anymore, so the desired end state (seat free) already
// holds, and Release returns nil instead of an error. Releasing a hold id that
// never owned a seat is likewise a no-op.
//
// # Scope
//
// This package has no HTTP, database, or configuration dependency; what an
// unavailable Redis means for a caller (retry, or surface 503) is decided by T4.
package reservation
