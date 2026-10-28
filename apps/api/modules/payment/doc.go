// Package payment declares the module boundary behind the PaymentGateway
// integration surface. Simulated providers implement this boundary; real
// providers (deferred beyond v1) replace only the adapter.
//
// M0 foundation only. This package defines the boundary and exposes no domain
// behavior. Domain behavior is delivered in later milestones.
package payment

// Name is the stable identifier of the payment module boundary.
const Name = "payment"
