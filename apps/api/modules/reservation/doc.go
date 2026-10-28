// Package reservation declares the module boundary for reservation
// orchestration: all-or-none atomic holds, TTL expiry, and the coordination
// between the Redis coordination layer and the PostgreSQL source of truth.
//
// M0 foundation only. This package defines the boundary and exposes no domain
// behavior. Domain behavior is delivered in later milestones.
package reservation

// Name is the stable identifier of the reservation module boundary.
const Name = "reservation"
