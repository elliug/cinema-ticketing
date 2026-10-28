// Package identity declares the module boundary for identity and
// authorization concerns: authenticated users, guest email/phone
// verification, and role-based authorization (customer, cashier, admin,
// analyst).
//
// M0 foundation only. This package defines the boundary and exposes no domain
// behavior. Domain behavior is delivered in later milestones.
package identity

// Name is the stable identifier of the identity module boundary.
const Name = "identity"
